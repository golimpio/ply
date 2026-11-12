package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"ply/internal/store"
)

// Ratings (docs/M10-RATINGS.md): IMDb's datasets (ratings, and the episodes' IMDb ids), and MDBList's answers.
// The work is done here and run by the queue (cmd/ply): this package knows nothing of jobs, and
// Pace is how a piece of work yields to playback between its steps.

// SourceMDBList is the provider name Settings.Test takes for MDBList.
const SourceMDBList = "mdblist"

// Names of the rows in the settings table that the ratings own.
const (
	SettingRatingsPrimary = "ratings_primary"
	SettingRatingsShown   = "ratings_shown" // a JSON array of sources

	// The IMDb sets keep theirs as imdb_<set>_<what>, see imdbSet.key.
	settingMDBBudget = "mdblist_budget" // "YYYY-MM-DD used limit"
)

// Periods and sizes of the ratings work.
const (
	// IMDbEvery is how often the dataset is asked after: IMDb refreshes it daily, and a rating
	// that is a week old is as good.
	IMDbEvery = 7 * 24 * time.Hour
	// DefaultDailyBudget is the number of MDBList requests a day when MDBList does not say what
	// the key allows (the free key has 1,000; the rest is left to other uses of the account).
	DefaultDailyBudget = 900
	// IMDbRatingsRefit and IMDbEpisodesRefit are the least time between two writes from a dataset that
	// did not change, for a library that did: a first scan matches titles for a while, and one write in
	// that time is what it needs. Writing ratings is cheap (a 7 MB file); mapping episodes reads 45 MB.
	IMDbRatingsRefit  = 10 * time.Minute
	IMDbEpisodesRefit = time.Hour

	// After a failure to fetch a dataset the server is left alone for imdbBackoffBase, then twice
	// that, and so on up to imdbBackoffMax.
	imdbBackoffBase = 30 * time.Minute
	imdbBackoffMax  = 24 * time.Hour
)

// ErrNoMDBList means a call needs MDBList and there is no key.
var ErrNoMDBList = errors.New("MDBList is not set up: add an MDBList key in the settings")

// DefaultRatingsPrimary is the source whose rating a card shows, until a person picks another.
const DefaultRatingsPrimary = store.RatingIMDb

// RatingPrimaries are the sources that can be the primary one (Rotten Tomatoes' audience score cannot).
var RatingPrimaries = []string{store.RatingIMDb, store.RatingRTCritics, store.RatingMetacritic, store.RatingLetterboxd,
	store.RatingTrakt, store.RatingTMDB}

// validRatingsShown checks the list of sources to show: each a known source, none twice.
func validRatingsShown(list []string) error {
	seen := map[string]bool{}
	for _, s := range list {
		if !store.ValidRatingSource(s) {
			return fmt.Errorf("%w: ratingsShown has %q, which is not a source (%s)", ErrInvalidSetting, s, strings.Join(store.RatingSources, ", "))
		}
		if seen[s] {
			return fmt.Errorf("%w: ratingsShown has %q twice", ErrInvalidSetting, s)
		}
		seen[s] = true
	}
	return nil
}

// RatingsPrefs reads the source whose rating cards show, and the sources to show (in order). A
// stored value that is no longer valid is the default.
func (s *Settings) RatingsPrefs(ctx context.Context) (primary string, shown []string) {
	primary = DefaultRatingsPrimary
	if v := s.setting(ctx, SettingRatingsPrimary); slices.Contains(RatingPrimaries, v) {
		primary = v
	}
	shown = slices.Clone(store.RatingSources)
	if raw := s.setting(ctx, SettingRatingsShown); raw != "" {
		var list []string
		if json.Unmarshal([]byte(raw), &list) == nil && validRatingsShown(list) == nil {
			shown = list
		}
	}
	return primary, shown
}

// RatingsOptions configures NewRatings.
type RatingsOptions struct {
	Store    *store.Store
	Settings *Settings
	// Dir is where the IMDb datasets are kept (<data>/cache/imdb).
	Dir string
	// IMDbURL and IMDbEpisodesURL replace the addresses of the datasets (tests, a mirror); empty is
	// DefaultIMDbRatingsURL and DefaultIMDbEpisodesURL.
	IMDbURL         string
	IMDbEpisodesURL string
	HTTPClient      *http.Client
	// Pace is called between the steps of a piece of work (the chunks of a write, a request to a
	// provider): the queue's yield to playback and browsing. nil does not wait.
	Pace   func(ctx context.Context) error
	Logger *slog.Logger
	Now    func() time.Time
	// DailyBudget is the MDBList requests a day when MDBList does not say what the key allows;
	// 0 is DefaultDailyBudget.
	DailyBudget int
}

