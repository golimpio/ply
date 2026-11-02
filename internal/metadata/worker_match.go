package metadata

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"ply/internal/store"
)

// movieLocal is what sits next to a movie file.
type movieLocal struct {
	nfo NFO
	loc Local
}

// localMovie finds the NFO and artwork next to a movie, records the artwork,
// and reads the NFO.
func (w *Worker) localMovie(ctx context.Context, it store.Item, names func(dir string) []string) movieLocal {
	loc := MovieLocal(it.Path, names(filepath.Dir(it.Path)))
	w.setArt(ctx, it.ID, store.Art{Poster: loc.Poster, Backdrop: loc.Backdrop, Thumb: it.LocalThumb})
	return movieLocal{nfo: w.readNFO(loc.NFO), loc: loc}
}

func (w *Worker) setArt(ctx context.Context, id int64, a store.Art) {
	if err := w.o.Store.SetItemArt(ctx, id, a); err != nil && !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
		w.noteError(err)
	}
}

// readNFO reads an NFO file; a missing path or an unreadable file is an empty NFO.
func (w *Worker) readNFO(path string) NFO {
	if path == "" {
		return NFO{}
	}
	n, err := ReadNFO(path)
	if err != nil {
		w.log.Debug("metadata: reading an NFO", "path", path, "err", err)
		return NFO{}
	}
	return n
}

// movieQuery is the query for a movie: its NFO beats the file name.
func movieQuery(it store.Item, n NFO) Query {
	q := MovieQuery(it.Title, it.Year, it.Path)
	if n.Title != "" {
		q.Title = n.Title
		if n.Year != 0 {
			q.Year = n.Year
		}
	}
	q.Hints = n.IDs.fill(q.Hints)
	return q
}

// failed handles an error from TMDB or the network for a file that was due. A
// refused key ends the pass. Anything else is put off with a growing delay: a
// failure never makes a file unmatched. It reports whether the file was settled.
func (w *Worker) failed(ctx context.Context, err error, attempts int, deferFn func(next int64) error) bool {
	if ctx.Err() != nil {
		return false
	}
	if IsUnauthorized(err) {
		w.abort(err)
		return false
	}
	w.noteError(err)
	next := w.now().Add(RetryDelay(err, attempts+1)).Unix()
	if derr := deferFn(next); derr != nil && !errors.Is(derr, store.ErrNotFound) {
		w.noteError(derr)
		return false
	}
	return true
}

// adoptLocal makes a title built from local files update in place the local
// title the file already has, rather than add another.
func (w *Worker) adoptLocal(ctx context.Context, existing int64, in *store.TitleInput) {
	if existing == 0 {
		return
	}
	if t, err := w.o.Store.GetTitle(ctx, existing); err == nil && t.TMDBID == 0 && t.Kind == in.Kind {
		in.ID = t.ID
	}
}

// linkMovie stores a title and links the movie to it in one step that gives way
// to a person's decision: if the item is no longer pending (somebody fixed it
// while TMDB was being asked) nothing is written. It reports whether it wrote.
//
// nfoPath is the NFO that was laid over a TMDB title (see noteNFO), "" for none.
func (w *Worker) linkMovie(ctx context.Context, id int64, in *store.TitleInput, m store.Match, nfoPath string) bool {
	titleID, ok, err := w.o.Store.LinkItemTitle(ctx, id, in, m, store.MatchPending)
	if err != nil && !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
		w.noteError(err)
	}
	if ok && in.TMDBID != 0 {
		w.noteNFO(titleID, nfoPath)
	}
	return ok
}

func (w *Worker) linkSeries(ctx context.Context, show string, in *store.TitleInput, m store.Match, nfoPath string) bool {
	titleID, ok, err := w.o.Store.LinkSeriesTitle(ctx, show, in, m, store.MatchPending)
	if err != nil && !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
		w.noteError(err)
	}
	if ok && in.TMDBID != 0 {
		w.noteNFO(titleID, nfoPath)
	}
	return ok
}

