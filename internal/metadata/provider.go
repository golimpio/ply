package metadata

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"ply/internal/store"
)

// Candidate is a title a search found, in the terms the matcher scores.
type Candidate struct {
	TMDBID        int64
	Title         string
	OriginalTitle string
	Year          int // 0 = unknown
	Overview      string
	Poster        string // image ref, "tmdb:/abc.jpg"
	Popularity    float64
}

// Provider is where metadata comes from. TMDB implements it; the worker and
// the tests talk to this interface so that they can use a fake.
//
// Every error that satisfies IsTransient is worth retrying later; any other is
// final for that request (not found, bad key).
type Provider interface {
	// SearchMovies and SearchSeries return the candidates in relevance order.
	// year 0 searches any year.
	SearchMovies(ctx context.Context, query string, year int) ([]Candidate, error)
	SearchSeries(ctx context.Context, query string, year int) ([]Candidate, error)
	// Find looks a title up by an external id (ExternalIMDb, ExternalTVDB).
	Find(ctx context.Context, source, id string) (movies, series []Candidate, err error)
	// Movie returns a movie ready for store.UpsertTitle: details, credits,
	// certification, artwork and its collection.
	Movie(ctx context.Context, tmdbID int64) (*store.TitleInput, error)
	// Series returns a show with the given seasons (and their episodes).
	// Seasons TMDB does not know are left out.
	Series(ctx context.Context, tmdbID int64, seasons []int) (*store.TitleInput, error)
	// Person returns the details of a person; the TMDBID is set.
	Person(ctx context.Context, tmdbID int64) (*store.Person, error)
	// Changes returns the ids of the movies or shows (store.KindMovie,
	// store.KindSeries) that changed between start and end.
	Changes(ctx context.Context, kind string, start, end time.Time) ([]int64, error)
	// Check reports whether the credentials work.
	Check(ctx context.Context) error
	// ImageURL is where an image can be downloaded: size "w342", "original".
	ImageURL(size, file string) string
}

// TMDB is the Provider backed by themoviedb.org.
type TMDB struct{ c *Client }

var _ Provider = (*TMDB)(nil)

// NewTMDB wraps a client.
func NewTMDB(c *Client) *TMDB { return &TMDB{c: c} }

// Limits of what is stored per title.
const (
	maxCast = 30
)

// crewJobs are the crew jobs kept, most important first.
var crewJobs = []string{"Director", "Writer", "Screenplay", "Story", "Novel", "Original Music Composer",
	"Director of Photography", "Producer"}

// ImageRef is the stored reference of a TMDB file path: "tmdb:/abc.jpg".
// An empty path gives an empty ref.
func ImageRef(path string) string {
	if path == "" {
		return ""
	}
	return "tmdb:" + path
}

func (p *TMDB) SearchMovies(ctx context.Context, query string, year int) ([]Candidate, error) {
	res, err := p.c.SearchMovies(ctx, query, year)
	if err != nil {
		return nil, err
	}
	return movieCandidates(res), nil
}

func (p *TMDB) SearchSeries(ctx context.Context, query string, year int) ([]Candidate, error) {
	res, err := p.c.SearchTV(ctx, query, year)
	if err != nil {
		return nil, err
	}
	return tvCandidates(res), nil
}

func movieCandidates(res []MovieResult) []Candidate {
	out := make([]Candidate, 0, len(res))
	for _, r := range res {
		out = append(out, Candidate{TMDBID: r.ID, Title: r.Title, OriginalTitle: r.OriginalTitle,
			Year: yearOf(r.ReleaseDate), Overview: r.Overview, Poster: ImageRef(r.PosterPath), Popularity: r.Popularity})
	}
	return out
}

func tvCandidates(res []TVResult) []Candidate {
	out := make([]Candidate, 0, len(res))
	for _, r := range res {
		out = append(out, Candidate{TMDBID: r.ID, Title: r.Name, OriginalTitle: r.OriginalName,
			Year: yearOf(r.FirstAirDate), Overview: r.Overview, Poster: ImageRef(r.PosterPath), Popularity: r.Popularity})
	}
	return out
}

