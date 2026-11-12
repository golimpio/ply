// Command ply is a local media server.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"ply/internal/api"
	"ply/internal/art"
	"ply/internal/config"
	"ply/internal/discovery"
	"ply/internal/events"
	"ply/internal/hwaccel"
	"ply/internal/jobs"
	"ply/internal/library"
	"ply/internal/metadata"
	"ply/internal/probe"
	"ply/internal/proc"
	"ply/internal/schedule"
	"ply/internal/store"
	"ply/internal/stream"
	"ply/internal/subs"
	"ply/internal/tier"
	"ply/internal/watch"
	"ply/web"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "0.1.0"

// scanNice is the nice value for scan-time ffprobe processes.
const scanNice = 10

func main() {
	// ply --reap <pgid> <ppid>: the helper that ply starts, on macOS, for every ffmpeg
	// it runs (see stream.ReapMain). It takes no flags and starts no server.
	if len(os.Args) > 1 && os.Args[1] == "--reap" {
		os.Exit(stream.ReapMain(os.Args[2:]))
	}
	// ply --watch: the process that holds the folder watches, on macOS (see
	// internal/watch/helper.go). It talks to the ply that started it on stdin and stdout.
	if len(os.Args) > 1 && os.Args[1] == watch.HelperArg {
		os.Exit(watch.HelperMain(os.Stdin, os.Stdout))
	}
	if err := run(); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "ply:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Parse(os.Args[1:], os.Stderr)
	if err != nil {
		return err
	}
	log, closeLog, isInteractive := setupLogger(cfg)
	defer closeLog()
	if cfg.ScanIntervalSet {
		// The scan no longer runs on an interval: the schedules page decides when, and the database
		// remembers it. The flag is still read so that an old unit file starts.
		log.Warn("-scan-interval no longer sets the scan: it runs on its schedule",
			"interval", cfg.ScanInterval, "configure", "Settings › Schedules")
	}

	if err := os.MkdirAll(cfg.Data, 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	id, err := config.ServerID(cfg.Data)
	if err != nil {
		return fmt.Errorf("server id: %w", err)
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	var closeStore sync.Once
	defer closeStore.Do(func() { _ = st.Close() })
	for _, n := range st.Notices() {
		log.Info(n)
	}
	for _, w := range st.Warnings() {
		log.Warn(w)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigC := make(chan os.Signal, 10)
	signal.Notify(sigC, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigC)

	// Listen before anything advertises the address, so ":0" works.
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port

	ffmpegBin := stream.ResolveFFmpeg(cfg.FFmpeg)
	// Probing the GPU takes a moment: do it in the background, playback
	// requests wait for the result if they need it.
	hw := hwaccel.Start(ctx, hwaccel.Options{FFmpeg: ffmpegBin, Mode: cfg.HWAccel, Logger: log})
	// The reaper that guards each ffmpeg is this same program (macOS; see stream.Options.Reaper).
	self, err := os.Executable()
	if err != nil {
		log.Warn("cannot find this program; ffmpeg will not be guarded by a reaper", "err", err)
		self = ""
	}
	streams, err := stream.NewManager(stream.Options{
		FFmpeg:      ffmpegBin,
		FFprobe:     cfg.FFprobe,
		Data:        cfg.Data,
		Reaper:      self,
		IdleTimeout: cfg.StreamIdle,
		Cache:       st,
		Logger:      log,
	})
	if err != nil {
		return err
	}
	defer streams.Close() // also run explicitly on shutdown; Close is idempotent

	// Playback first, browsing second, the background queue third: the governor
	// holds queued work while a viewer or a person browsing needs the machine.
	gov := tier.New(tier.Options{Playback: playbackLoad{streams}})
	streams.OnLoadChange(gov.Notify)
	// The live feed (GET /api/events, docs/M11-CLIENTS.md): the queue, the library,
	// the plays and the tiers publish to it.
	feed := events.New(512, time.Now())
	streams.OnPlayChange(func(c stream.PlayChange) {
		typ, data := api.SessionEventOf(c)
		feed.Publish(events.Sessions, typ, data)
	})

	// The ffmpeg of subtitles and artwork are guarded as the playback ones are: by the kernel
	// (Linux), or by a reaper each (macOS), the same link the stream manager makes.
	reaper := ""
	if self != "" && proc.ReaperWanted {
		reaper = proc.ReaperLink(cfg.Data, self, log)
	}

	artGen, err := art.New(ctx, art.Options{Dir: filepath.Join(cfg.Data, "art"), FFmpeg: ffmpegBin, Reaper: reaper, Logger: log})
	if err != nil {
		log.Warn("artwork disabled", "err", err)
		artGen = nil
	}

	// A viewer who asked for a track and left, or a play that starts while a film's subtitles
	// are read, hold that read like any other background work (diskHold).
	subGen, err := subs.New(subs.Options{Dir: filepath.Join(cfg.Data, "subs"), FFmpeg: ffmpegBin,
		Reaper: reaper, Hold: newDiskHold(gov).check, Logger: log})
	if err != nil {
		log.Warn("text subtitles disabled", "err", err)
		subGen = nil
	}

	// The library folders: the ones on the command line, saved so that the web
	// page lists them, and the ones added from that page in earlier runs.
	flagRoots := make([]library.Root, len(cfg.Roots))
	for i, r := range cfg.Roots {
		flagRoots[i] = library.Root{Path: r.Path, Kind: r.Kind}
	}
	// A folder typed on the command line for the first time may have items from before
	// types existed, to be read again once the scanner has its roots (rereadPending
	// below): it is noted before the folder is saved, and forgotten when it is done.
	roots, before, err := prepareFolders(ctx, st, flagRoots, log)
	if err != nil {
		return fmt.Errorf("library folders: %w", err)
	}
	if len(roots) == 0 {
		log.Warn("no library folders yet; add one in Settings (or start ply with -movies, -shows or -media)")
	}
	prober := probe.FFprobe{Bin: cfg.FFprobe, Niceness: scanNice}
	scanner := library.NewScanner(ctx, st, prober, rootPaths(roots), log)
	scanner.SetSettle(library.DefaultSettle)

	// Metadata: the TMDB key and switches (flags, environment, the settings
	// table), the image cache, and the worker that matches files in the
	// background. The scanner tells the worker about new files and sidecars; both
	// calls only set a flag.
	settings := metadata.NewSettings(metadata.SettingsOptions{
		Store: st, KeyFlag: cfg.TMDBKey, KeyEnv: cfg.TMDBKeyEnv, KeyDefault: metadata.DefaultKey,
		TVDBKeyFlag: cfg.TVDBKey, TVDBKeyEnv: cfg.TVDBKeyEnv, TVDBKeyDefault: metadata.DefaultTVDBKey,
		TVDBPINFlag: cfg.TVDBPIN, TVDBPINEnv: cfg.TVDBPINEnv,
		MDBListKeyFlag: cfg.MDBListKey, MDBListKeyEnv: cfg.MDBListKeyEnv, MDBListBaseURL: cfg.MDBListURL,
		Disabled: !cfg.Metadata, Language: cfg.MetadataLanguage,
		BaseURL: cfg.TMDBURL, ImageBaseURL: cfg.TMDBImageURL, TVDBBaseURL: cfg.TVDBURL, TVDBImageBaseURL: cfg.TVDBImageURL, Logger: log,
	})
	// Ratings (M10): IMDb's datasets (kept under <data>/cache/imdb: the ratings, and which episode
	// is which of a show) and MDBList's answers, all run by the queue and paced to playback and
	// browsing.
	ratings := metadata.NewRatings(metadata.RatingsOptions{
		Store: st, Settings: settings, Dir: filepath.Join(cfg.Data, "cache", "imdb"),
		IMDbURL: cfg.IMDbRatingsURL, IMDbEpisodesURL: cfg.IMDbEpisodesURL,
		Pace: paceFor(gov, tier.Need{CPU: true}), Logger: log,
	})
	images, err := metadata.NewImageCache(metadata.ImageOptions{
		Dir: filepath.Join(cfg.Data, "images"), URL: settings.ImageURL, TVDBURL: settings.TVDBImageURL,
		FFmpeg: ffmpegBin, Niceness: scanNice, Logger: log,
	})
	if err != nil {
		return fmt.Errorf("image cache: %w", err)
	}
	defer images.Close() // also run explicitly on shutdown; Close is idempotent
	// The worker's passes run as queue jobs: whatever wakes it (new files, a
	// refresh asked for) queues one; queue is set before anything can wake it.
	var queue *jobs.Queue
	var subsFeed *subsFeeder // set with the queue; nil without text subtitles
	worker := metadata.NewWorker(metadata.WorkerOptions{
		Store: st, Source: settings, Images: images, Probe: prober.Probe, Refresh: cfg.MetadataRefresh, Logger: log,
		// Each item of a pass waits while playback or browsing needs the machine.
		Pace: paceFor(gov, tier.Need{CPU: true}),
		// Off the caller's goroutine: a person's "refresh" request must not wait
		// for the queue's (low-priority) write.
		OnWake: func() {
			go func() {
				if _, err := queue.Enqueue(ctx, kindMetaMatch, jobs.Opt{Key: "match"}); err != nil && ctx.Err() == nil {
					log.Warn("queue: metadata", "err", err)
				}
			}()
		},
		// The open pages fetch the library again (GET /api/events, "library").
		OnChanged: func() { feed.Publish(events.Library, "library", nil) },
	})
	scanner.OnChanged(func() { feed.Publish(events.Library, "library", nil) })
	scanner.OnAdded(func(ids []int64) {
		worker.Notify()
		if subsFeed != nil {
			subsFeed.Add(ids...) // a new file is the likelier to be played soon: its subtitles first
		}
	})
	scanner.OnSidecarsChanged(worker.NotifySidecars)
	// A folder that was removed and added back, or whose type was changed and
	// changed back: the manual matches its files had are applied again, in the
	// background (a scan does not wait for TMDB).
	restorer := newMatchRestorer(st, worker, log)
	scanner.OnRestoreMatch(restorer.add)
	// The archived matches are applied only while online lookups are on; the scanner
	// reports them again when they come on (checked every few seconds, below).
	scanner.SetMatchGate(func() bool { return settings.Resolve(ctx).Online() })
	// The queue: scans, lookups and cache work, persisted, by priority, below
	// playback and browsing. It is built before the scanner gets its roots, so
	// that the scanner's first passes already yield.
	var active struct {
		sync.Mutex
		roots []string
	}
	active.roots = rootPaths(roots)
	queue = newQueue(queueDeps{store: st, gov: gov, scanner: scanner, worker: worker, art: artGen, subs: subGen, ratings: ratings,
		roots: func() []string { active.Lock(); defer active.Unlock(); return slices.Clone(active.roots) }, log: log, events: feed})
	if subGen != nil {
		subsFeed = newSubsFeeder(st, queue, subGen, log)
	}
	// The schedules (docs/M12-SCHEDULES.md): which background tasks run, at what time of night and on
	// which days, kept in the database so that a restart neither repeats a run nor loses one. Each
	// queues a kind the queue already has, so a scheduled run is the job a button would queue.
	sched, err := schedule.New(schedule.Options{Store: st, Queue: queue, Tasks: scheduleTasks(),
		Check: scheduleChecker(settings), Logger: log})
	if err != nil {
		return fmt.Errorf("schedules: %w", err)
	}

	// The scanner gets the folders' types last, with its settle time and its hooks
	// in place: first the types they had before this start's flags, which it takes
	// as the ones its items were read with, then the ones they have now. A type that
	// a flag changed is read again (the scanner does it in the background); a
	// folder it had no type for, a -media folder, reads nothing again: ply does not
	// restructure a library on its own. A type a flag gives a folder that was not
	// saved yet may be on items from before types existed: those are read again too
	// (the log says how many items change).
	scanner.SetRoots(previousRoots(roots, before))
	scanner.SetRoots(roots)
	rereadPending(ctx, st, scanner, log)
	// Files left pending by the last run are looked up again (what worker.Run did first).
	if err := st.ReleasePending(ctx); err != nil && ctx.Err() == nil {
		log.Warn("metadata: releasing pending files", "err", err)
	}

	var bg sync.WaitGroup // the queue, its schedules, the watcher, the match restorer, the lookups check, the ratings wake and the subtitle feeder
	bg.Add(5)
	if subsFeed != nil {
		bg.Add(1)
		go func() {
			defer bg.Done()
			subsFeed.Run(ctx)
		}()
	}
	go func() {
		defer bg.Done()
		watchRatingsWake(ctx, ratings, sched)
	}()
	go func() {
		defer bg.Done()
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				scanner.ReportMatchesIfDue(ctx)
			}
		}
	}()
	go func() {
		defer bg.Done()
		queue.Run(ctx)
	}()
	go func() {
		defer bg.Done()
		sched.Run(ctx)
	}()
	go func() {
		defer bg.Done()
		restorer.run(ctx)
	}()
	// The watcher runs even with no folders, for the ones added later.
	var watcher *watch.Watcher
	if cfg.Watch {
		watcher = watch.NewSpecs(queuedScanner{queue}, rootSpecs(roots), log)
		bg.Add(1)
		go func() {
			defer bg.Done()
			watcher.Run(ctx)
		}()
	}
	// Called by the API after every change to the folders, with its lock held: it
	// must not block. The scanner scans a new folder and reads a changed one again
	// in the background.
	setRoots := func(roots []library.Root) {
		active.Lock()
		active.roots = rootPaths(roots)
		active.Unlock()
		scanner.SetRoots(roots)
		if watcher != nil {
			watcher.SetRootSpecs(rootSpecs(roots))
		}
		log.Info("library folders changed", "folders", rootPaths(roots))
	}

	// The AirPlay targets on the LAN (docs/AIRPLAY.md): a browse over mDNS beside the
	// advertisement. Without it, a play keeps the page's own address and no receiver is listed.
	var receivers *discovery.Browser
	if b, err := discovery.Browse(); err != nil {
		log.Warn("AirPlay receiver discovery unavailable", "err", err)
	} else {
		receivers = b
	}

	apiOpts := api.Options{
		Store:   st,
		Scanner: scanControl{q: queue, scanner: scanner},
		UI:      web.Dist,
		ID:      id,
		Name:    cfg.Name,
		Version: version,
		Token:   cfg.Token,
		// A play is routed only to an address this server listens on.
		ListenAddr: ln.Addr(),

		FlagFolders: flagRoots,
		SetRoots:    setRoots,
		// DropRoot waits for a scan that is running and holds the scan semaphore while
		// it archives and removes the folder's items.
		DropRoot: scanner.DropRoot,
		LastScan: scanner.LastScan,
		ScanFolder: func(path string) bool {
			if queue.Busy(kindScanFull) {
				return false
			}
			_, err := queue.Enqueue(context.Background(), kindScanDirs, jobs.Opt{
				Payload: []library.Dir{{Path: path, Recursive: true}},
				User:    true,
			})
			return err == nil
		},

		AllowedHosts: cfg.AllowHosts,
		Logger:       log,
		Streams:      streams,

		MaxStreamsFlag:    cfg.MaxStreams,
		MaxTranscodesFlag: cfg.MaxTranscodes,
		HW:                hw,
		Art:               artGen,
		ArtLater:          artLater(ctx, queue, log),
		Subs:              subGen,

		Metadata:  worker,
		Settings:  settings,
		Images:    images,
		Ratings:   ratings,
		Schedules: sched,

		Jobs:     queue,
		Governor: gov,
	}
	if watcher != nil {
		apiOpts.Watcher = watcher // a nil *Watcher in the interface would not be nil
	}
	if receivers != nil {
		apiOpts.Receivers = receivers // a typed nil in the interface would not be nil
	}
	handler := api.New(apiOpts)
	// The limits on plays: the flags, then the settings, then the defaults; the default
	// number of transcodes needs the answer of the GPU probe, which may still be running.
	handler.ApplyLimitsWhenReady(ctx)
	var rescanSubs func()
	if subsFeed != nil {
		rescanSubs = subsFeed.Rescan
	}
	go afterScans(ctx, scanner, queue, worker, rescanSubs, log)
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	adv, err := discovery.Advertise(cfg.Name, id, port, api.Version, api.VersionMin)
	if err != nil {
		log.Warn("mDNS advertisement unavailable", "err", err)
	}
	urls := reachableURLs(ln.Addr())
	if isInteractive && !cfg.LogStderr {
		fmt.Fprint(os.Stderr, banner(cfg.Name, version, cfg.LogPath(), urls))
	}
	log.Info("ply listening", "ffmpeg", ffmpegBin, "version", version, "name", cfg.Name, "id", id, "data", cfg.Data,
		"log", cfg.LogPath(), "auth", cfg.Token != "", "media", rootPaths(roots))
	for _, u := range urls {
		log.Info("reachable at", "url", u)
	}

	var serveFailure error
	select {
	case <-sigC:
		if isInteractive && !cfg.LogStderr {
			fmt.Fprintln(os.Stderr, "\nShutting down... (press Ctrl-C again to quit immediately)")
		}
		log.Info("shutting down; press Ctrl-C again to quit at once")
		go func() {
			for sig := range sigC {
				if sig == os.Interrupt || sig == syscall.SIGTERM {
					if isInteractive && !cfg.LogStderr {
						fmt.Fprintln(os.Stderr, "\nForced exit.")
					}
					os.Exit(130)
				}
			}
		}()
		cancel()
	case serveFailure = <-serveErr:
		cancel()
	}

	shutCtx, cancelShut := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShut()
	clean := stopper{
		log:      log,
		every:    2 * time.Second,
		deadline: 15 * time.Second,
		quick:    sigC,
		reportProgress: func(step string, waited time.Duration, left int) {
			if isInteractive && !cfg.LogStderr {
				fmt.Fprintf(os.Stderr, "Still waiting for %s (%s)... press Ctrl-C again to quit immediately\n", step, waited)
			}
		},
		onQuick: func() {
			if isInteractive && !cfg.LogStderr {
				fmt.Fprintln(os.Stderr, "\nQuitting at once.")
			}
			os.Exit(130)
		},
	}.run([]stopStep{
		{"network announcement", adv.Shutdown},
		{"receiver discovery", receivers.Close},
		// Kill every ffmpeg and drop the temp dirs first: requests waiting on
		// segments return at once instead of holding the HTTP shutdown up. All three
		// at once: each waits a moment for the exits (an ffmpeg in a read from a slow
		// share does not die until the read returns), and the waits must not add up.
		{"playback, subtitle extraction and artwork", func() {
			var wg sync.WaitGroup
			for _, closeFn := range []func(){
				streams.Close,
				func() { // a first extraction of a big file can run for minutes
					if subGen != nil {
						subGen.Close()
					}
				},
				func() { // a frame grab on a slow mount can too
					if artGen != nil {
						artGen.Close()
					}
				},
			} {
				wg.Add(1)
				go func() { defer wg.Done(); closeFn() }()
			}
			wg.Wait()
		}},
		{"web requests", func() {
			if err := srv.Shutdown(shutCtx); err != nil {
				srv.Close() // long-lived streams outlived the grace period
			}
		}},
		{"background jobs", bg.Wait},
		{"metadata", worker.Close},
		{"library scan", scanner.Wait},
		{"image downloads", images.Close},
		{"database", func() { closeStore.Do(func() { _ = st.Close() }) }},
	})
	if !clean {
		log.Warn("shutdown timed out; forcing exit")
		if isInteractive && !cfg.LogStderr {
			fmt.Fprintln(os.Stderr, "\nShutdown timed out; forced exit.")
		}
		closeLog()
		os.Exit(1)
	}
	if isInteractive && !cfg.LogStderr {
		fmt.Fprintln(os.Stderr, "Stopped.")
	}
	closeLog()
	if serveFailure != nil {
		return serveFailure
	}
	os.Exit(0)
	return nil
}

// storedKinds are the types the saved folders have, by path: what the last run left,
// before this start's flags change any of them.
func storedKinds(ctx context.Context, st *store.Store) (map[string]string, error) {
	rows, err := st.Folders(ctx)
	if err != nil {
		return nil, err
	}
	kinds := make(map[string]string, len(rows))
	for _, f := range rows {
		kinds[f.Path] = f.Kind
	}
	return kinds, nil
}

// firstTyped are the folders given a type on the command line that were not saved
// before this start: if they have items, an earlier version read those without a
// type, and the type given is a choice the items follow (Scanner.Reread). A saved
// folder that a flag gave another type is read again by the change itself.
func firstTyped(flags []library.Root, before map[string]string) []string {
	var out []string
	for _, r := range flags {
		if _, saved := before[r.Path]; !saved && r.Kind != "" {
			out = append(out, r.Path)
		}
	}
	return out
}

// previousRoots is roots with the types they had before this start: those in
// before, the rest as they are (a folder that was not saved has no earlier type to
// differ from, so SetRoots reads nothing again; firstTyped names the ones a flag
// types for the first time, which are read again by Scanner.Reread).
func previousRoots(roots []library.Root, before map[string]string) []library.Root {
	out := make([]library.Root, len(roots))
	for i, r := range roots {
		if kind, ok := before[r.Path]; ok {
			r.Kind = kind
		}
		out[i] = r
	}
	return out
}

// startupRoots saves the folders given on the command line (-movies and
// -shows set the type, -media keeps the saved one) and returns the active roots:
// those folders, then the ones saved from the web page. A -media folder with no
// saved type comes back with Kind "": the scanner guesses it on its first scan.
func startupRoots(ctx context.Context, st *store.Store, flags []library.Root, log *slog.Logger) ([]library.Root, error) {
	for _, r := range flags {
		_, changed, err := st.UpsertFlagFolder(ctx, r.Path, r.Kind, "")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r.Path, err)
		}
		if changed {
			// The scanner reads the folder's items again (SetRoots at start).
			log.Info("the type of a folder changed on the command line; its files will be read again",
				"folder", r.Path, "kind", r.Kind)
		}
	}
	rows, err := st.Folders(ctx)
	if err != nil {
		return nil, err
	}
	saved := make([]library.Root, len(rows))
	for i, f := range rows {
		saved[i] = library.Root{Path: f.Path, Kind: f.Kind}
	}
	noteFormerFlags(ctx, st, flags, rows, log)
	roots := library.MergeRoots(flags, saved)
	for _, p := range overlappingPairs(roots, flags) {
		log.Warn("two library folders overlap; a file is indexed under only one of them", "folder", p[0], "other", p[1])
	}
	return roots, nil
}

