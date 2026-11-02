package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// KindSeries is the kind of a show's title (movies use KindMovie).
const KindSeries = "series"

// Match states of an item or series.
const (
	MatchPending   = "pending"   // not looked at yet, or waiting for a retry
	MatchMatched   = "matched"   // linked to a title (see NeedsReview)
	MatchUnmatched = "unmatched" // searched, nothing good enough
	MatchManual    = "manual"    // the user decided; never re-matched automatically
	MatchDisabled  = "disabled"
)

// Where a title's data came from.
const (
	SourceTMDB  = "tmdb"
	SourceNFO   = "nfo"
	SourceMixed = "mixed" // TMDB data with NFO text on top
)

// Credit kinds.
const (
	CreditCast = "cast"
	CreditCrew = "crew"
)

// Title is a movie or a show as the metadata sources describe it. Zero values
// mean unknown. Images are refs: "tmdb:/abc.jpg".
type Title struct {
	ID            int64
	Kind          string // KindMovie | KindSeries
	TMDBID        int64
	IMDbID        string
	TVDBID        int64
	Title         string
	OriginalTitle string
	SortTitle     string
	Year          int
	Overview      string
	Tagline       string
	ReleaseDate   string // YYYY-MM-DD; a show's first air date
	EndDate       string // a show's last air date
	Status        string
	RuntimeMin    int
	Rating        float64
	VoteCount     int
	Certification string
	Poster        string
	Backdrop      string
	Logo          string
	CollectionID  int64 // 0 = none
	Source        string
	Locked        bool   // the user edited the text: a refresh keeps it
	NextAirDate   string // a show's next episode, YYYY-MM-DD
	FetchedAt     int64  // unix seconds; 0 = never
	CreatedAt     int64
	UpdatedAt     int64
}

// Genre is a TMDB genre, or an NFO-only one (negative id; UpsertTitle assigns
// the id when it is 0).
type Genre struct {
	ID   int64
	Name string
}

// Collection is a movie collection ("The Matrix Collection").
type Collection struct {
	ID       int64
	TMDBID   int64 // 0 = an NFO-only collection, deduped by exact name
	Name     string
	Overview string
	Poster   string
	Backdrop string
}

// Person is an actor or crew member. UpsertTitle only writes ID, TMDBID, Name
// and Profile of a credit's person; SetPersonDetails writes the rest.
type Person struct {
	ID           int64
	TMDBID       int64 // 0 = NFO-only, deduped by exact name
	Name         string
	Profile      string
	Biography    string
	Birthday     string
	Deathday     string
	PlaceOfBirth string
	KnownFor     string
	IMDbID       string
	FetchedAt    int64 // unix seconds; 0 = details never fetched
}

// Credit ties a person to a title. Job is empty for cast.
type Credit struct {
	Person    Person
	Kind      string // CreditCast | CreditCrew
	Job       string
	Character string
	Order     int
}

// Season is one season of a show. Episodes are the fetched episodes of the
// season; UpsertTitle replaces the season's episodes with them.
type Season struct {
	Number   int
	Name     string
	Overview string
	Poster   string
	AirDate  string
	Episodes []Episode

	// EpisodeCount is filled by Seasons: the number of stored episodes.
	EpisodeCount int
}

// Episode is the metadata of one episode.
type Episode struct {
	Season     int
	Number     int
	Name       string
	Overview   string
	AirDate    string
	RuntimeMin int
	Still      string
	Rating     float64
}

// TitleInput is everything UpsertTitle writes in one transaction.
//
// The row is found by (Kind, TMDBID); without a TMDBID, by ID when that row
// has no TMDB id either (an NFO-only title); otherwise a row is inserted.
// A locked title keeps its title, sort title, year, overview and tagline.
type TitleInput struct {
	Title
	Genres     []Genre
	Collection *Collection // nil = the title belongs to none
	Credits    []Credit
	Seasons    []Season // the seasons fetched; others are left alone
}

const titleCols = `id, kind, tmdb_id, imdb_id, tvdb_id, title, original_title, sort_title, year,
	overview, tagline, release_date, end_date, status, runtime_min, rating, vote_count, certification,
	poster, backdrop, logo, collection_id, source, locked, next_air_date, fetched_at, created_at, updated_at`

