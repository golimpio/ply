package metadata

import (
	"context"
	"errors"
	"strconv"
	"time"

	"ply/internal/store"
)

// SettingTVDBSwitched is "1" once the shows that came from TMDB have been offered to
// TVDB (see switchToTVDB). SettingTVDBSwitchUpTo is, while that is under way, the highest
// title id that is offered: the shows matched after TVDB came on are not. A new TVDB key,
// or none, clears both.
const (
	SettingTVDBSwitched   = "metadata_tvdb_switched"
	SettingTVDBSwitchUpTo = "metadata_tvdb_switch_upto"
)

// switchBatch is how many shows one pass moves over to TVDB: each takes a few requests.
const switchBatch = 20

// tvdbRefusedDelay is how long a show waits when TVDB has refused the key: the key will not
// get better by itself, so it is not asked again at once.
const tvdbRefusedDelay = time.Hour

// isTVDB reports whether err is TVDB's (a *TVDBError, however it is wrapped).
func isTVDB(err error) bool {
	var te *TVDBError
	return errors.As(err, &te)
}

// tvdbKeyRefused reports whether err is TVDB refusing the key or the PIN. That is not the end of
// the pass, as a refused TMDB key is: TMDB's work (movies, the shows that are there) goes on,
// and what needs TVDB waits. The status line says "TVDB unreachable" meanwhile.
func tvdbKeyRefused(err error) bool { return IsUnauthorized(err) && isTVDB(err) }

// ErrNotTVDB means a show's episode order was asked for, and the show did not come from TVDB.
var ErrNotTVDB = errors.New("only a show from TVDB has an episode order")

// errSkip is what a fetch returns when the provider the title needs is not set up (a
// show from TVDB with the TVDB key gone, a movie with no TMDB key): there is nothing
// to ask, and the title keeps what it has.
var errSkip = errors.New("metadata: no provider for this title")

// hasRemote reports whether a title has an id at TMDB or TVDB, which is to say that it
// is not made of local files alone.
func hasRemote(t store.Title) bool { return t.TMDBID != 0 || t.TVDBID != 0 }

// fetchSeries fetches a stored show again from where it came from: TVDB for a show of
// TVDB, in its episode order, else TMDB. errSkip when that provider is not set up: a
// show does not change provider because the other one is the one that is there.
func (w *Worker) fetchSeries(ctx context.Context, p *Providers, t store.Title, seasons []int) (*store.TitleInput, error) {
	if t.Source == store.SourceTVDB {
		if !p.HasTVDB() || t.TVDBID == 0 {
			return nil, errSkip
		}
		in, err := p.TVDB.Series(ctx, t.TVDBID, t.EpisodeOrder, seasons)
		if err == nil {
			// What was fetched is in the order the title had when the fetch began. If a person
			// changes it meanwhile, the store drops these seasons; the order is not written back.
			in.ExpectOrder, in.EpisodeOrder = in.EpisodeOrder, ""
		}
		return in, err
	}
	if p.For(SourceTMDB) == nil || t.TMDBID == 0 {
		return nil, errSkip
	}
	return p.TMDB.Series(ctx, t.TMDBID, "", seasons)
}

// fetchShow fetches a show by an id at a provider, for a match: in the episode order
// the show already has when it is known (a re-match keeps what a person chose).
func (w *Worker) fetchShow(ctx context.Context, p *Providers, source string, id int64, seasons []int) (*store.TitleInput, error) {
	prov := p.For(source)
	if prov == nil {
		return nil, ErrLookupsOff
	}
	order := ""
	if source == SourceTVDB {
		order = store.OrderAired
		if t, err := w.o.Store.GetTitleByTVDB(ctx, id); err == nil && store.ValidEpisodeOrder(t.EpisodeOrder) {
			order = t.EpisodeOrder
		}
	}
	return prov.Series(ctx, id, order, seasons)
}

// overlayShowNFO lays the NFO next to the first file of the show that title id belongs to
// over in, and returns the path of that NFO ("" for none).
func (w *Worker) overlayShowNFO(ctx context.Context, titleID int64, in *store.TitleInput) string {
	cards, err := w.o.Store.TitleCards(ctx, []int64{titleID})
	if err != nil || cards[titleID].Show == "" {
		return ""
	}
	items, err := w.o.Store.ShowItems(ctx, cards[titleID].Show)
	if err != nil || len(items) == 0 {
		return ""
	}
	dir := showDir(items[0].Path)
	// Not w.names: this is called from a request too, outside a pass.
	nfoPath := ShowLocal(dir, dirNames(dir)).NFO
	overlayNFO(in, w.readNFO(nfoPath))
	return nfoPath
}

// switchCutoff says which titles the move to TVDB (switchToTVDB) is about, before the pass
// matches anything new: the ones that exist now, if TVDB is on and the move is not done.
// A show the pass matches at TMDB because TVDB found nothing above the threshold has a
// higher id, and stays there. ok is false when there is no move to make. It is called
// first in a pass; with no TVDB key it clears the notes, so that the next key moves the
// shows (not when lookups are only switched off).
func (w *Worker) switchCutoff(ctx context.Context, p *Providers, res Resolved) (upTo int64, ok bool) {
	if !p.HasTVDB() {
		if res.Enabled && !res.TVDBKeySet() {
			// A read first: with no TVDB key this runs every pass, and a write is dearer.
			for _, key := range []string{SettingTVDBSwitched, SettingTVDBSwitchUpTo} {
				if _, found, err := w.o.Store.GetSetting(ctx, key); err != nil || !found {
					continue
				}
				if err := w.o.Store.DeleteSetting(ctx, key); err != nil && ctx.Err() == nil {
					w.noteError(err)
				}
			}
		}
		return 0, false
	}
	if v, found, err := w.o.Store.GetSetting(ctx, SettingTVDBSwitched); err != nil || (found && v == "1") {
		return 0, false
	}
	if v, found, err := w.o.Store.GetSetting(ctx, SettingTVDBSwitchUpTo); err == nil && found {
		if n, perr := strconv.ParseInt(v, 10, 64); perr == nil {
			return n, true
		}
	}
	max, err := w.o.Store.MaxTitleID(ctx)
	if err != nil {
		w.noteError(err)
		return 0, false
	}
	if err := w.o.Store.SetSetting(ctx, SettingTVDBSwitchUpTo, strconv.FormatInt(max, 10)); err != nil {
		w.noteError(err)
		return 0, false
	}
	return max, true
}