// Ratings does the work of M10. It is safe for concurrent use.
type Ratings struct {
	o     RatingsOptions
	wake  chan struct{} // look for work now
	nudge chan struct{} // look for work soon: titles were matched, and more may come

	imdbMu sync.Mutex // one run of the IMDb job at a time

	bmu       sync.Mutex // guards the budget and syncedDay
	syncedDay string     // the UTC day MDBList's own count was last read for

	fmu   sync.Mutex    // guards fails
	fails map[int64]int // titles MDBList answered with a server error, and how many times in a row

	// What RunMDBListDue calls to list the titles due and to rate one: the methods, but for tests.
	due func(ctx context.Context, n int) ([]store.RatingsDue, error)
	run func(ctx context.Context, titleID int64) error
}

// NewRatings returns the service.
func NewRatings(o RatingsOptions) *Ratings {
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 10 * time.Minute}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.DailyBudget <= 0 {
		o.DailyBudget = DefaultDailyBudget
	}
	if o.IMDbURL == "" {
		o.IMDbURL = DefaultIMDbRatingsURL
	}
	if o.IMDbEpisodesURL == "" {
		o.IMDbEpisodesURL = DefaultIMDbEpisodesURL
	}
	r := &Ratings{o: o, wake: make(chan struct{}, 1), nudge: make(chan struct{}, 1), fails: map[int64]int{}}
	r.due, r.run = r.DueMDBList, r.RunMDBList
	return r
}

func (r *Ratings) now() time.Time { return r.o.Now() }

// Wake asks the schedule to look for work now (a key was stored, a pass matched new titles). It
// never blocks.
func (r *Ratings) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Woken is the channel Wake signals on: a receive means look for work.
func (r *Ratings) Woken() <-chan struct{} { return r.wake }

// Nudge says titles were matched: there may be IMDb ids to rate. The schedule looks once things have
// settled, not for each nudge. It never blocks.
func (r *Ratings) Nudge() {
	select {
	case r.nudge <- struct{}{}:
	default:
	}
}

// Nudged is the channel Nudge signals on.
func (r *Ratings) Nudged() <-chan struct{} { return r.nudge }

func (r *Ratings) pace(ctx context.Context) error {
	if r.o.Pace == nil {
		return ctx.Err()
	}
	return r.o.Pace(ctx)
}

// setting and settingInt read a stored value; "" and 0 when there is none.
func (r *Ratings) setting(ctx context.Context, key string) string {
	if v, ok, err := r.o.Store.GetSetting(ctx, key); err == nil && ok {
		return v
	}
	return ""
}

func (r *Ratings) settingInt(ctx context.Context, key string) int64 {
	n, _ := strconv.ParseInt(r.setting(ctx, key), 10, 64)
	return n
}

func (r *Ratings) setInt(ctx context.Context, key string, v int64) error {
	return r.o.Store.SetSetting(ctx, key, strconv.FormatInt(v, 10))
}

// RatingsAvailable lists the sources this install can produce a rating from, in the fixed order:
// none when nothing may leave the machine; IMDb always (its dataset needs no key); TMDB's with a
// TMDB key; the four of MDBList with its key.
func (s *Settings) RatingsAvailable(ctx context.Context) []string {
	res := s.Resolve(ctx)
	if !res.Enabled {
		return []string{}
	}
	out := []string{store.RatingIMDb}
	if res.MDBListKeySet() {
		out = append(out, store.MDBListSources...)
	}
	if res.KeySet() {
		out = append(out, store.RatingTMDB)
	}
	slices.SortStableFunc(out, func(a, b string) int {
		return slices.Index(store.RatingSources, a) - slices.Index(store.RatingSources, b)
	})
	return out
}

// ---- IMDb ----

// imdbSet is one of the two IMDb files: where it is kept, what its first line is, and the names of
// the settings that remember what is known of the copy.
type imdbSet struct {
	name   string // in the names of the settings
	file   string
	header string
	refit  time.Duration // the least time between two writes for a library that changed
}

