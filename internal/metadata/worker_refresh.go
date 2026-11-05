package metadata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"ply/internal/store"
)

// refreshTitle fetches a title again from where it came from, overlays the NFO
// next to its first file and stores it. A title its provider no longer knows is left
// as it is and marked looked at, so that it is not asked for again at once. errSkip
// when the provider it came from is not set up.
func (w *Worker) refreshTitle(ctx context.Context, p *Providers, t store.Title) error {
	cards, err := w.o.Store.TitleCards(ctx, []int64{t.ID})
	if err != nil {
		return err
	}
	card := cards[t.ID] // zero when the title is in no one's library
	var (
		in      *store.TitleInput
		nfoPath string // the NFO laid over the title
	)
	switch t.Kind {
	case store.KindSeries:
		seasons, _ := w.o.Store.SeasonsWithFiles(ctx, t.ID)
		in, err = w.fetchSeries(ctx, p, t, seasons)
		if err == nil && card.Show != "" {
			if items, ierr := w.o.Store.ShowItems(ctx, card.Show); ierr == nil && len(items) > 0 {
				dir := showDir(items[0].Path)
				nfoPath = ShowLocal(dir, w.names(dir)).NFO
				overlayNFO(in, w.readNFO(nfoPath))
			}
		}
	default:
		tm := p.Movies()
		if tm == nil || t.TMDBID == 0 {
			return errSkip
		}
		in, err = tm.Movie(ctx, t.TMDBID)
		if err == nil && card.ItemID != 0 {
			if e, gerr := w.o.Store.GetEntry(ctx, card.ItemID); gerr == nil {
				local := w.localMovie(ctx, e.Item, w.names)
				nfoPath = local.loc.NFO
				overlayNFO(in, local.nfo)
			}
		}
	}
	if err != nil {
		if IsNotFound(err) {
			return w.o.Store.TouchTitle(ctx, t.ID, w.now().Unix())
		}
		return err
	}
	id, err := w.o.Store.UpsertTitle(ctx, in)
	if err != nil {
		return err
	}
	w.noteNFO(id, nfoPath)
	w.prefetch(ctx, in)
	return nil
}

// nfoStamp identifies the state of an NFO file ("" for none), so that a change
// to it can be told from a notification about something else next to it.
func nfoStamp(path string) string {
	if path == "" {
		return ""
	}
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano())
}

// noteNFO records that the NFO at path is what is laid over title id now.
func (w *Worker) noteNFO(id int64, path string) {
	stamp := nfoStamp(path)
	w.mu.Lock()
	w.nfoApplied[id] = stamp
	w.mu.Unlock()
}

// rereadRemoteTitle takes in the local sources of a title that has TMDB or TVDB data.
// Its artwork is already recorded; its NFO is laid over the provider's data, so the
// title is fetched again, but only when the NFO is not the one that was laid over it
// last: a poster that came or went, or a moved file, asks nothing of the provider.
func (w *Worker) rereadRemoteTitle(ctx context.Context, p *Providers, t store.Title, nfoPath string) {
	if p == nil {
		return
	}
	w.mu.Lock()
	was, known := w.nfoApplied[t.ID]
	w.mu.Unlock()
	if known && was == nfoStamp(nfoPath) {
		return
	}
	w.refreshTitles(ctx, p, []store.Title{t}, false)
}

