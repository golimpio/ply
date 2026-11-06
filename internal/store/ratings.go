package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"slices"
	"time"
)

// The sources of a rating, in the order the API lists them (docs/M10-RATINGS.md). IMDb comes from
// IMDb's dataset, TMDB from titles.rating (it is not stored a second time), the others from MDBList.
const (
	RatingIMDb       = "imdb"
	RatingRTCritics  = "rt_critics"
	RatingRTAudience = "rt_audience"
	RatingMetacritic = "metacritic"
	RatingLetterboxd = "letterboxd"
	RatingTrakt      = "trakt"
	RatingTMDB       = "tmdb"
)

// RatingSources is every source, in the fixed order: the order of a title's ratings, and the one
// the first available is picked in when the primary source has none.
var RatingSources = []string{RatingIMDb, RatingRTCritics, RatingRTAudience, RatingMetacritic, RatingLetterboxd, RatingTrakt, RatingTMDB}

// MDBListSources are the sources MDBList's answer is stored as.
var MDBListSources = []string{RatingRTCritics, RatingRTAudience, RatingMetacritic, RatingLetterboxd, RatingTrakt}

// Rotten Tomatoes states, kept in Rating.State.
const (
	StateFresh       = "fresh"
	StateRotten      = "rotten"
	StateCertified   = "certified"
	StateUpright     = "upright"
	StateSpilled     = "spilled"
	StateVerifiedHot = "verified_hot"
)

// ValidRatingSource reports whether s is one of RatingSources.
func ValidRatingSource(s string) bool { return slices.Contains(RatingSources, s) }

func ratingRank(source string) int {
	if i := slices.Index(RatingSources, source); i >= 0 {
		return i
	}
	return len(RatingSources)
}

// Rating is one source's rating of a title or an episode. Value is on the source's own Scale (IMDb
// 10, Metacritic 100, Letterboxd 5, ...); Votes is 0 when unknown; State is for Rotten Tomatoes.
type Rating struct {
	Source    string
	Value     float64
	Scale     float64
	Votes     int
	State     string
	URL       string
	FetchedAt int64 // unix seconds
}

// Score100 is Value / Scale × 100, rounded: what sorting and filtering go by.
func (r Rating) Score100() int {
	if r.Scale <= 0 {
		return 0
	}
	return int(math.Round(r.Value / r.Scale * 100))
}

// sortRatings puts ratings in the fixed order of RatingSources.
func sortRatings(rs []Rating) {
	slices.SortStableFunc(rs, func(a, b Rating) int { return ratingRank(a.Source) - ratingRank(b.Source) })
}

// tmdbRating is TMDB's score of a title, which titles.rating and vote_count already hold: a title
// from TMDB (or with an NFO over it) that has one. An NFO's own rating is not TMDB's, and TVDB has none.
func tmdbRating(kind, source string, tmdbID int64, rating float64, votes int, fetchedAt int64) (Rating, bool) {
	if rating <= 0 || tmdbID == 0 || (source != SourceTMDB && source != SourceMixed) {
		return Rating{}, false
	}
	path := "movie"
	if kind == KindSeries {
		path = "tv"
	}
	return Rating{Source: RatingTMDB, Value: rating, Scale: 10, Votes: votes,
		URL: fmt.Sprintf("https://www.themoviedb.org/%s/%d", path, tmdbID), FetchedAt: fetchedAt}, true
}

// attachRatings adds the stored ratings (not TMDB's, which scanCard put there) to the cards, in the
// fixed order. A title can be in several cards (a person's credits).
func attachRatings(ctx context.Context, q queryer, cards []TitleCard) error {
	at := map[int64][]int{}
	var ids []int64
	for i, c := range cards {
		if _, seen := at[c.ID]; !seen {
			ids = append(ids, c.ID)
		}
		at[c.ID] = append(at[c.ID], i)
	}
	stored, err := storedRatings(ctx, q, ids)
	if err != nil {
		return err
	}
	for id, idx := range at {
		for _, i := range idx {
			cards[i].Ratings = append(cards[i].Ratings, stored[id]...)
			sortRatings(cards[i].Ratings)
		}
	}
	return nil
}