func scanTitle(sc scanner) (Title, error) {
	var (
		t                      Title
		tmdb, tvdb, collection sql.NullInt64
		nextAir                sql.NullString
	)
	err := sc.Scan(&t.ID, &t.Kind, &tmdb, &t.IMDbID, &tvdb, &t.Title, &t.OriginalTitle, &t.SortTitle,
		&t.Year, &t.Overview, &t.Tagline, &t.ReleaseDate, &t.EndDate, &t.Status, &t.RuntimeMin,
		&t.Rating, &t.VoteCount, &t.Certification, &t.Poster, &t.Backdrop, &t.Logo, &collection,
		&t.Source, &t.Locked, &nextAir, &t.FetchedAt, &t.CreatedAt, &t.UpdatedAt)
	t.TMDBID, t.TVDBID, t.CollectionID = tmdb.Int64, tvdb.Int64, collection.Int64
	t.NextAirDate = nextAir.String
	return t, err
}

// nullInt maps 0 to NULL, for ids that are unique when present.
func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// UpsertTitle writes the title, its genres, credits (and the people in them),
// collection, seasons and episodes in one transaction and returns the title id.
// Genres and credits are replaced; seasons are replaced only for the ones given.
func (s *Store) UpsertTitle(ctx context.Context, in *TitleInput) (int64, error) {
	if in.Kind != KindMovie && in.Kind != KindSeries {
		return 0, fmt.Errorf("store: title kind %q", in.Kind)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	id, err := upsertTitleRow(ctx, tx, in)
	if err != nil {
		return 0, err
	}
	if err := setGenres(ctx, tx, id, in.Genres); err != nil {
		return 0, err
	}
	if err := setCredits(ctx, tx, id, in.Credits); err != nil {
		return 0, err
	}
	if err := setSeasons(ctx, tx, id, in.Seasons); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func upsertTitleRow(ctx context.Context, tx *sql.Tx, in *TitleInput) (int64, error) {
	now := time.Now().Unix()
	fetched := in.FetchedAt
	if fetched == 0 {
		fetched = now
	}
	source := in.Source
	switch source {
	case SourceTMDB, SourceNFO, SourceMixed:
	case "":
		source = SourceNFO
		if in.TMDBID != 0 {
			source = SourceTMDB
		}
	default:
		return 0, fmt.Errorf("store: title source %q", source)
	}
	collection, err := upsertCollection(ctx, tx, in.Collection)
	if err != nil {
		return 0, err
	}

	id, err := findTitle(ctx, tx, in)
	if err != nil {
		return 0, err
	}
	if id == 0 {
		err = tx.QueryRowContext(ctx, `
			INSERT INTO titles (kind, tmdb_id, imdb_id, tvdb_id, title, original_title, sort_title, year,
				overview, tagline, release_date, end_date, status, runtime_min, rating, vote_count,
				certification, poster, backdrop, logo, collection_id, source, next_air_date,
				fetched_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?)
			RETURNING id`,
			in.Kind, nullInt(in.TMDBID), in.IMDbID, nullInt(in.TVDBID), in.Title.Title, in.OriginalTitle,
			in.SortTitle, in.Year, in.Overview, in.Tagline, in.ReleaseDate, in.EndDate, in.Status,
			in.RuntimeMin, in.Rating, in.VoteCount, in.Certification, in.Poster, in.Backdrop, in.Logo,
			collection, source, in.NextAirDate, fetched, now, now).Scan(&id)
		return id, err
	}

	// The text the user can edit stays when the title is locked. Images are
	// never blanked: a source that has none right now must not remove the ones
	// we have.
	_, err = tx.ExecContext(ctx, `
		UPDATE titles SET
			tmdb_id = ?, imdb_id = ?, tvdb_id = ?, original_title = ?,
			title      = CASE WHEN locked = 1 THEN title      ELSE ? END,
			sort_title = CASE WHEN locked = 1 THEN sort_title ELSE ? END,
			year       = CASE WHEN locked = 1 THEN year       ELSE ? END,
			overview   = CASE WHEN locked = 1 THEN overview   ELSE ? END,
			tagline    = CASE WHEN locked = 1 THEN tagline    ELSE ? END,
			release_date = ?, end_date = ?, status = ?, runtime_min = ?, rating = ?, vote_count = ?,
			certification = ?,
			poster   = CASE WHEN ? <> '' THEN ? ELSE poster   END,
			backdrop = CASE WHEN ? <> '' THEN ? ELSE backdrop END,
			logo     = CASE WHEN ? <> '' THEN ? ELSE logo     END,
			collection_id = ?, source = ?, next_air_date = NULLIF(?, ''), fetched_at = ?, updated_at = ?
		WHERE id = ?`,
		nullInt(in.TMDBID), in.IMDbID, nullInt(in.TVDBID), in.OriginalTitle,
		in.Title.Title, in.SortTitle, in.Year, in.Overview, in.Tagline,
		in.ReleaseDate, in.EndDate, in.Status, in.RuntimeMin, in.Rating, in.VoteCount, in.Certification,
		in.Poster, in.Poster, in.Backdrop, in.Backdrop, in.Logo, in.Logo,
		collection, source, in.NextAirDate, fetched, now, id)
	return id, err
}

// findTitle returns the id of the row UpsertTitle updates, or 0.
func findTitle(ctx context.Context, tx *sql.Tx, in *TitleInput) (int64, error) {
	var id int64
	if in.TMDBID != 0 {
		err := tx.QueryRowContext(ctx, `SELECT id FROM titles WHERE kind = ? AND tmdb_id = ?`,
			in.Kind, in.TMDBID).Scan(&id)
		if !errors.Is(err, sql.ErrNoRows) {
			return id, err
		}
	}
	if in.ID == 0 {
		return 0, nil
	}
	// Addressing by id is for titles that have no TMDB identity yet; a row that
	// has one belongs to that identity.
	err := tx.QueryRowContext(ctx, `SELECT id FROM titles WHERE id = ? AND kind = ? AND tmdb_id IS NULL`,
		in.ID, in.Kind).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func upsertCollection(ctx context.Context, tx *sql.Tx, c *Collection) (any, error) {
	if c == nil {
		return nil, nil
	}
	var id int64
	var err error
	if c.TMDBID != 0 {
		err = tx.QueryRowContext(ctx, `
			INSERT INTO collections (tmdb_id, name, overview, poster, backdrop) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (tmdb_id) DO UPDATE SET name = excluded.name, overview = excluded.overview,
				poster   = CASE WHEN excluded.poster   <> '' THEN excluded.poster   ELSE poster   END,
				backdrop = CASE WHEN excluded.backdrop <> '' THEN excluded.backdrop ELSE backdrop END
			RETURNING id`, c.TMDBID, c.Name, c.Overview, c.Poster, c.Backdrop).Scan(&id)
		return id, err
	}
	err = tx.QueryRowContext(ctx, `SELECT id FROM collections WHERE tmdb_id IS NULL AND name = ?`, c.Name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `
			INSERT INTO collections (name, overview, poster, backdrop) VALUES (?, ?, ?, ?) RETURNING id`,
			c.Name, c.Overview, c.Poster, c.Backdrop).Scan(&id)
	}
	return id, err
}

func setGenres(ctx context.Context, tx *sql.Tx, titleID int64, genres []Genre) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM title_genres WHERE title_id = ?`, titleID); err != nil {
		return err
	}
	for _, g := range genres {
		g.Name = strings.TrimSpace(g.Name)
		if g.Name == "" {
			continue
		}
		id, err := resolveGenre(ctx, tx, g)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO title_genres (title_id, genre_id) VALUES (?, ?)`,
			titleID, id); err != nil {
			return err
		}
	}
	return nil
}

