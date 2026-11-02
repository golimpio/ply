package metadata

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"ply/internal/probe"
	"ply/internal/store"
)

// Timing and sizes of the worker.
const (
	DefaultTick  = time.Minute // how often retries and refreshes are looked at
	DefaultBatch = 25
	// ChangesWindow is how far back TMDB's changes feed reaches. A poll older
	// than this cannot use it and falls back to refreshing by age.
	ChangesWindow = 14 * 24 * time.Hour
	// idleRecheck is how long files wait when nothing can be asked of TMDB (no
	// key, or switched off) before the worker looks at them again.
	idleRecheck = 10 * time.Minute
	// seasonRetry is how long a season TMDB has no episodes for is left alone.
	seasonRetry = 6 * time.Hour
	// refreshRetry is how long a failed refresh waits.
	refreshRetry = 15 * time.Minute
	refreshBatch = 200
)

// Settings rows the worker keeps.
const (
	SettingLastRefresh = "metadata_last_refresh" // unix seconds of the last completed refresh
	SettingChangesPoll = "metadata_changes_poll" // unix seconds of the last changes-feed poll
)

// WorkerOptions configures NewWorker.
type WorkerOptions struct {
	Store  *store.Store
	Source Source
	// Images, when set, gets the poster and backdrop of every title the worker
	// stores, so that browsing is instant and works offline.
	Images *ImageCache
	// Probe reads the tags and cover of a media file (ffprobe); nil turns the
	// embedded tags off.
	Probe func(ctx context.Context, path string) (probe.Result, error)
	// Refresh is how often titles are refreshed (-metadata-refresh); 0 turns
	// the periodic refresh off but not the matching of new files.
	Refresh time.Duration
	Tick    time.Duration // default DefaultTick
	Batch   int           // default DefaultBatch
	Now     func() time.Time
	Logger  *slog.Logger
}

// Status is what GET /api/metadata/status reports.
type Status struct {
	Enabled     bool       `json:"enabled"` // the privacy switch
	KeySet      bool       `json:"keySet"`
	Running     bool       `json:"running"` // a pass is under way
	Pending     int        `json:"pending"`
	Unmatched   int        `json:"unmatched"`
	NeedsReview int        `json:"needsReview"`
	LastRefresh *time.Time `json:"lastRefresh"`
	LastError   string     `json:"lastError"`
}

// Worker matches the library against TMDB and the local files in the
// background: one goroutine, started by Run and stopped by cancelling its
// context. The methods other than Run are safe to call from any goroutine.
type Worker struct {
	o    WorkerOptions
	log  *slog.Logger
	wake chan struct{}

	mu             sync.Mutex
	sidecars       map[int64]struct{}
	jobs           []job
	reload         bool
	running        bool
	lastError      string
	retryRefreshAt time.Time
	probes         map[int64]probeInfo
	seasonTried    map[seasonKey]time.Time

	// Used by the goroutine that runs a pass only.
	dirs    map[string][]string
	handled map[string]bool
	aborted bool
}

// tagsView is the descriptive tags of a media file.
type tagsView = probe.Tags

type probeInfo struct {
	tags     probe.Tags
	cover    int
	hasCover bool
}

type seasonKey struct {
	title  int64
	season int
}

// job is a refresh a person asked for.
type job struct {
	itemID int64
	show   string
	all    bool
}