// flagFoldersKey is the setting that remembers which folders the last start was
// given on the command line: whether a folder came from the command line is not
// saved with the folder, since it is per run (docs/M7-FOLDERS.md).
const flagFoldersKey = "flag_folders"

// noteFormerFlags says so, once, for each folder that was on the command line at
// the last start and is not now: it stays in the library, as one of the folders
// added in Settings, and Settings can remove it.
func noteFormerFlags(ctx context.Context, st *store.Store, flags []library.Root, saved []store.Folder, log *slog.Logger) {
	now := rootPaths(flags)
	var before []string
	if v, ok, err := st.GetSetting(ctx, flagFoldersKey); err != nil {
		log.Warn("reading the folders of the last start", "err", err)
	} else if ok {
		json.Unmarshal([]byte(v), &before) // an unreadable list is as if there was none
	}
	for _, p := range before {
		if !slices.Contains(now, p) && slices.ContainsFunc(saved, func(f store.Folder) bool { return f.Path == p }) {
			log.Warn("a folder that was given on the command line before is now kept as a Settings folder", "folder", p)
		}
	}
	b, _ := json.Marshal(now)
	if err := st.SetSetting(ctx, flagFoldersKey, string(b)); err != nil {
		log.Warn("saving the command-line folders", "err", err)
	}
}

