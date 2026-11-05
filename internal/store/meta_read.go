package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// TitleCard is a title as a grid tile shows it. Poster and Backdrop are image
// refs; sidecar artwork ("local:item:<id>:poster") beats TMDB's.
type TitleCard struct {
	ID       int64
	Kind     string
	Title    string
	Year     int
	Poster   string
	Backdrop string
	Rating   float64
	ItemID   int64  // movies: the first file
	Show     string // shows: the first show name that is linked to the title

	// NextAirDate is when a show's next episode airs, YYYY-MM-DD; it may be past
	// until the show is refreshed.
	NextAirDate string
}

// CastMember and CrewMember are a credit with its person's name and photo.
type CastMember struct {
	PersonID  int64
	Name      string
	Character string
	Profile   string
}

type CrewMember struct {
	PersonID int64
	Name     string
	Job      string
	Profile  string
}

// TitleDetail is a title of the library with everything the item page shows.
type TitleDetail struct {
	TitleCard
	TMDBID        int64
	IMDbID        string
	TVDBID        int64
	OriginalTitle string
	SortTitle     string
	Overview      string
	Tagline       string
	ReleaseDate   string
	EndDate       string
	Status        string
	RuntimeMin    int
	VoteCount     int
	Certification string
	Logo          string
	Genres        []Genre
	Collection    *Collection
	Cast          []CastMember
	Crew          []CrewMember
	Source        string
	EpisodeOrder  string // a show's: aired, dvd or absolute
	Locked        bool
	FetchedAt     int64

	// MatchState is that of the first file (or show); NeedsReview is set when
	// any file (or show) linked to the title needs review.
	MatchState  string
	NeedsReview bool
}

// libraryTitle is true for titles that have a movie file, or a show with at
// least one episode file, in the library. It uses the alias t for titles.
const libraryTitle = `(EXISTS (SELECT 1 FROM items WHERE title_id = t.id)
	OR EXISTS (SELECT 1 FROM series_matches sm WHERE sm.title_id = t.id
		AND EXISTS (SELECT 1 FROM items e WHERE e.kind = 'episode' AND e.show = sm.show)))`

// The card query: each title with its first movie file or first show, and their
// sidecar artwork. Titles that are in neither are not in the library.
const (
	cardCols = `t.id, t.kind, t.title, t.year, t.poster, t.backdrop, t.rating, COALESCE(t.next_air_date, ''),
		COALESCE(mf.item_id, 0), COALESCE(mi.local_poster, ''), COALESCE(mi.local_backdrop, ''),
		COALESCE(sf.show, ''), COALESCE(ss.rowid, 0), COALESCE(ss.local_poster, ''), COALESCE(ss.local_backdrop, '')`
	cardJoins = `
		LEFT JOIN (SELECT title_id, MIN(id) AS item_id FROM items
			WHERE title_id IS NOT NULL GROUP BY title_id) mf ON mf.title_id = t.id
		LEFT JOIN items mi ON mi.id = mf.item_id
		LEFT JOIN (SELECT sm.title_id, MIN(sm.show) AS show FROM series_matches sm
			WHERE sm.title_id IS NOT NULL
				AND EXISTS (SELECT 1 FROM items e WHERE e.kind = 'episode' AND e.show = sm.show)
			GROUP BY sm.title_id) sf ON sf.title_id = t.id
		LEFT JOIN series_matches ss ON ss.show = sf.show`
	cardInLibrary = `(mf.item_id IS NOT NULL OR sf.show IS NOT NULL)`
)

// LocalRef is the image ref of sidecar artwork: "local:<item|series>:<id>:<slot>".
func LocalRef(kind string, id int64, slot string) string {
	return fmt.Sprintf("local:%s:%d:%s", kind, id, slot)
}

