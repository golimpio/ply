// Package trailers downloads a title's trailer into the library when it has none on disk
// (docs/M13-TRAILERS.md, "The download job"): a job of the background queue, which writes the
// file where Plex keeps it and indexes it at once.
package trailers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/unicode/norm"

	"ply/internal/jobs"
	"ply/internal/library"
	"ply/internal/metadata"
	"ply/internal/store"
	"ply/internal/tier"
	"ply/internal/trailerdl"
)

// Kind is the job's kind.
const Kind = "trailer.download"

// Key is the job's key for a title: one download at a time for it.
func Key(titleID int64) string { return "title:" + strconv.FormatInt(titleID, 10) }

// maxTries is how many of a title's videos a job tries, best first.
const maxTries = 3

// Download is a download of a title's trailer, as the title's page sees it.
type Download struct {
	State string `json:"state"` // queued | running | failed
	JobID int64  `json:"jobId,omitempty"`
	Error string `json:"error,omitempty"`
}

// Options are what a Service works with.
type Options struct {
	Store   *store.Store
	Queue   *jobs.Queue
	Scanner interface {
		IndexFile(ctx context.Context, path string) (int64, error)
	}
	// Videos are the title's trailers at the sites, best first (metadata.Worker.Trailers).
	Videos func(ctx context.Context, titleID int64) ([]store.Video, error)
	// Fetch downloads one into dir and returns the MP4 it made there (trailerdl.Fetcher.Fetch).
	Fetch   func(ctx context.Context, v store.Video, dir string, s trailerdl.Steps) (string, error)
	Online  func(ctx context.Context) bool
	TempDir string // where downloads are made, on ply's own disk: <data>/tmp/trailers
	Roots   func() []library.Root
	Now     func() time.Time
	Logger  *slog.Logger
}

// Service queues and runs the downloads.
type Service struct {
	o      Options
	mu     sync.Mutex
	failed map[int64]failure // by title id
}

// failure is a title's last failed download, not tried again until.
type failure struct {
	until    time.Time
	attempts int
	job      int64
	err      string
}

type payload struct {
	TitleID int64  `json:"titleId"`
	Name    string `json:"name,omitempty"` // "Vivre sa vie (1962)", for the Activity page
}

// New returns a Service. Register its Spec with the queue before the queue runs.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	return &Service{o: o, failed: map[int64]failure{}}
}

// Spec is the job kind: light (it waits for what each step needs: YieldFor), run once.
func (s *Service) Spec() jobs.Spec {
	return jobs.Spec{
		Kind:     Kind,
		Priority: jobs.PriorityUser, // a person opened the page and waits for it
		Light:    true,
		Title: func(p json.RawMessage) string {
			var pl payload
			_ = json.Unmarshal(p, &pl)
			return "Trailer: " + pl.Name
		},
		MaxAttempts: 1, // a failure is remembered (Ensure), not retried by the queue
		Run:         s.run,
	}
}

// Ensure is asked by a title's page when the title has no trailer on disk: it says how the
// download goes, and queues it when there is none and one can be made. nil: no download (no
// video to download, online lookups off, the title has a trailer or no files).
func (s *Service) Ensure(ctx context.Context, titleID int64) (*Download, error) {
	key := Key(titleID)
	for _, j := range append(s.o.Queue.Snapshot().Running, s.o.Queue.Snapshot().Queued...) {
		if j.Kind == Kind && j.Key == key {
			state := "queued"
			if j.State == jobs.Running {
				state = "running"
			}
			return &Download{State: state, JobID: j.ID}, nil
		}
	}
	if !s.o.Online(ctx) {
		return nil, nil
	}
	s.mu.Lock()
	f, failed := s.failed[titleID]
	s.mu.Unlock()
	if failed && s.o.Now().Before(f.until) {
		return &Download{State: "failed", JobID: f.job, Error: f.err}, nil
	}
	if local, err := s.o.Store.TitleTrailers(ctx, titleID); err != nil || len(local) > 0 {
		return nil, err
	}
	if files, err := s.o.Store.TitleFiles(ctx, titleID); err != nil || len(files) == 0 {
		return nil, err
	}
	videos, err := s.o.Videos(ctx, titleID)
	if err != nil || len(playable(videos)) == 0 {
		return nil, nil // the page's list says why, if anything; nothing to download
	}
	t, err := s.o.Store.GetTitle(ctx, titleID)
	if err != nil {
		return nil, err
	}
	j, err := s.o.Queue.Enqueue(ctx, Kind, jobs.Opt{Key: key, User: true,
		Payload: payload{TitleID: titleID, Name: filmName(t.Title, t.Year)}})
	if err != nil {
		return nil, err
	}
	return &Download{State: "queued", JobID: j.ID}, nil
}

func playable(vs []store.Video) []store.Video {
	var out []store.Video
	for _, v := range vs {
		if store.ValidVideo(v.Site, v.Key) {
			out = append(out, v)
		}
	}
	return out
}