// overlappingPairs lists the pairs of roots where one is or holds the other, as
// directories (through symlinks too, as the API refuses them), except pairs that
// are both on the command line: those keep nesting as they always did.
func overlappingPairs(roots, flags []library.Root) [][2]string {
	onFlags := map[string]bool{}
	for _, f := range flags {
		onFlags[f.Path] = true
	}
	var pairs [][2]string
	for i, a := range roots {
		for _, b := range roots[i+1:] {
			if library.Overlap(a.Path, b.Path) && !(onFlags[a.Path] && onFlags[b.Path]) {
				pairs = append(pairs, [2]string{a.Path, b.Path})
			}
		}
	}
	return pairs
}

// rootPaths are the paths of roots.
func rootPaths(roots []library.Root) []string {
	paths := make([]string, len(roots))
	for i, r := range roots {
		paths[i] = r.Path
	}
	return paths
}

// rootSpecs is how deep the watcher looks below each folder. A film is a directory of its own under
// a movies root, so that root is watched alone (a new film fires an event on it). A show's episodes
// live a season below the show, so a shows root is watched with its show directories: that is 91
// directories for a 90-show library instead of 440, which is what keeps a big library inside the
// process's descriptor budget (internal/watch.RootSpec). What the depth leaves out -- an episode
// added inside an existing season -- is left to the periodic scan.
func rootSpecs(roots []library.Root) []watch.RootSpec {
	specs := make([]watch.RootSpec, len(roots))
	for i, r := range roots {
		depth := 1 // shows: the root and each show directory
		if r.Kind == library.KindMovies {
			depth = 0 // movies: the root alone
		}
		specs[i] = watch.RootSpec{Path: r.Path, Depth: depth}
	}
	return specs
}

// afterScans queues the housekeeping each time a scan finishes, whatever started
// it (a job, the scanner's own pass for a folder just added): it wakes the
// metadata worker (files may have been added), has the subtitle feeder look at the
// library again (files may have changed) and queues the pruning of cached artwork and
// subtitles of items that are gone or changed. The scanner has no completion hook, so
// its status is polled.
func afterScans(ctx context.Context, sc api.ScanController, q *jobs.Queue, worker *metadata.Worker, rescanSubs func(), log *slog.Logger) {
	var seen time.Time
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s := sc.Status(ctx)
		if s.Running || s.LastFinished == nil || !s.LastFinished.After(seen) {
			continue
		}
		seen = *s.LastFinished
		worker.Notify()
		if rescanSubs != nil {
			rescanSubs() // files that changed since have subtitles to extract again
		}
		if _, err := q.Enqueue(ctx, kindCachePrune, jobs.Opt{Key: "prune"}); err != nil && ctx.Err() == nil {
			log.Warn("queue: cache prune", "err", err)
		}
	}
}