// matchMovie settles one movie that is due. It reports whether it did.
func (w *Worker) matchMovie(ctx context.Context, p Provider, d store.DueItem) bool {
	local := w.localMovie(ctx, d.Item, w.names)
	q := movieQuery(d.Item, local.nfo)
	pending := store.Match{State: store.MatchPending, Attempts: d.Attempts}

	if p == nil {
		// Nothing can be asked of TMDB. Take what the files say; the file stays
		// pending, so that it is matched once there is a key.
		pending.TitleID = d.TitleID
		pending.NextAttemptAt = w.now().Add(idleRecheck).Unix()
		if d.LocalPoster == "" {
			w.noteCover(ctx, d.Item)
		}
		if in, ok := localTitle(store.KindMovie, local.nfo, w.tagsFor(ctx, d.Item, local.nfo)); ok {
			w.adoptLocal(ctx, d.TitleID, in)
			return w.linkMovie(ctx, d.ID, in, pending, "")
		}
		ok, _ := w.o.Store.SetItemMatchIfPending(ctx, d.ID, pending)
		return ok
	}

	var (
		res Result
		in  *store.TitleInput
		err error
	)
	hints := q.Hints
	for round := 0; ; round++ {
		q.Hints = hints
		if res, err = Match(ctx, p, q); err != nil {
			return w.failed(ctx, err, d.Attempts, func(next int64) error { return w.o.Store.DeferItemMatch(ctx, d.ID, next) })
		}
		if res.State == store.MatchUnmatched && !res.ByID {
			// The tags inside the file may name the movie better than its file name.
			if tags := w.tagsFor(ctx, d.Item, local.nfo); tags.Title != "" && !strings.EqualFold(tags.Title, q.Title) {
				tq := Query{Kind: store.KindMovie, Title: tags.Title, Year: tags.Year, Hints: hints}
				tres, terr := Match(ctx, p, tq)
				if terr != nil {
					return w.failed(ctx, terr, d.Attempts, func(next int64) error { return w.o.Store.DeferItemMatch(ctx, d.ID, next) })
				}
				if tres.State == store.MatchMatched {
					res = tres
				}
			}
		}
		if res.TMDBID == 0 {
			break
		}
		if in, err = p.Movie(ctx, res.TMDBID); err != nil {
			if IsNotFound(err) && res.ByID && round == 0 {
				hints = Hints{} // the id in the name or NFO is wrong: go by the title
				continue
			}
			return w.failed(ctx, err, d.Attempts, func(next int64) error { return w.o.Store.DeferItemMatch(ctx, d.ID, next) })
		}
		break
	}

	if in == nil { // nothing matched
		m := store.Match{State: store.MatchUnmatched, Score: res.Score, Attempts: d.Attempts, TitleID: 0}
		if d.LocalPoster == "" {
			w.noteCover(ctx, d.Item)
		}
		if lt, ok := localTitle(store.KindMovie, local.nfo, w.tagsFor(ctx, d.Item, local.nfo)); ok {
			w.adoptLocal(ctx, d.TitleID, lt)
			return w.linkMovie(ctx, d.ID, lt, m, "")
		}
		ok, _ := w.o.Store.SetItemMatchIfPending(ctx, d.ID, m)
		return ok
	}

	overlayNFO(in, local.nfo)
	ok := w.linkMovie(ctx, d.ID, in, store.Match{State: store.MatchMatched, NeedsReview: res.NeedsReview, Score: res.Score}, local.loc.NFO)
	if in.Poster == "" && d.LocalPoster == "" {
		w.noteCover(ctx, d.Item)
	}
	if ok {
		w.prefetch(ctx, in)
	}
	return ok
}

// tagsFor returns the tags embedded in a movie's file, asking ffprobe only when
// the NFO has nothing to say about it. A file is probed once.
func (w *Worker) tagsFor(ctx context.Context, it store.Item, n NFO) tagsView {
	if n.HasData() {
		return tagsView{}
	}
	return w.probeOf(ctx, it).tags
}

