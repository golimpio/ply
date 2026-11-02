package api

import (
	"fmt"
	"strings"

	"ply/internal/store"
)

// Sizes of TMDB images the API links to, per use. (TMDB's own size names.)
const (
	posterSize   = "w342"
	backdropSize = "w1280"
	logoSize     = "w500"
	profileSize  = "w185"
	stillSize    = "w500"
)

// TitleCard is a title as a tile shows it. Poster and the other image fields are
// relative URLs that carry the token.
type TitleCard struct {
	ID       int64    `json:"id"`
	Kind     string   `json:"kind"` // movie | series
	Title    string   `json:"title"`
	Year     int      `json:"year"` // 0 when unknown
	Poster   string   `json:"poster,omitempty"`
	Backdrop string   `json:"backdrop,omitempty"`
	Rating   *float64 `json:"rating,omitempty"`
	ItemID   *int64   `json:"itemId,omitempty"` // movies: the first file
	Show     string   `json:"show,omitempty"`   // shows: the show's name in the library

	// NextAirDate is when a show's next episode airs (YYYY-MM-DD); it can be in the
	// past until the show is next refreshed.
	NextAirDate string `json:"nextAirDate,omitempty"`
}

// Genre, CollectionRef, CastMember and CrewMember are parts of a TitleDetail.
type Genre struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type CollectionRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type CastMember struct {
	PersonID  int64  `json:"personId"`
	Name      string `json:"name"`
	Character string `json:"character,omitempty"`
	Profile   string `json:"profile,omitempty"`
}

type CrewMember struct {
	PersonID int64  `json:"personId"`
	Name     string `json:"name"`
	Job      string `json:"job"`
	Profile  string `json:"profile,omitempty"`
}

// TitleDetail is a title with everything the item page shows.
type TitleDetail struct {
	TitleCard
	TMDBID        int64          `json:"tmdbId,omitempty"`
	IMDbID        string         `json:"imdbId,omitempty"`
	OriginalTitle string         `json:"originalTitle,omitempty"`
	SortTitle     string         `json:"sortTitle"`
	Overview      string         `json:"overview,omitempty"`
	Tagline       string         `json:"tagline,omitempty"`
	ReleaseDate   string         `json:"releaseDate,omitempty"`
	EndDate       string         `json:"endDate,omitempty"`
	Status        string         `json:"status,omitempty"`
	RuntimeMin    *int           `json:"runtimeMin,omitempty"`
	VoteCount     *int           `json:"voteCount,omitempty"`
	Certification string         `json:"certification,omitempty"`
	Logo          string         `json:"logo,omitempty"`
	Genres        []Genre        `json:"genres"`
	Collection    *CollectionRef `json:"collection,omitempty"`
	Cast          []CastMember   `json:"cast"`
	Crew          []CrewMember   `json:"crew"`
	Source        string         `json:"source"` // tmdb | nfo | mixed
	Locked        bool           `json:"locked"`
	MatchState    string         `json:"matchState"`
	NeedsReview   bool           `json:"needsReview"`
}

// NextEpisode is the episode of a show that airs next.
type NextEpisode struct {
	Season  int    `json:"season"`
	Episode int    `json:"episode"`
	Name    string `json:"name"` // "" when TMDB has no name for it yet
}

// UpcomingShow is an entry of GET /api/upcoming. Poster and Backdrop are
// relative URLs that carry the token.
type UpcomingShow struct {
	TitleID     int64        `json:"titleId"`
	Show        string       `json:"show"` // the show's name in the library
	Poster      string       `json:"poster,omitempty"`
	Backdrop    string       `json:"backdrop,omitempty"`
	NextAirDate string       `json:"nextAirDate"` // YYYY-MM-DD, today or later
	NextEpisode *NextEpisode `json:"nextEpisode,omitempty"`
}

// PersonCard is a person as a search result shows them.
type PersonCard struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Profile  string `json:"profile,omitempty"`
	KnownFor string `json:"knownFor,omitempty"`
}