// NewWorker returns a worker; call Run to start it.
func NewWorker(o WorkerOptions) *Worker {
	if o.Tick <= 0 {
		o.Tick = DefaultTick
	}
	if o.Batch <= 0 {
		o.Batch = DefaultBatch
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	return &Worker{
		o: o, log: o.Logger, wake: make(chan struct{}, 1),
		sidecars: map[int64]struct{}{}, probes: map[int64]probeInfo{}, seasonTried: map[seasonKey]time.Time{},
	}
}

func (w *Worker) now() time.Time { return w.o.Now() }

// Notify tells the worker that items may be waiting to be matched (the scanner
// added some). It never blocks.
func (w *Worker) Notify() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// NotifySidecars tells the worker that the NFO or artwork next to these items
// (ids of items) changed: it reads the local sources again, without matching
// them from scratch.
func (w *Worker) NotifySidecars(ids []int64) {
	if len(ids) == 0 {
		return
	}
	w.mu.Lock()
	for _, id := range ids {
		w.sidecars[id] = struct{}{}
	}
	w.mu.Unlock()
	w.Notify()
}

// Reload tells the worker that the settings changed (a new key, the privacy
// switch, the language): files it put off because it could not ask TMDB are
// looked at at once.
func (w *Worker) Reload() {
	w.mu.Lock()
	w.reload = true
	w.mu.Unlock()
	w.Notify()
}

// RefreshItem asks for a movie (or the show of an episode) to be fetched again
// now; one that is not matched is matched again.
func (w *Worker) RefreshItem(id int64) { w.addJob(job{itemID: id}) }

// RefreshShow asks for a show to be fetched again now.
func (w *Worker) RefreshShow(show string) { w.addJob(job{show: show}) }

// RefreshAll asks for every title to be fetched again and every unmatched file
// to be tried again.
func (w *Worker) RefreshAll() { w.addJob(job{all: true}) }

func (w *Worker) addJob(j job) {
	w.mu.Lock()
	w.jobs = append(w.jobs, j)
	w.mu.Unlock()
	w.Notify()
}

// Status reports the worker's state.
func (w *Worker) Status(ctx context.Context) Status {
	_, res := w.o.Source.Provider(ctx)
	st := Status{Enabled: res.Enabled, KeySet: res.KeySet()}
	if c, err := w.o.Store.CountMatches(ctx); err == nil {
		st.Pending, st.Unmatched, st.NeedsReview = c.Pending, c.Unmatched, c.NeedsReview
	}
	if t := w.settingTime(ctx, SettingLastRefresh); !t.IsZero() {
		st.LastRefresh = &t
	}
	w.mu.Lock()
	st.Running, st.LastError = w.running, w.lastError
	w.mu.Unlock()
	return st
}

func (w *Worker) settingTime(ctx context.Context, key string) time.Time {
	v, ok, err := w.o.Store.GetSetting(ctx, key)
	if err != nil || !ok {
		return time.Time{}
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

func (w *Worker) setSettingTime(ctx context.Context, key string, t time.Time) {
	if err := w.o.Store.SetSetting(ctx, key, strconv.FormatInt(t.Unix(), 10)); err != nil && ctx.Err() == nil {
		w.log.Warn("metadata: saving a setting", "key", key, "err", err)
	}
}

// Run does the work until ctx is cancelled: a pass at the start, then one each
// time the worker is woken or the tick comes. It returns when ctx is done.
func (w *Worker) Run(ctx context.Context) {
	if err := w.o.Store.ReleasePending(ctx); err != nil && ctx.Err() == nil {
		w.log.Warn("metadata: releasing pending files", "err", err)
	}
	w.pass(ctx)
	tick := time.NewTicker(w.o.Tick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-tick.C:
		}
		w.pass(ctx)
	}
}

// RunOnce does one pass of work now and returns. Run does the same on its own
// schedule; this is for tests and tools. It must not be called while Run is
// running.
func (w *Worker) RunOnce(ctx context.Context) { w.pass(ctx) }

func (w *Worker) setRunning(v bool) {
	w.mu.Lock()
	w.running = v
	w.mu.Unlock()
}

// noteError remembers the last problem for the status. The error text comes
// from this package's own errors, which never hold the key.
func (w *Worker) noteError(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	w.mu.Lock()
	w.lastError = err.Error()
	w.mu.Unlock()
	w.log.Warn("metadata", "err", err)
}

func (w *Worker) clearError() {
	w.mu.Lock()
	w.lastError = ""
	w.mu.Unlock()
}

// abort ends the pass: the key was refused, so asking again for each file
// would only repeat it.
func (w *Worker) abort(err error) {
	w.aborted = true
	w.noteError(err)
}

// pass is one round of work: what people asked for, what is waiting, what is
// due for a refresh.
func (w *Worker) pass(ctx context.Context) {
	w.setRunning(true)
	defer w.setRunning(false)
	w.dirs = map[string][]string{}
	w.handled = map[string]bool{}
	w.aborted = false

	if w.takeReload() {
		if err := w.o.Store.ReleasePending(ctx); err != nil && ctx.Err() == nil {
			w.noteError(err)
		}
	}
	p, _ := w.o.Source.Provider(ctx)
	did := false
	for ctx.Err() == nil && !w.aborted {
		worked := w.inbox(ctx, p)
		n := w.batch(ctx, p)
		did = did || worked || n > 0
		if !worked && n == 0 {
			break
		}
	}
	if p != nil && ctx.Err() == nil && !w.aborted {
		did = w.fetchSeasons(ctx, p) || did
		w.refreshDue(ctx, p)
		if !w.aborted {
			w.clearError()
		}
	}
	if did && ctx.Err() == nil {
		if _, err := w.o.Store.PruneMetadata(ctx); err != nil && ctx.Err() == nil {
			w.noteError(err)
		}
	}
}

func (w *Worker) takeReload() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	r := w.reload
	w.reload = false
	return r
}

// names lists a directory once per pass.
func (w *Worker) names(dir string) []string {
	if n, ok := w.dirs[dir]; ok {
		return n
	}
	n := dirNames(dir)
	w.dirs[dir] = n
	return n
}

// batch matches the files that are due, up to one batch of movies and one of
// shows, and returns how many it settled.
func (w *Worker) batch(ctx context.Context, p Provider) int {
	now := w.now().Unix()
	n := 0
	items, err := w.o.Store.DueItems(ctx, now, w.o.Batch)
	if err != nil {
		w.noteError(err)
		return 0
	}
	for _, d := range items {
		if ctx.Err() != nil || w.aborted {
			return n
		}
		if key := "i" + strconv.FormatInt(d.ID, 10); !w.handled[key] {
			w.handled[key] = true
			if w.matchMovie(ctx, p, d) {
				n++
			}
		}
	}
	shows, err := w.o.Store.DueSeries(ctx, now, w.o.Batch)
	if err != nil {
		w.noteError(err)
		return n
	}
	for _, d := range shows {
		if ctx.Err() != nil || w.aborted {
			return n
		}
		if key := "s" + d.Show; !w.handled[key] {
			w.handled[key] = true
			if w.matchSeries(ctx, p, d) {
				n++
			}
		}
	}
	return n
}

// inbox does what people asked for and what the scanner reported.
func (w *Worker) inbox(ctx context.Context, p Provider) bool {
	w.mu.Lock()
	ids := make([]int64, 0, len(w.sidecars))
	for id := range w.sidecars {
		ids = append(ids, id)
	}
	w.sidecars = map[int64]struct{}{}
	jobs := w.jobs
	w.jobs = nil
	w.mu.Unlock()

	for _, id := range ids {
		if ctx.Err() != nil || w.aborted {
			break
		}
		w.rereadLocal(ctx, p, id)
	}
	for _, j := range jobs {
		if ctx.Err() != nil || w.aborted {
			break
		}
		w.runJob(ctx, p, j)
	}
	return len(ids) > 0 || len(jobs) > 0
}

// prefetch caches the poster and backdrop of a stored title.
func (w *Worker) prefetch(ctx context.Context, in *store.TitleInput) {
	if w.o.Images == nil {
		return
	}
	for _, x := range []struct{ size, ref string }{{"w342", in.Poster}, {"w1280", in.Backdrop}} {
		if ctx.Err() != nil {
			return
		}
		if err := w.o.Images.Prefetch(ctx, x.size, x.ref); err != nil && ctx.Err() == nil {
			w.log.Debug("metadata: image prefetch", "size", x.size, "err", err)
		}
	}
}

// probeOf returns the tags and cover of an item's file, asking ffprobe once.
func (w *Worker) probeOf(ctx context.Context, it store.Item) probeInfo {
	w.mu.Lock()
	pi, ok := w.probes[it.ID]
	w.mu.Unlock()
	if ok || w.o.Probe == nil {
		return pi
	}
	res, err := w.o.Probe(ctx, it.Path)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Debug("metadata: probing for tags", "path", it.Path, "err", err)
		}
		return probeInfo{}
	}
	pi = probeInfo{tags: res.Tags, cover: res.Cover, hasCover: res.HasCover}
	w.mu.Lock()
	w.probes[it.ID] = pi
	w.mu.Unlock()
	return pi
}

// noteCover records the cover embedded in an item's file as its poster when it
// has nothing better (the store keeps sidecar art over it).
func (w *Worker) noteCover(ctx context.Context, it store.Item) {
	if pi := w.probeOf(ctx, it); pi.hasCover {
		if err := w.o.Store.SetItemEmbeddedCover(ctx, it.ID, pi.cover); err != nil && !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
			w.noteError(err)
		}
	}
}