// matchSeries settles one show that is due. It reports whether it did.
func (w *Worker) matchSeries(ctx context.Context, p Provider, d store.DueSeries) bool {
	dir := showDir(d.Path)
	loc := ShowLocal(dir, w.names(dir))
	if err := w.o.Store.SetSeriesArt(ctx, d.Show, store.Art{Poster: loc.Poster, Backdrop: loc.Backdrop}); err != nil &&
		!errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
		w.noteError(err)
	}
	nfo := w.readNFO(loc.NFO)
	items, err := w.o.Store.ShowItems(ctx, d.Show)
	if err != nil {
		w.noteError(err)
		return false
	}
	w.episodeArt(ctx, items, w.names)

	existing := int64(0)
	if sm, err := w.o.Store.GetSeriesMatch(ctx, d.Show); err == nil {
		existing = sm.TitleID
	}
	q := ShowQuery(d.Show, d.Path)
	if nfo.Title != "" {
		q.Title = nfo.Title
		if nfo.Year != 0 {
			q.Year = nfo.Year
		}
	}
	q.Hints = nfo.IDs.fill(q.Hints)
	deferFn := func(next int64) error { return w.o.Store.DeferSeriesMatch(ctx, d.Show, next) }

	localSeries := func() (*store.TitleInput, bool) {
		in, ok := localTitle(store.KindSeries, nfo, w.showTags(ctx, items))
		if ok {
			in.Seasons = w.localSeasons(items, w.names)
		}
		return in, ok
	}

	if p == nil {
		m := store.Match{State: store.MatchPending, Attempts: d.Attempts, TitleID: existing,
			NextAttemptAt: w.now().Add(idleRecheck).Unix()}
		if in, ok := localSeries(); ok {
			w.adoptLocal(ctx, existing, in)
			return w.linkSeries(ctx, d.Show, in, m, "")
		}
		ok, _ := w.o.Store.SetSeriesMatchIfPending(ctx, d.Show, m)
		return ok
	}

	seasons, _ := w.o.Store.ShowSeasons(ctx, d.Show)
	var (
		res Result
		in  *store.TitleInput
	)
	hints := q.Hints
	for round := 0; ; round++ {
		q.Hints = hints
		if res, err = Match(ctx, p, q); err != nil {
			return w.failed(ctx, err, d.Attempts, deferFn)
		}
		if res.State == store.MatchUnmatched && !res.ByID {
			if tags := w.showTags(ctx, items); tags.Show != "" && !strings.EqualFold(tags.Show, q.Title) {
				tres, terr := Match(ctx, p, Query{Kind: store.KindSeries, Title: tags.Show, Hints: hints})
				if terr != nil {
					return w.failed(ctx, terr, d.Attempts, deferFn)
				}
				if tres.State == store.MatchMatched {
					res = tres
				}
			}
		}
		if res.TMDBID == 0 {
			break
		}
		if in, err = p.Series(ctx, res.TMDBID, seasons); err != nil {
			if IsNotFound(err) && res.ByID && round == 0 {
				hints = Hints{}
				continue
			}
			return w.failed(ctx, err, d.Attempts, deferFn)
		}
		break
	}

	if in == nil {
		m := store.Match{State: store.MatchUnmatched, Score: res.Score, Attempts: d.Attempts}
		if lt, ok := localSeries(); ok {
			w.adoptLocal(ctx, existing, lt)
			return w.linkSeries(ctx, d.Show, lt, m, "")
		}
		ok, _ := w.o.Store.SetSeriesMatchIfPending(ctx, d.Show, m)
		return ok
	}

	overlayNFO(in, nfo)
	ok := w.linkSeries(ctx, d.Show, in, store.Match{State: store.MatchMatched, NeedsReview: res.NeedsReview, Score: res.Score}, loc.NFO)
	if ok {
		w.prefetch(ctx, in)
	}
	return ok
}

// showTags returns the tags of the first episode of a show whose file has any.
func (w *Worker) showTags(ctx context.Context, items []store.Item) tagsView {
	for _, it := range items {
		if t := w.probeOf(ctx, it).tags; t.Show != "" {
			return t
		}
		break // one file is enough; the rest of the show says the same
	}
	return tagsView{}
}

// episodeArt records the thumbnails next to the episode files.
func (w *Worker) episodeArt(ctx context.Context, items []store.Item, names func(dir string) []string) {
	for _, it := range items {
		if ctx.Err() != nil {
			return
		}
		loc := EpisodeLocal(it.Path, names(filepath.Dir(it.Path)))
		w.setArt(ctx, it.ID, store.Art{Poster: it.LocalPoster, Backdrop: it.LocalBackdrop, Thumb: loc.Thumb})
	}
}

// localSeasons builds seasons and episodes from the episodes' own NFO files,
// for a show that has no TMDB data.
func (w *Worker) localSeasons(items []store.Item, names func(dir string) []string) []store.Season {
	bySeason := map[int]*store.Season{}
	var order []int
	for _, it := range items {
		loc := EpisodeLocal(it.Path, names(filepath.Dir(it.Path)))
		n := w.readNFO(loc.NFO)
		if !n.HasData() {
			continue
		}
		se := bySeason[it.Season]
		if se == nil {
			se = &store.Season{Number: it.Season}
			bySeason[it.Season] = se
			order = append(order, it.Season)
		}
		se.Episodes = append(se.Episodes, store.Episode{Season: it.Season, Number: it.Episode, Name: n.Title,
			Overview: firstNonEmpty(n.Plot, n.Outline), AirDate: n.Premiered, RuntimeMin: n.RuntimeMin, Rating: n.Rating})
	}
	out := make([]store.Season, 0, len(order))
	for _, n := range order {
		out = append(out, *bySeason[n])
	}
	return out
}