// refreshTitles refreshes each title and reports whether it got through them all, and whether
// the titles from TVDB were passed over because TVDB refuses the key. It stops at the first
// failure of TMDB that may pass (out of reach); a refused TMDB key ends the pass. TVDB is not so
// much in the way: a show from TVDB that cannot be refreshed because TVDB refuses the key, or
// cannot be reached, is passed over, as are the others from TVDB after it in this call, and the
// titles at TMDB go on (complete is false for an outage, so that it is tried again soon). A title
// whose provider is not set up is passed over.
//
// paced asks Pace before each title; a refresh that is one item of the inbox has been
// paced as that item, and says false.
func (w *Worker) refreshTitles(ctx context.Context, p *Providers, titles []store.Title, paced bool) (complete, tvdbRefused bool) {
	tvdbOff := false
	complete = true
	for _, t := range titles {
		if ctx.Err() != nil || w.aborted {
			return false, tvdbRefused
		}
		if tvdbOff && t.Source == store.SourceTVDB {
			continue
		}
		if paced && !w.pace(ctx) {
			return false, tvdbRefused
		}
		err := w.refreshTitle(ctx, p, t)
		// A show from TVDB is fetched from TVDB alone: what fails is TVDB's, whatever the error is.
		fromTVDB := t.Source == store.SourceTVDB || isTVDB(err)
		switch {
		case err == nil, errors.Is(err, errSkip):
		case fromTVDB && IsUnauthorized(err):
			w.noteError(err)
			tvdbOff, tvdbRefused = true, true
		case fromTVDB && IsTransient(err):
			w.noteError(err)
			tvdbOff, complete = true, false
		case IsUnauthorized(err):
			w.abort(err)
			return false, tvdbRefused
		case IsTransient(err):
			w.noteError(err)
			return false, tvdbRefused
		default:
			w.noteError(err)
		}
	}
	return complete, tvdbRefused
}

// refreshParts are the two shares of a refresh, one for each provider's titles, each with its
// own changes-feed poll time and its own time of the last refresh: a provider out of reach, or
// refusing the key, holds back its own share and nobody else's, however long it lasts.
var refreshParts = []struct{ source, poll, last string }{
	{SourceTMDB, SettingChangesPoll, SettingTMDBLastRefresh},     // movies, and the shows that are not from TVDB
	{SourceTVDB, SettingTVDBChangesPoll, SettingTVDBLastRefresh}, // the shows from TVDB
}

// refreshDue refreshes what is due. Shows whose next episode has aired are
// always looked at. Everything else follows the -metadata-refresh period, a provider
// at a time (refreshPart).
func (w *Worker) refreshDue(ctx context.Context, p *Providers) {
	if w.o.Refresh <= 0 {
		return
	}
	now := w.now()
	if airing, err := w.o.Store.AiringTitlesDue(ctx, now, refreshBatch); err != nil {
		w.noteError(err)
	} else if len(airing) > 0 {
		w.refreshTitles(ctx, p, airing, true)
	}
	if ctx.Err() != nil || w.aborted {
		return
	}

	var (
		old     []store.Title // the titles whose data is old: read once, when a part needs it
		oldRead bool
	)
	byAge := func(source string) ([]store.Title, error) {
		if !oldRead {
			var err error
			if old, err = w.o.Store.TitlesDueForRefresh(ctx, now, 1<<20); err != nil {
				return nil, err
			}
			oldRead = true
		}
		var mine []store.Title
		for _, t := range old {
			if (t.Source == store.SourceTVDB) == (source == SourceTVDB) {
				mine = append(mine, t)
			}
		}
		return mine, nil
	}
	for _, part := range refreshParts {
		if ctx.Err() != nil || w.aborted {
			return
		}
		if p.For(part.source) == nil {
			continue // not set up: nothing of its own to refresh
		}
		// A share is due once a period after its own last time (an install from before the
		// shares had each their own time has the one time of the refresh).
		last := w.settingTime(ctx, part.last)
		if last.IsZero() {
			last = w.settingTime(ctx, SettingLastRefresh)
		}
		if !last.IsZero() && now.Sub(last) < w.o.Refresh || w.refreshPutOff(part.source, now) {
			continue
		}
		if w.refreshPart(ctx, p, part.source, part.poll, now, byAge) {
			w.setSettingTime(ctx, part.last, now)
		}
	}
	if ctx.Err() == nil && !w.aborted {
		w.setLastRefresh(ctx, p)
	}
}

// setLastRefresh keeps SettingLastRefresh, the time of the last completed refresh, at the
// older of the shares' times, once every share that can be done has one.
func (w *Worker) setLastRefresh(ctx context.Context, p *Providers) {
	var oldest time.Time
	for _, part := range refreshParts {
		if p.For(part.source) == nil {
			continue
		}
		t := w.settingTime(ctx, part.last)
		if t.IsZero() {
			return // a share that has not been done yet: the refresh is not complete
		}
		if oldest.IsZero() || t.Before(oldest) {
			oldest = t
		}
	}
	if !oldest.IsZero() {
		w.setSettingTime(ctx, SettingLastRefresh, oldest)
	}
}