// PersonCredit is one role of a person in a title of the library.
type PersonCredit struct {
	TitleID   int64     `json:"titleId"`
	Kind      string    `json:"kind"` // cast | crew
	Job       string    `json:"job,omitempty"`
	Character string    `json:"character,omitempty"`
	Title     TitleCard `json:"title"`
}

// Person is GET /api/people/{id}.
type Person struct {
	PersonCard
	Biography    string         `json:"biography,omitempty"`
	Birthday     string         `json:"birthday,omitempty"`
	Deathday     string         `json:"deathday,omitempty"`
	PlaceOfBirth string         `json:"placeOfBirth,omitempty"`
	IMDbID       string         `json:"imdbId,omitempty"`
	Credits      []PersonCredit `json:"credits"`
}

// GenreCount is an entry of GET /api/genres.
type GenreCount struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// CollectionDetail is GET /api/collections/{id}.
type CollectionDetail struct {
	ID       int64       `json:"id"`
	Name     string      `json:"name"`
	Overview string      `json:"overview,omitempty"`
	Poster   string      `json:"poster,omitempty"`
	Backdrop string      `json:"backdrop,omitempty"`
	Titles   []TitleCard `json:"titles"`
}

// EpisodeInfo is the metadata of one episode.
type EpisodeInfo struct {
	Name     string   `json:"name,omitempty"`
	Overview string   `json:"overview,omitempty"`
	AirDate  string   `json:"airDate,omitempty"`
	Still    string   `json:"still,omitempty"`
	Rating   *float64 `json:"rating,omitempty"`
}

// MetaFields are the metadata fields that list rows (items, shows) carry.
type MetaFields struct {
	TitleID       *int64   `json:"titleId,omitempty"`
	Poster        string   `json:"poster,omitempty"`
	Backdrop      string   `json:"backdrop,omitempty"`
	Logo          string   `json:"logo,omitempty"`
	Rating        *float64 `json:"rating,omitempty"`
	Certification string   `json:"certification,omitempty"`
	Genres        []string `json:"genres,omitempty"`
	MatchState    string   `json:"matchState"`
	NeedsReview   bool     `json:"needsReview"`
}

// SeasonInfo is a season of GET /api/shows/{name}.
type SeasonInfo struct {
	Season       int    `json:"season"`
	Name         string `json:"name"`
	Overview     string `json:"overview,omitempty"`
	Poster       string `json:"poster,omitempty"`
	AirDate      string `json:"airDate,omitempty"`
	EpisodeCount int    `json:"episodeCount"` // episode files in the library
}

// ShowDetail is GET /api/shows/{name}.
type ShowDetail struct {
	Show         string `json:"show"`
	EpisodeCount int    `json:"episodeCount"`
	Year         *int   `json:"year,omitempty"`
	Overview     string `json:"overview,omitempty"`
	MetaFields
	Meta    *TitleDetail `json:"meta,omitempty"`
	Seasons []SeasonInfo `json:"seasons"`
}

// Candidate is an entry of GET /api/metadata/search.
type Candidate struct {
	TMDBID        int64   `json:"tmdbId"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"originalTitle,omitempty"`
	Year          int     `json:"year"`
	Overview      string  `json:"overview,omitempty"`
	Poster        string  `json:"poster,omitempty"`
	Score         float64 `json:"score"`
}

// ReviewEntry is an entry of GET /api/metadata/review.
type ReviewEntry struct {
	Kind        string     `json:"kind"` // movie | series
	ItemID      *int64     `json:"itemId,omitempty"`
	Show        string     `json:"show,omitempty"`
	Title       string     `json:"title"`
	Year        int        `json:"year"`
	MatchState  string     `json:"matchState"`
	NeedsReview bool       `json:"needsReview"`
	Score       float64    `json:"score"`
	Candidate   *TitleCard `json:"candidate,omitempty"`
}