func (p *TMDB) Find(ctx context.Context, source, id string) (movies, series []Candidate, err error) {
	res, err := p.c.Find(ctx, source, id)
	if err != nil {
		return nil, nil, err
	}
	return movieCandidates(res.MovieResults), tvCandidates(res.TVResults), nil
}

func (p *TMDB) Movie(ctx context.Context, tmdbID int64) (*store.TitleInput, error) {
	d, err := p.c.MovieDetails(ctx, tmdbID)
	if err != nil {
		return nil, err
	}
	var coll *CollectionDetails
	if ref := d.BelongsToCollection; ref != nil && ref.ID != 0 {
		coll, err = p.c.CollectionDetails(ctx, ref.ID)
		if err != nil {
			if IsTransient(err) {
				return nil, err
			}
			// The collection is gone or unreadable: keep what the movie says of it.
			coll = &CollectionDetails{ID: ref.ID, Name: ref.Name, PosterPath: ref.PosterPath, BackdropPath: ref.BackdropPath}
		}
	}
	return movieInput(d, coll, p.c.langCode(), p.c.Region()), nil
}

func (p *TMDB) Series(ctx context.Context, tmdbID int64, seasons []int) (*store.TitleInput, error) {
	d, err := p.c.TVDetails(ctx, tmdbID)
	if err != nil {
		return nil, err
	}
	in := tvInput(d, p.c.langCode(), p.c.Region())
	seen := map[int]bool{}
	for _, n := range seasons {
		if seen[n] {
			continue
		}
		seen[n] = true
		sd, err := p.c.SeasonDetails(ctx, tmdbID, n)
		if IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		in.Seasons = append(in.Seasons, seasonInput(sd))
	}
	slices.SortFunc(in.Seasons, func(a, b store.Season) int { return a.Number - b.Number })
	return in, nil
}

func (p *TMDB) Person(ctx context.Context, tmdbID int64) (*store.Person, error) {
	d, err := p.c.PersonDetails(ctx, tmdbID)
	if err != nil {
		return nil, err
	}
	return &store.Person{TMDBID: d.ID, Name: d.Name, Profile: ImageRef(d.ProfilePath), Biography: d.Biography,
		Birthday: d.Birthday, Deathday: d.Deathday, PlaceOfBirth: d.PlaceOfBirth,
		KnownFor: d.KnownForDepartment, IMDbID: d.IMDbID}, nil
}

func (p *TMDB) Changes(ctx context.Context, kind string, start, end time.Time) ([]int64, error) {
	if kind == store.KindSeries {
		return p.c.TVChanges(ctx, start, end)
	}
	return p.c.MovieChanges(ctx, start, end)
}

func (p *TMDB) Check(ctx context.Context) error {
	_, err := p.c.Configuration(ctx)
	return err
}

func (p *TMDB) ImageURL(size, file string) string { return p.c.ImageURL(size, file) }