// switchToTVDB moves the shows that came from TMDB over to TVDB when TVDB becomes
// available, by the TVDB id TMDB gave them (external_ids): no search is made. A match a
// person made moves the same way and stays theirs (its state is not touched); a show
// TVDB does not know stays on TMDB. A show that TMDB gave no TVDB id stays too. It is
// done once for a TVDB key: the note SettingTVDBSwitched says so, and a new key, or
// none, clears it (so a show a person later matches to TMDB by hand while TVDB is on
// sticks). Only the titles up to the cutoff (switchCutoff) are offered. It takes
// switchBatch shows a pass; TVDB out of reach ends it for a while. It reports whether it
// stored anything.
func (w *Worker) switchToTVDB(ctx context.Context, p *Providers, upTo int64) bool {
	now := w.now()
	w.mu.Lock()
	retry := w.switchRetryAt
	w.mu.Unlock()
	if now.Before(retry) {
		return false
	}
	titles, err := w.o.Store.TMDBSeriesWithTVDBID(ctx, upTo)
	if err != nil {
		w.noteError(err)
		return false
	}
	did, asked := false, 0
	for _, t := range titles {
		if ctx.Err() != nil || w.aborted {
			return did
		}
		w.mu.Lock()
		tried := w.switchTried[t.ID]
		w.mu.Unlock()
		if tried {
			continue
		}
		if asked == switchBatch {
			w.signal() // the rest is for the next pass
			return did
		}
		if !w.pace(ctx) {
			return did
		}
		asked++
		in, err := p.TVDB.Series(ctx, t.TVDBID, store.OrderAired, nil)
		switch {
		case err == nil:
		case tvdbKeyRefused(err):
			w.noteError(err)
			w.mu.Lock()
			w.switchRetryAt = now.Add(tvdbRefusedDelay)
			w.mu.Unlock()
			return did
		case IsTransient(err):
			w.noteError(err)
			w.mu.Lock()
			w.switchRetryAt = now.Add(refreshRetry)
			w.mu.Unlock()
			return did
		default:
			// TVDB does not know the show (or will not say): it stays on TMDB.
			w.markSwitchTried(t.ID)
			continue
		}
		w.markSwitchTried(t.ID)
		nfoPath := w.overlayShowNFO(ctx, t.ID, in)
		id, err := w.o.Store.UpsertTitle(ctx, in)
		if err != nil {
			if ctx.Err() == nil {
				w.noteError(err)
			}
			continue
		}
		w.noteNFO(id, nfoPath)
		w.prefetch(ctx, in)
		did = true
	}
	if ctx.Err() == nil {
		if err := w.o.Store.SetSetting(ctx, SettingTVDBSwitched, "1"); err != nil {
			w.noteError(err)
		} else if err := w.o.Store.DeleteSetting(ctx, SettingTVDBSwitchUpTo); err != nil {
			w.noteError(err)
		}
	}
	return did
}

func (w *Worker) markSwitchTried(id int64) {
	w.mu.Lock()
	w.switchTried[id] = true
	w.mu.Unlock()
}

// SetEpisodeOrder changes how the files of a show from TVDB are mapped to its episodes
// (store.OrderAired, OrderDVD, OrderAbsolute) and fetches the show again in that
// order: the stored episodes are replaced by TVDB's in the new numbering, so the
// files find theirs by season and number. Nothing changes if TVDB cannot give the
// show in that order (ErrNoSuchOrder) or cannot be reached. The errors are
// store.ErrNotFound for an unknown title, ErrNotTVDB, ErrNoTVDB or ErrLookupsOff when
// TVDB is not set up, and what TVDB said.
func (w *Worker) SetEpisodeOrder(ctx context.Context, titleID int64, order string) error {
	if !store.ValidEpisodeOrder(order) {
		return errors.New("metadata: unknown episode order " + order)
	}
	t, err := w.o.Store.GetTitle(ctx, titleID)
	if err != nil {
		return err
	}
	if t.Kind != store.KindSeries || t.Source != store.SourceTVDB || t.TVDBID == 0 {
		return ErrNotTVDB
	}
	p, err := w.provider(ctx)
	if err != nil {
		return err
	}
	if !p.HasTVDB() {
		return ErrNoTVDB
	}
	in, err := p.TVDB.Series(ctx, t.TVDBID, order, nil)
	if IsNotFound(err) {
		return ErrNoSuchTitle
	}
	if err != nil {
		return err
	}
	in.EpisodeOrder, in.ResetSeasons = order, true
	nfoPath := w.overlayShowNFO(ctx, t.ID, in)
	id, err := w.o.Store.UpsertTitle(ctx, in)
	if err != nil {
		return err
	}
	w.noteNFO(id, nfoPath)
	w.prefetchLater(in)
	return nil
}