// resolveGenre returns the id of the genre with g's name (compared without
// case), creating it when there is none: with g's id, or a new negative one.
func resolveGenre(ctx context.Context, tx *sql.Tx, g Genre) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM genres WHERE name = ? COLLATE NOCASE`, g.Name).Scan(&id)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return id, err
	}
	if g.ID != 0 {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO genres (id, name) VALUES (?, ?)
			ON CONFLICT (id) DO UPDATE SET name = excluded.name`, g.ID, g.Name)
		return g.ID, err
	}
	err = tx.QueryRowContext(ctx, `SELECT MIN(0, COALESCE(MIN(id), 0)) - 1 FROM genres`).Scan(&id)
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO genres (id, name) VALUES (?, ?)`, id, g.Name)
	return id, err
}

func setCredits(ctx context.Context, tx *sql.Tx, titleID int64, credits []Credit) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM credits WHERE title_id = ?`, titleID); err != nil {
		return err
	}
	people := map[Person]int64{} // credits repeat people: look each up once
	for _, c := range credits {
		if c.Kind != CreditCast && c.Kind != CreditCrew {
			return fmt.Errorf("store: credit kind %q", c.Kind)
		}
		c.Person.Name = strings.TrimSpace(c.Person.Name)
		if c.Person.Name == "" {
			continue
		}
		key := Person{TMDBID: c.Person.TMDBID, Name: c.Person.Name, Profile: c.Person.Profile}
		pid, ok := people[key]
		if !ok {
			var err error
			if pid, err = upsertPerson(ctx, tx, key); err != nil {
				return err
			}
			people[key] = pid
		}
		job := c.Job
		if c.Kind == CreditCast {
			job = ""
		}
		// A person can be listed twice for one job; the first (lowest order) wins.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO credits (title_id, person_id, kind, job, character, ord) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT DO NOTHING`, titleID, pid, c.Kind, job, c.Character, c.Order); err != nil {
			return err
		}
	}
	return nil
}