// movieInput maps a movie fetch to a title. lang is the language code ("en")
// logos are chosen in, region the country ("US") certifications are read for.
func movieInput(d *MovieDetails, coll *CollectionDetails, lang, region string) *store.TitleInput {
	imdb := d.IMDbID
	if imdb == "" {
		imdb = d.ExternalIDs.IMDbID
	}
	in := &store.TitleInput{Title: store.Title{
		Kind: store.KindMovie, TMDBID: d.ID, IMDbID: imdb,
		Title: d.Title, OriginalTitle: d.OriginalTitle, SortTitle: SortTitle(d.Title),
		Year: yearOf(d.ReleaseDate), Overview: d.Overview, Tagline: d.Tagline,
		ReleaseDate: d.ReleaseDate, Status: d.Status, RuntimeMin: d.Runtime,
		Rating: d.VoteAverage, VoteCount: d.VoteCount,
		Certification: movieCertification(d.ReleaseDates, region),
		Poster:        ImageRef(d.PosterPath), Backdrop: ImageRef(d.BackdropPath), Logo: ImageRef(bestLogo(d.Images, lang)),
		Source: store.SourceTMDB,
	}}
	for _, g := range d.Genres {
		in.Genres = append(in.Genres, store.Genre{ID: g.ID, Name: g.Name})
	}
	if coll != nil {
		in.Collection = &store.Collection{TMDBID: coll.ID, Name: coll.Name, Overview: coll.Overview,
			Poster: ImageRef(coll.PosterPath), Backdrop: ImageRef(coll.BackdropPath)}
	}

	cast := slices.Clone(d.Credits.Cast)
	slices.SortStableFunc(cast, func(a, b CastCredit) int { return a.Order - b.Order })
	for i, c := range cast[:min(len(cast), maxCast)] {
		in.Credits = append(in.Credits, castCredit(c.ID, c.Name, c.Character, c.ProfilePath, i))
	}
	var crew []store.Credit
	for _, c := range d.Credits.Crew {
		if slices.Contains(crewJobs, c.Job) {
			crew = append(crew, crewCredit(c.ID, c.Name, c.Job, c.ProfilePath))
		}
	}
	in.Credits = append(in.Credits, orderCrew(crew)...)
	return in
}

// tvInput maps a show fetch (without seasons) to a title.
func tvInput(d *TVDetails, lang, region string) *store.TitleInput {
	imdb := d.ExternalIDs.IMDbID
	in := &store.TitleInput{Title: store.Title{
		Kind: store.KindSeries, TMDBID: d.ID, IMDbID: imdb, TVDBID: d.ExternalIDs.TVDBID,
		Title: d.Name, OriginalTitle: d.OriginalName, SortTitle: SortTitle(d.Name),
		Year: yearOf(d.FirstAirDate), Overview: d.Overview, Tagline: d.Tagline,
		ReleaseDate: d.FirstAirDate, EndDate: d.LastAirDate, Status: d.Status,
		Rating: d.VoteAverage, VoteCount: d.VoteCount,
		Certification: tvCertification(d.ContentRatings, region),
		Poster:        ImageRef(d.PosterPath), Backdrop: ImageRef(d.BackdropPath), Logo: ImageRef(bestLogo(d.Images, lang)),
		Source: store.SourceTMDB,
	}}
	for _, n := range d.EpisodeRunTime {
		if n > 0 {
			in.RuntimeMin = n
			break
		}
	}
	if d.NextEpisodeToAir != nil {
		in.NextAirDate = d.NextEpisodeToAir.AirDate
	}
	for _, g := range d.Genres {
		in.Genres = append(in.Genres, store.Genre{ID: g.ID, Name: g.Name})
	}

	var crew []store.Credit
	for _, c := range d.CreatedBy {
		crew = append(crew, crewCredit(c.ID, c.Name, "Creator", c.ProfilePath))
	}
	cast := slices.Clone(d.AggregateCredits.Cast)
	slices.SortStableFunc(cast, func(a, b AggregateCast) int { return a.Order - b.Order })
	for i, c := range cast[:min(len(cast), maxCast)] {
		character := ""
		best := -1
		for _, r := range c.Roles { // the role played in most episodes
			if r.EpisodeCount > best {
				character, best = r.Character, r.EpisodeCount
			}
		}
		in.Credits = append(in.Credits, castCredit(c.ID, c.Name, character, c.ProfilePath, i))
	}
	for _, c := range d.AggregateCredits.Crew {
		for _, j := range c.Jobs {
			if slices.Contains(crewJobs, j.Job) {
				crew = append(crew, crewCredit(c.ID, c.Name, j.Job, c.ProfilePath))
			}
		}
	}
	in.Credits = append(in.Credits, orderCrew(crew)...)
	return in
}