var (
	setRatings  = imdbSet{"ratings", IMDbRatingsFile, imdbRatingsHeader, IMDbRatingsRefit}
	setEpisodes = imdbSet{"episodes", IMDbEpisodesFile, imdbEpisodesHeader, IMDbEpisodesRefit}
)

// key is the name of one of a set's settings: etag and modified (what the server said of the copy),
// checked (when the server last answered), updated (the copy's own date), rows (its data rows),
// fails and retry (the outage backoff), applied and ran (what was written from it, and when), stamp.
func (d imdbSet) key(what string) string { return "imdb_" + d.name + "_" + what }

func (r *Ratings) urlOf(d imdbSet) string {
	if d == setEpisodes {
		return r.o.IMDbEpisodesURL
	}
	return r.o.IMDbURL
}

func (r *Ratings) path(d imdbSet) string { return filepath.Join(r.o.Dir, d.file) }

func (r *Ratings) have(d imdbSet) bool {
	_, err := os.Stat(r.path(d))
	return err == nil
}

// IMDbDatasetUpdatedAt is the date of the ratings dataset the ratings come from; ok is false before
// the first download.
func (r *Ratings) IMDbDatasetUpdatedAt(ctx context.Context) (time.Time, bool) {
	n := r.settingInt(ctx, setRatings.key("updated"))
	if n == 0 {
		return time.Time{}, false
	}
	return time.Unix(n, 0), true
}

// sumOf is the sum of the hashes of the strings add is given: it does not depend on their order.
func sumOf(each func(add func(string))) uint64 {
	var sum uint64
	each(func(s string) {
		h := fnv.New64a()
		h.Write([]byte(s))
		sum += h.Sum64()
	})
	return sum
}

// libraryFingerprint identifies the IMDb ids of the library and the titles that have them: the
// ratings written for one fingerprint are stale for another.
func libraryFingerprint(lib store.IMDbLibrary) string {
	sum := sumOf(func(add func(string)) {
		for tconst, ids := range lib.Titles {
			for _, id := range ids {
				add("t/" + tconst + "/" + strconv.FormatInt(id, 10))
			}
		}
		for tconst := range lib.Episodes {
			add("e/" + tconst)
		}
	})
	return fmt.Sprintf("%d:%016x", lib.Len(), sum)
}

// showsFingerprint identifies the shows the episodes are mapped for, and how many episodes each
// has stored: a show that came, or got episodes, is one the mapping was not made for.
func showsFingerprint(shows []store.IMDbShow) string {
	sum := sumOf(func(add func(string)) {
		for _, s := range shows {
			add(strconv.FormatInt(s.TitleID, 10) + "/" + s.IMDbID + "/" + strconv.Itoa(s.Episodes))
		}
	})
	return fmt.Sprintf("%d:%016x", len(shows), sum)
}

// token says which copy of a set is here: what the server called it, else its date. What was written
// from a copy is recorded as the library's fingerprint, "|", and this.
func (r *Ratings) token(ctx context.Context, d imdbSet) string {
	for _, what := range []string{"etag", "modified", "updated"} {
		if v := r.setting(ctx, d.key(what)); v != "" {
			return v
		}
	}
	return ""
}

// fetchDue: the server is to be asked after the set. That is when the copy is not here, or the
// server was last asked a week ago; and not before the retry time that an outage or a refused file
// set.
func (r *Ratings) fetchDue(ctx context.Context, d imdbSet) bool {
	now := r.now()
	if now.Unix() < r.settingInt(ctx, d.key("retry")) {
		return false
	}
	return !r.have(d) || now.Sub(time.Unix(r.settingInt(ctx, d.key("checked")), 0)) >= IMDbEvery
}

// applyDue: what was written from the set is not what there is to write (the library's ids or the
// copy changed, or a write failed half way), and the set's refit time has passed since the last
// write, so that a library being scanned is not read again at every look. A new copy comes a week
// after the last write, and a write that failed leaves the time of the last that did not: neither waits.
func (r *Ratings) applyDue(ctx context.Context, d imdbSet, fingerprint string) bool {
	if !r.have(d) || r.setting(ctx, d.key("applied")) == fingerprint+"|"+r.token(ctx, d) {
		return false
	}
	return r.now().Sub(time.Unix(r.settingInt(ctx, d.key("ran")), 0)) >= d.refit
}