// refreshPart refreshes the titles of one provider that are due: those its changes feed lists
// since the last poll, or, when there is no poll within the feed's reach, those whose data is
// old. It reports whether the part is done. When it is not (the provider is out of reach, or
// refuses the key, and the titles that need it were passed over) the part is put off for a while
// (only it: the other provider's part is not held back by it) and its poll time stays where it was,
// so that the next try asks the feed for everything since then.
func (w *Worker) refreshPart(ctx context.Context, p *Providers, source, pollSetting string, now time.Time,
	byAge func(source string) ([]store.Title, error)) bool {
	var (
		titles  []store.Title
		err     error
		advance = true
	)
	poll := w.settingTime(ctx, pollSetting)
	if poll.IsZero() || now.Sub(poll) > ChangesWindow {
		titles, err = byAge(source)
	} else if titles, err = w.changedTitles(ctx, p, source, poll, now); err != nil && ctx.Err() == nil {
		switch {
		case source == SourceTVDB && IsUnauthorized(err):
			// TVDB refuses the key: the shows from TVDB wait, and what is at TMDB went on.
			w.noteError(err)
			w.putOffRefresh(source, now, tvdbRefusedDelay)
			return false
		case IsTransient(err):
			w.noteError(err)
			w.putOffRefresh(source, now, refreshRetry)
			return false
		case IsUnauthorized(err):
			w.abort(err)
			return false
		}
		// The feed cannot be used (it is too long to follow): go by age instead, and leave the poll
		// time where it is, so that the next refresh asks the feed for everything since then.
		w.noteError(err)
		advance = false
		titles, err = byAge(source)
	}
	if err != nil {
		if ctx.Err() == nil {
			w.noteError(err)
			w.putOffRefresh(source, now, refreshRetry)
		}
		return false
	}
	complete, refused := w.refreshTitles(ctx, p, titles, true)
	switch {
	case ctx.Err() != nil || w.aborted:
		return false
	case refused: // the shows from TVDB were passed over: they are not refreshed, so the poll does not move
		w.putOffRefresh(source, now, tvdbRefusedDelay)
		return false
	case !complete:
		w.putOffRefresh(source, now, refreshRetry)
		return false
	}
	if advance {
		w.setSettingTime(ctx, pollSetting, now)
	}
	return true
}

func (w *Worker) putOffRefresh(source string, now time.Time, d time.Duration) {
	w.mu.Lock()
	w.retryRefreshAt[source] = now.Add(d)
	w.mu.Unlock()
}

// refreshPutOff reports whether the part of the refresh for a provider has been put off, and not yet for long enough.
func (w *Worker) refreshPutOff(source string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return now.Before(w.retryRefreshAt[source])
}

// changedTitles returns the titles of the library that one provider's changes feed lists
// between since and now: TMDB's for movies and for the shows from TMDB, TVDB's updates
// for the shows from TVDB (source is SourceTMDB or SourceTVDB).
func (w *Worker) changedTitles(ctx context.Context, p *Providers, source string, since, now time.Time) ([]store.Title, error) {
	// changed is keyed by what the ids are: the kind for TMDB's, "tvdb" for TVDB's.
	changed := map[string]map[int64]bool{}
	feed := func(key string, prov Provider, kind string) error {
		if prov == nil {
			return nil
		}
		ids, err := prov.Changes(ctx, kind, since, now)
		if err != nil {
			return err
		}
		set := make(map[int64]bool, len(ids))
		for _, id := range ids {
			set[id] = true
		}
		changed[key] = set
		return nil
	}
	if source == SourceTVDB {
		if err := feed(SourceTVDB, p.For(SourceTVDB), store.KindSeries); err != nil {
			return nil, err
		}
	} else {
		for _, kind := range []string{store.KindMovie, store.KindSeries} {
			if err := feed(kind, p.For(SourceTMDB), kind); err != nil {
				return nil, err
			}
		}
	}
	refs, err := w.o.Store.LibraryTitleRefs(ctx)
	if err != nil {
		return nil, err
	}
	var titles []store.Title
	for _, r := range refs {
		if (r.Source == store.SourceTVDB) != (source == SourceTVDB) {
			continue // the other provider's, with its own feed
		}
		if source == SourceTVDB {
			if !changed[SourceTVDB][r.TVDBID] {
				continue
			}
		} else if !changed[r.Kind][r.TMDBID] {
			continue
		}
		t, err := w.o.Store.GetTitle(ctx, r.ID)
		if err != nil {
			continue // pruned meanwhile
		}
		titles = append(titles, t)
	}
	return titles, nil
}

