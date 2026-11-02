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

// refreshTitle fetches a title again, overlays the NFO next to its first file
// and stores it. A title TMDB no longer knows is left as it is and marked
// looked at, so that it is not asked for again at once.
func (w *Worker) refreshTitle(ctx context.Context, p Provider, t store.Title) error {
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
		in, err = p.Series(ctx, t.TMDBID, seasons)
		if err == nil && card.Show != "" {
			if items, ierr := w.o.Store.ShowItems(ctx, card.Show); ierr == nil && len(items) > 0 {
				dir := showDir(items[0].Path)
				nfoPath = ShowLocal(dir, w.names(dir)).NFO
				overlayNFO(in, w.readNFO(nfoPath))
			}
		}
	default:
		in, err = p.Movie(ctx, t.TMDBID)
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

// rereadTMDBTitle takes in the local sources of a title that has TMDB data. Its
// artwork is already recorded; its NFO is laid over TMDB's data, so the title is
// fetched again, but only when the NFO is not the one that was laid over it
// last: a poster that came or went, or a moved file, asks nothing of TMDB.
func (w *Worker) rereadTMDBTitle(ctx context.Context, p Provider, t store.Title, nfoPath string) {
	if p == nil {
		return
	}
	w.mu.Lock()
	was, known := w.nfoApplied[t.ID]
	w.mu.Unlock()
	if known && was == nfoStamp(nfoPath) {
		return
	}
	w.refreshTitles(ctx, p, []store.Title{t})
}

// refreshTitles refreshes each title and reports whether it got through them
// all. It stops at the first failure that may pass (TMDB out of reach); a
// refused key ends the pass.
func (w *Worker) refreshTitles(ctx context.Context, p Provider, titles []store.Title) bool {
	for _, t := range titles {
		if ctx.Err() != nil || w.aborted {
			return false
		}
		err := w.refreshTitle(ctx, p, t)
		switch {
		case err == nil:
		case IsUnauthorized(err):
			w.abort(err)
			return false
		case IsTransient(err):
			w.noteError(err)
			return false
		default:
			w.noteError(err)
		}
	}
	return true
}

// refreshDue refreshes what is due. Shows whose next episode has aired are
// always looked at. Everything else follows the -metadata-refresh period: the
// titles in TMDB's changes feed since the last poll, or, when there is no poll
// within the feed's reach, the titles whose data is old.
func (w *Worker) refreshDue(ctx context.Context, p Provider) {
	if w.o.Refresh <= 0 {
		return
	}
	now := w.now()
	if airing, err := w.o.Store.AiringTitlesDue(ctx, now, refreshBatch); err != nil {
		w.noteError(err)
	} else if len(airing) > 0 {
		w.refreshTitles(ctx, p, airing)
	}
	if ctx.Err() != nil || w.aborted {
		return
	}

	if last := w.settingTime(ctx, SettingLastRefresh); !last.IsZero() && now.Sub(last) < w.o.Refresh {
		return
	}
	w.mu.Lock()
	retryAt := w.retryRefreshAt
	w.mu.Unlock()
	if now.Before(retryAt) {
		return
	}

	var (
		titles []store.Title
		err    error
	)
	poll := w.settingTime(ctx, SettingChangesPoll)
	advancePoll := true
	if poll.IsZero() || now.Sub(poll) > ChangesWindow {
		titles, err = w.o.Store.TitlesDueForRefresh(ctx, now, 1<<20)
	} else if titles, err = w.changedTitles(ctx, p, poll, now); err != nil && !IsTransient(err) && !IsUnauthorized(err) && ctx.Err() == nil {
		// The feed cannot be used (it refuses us, or it is too long to follow): go
		// by age instead, and leave the poll time where it is, so that the next
		// refresh asks the feed for everything since then.
		w.noteError(err)
		advancePoll = false
		titles, err = w.o.Store.TitlesDueForRefresh(ctx, now, 1<<20)
	}
	if err != nil {
		if IsUnauthorized(err) {
			w.abort(err)
		} else if ctx.Err() == nil {
			w.noteError(err)
			w.setRetryRefresh(now)
		}
		return
	}
	if !w.refreshTitles(ctx, p, titles) {
		if ctx.Err() == nil && !w.aborted {
			w.setRetryRefresh(now)
		}
		return
	}
	if advancePoll {
		w.setSettingTime(ctx, SettingChangesPoll, now)
	}
	w.setSettingTime(ctx, SettingLastRefresh, now)
}

func (w *Worker) setRetryRefresh(now time.Time) {
	w.mu.Lock()
	w.retryRefreshAt = now.Add(refreshRetry)
	w.mu.Unlock()
}

// changedTitles returns the titles of the library that TMDB's changes feed
// lists between since and now.
func (w *Worker) changedTitles(ctx context.Context, p Provider, since, now time.Time) ([]store.Title, error) {
	changed := map[string]map[int64]bool{}
	for _, kind := range []string{store.KindMovie, store.KindSeries} {
		ids, err := p.Changes(ctx, kind, since, now)
		if err != nil {
			return nil, err
		}
		set := make(map[int64]bool, len(ids))
		for _, id := range ids {
			set[id] = true
		}
		changed[kind] = set
	}
	refs, err := w.o.Store.LibraryTitleRefs(ctx)
	if err != nil {
		return nil, err
	}
	var titles []store.Title
	for _, r := range refs {
		if !changed[r.Kind][r.TMDBID] {
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
// for a few hours. It reports whether it fetched anything.
func (w *Worker) fetchSeasons(ctx context.Context, p Provider) bool {
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
		if err := w.refreshTitle(ctx, p, t); err != nil {
			w.mu.Lock()
			for _, k := range keys {
				delete(w.seasonTried, k) // try again next pass
			}
			w.mu.Unlock()
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
func (w *Worker) runJob(ctx context.Context, p Provider, j job) {
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
		w.refreshTitles(ctx, p, titles)
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

func (w *Worker) refreshShow(ctx context.Context, p Provider, show string) {
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

// refreshLinked fetches a linked TMDB title again; a file without one (or whose
// title is only local) is matched again instead, by requeue, unless a person
// decided it.
func (w *Worker) refreshLinked(ctx context.Context, p Provider, titleID int64, requeue func() error) {
	if titleID != 0 {
		if t, err := w.o.Store.GetTitle(ctx, titleID); err == nil && t.TMDBID != 0 {
			if p == nil {
				return
			}
			w.refreshTitles(ctx, p, []store.Title{t})
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
func (w *Worker) rereadLocal(ctx context.Context, p Provider, id int64, done map[string]bool) {
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
	if t.TMDBID != 0 {
		// Only the NFO next to the title's first file is laid over it (see
		// refreshTitle): the other versions say nothing new about it.
		if cards, err := w.o.Store.TitleCards(ctx, []int64{t.ID}); err == nil && cards[t.ID].ItemID == e.ID {
			w.rereadTMDBTitle(ctx, p, t, local.loc.NFO)
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

func (w *Worker) rereadEpisode(ctx context.Context, p Provider, it store.Item, done map[string]bool) {
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
	if t.TMDBID != 0 {
		w.rereadTMDBTitle(ctx, p, t, show.NFO)
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