// IMDbDue reports whether the IMDb job has something to do: a dataset to ask after, or ids in the
// library that nothing was written for. It is false when nothing may leave the machine, and when
// the library has no IMDb id at all. The episode dataset counts only when a show is to be mapped.
func (r *Ratings) IMDbDue(ctx context.Context) bool {
	if !r.o.Settings.Resolve(ctx).Enabled {
		return false
	}
	lib, err := r.o.Store.IMDbLibrary(ctx)
	if err != nil || lib.Len() == 0 {
		return false
	}
	if r.fetchDue(ctx, setRatings) || r.applyDue(ctx, setRatings, libraryFingerprint(lib)) {
		return true
	}
	shows, err := r.o.Store.IMDbShows(ctx)
	if err != nil || len(shows) == 0 {
		return false
	}
	return r.fetchDue(ctx, setEpisodes) || r.applyDue(ctx, setEpisodes, showsFingerprint(shows))
}

// RunIMDb brings what IMDb's datasets give up to date. For the shows of the library (those with an
// IMDb id, in the aired order) it maps the episodes' IMDb ids from the episode dataset; then it
// stores the ratings of the library's titles and episodes from the ratings dataset. Each dataset is
// asked after when a week has passed (a conditional request: a 304 leaves the file alone), read as
// a stream, and written in chunks, between which the job yields. The episode dataset is not asked
// after unless a show needs it. It does nothing, and asks for nothing, when the privacy switch is
// off or the library has no IMDb id. A dataset that was written from and has not changed is not
// read again, unless the library changed.
//
// A failure to get the episode dataset does not stop the ratings; both are returned.
func (r *Ratings) RunIMDb(ctx context.Context) error {
	r.imdbMu.Lock()
	defer r.imdbMu.Unlock()
	if !r.o.Settings.Resolve(ctx).Enabled {
		return nil
	}
	lib, err := r.o.Store.IMDbLibrary(ctx)
	if err != nil {
		return err
	}
	if lib.Len() == 0 {
		return nil
	}
	shows, err := r.o.Store.IMDbShows(ctx)
	if err != nil {
		return err
	}
	var epErr error
	if len(shows) > 0 {
		if epErr = r.runEpisodes(ctx, shows); epErr != nil && ctx.Err() != nil {
			return epErr
		}
		// The mapping may have given the library episodes to rate.
		if lib, err = r.o.Store.IMDbLibrary(ctx); err != nil {
			return err
		}
	}
	return errors.Join(epErr, r.runRatings(ctx, lib))
}

// syncSet asks for the copy of a set if it is due, and keeps the one we have when the server cannot
// be reached, or gives something that is not the dataset, or far less of it than before. It says
// whether there is a copy afterwards; the error is the reason for there being none.
//
// After an outage the server is left alone for 30 minutes, then an hour, then double that again,
// up to a day. After a refused file, for a week.
func (r *Ratings) syncSet(ctx context.Context, d imdbSet) (bool, error) {
	if !r.fetchDue(ctx, d) {
		return r.have(d), nil
	}
	var v imdbValidators
	if r.have(d) {
		v = imdbValidators{ETag: r.setting(ctx, d.key("etag")), LastModified: r.setting(ctx, d.key("modified"))}
	}
	// Less than half of the rows of the last copy is not a dataset that shrank: it is a bad mirror.
	minRows := int(r.settingInt(ctx, d.key("rows")) / 2)
	got, err := fetchIMDb(ctx, r.o.HTTPClient, r.urlOf(d), r.path(d), d.header, minRows, v)
	if err == nil {
		return true, r.recordFetch(ctx, d, got)
	}
	have := r.have(d)
	if ctx.Err() != nil {
		return have, err
	}
	wait := imdbBackoff(int(r.settingInt(ctx, d.key("fails"))) + 1)
	if refused := errors.Is(err, ErrIMDbFormat) || errors.Is(err, ErrIMDbShort); refused {
		wait = IMDbEvery
	} else if serr := r.setInt(ctx, d.key("fails"), r.settingInt(ctx, d.key("fails"))+1); serr != nil {
		return have, serr
	}
	if serr := r.setInt(ctx, d.key("retry"), r.now().Add(wait).Unix()); serr != nil {
		return have, serr
	}
	r.o.Logger.Warn("ratings: IMDb dataset not fetched", "set", d.name, "have_a_copy", have, "again_in", wait, "err", err)
	if have {
		return true, nil
	}
	return false, err
}