// upsertPerson finds or creates a person by TMDB id, or by exact name when
// there is none. An empty profile never replaces a known one.
func upsertPerson(ctx context.Context, tx *sql.Tx, p Person) (int64, error) {
	var id int64
	if p.TMDBID != 0 {
		err := tx.QueryRowContext(ctx, `
			INSERT INTO people (tmdb_id, name, profile) VALUES (?, ?, ?)
			ON CONFLICT (tmdb_id) DO UPDATE SET name = excluded.name,
				profile = CASE WHEN excluded.profile <> '' THEN excluded.profile ELSE profile END
			RETURNING id`, p.TMDBID, p.Name, p.Profile).Scan(&id)
		return id, err
	}
	err := tx.QueryRowContext(ctx, `SELECT id FROM people WHERE tmdb_id IS NULL AND name = ?`, p.Name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `INSERT INTO people (name, profile) VALUES (?, ?) RETURNING id`,
			p.Name, p.Profile).Scan(&id)
	} else if err == nil && p.Profile != "" {
		_, err = tx.ExecContext(ctx, `UPDATE people SET profile = ? WHERE id = ?`, p.Profile, id)
	}
	return id, err
}

func setSeasons(ctx context.Context, tx *sql.Tx, titleID int64, seasons []Season) error {
	for _, se := range seasons {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO seasons (title_id, season, name, overview, poster, air_date) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (title_id, season) DO UPDATE SET name = excluded.name, overview = excluded.overview,
				poster   = CASE WHEN excluded.poster <> '' THEN excluded.poster ELSE poster END,
				air_date = excluded.air_date`,
			titleID, se.Number, se.Name, se.Overview, se.Poster, se.AirDate); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM episodes WHERE title_id = ? AND season = ?`,
			titleID, se.Number); err != nil {
			return err
		}
		for _, ep := range se.Episodes {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO episodes (title_id, season, episode, name, overview, air_date, runtime_min, still, rating)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (title_id, season, episode) DO UPDATE SET name = excluded.name,
					overview = excluded.overview, air_date = excluded.air_date,
					runtime_min = excluded.runtime_min, still = excluded.still, rating = excluded.rating`,
				titleID, se.Number, ep.Number, ep.Name, ep.Overview, ep.AirDate, ep.RuntimeMin, ep.Still,
				ep.Rating); err != nil {
				return err
			}
		}
	}
	return nil
}

// GetTitle returns the title row, or ErrNotFound.
func (s *Store) GetTitle(ctx context.Context, id int64) (Title, error) {
	t, err := scanTitle(s.db.QueryRowContext(ctx, `SELECT `+titleCols+` FROM titles WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Title{}, ErrNotFound
	}
	return t, err
}

// TitlePatch is a user edit; nil fields are left alone.
type TitlePatch struct {
	Title     *string
	SortTitle *string
	Year      *int
	Overview  *string
	Tagline   *string
}

// PatchTitle applies a user edit and locks the title, so that a refresh keeps
// the edited text. It returns ErrNotFound for an unknown id.
func (s *Store) PatchTitle(ctx context.Context, id int64, p TitlePatch) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE titles SET
			title      = COALESCE(?, title),
			sort_title = COALESCE(?, sort_title),
			year       = COALESCE(?, year),
			overview   = COALESCE(?, overview),
			tagline    = COALESCE(?, tagline),
			locked = 1, updated_at = ?
		WHERE id = ?`, p.Title, p.SortTitle, p.Year, p.Overview, p.Tagline, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