// SettingsView is GET /api/settings. The key itself is never in it.
type SettingsView struct {
	TMDBKeySet        bool   `json:"tmdbKeySet"`
	TMDBKeySource     string `json:"tmdbKeySource"` // flag | env | settings | default | ""
	MetadataEnabled   bool   `json:"metadataEnabled"`
	MetadataForcedOff bool   `json:"metadataForcedOff"` // -metadata=false: the switch cannot be turned on here
	Language          string `json:"language"`
	// TMDBKeyChecked is in the answer to a PUT that stored a key: whether the key was
	// checked against TMDB. It was not with -metadata=false, when nothing may be sent.
	TMDBKeyChecked *bool `json:"tmdbKeyChecked,omitempty"`
}

// imageURL turns an image ref ("tmdb:/abc.jpg", "local:item:12:poster") into a
// relative URL with the token, or "" for a ref it does not understand. size is
// used for TMDB images.
func (s *Server) imageURL(ref, size string) string {
	switch {
	case strings.HasPrefix(ref, "tmdb:/"):
		file := strings.TrimPrefix(ref, "tmdb:/")
		return s.withToken(fmt.Sprintf("/api/images/tmdb/%s/%s", size, file))
	case strings.HasPrefix(ref, "local:"):
		parts := strings.Split(ref, ":")
		if len(parts) != 4 {
			return ""
		}
		return s.withToken(fmt.Sprintf("/api/images/local/%s/%s/%s", parts[1], parts[2], parts[3]))
	}
	return ""
}

func ptr[T any](v T) *T { return &v }

// optFloat is nil for 0 ("unknown").
func optFloat(v float64) *float64 {
	if v == 0 {
		return nil
	}
	return &v
}

func optInt(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}

func (s *Server) titleCard(c store.TitleCard) TitleCard {
	out := TitleCard{ID: c.ID, Kind: c.Kind, Title: c.Title, Year: c.Year, Poster: s.imageURL(c.Poster, posterSize),
		Backdrop: s.imageURL(c.Backdrop, backdropSize), Rating: optFloat(c.Rating), Show: c.Show,
		NextAirDate: c.NextAirDate}
	if c.ItemID != 0 {
		out.ItemID = ptr(c.ItemID)
	}
	return out
}

func (s *Server) titleCards(cards []store.TitleCard) []TitleCard {
	out := make([]TitleCard, len(cards))
	for i, c := range cards {
		out[i] = s.titleCard(c)
	}
	return out
}

func (s *Server) titleDetail(d store.TitleDetail) *TitleDetail {
	out := &TitleDetail{
		TitleCard: s.titleCard(d.TitleCard),
		TMDBID:    d.TMDBID, IMDbID: d.IMDbID, OriginalTitle: d.OriginalTitle, SortTitle: d.SortTitle,
		Overview: d.Overview, Tagline: d.Tagline, ReleaseDate: d.ReleaseDate, EndDate: d.EndDate,
		Status: d.Status, RuntimeMin: optInt(d.RuntimeMin), VoteCount: optInt(d.VoteCount), Certification: d.Certification,
		Logo:   s.imageURL(d.Logo, logoSize),
		Genres: []Genre{}, Cast: []CastMember{}, Crew: []CrewMember{},
		Source: d.Source, Locked: d.Locked, MatchState: d.MatchState, NeedsReview: d.NeedsReview,
	}
	for _, g := range d.Genres {
		out.Genres = append(out.Genres, Genre{ID: g.ID, Name: g.Name})
	}
	if d.Collection != nil {
		out.Collection = &CollectionRef{ID: d.Collection.ID, Name: d.Collection.Name}
	}
	for _, c := range d.Cast {
		out.Cast = append(out.Cast, CastMember{PersonID: c.PersonID, Name: c.Name, Character: c.Character,
			Profile: s.imageURL(c.Profile, profileSize)})
	}
	for _, c := range d.Crew {
		out.Crew = append(out.Crew, CrewMember{PersonID: c.PersonID, Name: c.Name, Job: c.Job,
			Profile: s.imageURL(c.Profile, profileSize)})
	}
	return out
}