// imdbBackoff is how long to leave the server alone after its nth failure in a row.
func imdbBackoff(fails int) time.Duration {
	return min(imdbBackoffBase<<min(max(fails, 1)-1, 6), imdbBackoffMax)
}

// recordFetch remembers what the server said of the copy that was just fetched (or confirmed).
func (r *Ratings) recordFetch(ctx context.Context, d imdbSet, got imdbFetch) error {
	if !got.NotModified {
		updated := parseHTTPTime(got.LastModified)
		if updated.IsZero() {
			updated = r.now()
		}
		for key, val := range map[string]string{"etag": got.ETag, "modified": got.LastModified} {
			if val == "" {
				if err := r.o.Store.DeleteSetting(ctx, d.key(key)); err != nil {
					return err
				}
			} else if err := r.o.Store.SetSetting(ctx, d.key(key), val); err != nil {
				return err
			}
		}
		if err := r.setInt(ctx, d.key("updated"), updated.Unix()); err != nil {
			return err
		}
		if err := r.setInt(ctx, d.key("rows"), int64(got.Rows)); err != nil {
			return err
		}
	}
	for _, key := range []string{"fails", "retry"} {
		if err := r.o.Store.DeleteSetting(ctx, d.key(key)); err != nil {
			return err
		}
	}
	return r.setInt(ctx, d.key("checked"), r.now().Unix())
}

// discard drops a copy that turned out not to be the dataset when it was read (it was fetched
// whole and checked, so something changed it on disk), with what is known of it, so that the next
// look fetches it again.
func (r *Ratings) discard(ctx context.Context, d imdbSet) {
	os.Remove(r.path(d))
	for _, what := range []string{"etag", "modified", "checked"} {
		r.o.Store.DeleteSetting(ctx, d.key(what))
	}
}

// runEpisodes maps the episodes of the shows from the episode dataset, if it changed or the shows
// did.
func (r *Ratings) runEpisodes(ctx context.Context, shows []store.IMDbShow) error {
	d := setEpisodes
	have, err := r.syncSet(ctx, d)
	if err != nil || !have {
		return err
	}
	fingerprint := showsFingerprint(shows)
	token := r.token(ctx, d)
	if r.setting(ctx, d.key("applied")) == fingerprint+"|"+token {
		return nil
	}
	f, err := os.Open(r.path(d))
	if err != nil {
		return err
	}
	defer f.Close()
	parents := make(map[string]struct{}, len(shows))
	for _, s := range shows {
		parents[s.IMDbID] = struct{}{}
	}
	eps, stats, err := ParseIMDbEpisodes(ctx, f, func(parent []byte) bool {
		_, ok := parents[string(parent)]
		return ok
	}, r.pace)
	if err != nil {
		if errors.Is(err, ErrIMDbFormat) {
			r.discard(ctx, d)
		}
		return err
	}
	plan, err := r.o.Store.PlanEpisodeIMDb(ctx, eps)
	if err != nil {
		return err
	}
	if err := r.o.Store.ApplyEpisodeIMDb(ctx, plan, r.pace); err != nil {
		return err
	}
	mapped := plan.Stats
	if err := r.setInt(ctx, d.key("ran"), r.now().Unix()); err != nil {
		return err
	}
	if err := r.o.Store.SetSetting(ctx, d.key("applied"), fingerprint+"|"+token); err != nil {
		return err
	}
	r.o.Logger.Info("ratings: episodes' IMDb ids mapped", "shows", mapped.Shows, "seasons", mapped.Seasons,
		"seasons_left", mapped.Skipped, "mapped", mapped.Mapped, "cleared", mapped.Cleared, "rows", stats.Rows, "kept", stats.Kept)
	return nil
}

// runRatings stores the ratings of the library from the ratings dataset, if it changed or the
// library did.
func (r *Ratings) runRatings(ctx context.Context, lib store.IMDbLibrary) error {
	d := setRatings
	have, err := r.syncSet(ctx, d)
	if err != nil || !have {
		return err
	}
	applied := libraryFingerprint(lib) + "|" + r.token(ctx, d)
	if r.setting(ctx, d.key("applied")) == applied {
		return nil // the dataset is the same and so is the library: nothing to write
	}
	return r.applyIMDb(ctx, lib, applied)
}