// storedRatings reads the title_ratings rows of the titles, in chunks.
func storedRatings(ctx context.Context, q queryer, ids []int64) (map[int64][]Rating, error) {
	out := map[int64][]Rating{}
	for start := 0; start < len(ids); start += briefChunk {
		chunk := ids[start:min(start+briefChunk, len(ids))]
		rows, err := q.QueryContext(ctx, `SELECT title_id, source, value, scale, votes, state, url, fetched_at
			FROM title_ratings WHERE title_id IN (`+placeholders(len(chunk))+`)`, idArgs(chunk)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var (
				id int64
				r  Rating
			)
			if err := rows.Scan(&id, &r.Source, &r.Value, &r.Scale, &r.Votes, &r.State, &r.URL, &r.FetchedAt); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = append(out[id], r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ---- the IMDb dataset ----

// IMDbLibrary is the IMDb ids (tconst) of the library: the titles that have one, and the episodes.
type IMDbLibrary struct {
	Titles   map[string][]int64  // tconst -> the titles that have it
	Episodes map[string]struct{} // tconst of the episodes of library titles
}

// Len is the number of tconst the library has.
func (l IMDbLibrary) Len() int { return len(l.Titles) + len(l.Episodes) }

// IMDbLibrary lists the IMDb ids of the titles in the library and of their episodes: the only ones
// the dataset's ratings are kept for.
func (s *Store) IMDbLibrary(ctx context.Context) (lib IMDbLibrary, err error) {
	lib = IMDbLibrary{Titles: map[string][]int64{}, Episodes: map[string]struct{}{}}
	err = s.snapshot(ctx, func(q queryer) error {
		rows, err := q.QueryContext(ctx, `SELECT t.imdb_id, t.id FROM titles t WHERE t.imdb_id <> '' AND `+libraryTitle)
		if err != nil {
			return err
		}
		for rows.Next() {
			var (
				tconst string
				id     int64
			)
			if err := rows.Scan(&tconst, &id); err != nil {
				rows.Close()
				return err
			}
			lib.Titles[tconst] = append(lib.Titles[tconst], id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		rows, err = q.QueryContext(ctx, `
			WITH lib AS (SELECT t.id FROM titles t WHERE `+libraryTitle+`)
			SELECT DISTINCT e.imdb_id FROM episodes e WHERE e.imdb_id <> '' AND e.title_id IN (SELECT id FROM lib)`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var tconst string
			if err := rows.Scan(&tconst); err != nil {
				return err
			}
			lib.Episodes[tconst] = struct{}{}
		}
		return rows.Err()
	})
	return lib, err
}

// IMDbTitleRating is the dataset's rating for a title; IMDbEpisodeRating for an episode.
type IMDbTitleRating struct {
	TitleID int64
	Value   float64
	Votes   int
	URL     string
}

type IMDbEpisodeRating struct {
	IMDbID string
	Value  float64
	Votes  int
}

// SetIMDbRatings writes the ratings, at most writeChunk rows to a transaction at background
// priority, and calls pace between the transactions (the queue's yield to playback). Every row is
// stamped fetchedAt; PruneIMDbRatings then removes what the stamp did not reach.
func (s *Store) SetIMDbRatings(ctx context.Context, titles []IMDbTitleRating, episodes []IMDbEpisodeRating, fetchedAt int64,
	pace func(context.Context) error) error {
	step := func() error {
		if pace == nil {
			return nil
		}
		return pace(ctx)
	}
	for start := 0; start < len(titles); start += writeChunk {
		chunk := titles[start:min(start+writeChunk, len(titles))]
		if err := s.write(ctx, prioLow, func(tx *sql.Tx) error {
			for _, r := range chunk {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO title_ratings (title_id, source, value, scale, votes, state, url, fetched_at)
					VALUES (?, 'imdb', ?, 10, ?, '', ?, ?)
					ON CONFLICT (title_id, source) DO UPDATE SET value = excluded.value, scale = excluded.scale,
						votes = excluded.votes, url = excluded.url, fetched_at = excluded.fetched_at`,
					r.TitleID, r.Value, r.Votes, r.URL, fetchedAt); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if err := step(); err != nil {
			return err
		}
	}
	for start := 0; start < len(episodes); start += writeChunk {
		chunk := episodes[start:min(start+writeChunk, len(episodes))]
		if err := s.write(ctx, prioLow, func(tx *sql.Tx) error {
			for _, r := range chunk {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO episode_ratings (imdb_id, source, value, scale, votes, fetched_at)
					VALUES (?, 'imdb', ?, 10, ?, ?)
					ON CONFLICT (imdb_id, source) DO UPDATE SET value = excluded.value, scale = excluded.scale,
						votes = excluded.votes, fetched_at = excluded.fetched_at`,
					r.IMDbID, r.Value, r.Votes, fetchedAt); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// PruneIMDbRatings removes the IMDb ratings that are older than stamp (written before the last
// SetIMDbRatings, so for ids that are no longer in the library or the dataset), in chunks. It
// returns how many rows it removed.
func (s *Store) PruneIMDbRatings(ctx context.Context, stamp int64, pace func(context.Context) error) (int, error) {
	total := 0
	for _, del := range []string{
		`DELETE FROM title_ratings WHERE rowid IN (SELECT rowid FROM title_ratings WHERE source = 'imdb' AND fetched_at < ? LIMIT ?)`,
		`DELETE FROM episode_ratings WHERE rowid IN (SELECT rowid FROM episode_ratings WHERE fetched_at < ? LIMIT ?)`,
	} {
		for {
			var n int64
			err := s.write(ctx, prioLow, func(tx *sql.Tx) error {
				res, err := tx.ExecContext(ctx, del, stamp, writeChunk)
				if err != nil {
					return err
				}
				n, _ = res.RowsAffected()
				return nil
			})
			if err != nil {
				return total, err
			}
			total += int(n)
			if n < writeChunk {
				break
			}
			if pace != nil {
				if err := pace(ctx); err != nil {
					return total, err
				}
			}
		}
	}
	return total, nil
}

// ---- MDBList ----

// RatingsDue is a library title MDBList is to be asked about.
type RatingsDue struct {
	ID          int64
	Title       string
	Kind        string
	TMDBID      int64 // 0 = none
	IMDbID      string
	ReleaseDate string
	Checked     int64 // when it was last asked about, unix seconds; 0 = never
}

// Ratings refresh periods: a title with no ratings yet is asked at once; one with ratings is
// asked again after RatingsAge, or RatingsRecentAge when it came out in the last RatingsRecentDays.
const (
	RatingsAge        = 14 * 24 * time.Hour
	RatingsRecentAge  = 7 * 24 * time.Hour
	RatingsRecentDays = 90
)

// DueRatings lists the library titles that have a TMDB or IMDb id and are due for MDBList: never
// asked about, or last asked longer ago than RatingsAge (RatingsRecentAge for a title released in
// the last RatingsRecentDays days), the ones waiting longest first.
func (s *Store) DueRatings(ctx context.Context, now time.Time, limit int) ([]RatingsDue, error) {
	recent := now.AddDate(0, 0, -RatingsRecentDays).Format(time.DateOnly)
	rows, err := s.ro.QueryContext(ctx, `
		SELECT t.id, t.title, t.kind, COALESCE(t.tmdb_id, 0), t.imdb_id, t.release_date, t.ratings_checked
		FROM titles t
		WHERE (t.tmdb_id IS NOT NULL OR t.imdb_id <> '') AND `+libraryTitle+`
			AND (t.ratings_checked = 0 OR t.ratings_checked <
				CASE WHEN t.release_date >= ? THEN ? ELSE ? END)
		ORDER BY t.ratings_checked, t.id LIMIT ?`,
		recent, now.Add(-RatingsRecentAge).Unix(), now.Add(-RatingsAge).Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RatingsDue
	for rows.Next() {
		var d RatingsDue
		if err := rows.Scan(&d.ID, &d.Title, &d.Kind, &d.TMDBID, &d.IMDbID, &d.ReleaseDate, &d.Checked); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ReplaceMDBListRatings stores MDBList's answer for a title: it replaces the ratings of MDBList's
// sources (a source it no longer gives is removed) and records that the title was asked about at
// checkedAt, also when it has nothing. ErrNotFound for a title that is gone.
func (s *Store) ReplaceMDBListRatings(ctx context.Context, titleID int64, ratings []Rating, checkedAt int64) error {
	for _, r := range ratings {
		if !slices.Contains(MDBListSources, r.Source) {
			return fmt.Errorf("store: %q is not a rating MDBList gives", r.Source)
		}
		if r.Scale <= 0 {
			return fmt.Errorf("store: rating %q has scale %v", r.Source, r.Scale)
		}
	}
	return s.write(ctx, prioLow, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE titles SET ratings_checked = ? WHERE id = ?`, checkedAt, titleID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM title_ratings WHERE title_id = ? AND source IN (`+
			placeholders(len(MDBListSources))+`)`, append([]any{titleID}, anyStrings(MDBListSources)...)...); err != nil {
			return err
		}
		for _, r := range ratings {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO title_ratings (title_id, source, value, scale, votes, state, url, fetched_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				titleID, r.Source, r.Value, r.Scale, r.Votes, r.State, r.URL, checkedAt); err != nil {
				return err
			}
		}
		return nil
	})
}

// TouchRatingsChecked records that MDBList was asked about the title at checkedAt and would not
// give its ratings, keeping the ones it has. ErrNotFound for a title that is gone. No list shows
// when it was asked: it leaves Generation alone.
func (s *Store) TouchRatingsChecked(ctx context.Context, titleID, checkedAt int64) error {
	res, err := s.execQuiet(ctx, prioLow, `UPDATE titles SET ratings_checked = ? WHERE id = ?`, checkedAt, titleID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func anyStrings(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// ---- episodes' IMDb ids ----

// The season answers of TMDB and TVDB have no IMDb ids for episodes. They come from IMDb's own
// episode dataset (title.episode.tsv.gz: an episode, its show, season and number), which the metadata
// package reads and MapEpisodeIMDb maps onto the episodes a show has stored, by season and number.

// A season is mapped only when the stored episodes and the dataset's are as many: a provider that
// merges a two-parter or splits an episode shifts every number after it, and nothing but the count
// says so. The exception is the show's latest season, which may be on air (more announced than IMDb
// lists yet): there the counts may differ by EpisodeCountSlack, unless the season has
// EpisodeSmallSeason episodes or fewer (on either side), where two is most of it.
const (
	EpisodeCountSlack  = 2
	EpisodeSmallSeason = 5
)

// IMDbShow is a show of the library whose episodes can be mapped: it has an IMDb id, and its
// episodes are in the aired order, which is the numbering the dataset has.
type IMDbShow struct {
	TitleID  int64
	IMDbID   string
	Episodes int // stored, specials (season 0) apart
}

// IMDbShows lists the library's shows that can be mapped, leaving out the ones with no episodes.
func (s *Store) IMDbShows(ctx context.Context) ([]IMDbShow, error) {
	rows, err := s.ro.QueryContext(ctx, `
		SELECT t.id, t.imdb_id, (SELECT COUNT(*) FROM episodes e WHERE e.title_id = t.id AND e.season > 0)
		FROM titles t
		WHERE t.kind = 'series' AND t.imdb_id <> '' AND t.episode_order = 'aired' AND `+libraryTitle+`
		ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IMDbShow
	for rows.Next() {
		var sh IMDbShow
		if err := rows.Scan(&sh.TitleID, &sh.IMDbID, &sh.Episodes); err != nil {
			return nil, err
		}
		if sh.Episodes > 0 {
			out = append(out, sh)
		}
	}
	return out, rows.Err()
}

// DatasetEpisode is a row of IMDb's episode dataset.
type DatasetEpisode struct {
	IMDbID string
	Season int
	Number int
}

// EpisodeMapStats says what a mapping did.
type EpisodeMapStats struct {
	Shows   int // shows the dataset has episodes for
	Seasons int // seasons whose counts agreed, so that they were mapped
	Skipped int // seasons left alone, their counts being too far apart
	Mapped  int // episodes given an id, or another one
	Cleared int // episodes whose id from the dataset is no longer the dataset's
}

type episodeSlot struct{ season, number int }

type storedEpisode struct {
	slot      episodeSlot
	imdb, src string
}

// epChange is an episode's new id: "" with source "" clears it.
type epChange struct {
	titleID   int64
	slot      episodeSlot
	imdb, src string
}

// EpisodeIDPlan is the episodes whose IMDb id is to change, as PlanEpisodeIMDb decided them.
type EpisodeIDPlan struct {
	changes []epChange
	Stats   EpisodeMapStats
}

// Len is the number of episodes the plan changes.
func (p EpisodeIDPlan) Len() int { return len(p.changes) }

// PlanEpisodeIMDb decides the IMDb ids of the episodes of the library's shows (IMDbShows) from the
// dataset's, which are the episodes of each show by IMDb's id for it. The rules:
//   - by season and number, in the aired order, and not for specials (season 0);
//   - a season is mapped only when the stored episodes and the dataset's are as many (give or take
//     EpisodeCountSlack for the latest season of a show, if it has more than EpisodeSmallSeason):
//     otherwise the numbering is not the same, and the ids that an earlier mapping gave to its
//     episodes are taken away;
//   - an id that a provider's data gave (IMDbFromProvider) is never changed;
//   - a slot that two episodes of the dataset claim is mapped to none;
//   - a show the dataset has no episodes for is not touched: a short file must not take ids away.
//
// Only the episodes whose id changes are in the plan. It writes nothing: ApplyEpisodeIMDb does.
func (s *Store) PlanEpisodeIMDb(ctx context.Context, dataset map[string][]DatasetEpisode) (EpisodeIDPlan, error) {
	var plan EpisodeIDPlan
	shows, err := s.IMDbShows(ctx)
	if err != nil {
		return plan, err
	}
	for _, show := range shows {
		eps := dataset[show.IMDbID]
		if len(eps) == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return plan, err
		}
		stored, err := s.storedEpisodes(ctx, show.TitleID)
		if err != nil {
			return plan, err
		}
		plan.Stats.Shows++
		plan.changes = append(plan.changes, planEpisodeMap(show.TitleID, stored, eps, &plan.Stats)...)
	}
	return plan, nil
}

// ApplyEpisodeIMDb writes a plan, at most writeChunk episodes to a transaction at background
// priority, calling pace between the transactions. An id a provider gave, and an episode of a show
// that is no longer in the aired order, are left alone even if the plan says otherwise: the plan
// was made some time ago.
func (s *Store) ApplyEpisodeIMDb(ctx context.Context, plan EpisodeIDPlan, pace func(context.Context) error) error {
	for start := 0; start < len(plan.changes); start += writeChunk {
		chunk := plan.changes[start:min(start+writeChunk, len(plan.changes))]
		if err := s.write(ctx, prioLow, func(tx *sql.Tx) error {
			for _, c := range chunk {
				if _, err := tx.ExecContext(ctx, `
					UPDATE episodes SET imdb_id = ?, imdb_src = ?
					WHERE title_id = ? AND season = ? AND episode = ? AND imdb_src <> ?
						AND EXISTS (SELECT 1 FROM titles t WHERE t.id = ? AND t.episode_order = 'aired')`,
					c.imdb, c.src, c.titleID, c.slot.season, c.slot.number, IMDbFromProvider, c.titleID); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if pace != nil {
			if err := pace(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// storedEpisodes reads the episodes of a show, specials apart, with the ids they have.
func (s *Store) storedEpisodes(ctx context.Context, titleID int64) ([]storedEpisode, error) {
	rows, err := s.ro.QueryContext(ctx, `SELECT season, episode, imdb_id, imdb_src FROM episodes
		WHERE title_id = ? AND season > 0`, titleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storedEpisode
	for rows.Next() {
		var e storedEpisode
		if err := rows.Scan(&e.slot.season, &e.slot.number, &e.imdb, &e.src); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// planEpisodeMap decides the ids of one show's stored episodes from the dataset's, counting in st.
func planEpisodeMap(titleID int64, stored []storedEpisode, eps []DatasetEpisode, st *EpisodeMapStats) []epChange {
	claimed := map[episodeSlot]string{} // "" = two episodes claim the slot
	inSeason := map[int]int{}
	for _, e := range eps {
		k := episodeSlot{e.Season, e.Number}
		if prev, dup := claimed[k]; dup {
			if prev != e.IMDbID {
				claimed[k] = ""
			}
			continue
		}
		claimed[k] = e.IMDbID
		inSeason[e.Season]++
	}
	have := map[int]int{}
	for _, e := range stored {
		have[e.slot.season]++
	}
	latest := 0 // the show's latest season, by either list: one the dataset is ahead of is not on air
	for season := range have {
		latest = max(latest, season)
	}
	for season := range inSeason {
		latest = max(latest, season)
	}
	mappable := map[int]bool{}
	for season, n := range have {
		m := inSeason[season]
		slack := 0
		if season == latest && min(n, m) > EpisodeSmallSeason {
			slack = EpisodeCountSlack
		}
		if diff := n - m; diff > slack || -diff > slack {
			st.Skipped++
			continue
		}
		mappable[season] = true
		if m > 0 {
			st.Seasons++
		}
	}
	var out []epChange
	for _, e := range stored {
		if e.src == IMDbFromProvider && e.imdb != "" {
			continue
		}
		want := ""
		if mappable[e.slot.season] {
			want = claimed[e.slot]
		}
		switch {
		case want == e.imdb:
		case want != "":
			out = append(out, epChange{titleID, e.slot, want, IMDbFromDataset})
			st.Mapped++
		default:
			out = append(out, epChange{titleID, e.slot, "", ""})
			st.Cleared++
		}
	}
	return out
}

// epIDs is the IMDb id a stored episode has, and where it came from.
type epIDs struct{ imdb, src string }

// episodeMemory is the ids the episodes of a title had, by season and number: a refresh of the
// title writes its seasons again, and brings none.
type episodeMemory map[[2]int]epIDs

// loadEpisodeIDs reads the ids stored for the episodes of a title.
func loadEpisodeIDs(ctx context.Context, tx *sql.Tx, titleID int64) (episodeMemory, error) {
	mem := episodeMemory{}
	rows, err := tx.QueryContext(ctx, `SELECT season, episode, imdb_id, imdb_src FROM episodes
		WHERE title_id = ? AND imdb_id <> ''`, titleID)
	if err != nil {
		return mem, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			season, number int
			id             epIDs
		)
		if err := rows.Scan(&season, &number, &id.imdb, &id.src); err != nil {
			return mem, err
		}
		mem[[2]int{season, number}] = id
	}
	return mem, rows.Err()
}