func seasonInput(sd *SeasonDetails) store.Season {
	se := store.Season{Number: sd.SeasonNumber, Name: sd.Name, Overview: sd.Overview,
		Poster: ImageRef(sd.PosterPath), AirDate: sd.AirDate}
	for _, e := range sd.Episodes {
		se.Episodes = append(se.Episodes, store.Episode{Season: sd.SeasonNumber, Number: e.EpisodeNumber,
			Name: e.Name, Overview: e.Overview, AirDate: e.AirDate, RuntimeMin: e.Runtime,
			Still: ImageRef(e.StillPath), Rating: e.VoteAverage})
	}
	return se
}

func castCredit(id int64, name, character, profile string, order int) store.Credit {
	return store.Credit{Person: store.Person{TMDBID: id, Name: name, Profile: ImageRef(profile)},
		Kind: store.CreditCast, Character: character, Order: order}
}

func crewCredit(id int64, name, job, profile string) store.Credit {
	return store.Credit{Person: store.Person{TMDBID: id, Name: name, Profile: ImageRef(profile)},
		Kind: store.CreditCrew, Job: job}
}

// orderCrew sorts crew by the importance of the job ("Creator" first, then
// crewJobs), keeping TMDB's order within a job, and numbers them.
func orderCrew(crew []store.Credit) []store.Credit {
	rank := func(job string) int {
		if job == "Creator" {
			return -1
		}
		return slices.Index(crewJobs, job)
	}
	slices.SortStableFunc(crew, func(a, b store.Credit) int { return rank(a.Job) - rank(b.Job) })
	for i := range crew {
		crew[i].Order = i
	}
	return crew
}

// movieCertification is the certification of the first release in the region,
// else in the US. Theatrical releases (type 3) count before the others.
func movieCertification(rd ReleaseDates, region string) string {
	for _, country := range []string{region, "US"} {
		for _, r := range rd.Results {
			if r.Country != country {
				continue
			}
			for _, pass := range []func(typ int) bool{func(t int) bool { return t == 3 }, func(int) bool { return true }} {
				for _, rel := range r.ReleaseDates {
					if rel.Certification != "" && pass(rel.Type) {
						return rel.Certification
					}
				}
			}
		}
	}
	return ""
}

func tvCertification(cr ContentRatings, region string) string {
	for _, country := range []string{region, "US"} {
		for _, r := range cr.Results {
			if r.Country == country && r.Rating != "" {
				return r.Rating
			}
		}
	}
	return ""
}

// bestLogo picks the highest-rated logo in the language, else the highest-rated
// textless one, and returns its file path ("" when there is none).
func bestLogo(imgs Images, lang string) string {
	var best *Image
	bestRank := -1
	for i := range imgs.Logos {
		l := &imgs.Logos[i]
		rank := -1
		switch {
		case l.Language != nil && strings.EqualFold(*l.Language, lang):
			rank = 2
		case l.Language == nil || *l.Language == "":
			rank = 1
		}
		if rank < 0 || l.FilePath == "" {
			continue
		}
		if best == nil || rank > bestRank ||
			(rank == bestRank && (l.VoteAverage > best.VoteAverage ||
				(l.VoteAverage == best.VoteAverage && l.VoteCount > best.VoteCount))) {
			best, bestRank = l, rank
		}
	}
	if best == nil {
		return ""
	}
	return best.FilePath
}

// yearOf returns the year of a YYYY-MM-DD date, or 0.
func yearOf(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, err := strconv.Atoi(date[:4])
	if err != nil || y < 1800 || y > 2200 {
		return 0
	}
	return y
}

// SortTitle is the title with a leading article moved to the end: "The Matrix"
// sorts as "Matrix, The".
func SortTitle(title string) string {
	title = strings.TrimSpace(title)
	for _, article := range []string{"The", "A", "An"} {
		if len(title) > len(article)+1 && strings.EqualFold(title[:len(article)], article) && title[len(article)] == ' ' {
			rest := strings.TrimSpace(title[len(article)+1:])
			if rest != "" {
				return rest + ", " + title[:len(article)]
			}
		}
	}
	return title
}