// applyIMDb reads the ratings dataset and stores the ratings of lib's titles and episodes, then
// records applied as what they were written from.
func (r *Ratings) applyIMDb(ctx context.Context, lib store.IMDbLibrary, applied string) error {
	d := setRatings
	f, err := os.Open(r.path(d))
	if err != nil {
		return err
	}
	defer f.Close()
	want := func(tconst []byte) bool {
		if _, ok := lib.Titles[string(tconst)]; ok {
			return true
		}
		_, ok := lib.Episodes[string(tconst)]
		return ok
	}
	kept, stats, err := ParseIMDbRatings(ctx, f, want, r.pace)
	if err != nil {
		if errors.Is(err, ErrIMDbFormat) {
			r.discard(ctx, d)
		}
		return err
	}
	var titles []store.IMDbTitleRating
	var episodes []store.IMDbEpisodeRating
	for tconst, rt := range kept {
		for _, id := range lib.Titles[tconst] {
			titles = append(titles, store.IMDbTitleRating{TitleID: id, Value: rt.Value, Votes: rt.Votes, URL: IMDbURL(tconst)})
		}
		if _, ok := lib.Episodes[tconst]; ok {
			episodes = append(episodes, store.IMDbEpisodeRating{IMDbID: tconst, Value: rt.Value, Votes: rt.Votes})
		}
	}
	// Every row of this write gets a stamp later than the last one, so that what the stamp misses
	// (a title that lost its id, an id the dataset no longer has) can be pruned.
	stamp := max(r.now().Unix(), r.settingInt(ctx, d.key("stamp"))+1)
	if err := r.o.Store.SetIMDbRatings(ctx, titles, episodes, stamp, r.pace); err != nil {
		return err
	}
	// A dataset that has none of the library's ids is not a reason to take away the ratings that
	// were written: it may be a short file that got past the checks, and the ids are waiting.
	pruned := 0
	if stats.Kept > 0 {
		if pruned, err = r.o.Store.PruneIMDbRatings(ctx, stamp, r.pace); err != nil {
			return err
		}
	} else {
		r.o.Logger.Warn("ratings: the IMDb dataset has none of the library's ids; no ratings were removed", "ids", lib.Len())
	}
	if err := r.setInt(ctx, d.key("stamp"), stamp); err != nil {
		return err
	}
	if err := r.setInt(ctx, d.key("ran"), r.now().Unix()); err != nil {
		return err
	}
	if err := r.o.Store.SetSetting(ctx, d.key("applied"), applied); err != nil {
		return err
	}
	r.o.Logger.Info("ratings: IMDb ratings stored", "titles", len(titles), "episodes", len(episodes),
		"rows", stats.Rows, "skipped", stats.Skipped, "pruned", pruned)
	return nil
}

// ---- MDBList ----

// budget is the day's count of MDBList requests: kept in the settings table so that a restart
// does not forget it.
type budget struct {
	day   string // UTC
	used  int
	limit int // what MDBList said the key allows; 0 = not said
}

// capOf is the number of requests a day ply makes: a tenth less than the allowance, to leave some
// for whatever else the account is used for; or the default when MDBList said nothing.
func capOf(limit, def int) int {
	if limit > 0 {
		return limit - limit/10
	}
	return def
}

func (r *Ratings) loadBudget(ctx context.Context) budget {
	b := budget{day: r.now().UTC().Format(time.DateOnly)}
	f := strings.Fields(r.setting(ctx, settingMDBBudget))
	if len(f) != 3 {
		return b
	}
	used, err1 := strconv.Atoi(f[1])
	limit, err2 := strconv.Atoi(f[2])
	if err1 != nil || err2 != nil || used < 0 || limit < 0 {
		return b
	}
	b.limit = limit // the allowance holds across days
	if f[0] == b.day {
		b.used = used
	}
	return b
}

func (r *Ratings) saveBudget(ctx context.Context, b budget) error {
	return r.o.Store.SetSetting(ctx, settingMDBBudget, fmt.Sprintf("%s %d %d", b.day, b.used, b.limit))
}

// RemainingMDBList is how many more requests may be made today.
func (r *Ratings) RemainingMDBList(ctx context.Context) int {
	b := r.loadBudget(ctx)
	return max(capOf(b.limit, r.o.DailyBudget)-b.used, 0)
}