func (s *Service) run(ctx context.Context, r *jobs.Run) error {
	var pl payload
	if err := r.Decode(&pl); err != nil {
		return jobs.Permanent(err)
	}
	err := s.download(ctx, r, pl.TitleID)
	if ctx.Err() != nil {
		return err // cancelled, or ply stopping (the queue runs it again at the next start)
	}
	if errors.Is(err, errLookupsOff) {
		return jobs.Permanent(err) // not a failure of the title's
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		delete(s.failed, pl.TitleID)
		return nil
	}
	f := s.failed[pl.TitleID]
	s.failed[pl.TitleID] = failure{attempts: f.attempts + 1, job: r.ID(), err: err.Error(),
		until: s.o.Now().Add(metadata.RetryAfter(f.attempts, errors.Is(err, trailerdl.ErrUnavailable)))}
	return jobs.Permanent(err)
}

var errLookupsOff = errors.New("Online lookups are off")

// Notes of the Activity page.
const noteSaving = "Saving to the library"

func (s *Service) download(ctx context.Context, r *jobs.Run, titleID int64) error {
	if !s.o.Online(ctx) {
		return errLookupsOff
	}
	if local, err := s.o.Store.TitleTrailers(ctx, titleID); err != nil || len(local) > 0 {
		return err
	}
	files, err := s.o.Store.TitleFiles(ctx, titleID)
	if err != nil {
		return err
	}
	dir, name, err := Destination(files, s.o.Roots())
	if err != nil {
		return err
	}
	final := filepath.Join(dir, name)
	if _, err := os.Stat(final); err == nil { // never overwritten: it is the title's trailer
		_, err := s.o.Scanner.IndexFile(ctx, final)
		return err
	}
	videos, err := s.o.Videos(ctx, titleID)
	if err != nil {
		return err
	}
	videos = playable(videos)
	if len(videos) == 0 {
		return fmt.Errorf("no trailer to download: %w", trailerdl.ErrUnavailable)
	}
	tmp := filepath.Join(s.o.TempDir, strconv.FormatInt(r.ID(), 10))
	defer os.RemoveAll(tmp)
	var out string
	for i, v := range videos[:min(len(videos), maxTries)] {
		if err = r.YieldFor(ctx, tier.Need{}); err != nil {
			return err
		}
		os.RemoveAll(tmp)
		if err = os.MkdirAll(tmp, 0o700); err != nil {
			return err
		}
		out, err = s.o.Fetch(ctx, v, tmp, trailerdl.Steps{
			Progress:   func(done, total int64, note string) { r.Progress(int(done), int(total), note) },
			Converting: func(ctx context.Context) error { return r.YieldFor(ctx, tier.Need{CPU: true}) },
		})
		if err == nil || ctx.Err() != nil {
			break
		}
		s.o.Logger.Info("trailer: download failed", "title", titleID, "site", v.Site, "key", v.Key, "try", i+1, "err", err)
	}
	if err != nil {
		return err
	}
	r.Progress(0, 0, noteSaving)
	if err := r.YieldFor(ctx, tier.Need{Disks: []string{dir}}); err != nil {
		return err
	}
	if err := place(out, dir, name); err != nil {
		return err
	}
	_, err = s.o.Scanner.IndexFile(ctx, final)
	return err
}

// place copies the file at from into dir as name: through a hidden file that the scanner and
// Plex skip, synced, then renamed. Nothing is left behind on a failure, and nothing is replaced.
func place(from, dir, name string) (err error) {
	part := filepath.Join(dir, "."+name+".part")
	out, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("The folder is read-only: %s", dir)
	}
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			out.Close()
			os.Remove(part)
		}
	}()
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	final := filepath.Join(dir, name)
	if _, statErr := os.Stat(final); statErr == nil {
		return os.Remove(part) // written meanwhile: theirs stands
	}
	return os.Rename(part, final)
}

// Destination is the folder and the name of a title's trailer when it has none (docs/M13-
// TRAILERS.md, "Rules for writing"): next to the title's first file, named as Plex names a
// trailer inline. A film's is "<Title> (<Year>)-trailer.mp4", the title and year its file's name
// says (so the scanner gives it to that film in a folder of several); a show's is in the show's
// folder, named after it.
func Destination(files []store.Item, roots []library.Root) (dir, name string, err error) {
	if len(files) == 0 {
		return "", "", errors.New("the title has no files")
	}
	first := files[0]
	for _, f := range files[1:] {
		if f.ID < first.ID {
			first = f
		}
	}
	if first.Kind == store.KindEpisode {
		var root library.Root
		for _, r := range roots {
			if (strings.HasPrefix(first.Path, r.Path+string(filepath.Separator))) && len(r.Path) > len(root.Path) {
				root = r
			}
		}
		if root.Path == "" {
			return "", "", fmt.Errorf("no library folder holds %s", first.Path)
		}
		dir = library.ShowDir(root.Path, first.Path)
		return dir, fileName(filepath.Base(dir)) + "-trailer.mp4", nil
	}
	return filepath.Dir(first.Path), fileName(filmName(first.Title, first.Year)) + "-trailer.mp4", nil
}

// filmName is "Title (Year)", or the title alone without a year.
func filmName(title string, year int) string {
	if year == 0 {
		return title
	}
	return fmt.Sprintf("%s (%d)", title, year)
}

// fileName is s with what an SMB or HFS file name cannot hold turned into spaces, NFC.
func fileName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) {
			return ' '
		}
		return r
	}, norm.NFC.String(s))
	return strings.Join(strings.Fields(s), " ")
}