// fetchSeasons fetches, at once, the seasons of matched shows that have files
// and no stored episodes. A season TMDB has nothing for is not asked for again
// for a few hours. It reports whether it fetched anything. A show from TVDB is not
// among them (SeasonsNeedingFetchAll leaves it out), so TVDB refusing its key cannot end it.
func (w *Worker) fetchSeasons(ctx context.Context, p *Providers) bool {
	need, err := w.o.Store.SeasonsNeedingFetchAll(ctx)
	if err != nil {
		w.noteError(err)
		return false
	}
	ids := make([]int64, 0, len(need))
	for id := range need {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	now := w.now()
	did := false
	for _, id := range ids {
		if ctx.Err() != nil || w.aborted {
			return did
		}
		var keys []seasonKey
		w.mu.Lock()
		for _, season := range need[id] {
			k := seasonKey{id, season}
			if t, ok := w.seasonTried[k]; !ok || now.Sub(t) >= seasonRetry {
				keys = append(keys, k)
				w.seasonTried[k] = now
			}
		}
		w.mu.Unlock()
		if len(keys) == 0 {
			continue
		}
		t, err := w.o.Store.GetTitle(ctx, id)
		if err != nil {
			continue
		}
		did = true
		if !w.pace(ctx) {
			w.mu.Lock()
			for _, k := range keys {
				delete(w.seasonTried, k) // not asked, so not tried
			}
			w.mu.Unlock()
			return did
		}
		if err := w.refreshTitle(ctx, p, t); err != nil {
			w.mu.Lock()
			for _, k := range keys {
				delete(w.seasonTried, k) // try again next pass
			}
			w.mu.Unlock()
			if errors.Is(err, errSkip) {
				continue
			}
			if IsUnauthorized(err) {
				w.abort(err)
			} else {
				w.noteError(err)
			}
			if IsTransient(err) || IsUnauthorized(err) {
				return did
			}
		}
	}
	return did
}

// runJob does a refresh somebody asked for.
func (w *Worker) runJob(ctx context.Context, p *Providers, j job) {
	switch {
	case j.all:
		if _, err := w.o.Store.RequeueUnmatched(ctx); err != nil {
			w.noteError(err)
		}
		if p == nil {
			return
		}
		refs, err := w.o.Store.LibraryTitleRefs(ctx)
		if err != nil {
			w.noteError(err)
			return
		}
		var titles []store.Title
		for _, r := range refs {
			if t, err := w.o.Store.GetTitle(ctx, r.ID); err == nil {
				titles = append(titles, t)
			}
		}
		w.refreshTitles(ctx, p, titles, true)
	case j.show != "":
		w.refreshShow(ctx, p, j.show)
	default:
		e, err := w.o.Store.GetEntry(ctx, j.itemID)
		if err != nil {
			return
		}
		if e.Kind == store.KindEpisode {
			w.refreshShow(ctx, p, e.Show)
			return
		}
		w.refreshLinked(ctx, p, e.TitleID, func() error {
			if e.MatchState == store.MatchPending || e.MatchState == store.MatchUnmatched {
				return w.o.Store.SetItemMatch(ctx, e.ID, store.Match{State: store.MatchPending})
			}
			return nil
		})
	}
}

func (w *Worker) refreshShow(ctx context.Context, p *Providers, show string) {
	sm, err := w.o.Store.GetSeriesMatch(ctx, show)
	if err != nil {
		return
	}
	w.refreshLinked(ctx, p, sm.TitleID, func() error {
		if sm.State == store.MatchPending || sm.State == store.MatchUnmatched {
			return w.o.Store.SetSeriesMatch(ctx, show, store.Match{State: store.MatchPending})
		}
		return nil
	})
}

// refreshLinked fetches a linked TMDB or TVDB title again; a file without one (or whose
// title is only local) is matched again instead, by requeue, unless a person
// decided it.
func (w *Worker) refreshLinked(ctx context.Context, p *Providers, titleID int64, requeue func() error) {
	if titleID != 0 {
		if t, err := w.o.Store.GetTitle(ctx, titleID); err == nil && hasRemote(t) {
			if p == nil {
				return
			}
			w.refreshTitles(ctx, p, []store.Title{t}, false)
			return
		}
	}
	if err := requeue(); err != nil && !errors.Is(err, store.ErrNotFound) {
		w.noteError(err)
	}
}

// rereadLocal reads the local sources of one item again: its artwork, and its
// NFO text over the title it has. Nothing is matched from scratch. done holds
// the shows already taken in by this batch: the 40 episodes of a show that
// moved are one show to read again, not 40.
func (w *Worker) rereadLocal(ctx context.Context, p *Providers, id int64, done map[string]bool) {
	e, err := w.o.Store.GetEntry(ctx, id)
	if err != nil {
		return
	}
	if e.Kind == store.KindEpisode {
		w.rereadEpisode(ctx, p, e.Item, done)
		return
	}
	local := w.localMovie(ctx, e.Item, dirNames)
	if e.TitleID == 0 {
		// An NFO may have appeared for a file nothing was found for.
		if e.MatchState == store.MatchPending || e.MatchState == store.MatchUnmatched {
			if err := w.o.Store.SetItemMatch(ctx, e.ID, store.Match{State: store.MatchPending}); err != nil {
				w.noteError(err)
			}
		}
		return
	}
	t, err := w.o.Store.GetTitle(ctx, e.TitleID)
	if err != nil {
		return
	}
	if hasRemote(t) {
		// Only the NFO next to the title's first file is laid over it (see
		// refreshTitle): the other versions say nothing new about it.
		if cards, err := w.o.Store.TitleCards(ctx, []int64{t.ID}); err == nil && cards[t.ID].ItemID == e.ID {
			w.rereadRemoteTitle(ctx, p, t, local.loc.NFO)
		}
		return
	}
	if in, ok := localTitle(store.KindMovie, local.nfo, w.tagsFor(ctx, e.Item, local.nfo)); ok {
		in.ID = t.ID
		if _, err := w.o.Store.UpsertTitle(ctx, in); err != nil {
			w.noteError(err)
		}
	}
}

func (w *Worker) rereadEpisode(ctx context.Context, p *Providers, it store.Item, done map[string]bool) {
	loc := EpisodeLocal(it.Path, dirNames(filepath.Dir(it.Path)))
	w.setArt(ctx, it.ID, store.Art{Poster: it.LocalPoster, Backdrop: it.LocalBackdrop, Thumb: loc.Thumb})
	if it.Show == "" || done["s"+it.Show] {
		return
	}
	done["s"+it.Show] = true
	dir := showDir(it.Path)
	show := ShowLocal(dir, dirNames(dir))
	if err := w.o.Store.SetSeriesArt(ctx, it.Show, store.Art{Poster: show.Poster, Backdrop: show.Backdrop}); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		w.noteError(err)
	}
	sm, err := w.o.Store.GetSeriesMatch(ctx, it.Show)
	if err != nil || sm.TitleID == 0 {
		return
	}
	t, err := w.o.Store.GetTitle(ctx, sm.TitleID)
	if err != nil {
		return
	}
	if hasRemote(t) {
		w.rereadRemoteTitle(ctx, p, t, show.NFO)
		return
	}
	items, err := w.o.Store.ShowItems(ctx, it.Show)
	if err != nil {
		return
	}
	if in, ok := localTitle(store.KindSeries, w.readNFO(show.NFO), w.showTags(ctx, items)); ok {
		in.ID = t.ID
		in.Seasons = w.localSeasons(items, dirNames)
		if _, err := w.o.Store.UpsertTitle(ctx, in); err != nil {
			w.noteError(err)
		}
	}
}