// reserve counts one request against the day. The first of a UTC day reads the key's allowance and
// use from MDBList (/user), which is the count that counts; it is false when the day's budget is
// spent.
func (r *Ratings) reserve(ctx context.Context, c *MDBListClient) (bool, error) {
	r.bmu.Lock()
	defer r.bmu.Unlock()
	b := r.loadBudget(ctx)
	if r.syncedDay != b.day {
		lim, err := c.Limits(ctx)
		r.o.Settings.NoteMDBList(err)
		switch {
		case err == nil:
			if lim.Limit > 0 {
				b.limit, b.used = lim.Limit, lim.Used
			}
			r.syncedDay = b.day
		default:
			if _, daily := IsMDBListDaily(err); !daily {
				return false, err
			}
			// The allowance is gone: the count says so, whatever it was.
			b.used = max(b.used, capOf(b.limit, r.o.DailyBudget))
			r.syncedDay = b.day
		}
	}
	if b.used >= capOf(b.limit, r.o.DailyBudget) {
		return false, r.saveBudget(ctx, b)
	}
	b.used++
	return true, r.saveBudget(ctx, b)
}

// exhaust records that MDBList says the day's allowance is used up.
func (r *Ratings) exhaust(ctx context.Context) {
	r.bmu.Lock()
	defer r.bmu.Unlock()
	b := r.loadBudget(ctx)
	b.used = max(b.used, capOf(b.limit, r.o.DailyBudget))
	if err := r.saveBudget(ctx, b); err != nil {
		r.o.Logger.Warn("ratings: saving the MDBList count", "err", err)
	}
}

// DueMDBList lists up to batch library titles that MDBList is to be asked about (no ratings yet,
// or older than 14 days, 7 for a title released in the last 90), never more than the day's
// budget has left. It is empty when nothing may leave the machine, there is no MDBList key, or
// MDBList refused the key.
func (r *Ratings) DueMDBList(ctx context.Context, batch int) ([]store.RatingsDue, error) {
	client, _ := r.o.Settings.MDBList(ctx)
	if client == nil || r.o.Settings.MDBListRefused() {
		return nil, nil
	}
	n := min(batch, r.RemainingMDBList(ctx))
	if n <= 0 {
		return nil, nil
	}
	return r.o.Store.DueRatings(ctx, r.now(), n)
}

// How many times in a row one title may be answered with a server error before it is given up on
// (marked as asked, to be asked again in its time), and how many titles in a row before the run
// stops.
const (
	mdblistTitleFailures = 2
	mdblistRunFailures   = 3
)

// serverFault reports whether err is MDBList answering one request badly (a 5xx, a timeout status, a
// body that is not JSON) rather than being out of reach or saying no to us: not the network (no
// answer at all), not a limit (429) and not the key.
func serverFault(err error) bool {
	var me *MDBListError
	return errors.As(err, &me) && me.Transient && me.Status != 0 && me.Status != http.StatusTooManyRequests
}

// noteFailure counts one more server error for the title, and returns how many in a row it has had.
func (r *Ratings) noteFailure(titleID int64) int {
	r.fmu.Lock()
	defer r.fmu.Unlock()
	r.fails[titleID]++
	return r.fails[titleID]
}

func (r *Ratings) clearFailures(titleID int64) {
	r.fmu.Lock()
	defer r.fmu.Unlock()
	delete(r.fails, titleID)
}