// scanCard reads cardCols; lead are destinations for columns selected before them.
func scanCard(sc scanner, lead ...any) (TitleCard, error) {
	var (
		c                          TitleCard
		itemPoster, itemBackdrop   string
		seriesID                   int64
		seriesPoster, seriesBackdr string
	)
	err := sc.Scan(append(lead, &c.ID, &c.Kind, &c.Title, &c.Year, &c.Poster, &c.Backdrop, &c.Rating, &c.NextAirDate,
		&c.ItemID, &itemPoster, &itemBackdrop, &c.Show, &seriesID, &seriesPoster, &seriesBackdr)...)
	if err != nil {
		return c, err
	}
	// A cover embedded in the file ranks below TMDB's poster; sidecar art beats it.
	if c.ItemID != 0 && itemPoster != "" && (c.Poster == "" || !strings.HasPrefix(itemPoster, EmbeddedPrefix)) {
		c.Poster = LocalRef(ArtItem, c.ItemID, "poster")
	}
	if c.ItemID != 0 && itemBackdrop != "" {
		c.Backdrop = LocalRef(ArtItem, c.ItemID, "backdrop")
	}
	if seriesID != 0 && seriesPoster != "" {
		c.Poster = LocalRef(ArtSeries, seriesID, "poster")
	}
	if seriesID != 0 && seriesBackdr != "" {
		c.Backdrop = LocalRef(ArtSeries, seriesID, "backdrop")
	}
	return c, nil
}

// queryCards returns the library titles matching where (which may be empty and
// refers to t, mf, mi, sf, ss) in the given order.
func queryCards(ctx context.Context, q queryer, where string, args []any, order string) ([]TitleCard, error) {
	if where != "" {
		where = " AND " + where
	}
	rows, err := q.QueryContext(ctx, `SELECT `+cardCols+` FROM titles t`+cardJoins+
		` WHERE `+cardInLibrary+where+` ORDER BY `+order, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cards []TitleCard
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		cards = append(cards, c)
	}
	return cards, rows.Err()
}

const (
	orderByName    = `LOWER(CASE WHEN t.sort_title <> '' THEN t.sort_title ELSE t.title END), t.year, t.id`
	orderByRelease = `CASE WHEN t.release_date = '' THEN '9999' ELSE t.release_date END, t.year, t.id`
)

// TitleFilter narrows Titles; zero fields match everything and the others combine.
type TitleFilter struct {
	GenreID      int64
	CollectionID int64
	PersonID     int64
}

// Titles lists the library's titles matching the filter, by name.
func (s *Store) Titles(ctx context.Context, f TitleFilter) (cards []TitleCard, err error) {
	var (
		where []string
		args  []any
	)
	if f.GenreID != 0 {
		where = append(where, `EXISTS (SELECT 1 FROM title_genres tg WHERE tg.title_id = t.id AND tg.genre_id = ?)`)
		args = append(args, f.GenreID)
	}
	if f.CollectionID != 0 {
		where = append(where, `t.collection_id = ?`)
		args = append(args, f.CollectionID)
	}
	if f.PersonID != 0 {
		where = append(where, `EXISTS (SELECT 1 FROM credits c WHERE c.title_id = t.id AND c.person_id = ?)`)
		args = append(args, f.PersonID)
	}
	err = s.snapshot(ctx, func(q queryer) (err error) {
		cards, err = queryCards(ctx, q, strings.Join(where, " AND "), args, orderByName)
		return err
	})
	return cards, err
}

// TitleCards returns the library titles among ids, keyed by id.
func (s *Store) TitleCards(ctx context.Context, ids []int64) (m map[int64]TitleCard, err error) {
	err = s.snapshot(ctx, func(q queryer) (err error) {
		m, err = cardsByID(ctx, q, ids)
		return err
	})
	return m, err
}