// RunMDBList asks MDBList for one title's ratings (by its TMDB id, else by its IMDb id; the id
// and the key are all that is sent) and stores the answer, also when there is none, so that the
// title is not asked about again at once. It does nothing when nothing may leave the machine,
// there is no key, or the budget of the day is spent. The failures:
//   - a refused key (IsUnauthorized) is an error that retrying does not fix;
//   - one that may pass (the network, a limit, MDBList being down: IsTransient) is an error to
//     retry, and is not charged to the budget when no request was sent;
//   - a server error for this title (serverFault) is that error too, the first time; the second
//     time in a row it is treated as the next case, so that a title MDBList always fails on is
//     given up on, and does not come back at every run;
//   - the day's allowance being used up ends the day's asking, without an error;
//   - any other answer is MDBList refusing this title, which asking again does not change: the
//     title is recorded as asked about (its ratings stay as they were) and the run goes on to
//     the next one, so that one title cannot hold up the rest.
func (r *Ratings) RunMDBList(ctx context.Context, titleID int64) error {
	client, _ := r.o.Settings.MDBList(ctx)
	if client == nil || r.o.Settings.MDBListRefused() {
		return nil
	}
	t, err := r.o.Store.GetTitle(ctx, titleID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	media := MDBListMovie
	if t.Kind == store.KindSeries {
		media = MDBListShow
	}
	// By TMDB id first, then IMDb, then TVDB: MDBList maps each, and a show from TVDB need not
	// carry a TMDB or an IMDb id at all.
	var lookups []func(context.Context) (*MDBListTitle, error)
	if t.TMDBID > 0 {
		lookups = append(lookups, func(ctx context.Context) (*MDBListTitle, error) {
			return client.ByTMDB(ctx, media, t.TMDBID)
		})
	}
	if reIMDbID.MatchString(t.IMDbID) {
		lookups = append(lookups, func(ctx context.Context) (*MDBListTitle, error) {
			return client.ByIMDb(ctx, media, t.IMDbID)
		})
	}
	if t.TVDBID > 0 {
		lookups = append(lookups, func(ctx context.Context) (*MDBListTitle, error) {
			return client.ByTVDB(ctx, media, t.TVDBID)
		})
	}
	var found *MDBListTitle
	for _, lookup := range lookups {
		// While MDBList has us paused for longer than the client waits, no request is sent: and
		// none is counted. The day's allowance gone is a pause to the end of the day, and the title
		// stays due.
		if err := client.Gate(); err != nil {
			if _, daily := IsMDBListDaily(err); daily {
				return nil
			}
			return err
		}
		ok, err := r.reserve(ctx, client)
		if err != nil {
			return err
		}
		if !ok {
			return nil // the day's budget is spent; the title stays due
		}
		mt, err := lookup(ctx)
		r.o.Settings.NoteMDBList(err)
		if err == nil {
			found = mt
			break
		}
		if _, daily := IsMDBListDaily(err); daily {
			r.exhaust(ctx)
			return nil
		}
		switch {
		case ctx.Err() != nil, IsUnauthorized(err):
			return err
		case IsNotFound(err):
			continue
		case IsTransient(err):
			if !serverFault(err) || r.noteFailure(titleID) < mdblistTitleFailures {
				return err
			}
		}
		r.clearFailures(titleID)
		r.o.Logger.Warn("ratings: MDBList would not give a title's ratings; it is asked again later", "title", titleID, "err", err)
		return r.o.Store.TouchRatingsChecked(ctx, titleID, r.now().Unix())
	}
	r.clearFailures(titleID)
	now := r.now().Unix()
	var ratings []store.Rating
	if found != nil {
		ratings = found.StoreRatings(now)
	}
	if err := r.o.Store.ReplaceMDBListRatings(ctx, titleID, ratings, now); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return nil
}

// RunMDBListDue goes through the titles that are due, batch at a time, until none is left, the day's
// budget is spent, or the run has to stop. before is called ahead of each title (the job reports
// progress and yields to playback there); its error stops the run.
//
// A title MDBList answers with a server error does not hold up the ones behind it: the run goes
// on, and RunMDBList gives up on the title when it fails the second time. The run stops, with the
// error, for a failure that is not the title's (the network, a limit, the key), and when
// mdblistRunFailures titles in a row fail with a server error (MDBList is down). A title is asked
// once in a run, whatever happens to it.
func (r *Ratings) RunMDBListDue(ctx context.Context, batch int, before func(ctx context.Context, done int, t store.RatingsDue) error) error {
	tried := map[int64]bool{}
	failed := 0 // titles in a row answered with a server error
	for done := 0; ; {
		due, err := r.due(ctx, batch)
		if err != nil {
			return err
		}
		due = slices.DeleteFunc(due, func(t store.RatingsDue) bool { return tried[t.ID] })
		if len(due) == 0 {
			return nil
		}
		for _, t := range due {
			tried[t.ID] = true
			if before != nil {
				if err := before(ctx, done, t); err != nil {
					return err
				}
			}
			err := r.run(ctx, t.ID)
			switch {
			case err == nil:
				failed = 0
			case serverFault(err) && ctx.Err() == nil:
				failed++
				r.o.Logger.Warn("ratings: MDBList failed on a title; going on", "title", t.ID, "in_a_row", failed, "err", err)
				if failed >= mdblistRunFailures {
					return err
				}
			default:
				return err
			}
			done++
		}
	}
}