func cardsByID(ctx context.Context, q queryer, ids []int64) (map[int64]TitleCard, error) {
	m := map[int64]TitleCard{}
	if len(ids) == 0 {
		return m, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	in := "t.id IN (?" + strings.Repeat(", ?", len(ids)-1) + ")"
	cards, err := queryCards(ctx, q, in, args, "t.id")
	if err != nil {
		return nil, err
	}
	for _, c := range cards {
		m[c.ID] = c
	}
	return m, nil
}

// UpcomingShow is a series of the library with an episode still to air.
type UpcomingShow struct {
	TitleCard           // NextAirDate is the day the episode airs
	NextEpisode Episode // Number is 0 when TMDB did not say which episode it is
}

// Upcoming lists the library's series whose next episode airs on today
// (YYYY-MM-DD) or later: soonest first, by name within a day, at most limit.
func (s *Store) Upcoming(ctx context.Context, today string, limit int) ([]UpcomingShow, error) {
	rows, err := s.ro.QueryContext(ctx, `
		SELECT t.next_episode_season, t.next_episode_number, t.next_episode_name, `+cardCols+`
		FROM titles t`+cardJoins+`
		WHERE `+cardInLibrary+` AND t.kind = 'series' AND t.next_air_date >= ?
		ORDER BY t.next_air_date, `+orderByName+` LIMIT ?`, today, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UpcomingShow
	for rows.Next() {
		var ep Episode
		c, err := scanCard(rows, &ep.Season, &ep.Number, &ep.Name)
		if err != nil {
			return nil, err
		}
		ep.AirDate = c.NextAirDate
		out = append(out, UpcomingShow{TitleCard: c, NextEpisode: ep})
	}
	return out, rows.Err()
}

// GetTitleDetail returns a title of the library with its genres, collection
// and credits, read from one snapshot. A title no file refers to is ErrNotFound.
func (s *Store) GetTitleDetail(ctx context.Context, id int64) (d TitleDetail, err error) {
	err = s.snapshot(ctx, func(q queryer) (err error) {
		d, err = getTitleDetail(ctx, q, id)
		return err
	})
	return d, err
}

func getTitleDetail(ctx context.Context, q queryer, id int64) (TitleDetail, error) {
	cards, err := queryCards(ctx, q, "t.id = ?", []any{id}, "t.id")
	if err != nil {
		return TitleDetail{}, err
	}
	if len(cards) == 0 {
		return TitleDetail{}, ErrNotFound
	}
	t, err := scanTitle(q.QueryRowContext(ctx, `SELECT `+titleCols+` FROM titles WHERE id = ?`, id))
	if err != nil {
		return TitleDetail{}, err
	}
	d := TitleDetail{
		TitleCard: cards[0], TMDBID: t.TMDBID, IMDbID: t.IMDbID, TVDBID: t.TVDBID,
		OriginalTitle: t.OriginalTitle, SortTitle: t.SortTitle, Overview: t.Overview, Tagline: t.Tagline,
		ReleaseDate: t.ReleaseDate, EndDate: t.EndDate, Status: t.Status, RuntimeMin: t.RuntimeMin,
		VoteCount: t.VoteCount, Certification: t.Certification, Logo: t.Logo,
		Source: t.Source, EpisodeOrder: t.EpisodeOrder, Locked: t.Locked, FetchedAt: t.FetchedAt,
	}
	if d.Genres, err = titleGenres(ctx, q, id); err != nil {
		return TitleDetail{}, err
	}
	if t.CollectionID != 0 {
		c, err := getCollection(ctx, q, t.CollectionID)
		if err != nil {
			return TitleDetail{}, err
		}
		d.Collection = &c
	}
	if d.Cast, d.Crew, err = titleCredits(ctx, q, id); err != nil {
		return TitleDetail{}, err
	}

	stateQuery, reviewQuery, key := `SELECT match_state FROM items WHERE id = ?`,
		`SELECT COALESCE(MAX(needs_review), 0) FROM items WHERE title_id = ?`, any(d.ItemID)
	if d.ItemID == 0 {
		stateQuery = `SELECT match_state FROM series_matches WHERE show = ?`
		reviewQuery = `SELECT COALESCE(MAX(needs_review), 0) FROM series_matches WHERE title_id = ?`
		key = d.Show
	}
	if err := q.QueryRowContext(ctx, stateQuery, key).Scan(&d.MatchState); err != nil {
		return TitleDetail{}, err
	}
	if err := q.QueryRowContext(ctx, reviewQuery, id).Scan(&d.NeedsReview); err != nil {
		return TitleDetail{}, err
	}
	return d, nil
}

func titleGenres(ctx context.Context, q queryer, titleID int64) ([]Genre, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT g.id, g.name FROM title_genres tg JOIN genres g ON g.id = tg.genre_id
		WHERE tg.title_id = ? ORDER BY LOWER(g.name), g.id`, titleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var genres []Genre
	for rows.Next() {
		var g Genre
		if err := rows.Scan(&g.ID, &g.Name); err != nil {
			return nil, err
		}
		genres = append(genres, g)
	}
	return genres, rows.Err()
}

func titleCredits(ctx context.Context, q queryer, titleID int64) (cast []CastMember, crew []CrewMember, err error) {
	rows, err := q.QueryContext(ctx, `
		SELECT c.kind, p.id, p.name, c.job, c.character, p.profile
		FROM credits c JOIN people p ON p.id = c.person_id WHERE c.title_id = ?
		ORDER BY c.kind, c.ord, c.job, p.name, p.id`, titleID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			kind, job, character, profile, name string
			pid                                 int64
		)
		if err := rows.Scan(&kind, &pid, &name, &job, &character, &profile); err != nil {
			return nil, nil, err
		}
		if kind == CreditCast {
			cast = append(cast, CastMember{PersonID: pid, Name: name, Character: character, Profile: profile})
		} else {
			crew = append(crew, CrewMember{PersonID: pid, Name: name, Job: job, Profile: profile})
		}
	}
	return cast, crew, rows.Err()
}

// Genres lists the genres of the library's titles with how many titles each has,
// by name. Genres without a title in the library are left out.
func (s *Store) Genres(ctx context.Context) ([]GenreCount, error) {
	rows, err := s.ro.QueryContext(ctx, `
		SELECT g.id, g.name, COUNT(*) FROM genres g
		JOIN title_genres tg ON tg.genre_id = g.id JOIN titles t ON t.id = tg.title_id
		WHERE `+libraryTitle+`
		GROUP BY g.id ORDER BY LOWER(g.name), g.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GenreCount
	for rows.Next() {
		var g GenreCount
		if err := rows.Scan(&g.ID, &g.Name, &g.Count); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GenreCount is a genre and the number of library titles in it.
type GenreCount struct {
	Genre
	Count int
}

// CollectionDetail is a collection with its titles in the library, in release order.
type CollectionDetail struct {
	Collection
	Titles []TitleCard
}

// GetCollection returns the collection, or ErrNotFound.
func (s *Store) GetCollection(ctx context.Context, id int64) (d CollectionDetail, err error) {
	err = s.snapshot(ctx, func(q queryer) error {
		c, err := getCollection(ctx, q, id)
		if err != nil {
			return err
		}
		d.Collection = c
		d.Titles, err = queryCards(ctx, q, "t.collection_id = ?", []any{id}, orderByRelease)
		return err
	})
	return d, err
}

func getCollection(ctx context.Context, q queryer, id int64) (Collection, error) {
	var (
		c    Collection
		tmdb sql.NullInt64
	)
	err := q.QueryRowContext(ctx, `SELECT id, tmdb_id, name, overview, poster, backdrop FROM collections WHERE id = ?`, id).
		Scan(&c.ID, &tmdb, &c.Name, &c.Overview, &c.Poster, &c.Backdrop)
	if errors.Is(err, sql.ErrNoRows) {
		return Collection{}, ErrNotFound
	}
	c.TMDBID = tmdb.Int64
	return c, err
}

// PersonCard is a person as a search result or a tile shows them.
type PersonCard struct {
	ID       int64
	Name     string
	Profile  string
	KnownFor string
}

// PersonCredit is one role of a person in a title of the library.
type PersonCredit struct {
	Kind      string // CreditCast | CreditCrew
	Job       string
	Character string
	Title     TitleCard
}

// PersonDetail is a person with their credits in the library, newest first.
type PersonDetail struct {
	Person
	Credits []PersonCredit
}

const personCols = `id, tmdb_id, tvdb_id, name, profile, biography, birthday, deathday, place_of_birth, known_for,
	imdb_id, fetched_at`

func scanPerson(sc scanner) (Person, error) {
	var (
		p          Person
		tmdb, tvdb sql.NullInt64
	)
	err := sc.Scan(&p.ID, &tmdb, &tvdb, &p.Name, &p.Profile, &p.Biography, &p.Birthday, &p.Deathday,
		&p.PlaceOfBirth, &p.KnownFor, &p.IMDbID, &p.FetchedAt)
	p.TMDBID, p.TVDBID = tmdb.Int64, tvdb.Int64
	return p, err
}

// GetPerson returns the person and their credits in titles that are in the
// library, newest first, or ErrNotFound.
func (s *Store) GetPerson(ctx context.Context, id int64) (d PersonDetail, err error) {
	err = s.snapshot(ctx, func(q queryer) error {
		p, err := scanPerson(q.QueryRowContext(ctx, `SELECT `+personCols+` FROM people WHERE id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		d.Person = p
		rows, err := q.QueryContext(ctx, `
			SELECT c.kind, c.job, c.character, `+cardCols+`
			FROM credits c JOIN titles t ON t.id = c.title_id`+cardJoins+`
			WHERE c.person_id = ? AND `+cardInLibrary+`
			ORDER BY t.year DESC, t.release_date DESC, LOWER(t.title), t.id, c.kind, c.ord, c.job`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pc PersonCredit
			if pc.Title, err = scanCard(rows, &pc.Kind, &pc.Job, &pc.Character); err != nil {
				return err
			}
			d.Credits = append(d.Credits, pc)
		}
		return rows.Err()
	})
	return d, err
}

// SetPersonDetails stores what the person page shows (biography, birthday,
// deathday, place of birth, known for, IMDb id and, when not empty, the photo)
// and marks the details fetched. It returns ErrNotFound for an unknown person.
func (s *Store) SetPersonDetails(ctx context.Context, id int64, p Person, fetchedAt int64) error {
	res, err := s.exec(ctx, prioHigh, `
		UPDATE people SET biography = ?, birthday = ?, deathday = ?, place_of_birth = ?, known_for = ?,
			imdb_id = ?, profile = CASE WHEN ? <> '' THEN ? ELSE profile END, fetched_at = ?
		WHERE id = ?`,
		p.Biography, p.Birthday, p.Deathday, p.PlaceOfBirth, p.KnownFor, p.IMDbID, p.Profile, p.Profile,
		fetchedAt, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SearchPeople finds people by name (substring, case-insensitive; names that
// start with q first) who have at least one credit in a title of the library.
func (s *Store) SearchPeople(ctx context.Context, q string, limit int) ([]PersonCard, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	rows, err := s.ro.QueryContext(ctx, `
		SELECT p.id, p.name, p.profile, p.known_for FROM people p
		WHERE p.name LIKE ? ESCAPE '\'
			AND EXISTS (SELECT 1 FROM credits c JOIN titles t ON t.id = c.title_id
				WHERE c.person_id = p.id AND `+libraryTitle+`)
		ORDER BY p.name NOT LIKE ? ESCAPE '\', LOWER(p.name), p.id LIMIT ?`,
		"%"+esc+"%", esc+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PersonCard
	for rows.Next() {
		var p PersonCard
		if err := rows.Scan(&p.ID, &p.Name, &p.Profile, &p.KnownFor); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ReviewEpisode is an episode the scanner flagged: its file name has no episode
// number, so it is episode 0 of its season.
type ReviewEpisode struct {
	ID   int64
	Path string
}

// ReviewEntry is a movie or show that needs a person's attention: nothing
// matched, a match the scorer was not sure about, or (a show) an episode whose
// name has no episode number.
type ReviewEntry struct {
	Kind        string // KindMovie | KindSeries
	ItemID      int64  // movies
	Show        string // shows
	Title       string // as parsed from the file name; the show name for shows
	Year        int
	MatchState  string
	NeedsReview bool
	Score       float64
	Candidate   *TitleCard // the title it is linked to, if any
	// Episodes are the show's flagged episodes, which are why it needs review when
	// its match does not.
	Episodes []ReviewEpisode
}

// ReviewList returns the movies and shows that are unmatched or need review (a
// show also does when one of its episodes is flagged), unmatched first, then by
// title.
func (s *Store) ReviewList(ctx context.Context) (list []ReviewEntry, err error) {
	err = s.snapshot(ctx, func(q queryer) error {
		rows, err := q.QueryContext(ctx, `
			SELECT 'movie', id, '', title, year, match_state, needs_review, match_score, COALESCE(title_id, 0)
			FROM items WHERE kind = 'movie' AND (match_state = 'unmatched' OR needs_review = 1)
			UNION ALL
			SELECT 'series', 0, e.show, e.show, 0, COALESCE(sm.match_state, 'pending'),
				COALESCE(sm.needs_review, 0) = 1 OR e.flagged = 1, COALESCE(sm.match_score, 0), COALESCE(sm.title_id, 0)
			FROM (SELECT show, MAX(needs_review) AS flagged FROM items WHERE kind = 'episode' GROUP BY show) e
			LEFT JOIN series_matches sm ON sm.show = e.show
			WHERE COALESCE(sm.match_state, 'pending') = 'unmatched' OR COALESCE(sm.needs_review, 0) = 1
				OR e.flagged = 1`)
		if err != nil {
			return err
		}
		defer rows.Close()
		var titleIDs []int64
		var candidates []int64 // parallel to list
		for rows.Next() {
			var (
				e   ReviewEntry
				tid int64
			)
			if err := rows.Scan(&e.Kind, &e.ItemID, &e.Show, &e.Title, &e.Year, &e.MatchState,
				&e.NeedsReview, &e.Score, &tid); err != nil {
				return err
			}
			list = append(list, e)
			candidates = append(candidates, tid)
			if tid != 0 {
				titleIDs = append(titleIDs, tid)
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()

		// The episodes the scanner flagged, with the show they are in.
		flagged, err := q.QueryContext(ctx, `
			SELECT show, id, path FROM items WHERE kind = 'episode' AND needs_review = 1 ORDER BY show, path`)
		if err != nil {
			return err
		}
		defer flagged.Close()
		byShow := map[string][]ReviewEpisode{}
		for flagged.Next() {
			var (
				show string
				ep   ReviewEpisode
			)
			if err := flagged.Scan(&show, &ep.ID, &ep.Path); err != nil {
				return err
			}
			byShow[show] = append(byShow[show], ep)
		}
		if err := flagged.Err(); err != nil {
			return err
		}
		flagged.Close()
		for i := range list {
			if list[i].Kind == KindSeries {
				list[i].Episodes = byShow[list[i].Show]
			}
		}

		cards, err := cardsByID(ctx, q, titleIDs)
		if err != nil {
			return err
		}
		for i, tid := range candidates {
			if c, ok := cards[tid]; ok {
				list[i].Candidate = &c
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(list, func(a, b ReviewEntry) int {
		if au, bu := a.MatchState == MatchUnmatched, b.MatchState == MatchUnmatched; au != bu {
			if au {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	})
	return list, nil
}

// Seasons returns the stored seasons of a show with their stored episode counts.
func (s *Store) Seasons(ctx context.Context, titleID int64) ([]Season, error) {
	rows, err := s.ro.QueryContext(ctx, `
		SELECT se.season, se.name, se.overview, se.poster, se.air_date,
			(SELECT COUNT(*) FROM episodes e WHERE e.title_id = se.title_id AND e.season = se.season)
		FROM seasons se WHERE se.title_id = ? ORDER BY se.season`, titleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Season
	for rows.Next() {
		var se Season
		if err := rows.Scan(&se.Number, &se.Name, &se.Overview, &se.Poster, &se.AirDate, &se.EpisodeCount); err != nil {
			return nil, err
		}
		out = append(out, se)
	}
	return out, rows.Err()
}

// Episodes returns every stored episode of a show, by season and number.
func (s *Store) Episodes(ctx context.Context, titleID int64) ([]Episode, error) {
	rows, err := s.ro.QueryContext(ctx, `
		SELECT season, episode, name, overview, air_date, runtime_min, still, rating
		FROM episodes WHERE title_id = ? ORDER BY season, episode`, titleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Episode
	for rows.Next() {
		var e Episode
		if err := rows.Scan(&e.Season, &e.Number, &e.Name, &e.Overview, &e.AirDate, &e.RuntimeMin, &e.Still, &e.Rating); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetEpisode returns one stored episode, or ErrNotFound.
func (s *Store) GetEpisode(ctx context.Context, titleID int64, season, episode int) (Episode, error) {
	var e Episode
	err := s.ro.QueryRowContext(ctx, `
		SELECT season, episode, name, overview, air_date, runtime_min, still, rating
		FROM episodes WHERE title_id = ? AND season = ? AND episode = ?`, titleID, season, episode).
		Scan(&e.Season, &e.Number, &e.Name, &e.Overview, &e.AirDate, &e.RuntimeMin, &e.Still, &e.Rating)
	if errors.Is(err, sql.ErrNoRows) {
		return Episode{}, ErrNotFound
	}
	return e, err
}

// SeasonsNeedingFetch returns the seasons that have episode files in the
// library, linked to the show title, and no stored episodes yet.
func (s *Store) SeasonsNeedingFetch(ctx context.Context, titleID int64) ([]int, error) {
	rows, err := s.ro.QueryContext(ctx, `
		SELECT DISTINCT i.season FROM series_matches sm JOIN items i ON i.kind = 'episode' AND i.show = sm.show
		WHERE sm.title_id = ?
			AND NOT EXISTS (SELECT 1 FROM episodes e WHERE e.title_id = sm.title_id AND e.season = i.season)
		ORDER BY i.season`, titleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var seasons []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		seasons = append(seasons, n)
	}
	return seasons, rows.Err()
}
