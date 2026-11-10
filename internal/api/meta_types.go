package api

import (
	"fmt"
	"strings"

	"ply/internal/metadata"
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
	ID    int64  `json:"id"`
	Kind  string `json:"kind"` // movie | series
	Title string `json:"title"`
	// SortTitle is the name the card is listed under, and what GET /api/titles is ordered by:
	// compare it without regard to case (ASCII), then Year, then ID. It is the title's sort title
	// ("Matrix, The"), or its title when it has none; see sortTitleOf.
	SortTitle string `json:"sortTitle"`
	Year      int    `json:"year"` // 0 when unknown
	Poster    string `json:"poster,omitempty"`
	Backdrop  string `json:"backdrop,omitempty"`
	// Rating (M10) is the primary source's rating, or the first the title has in the fixed order
	// (its source says which); null when it has none.
	Rating *RatingEntry `json:"rating"`
	ItemID *int64       `json:"itemId,omitempty"` // movies: the first file
	Show   string       `json:"show,omitempty"`   // shows: the show's name in the library

	// NextAirDate is when a show's next episode airs (YYYY-MM-DD); it can be in the
	// past until the show is next refreshed.
	NextAirDate string `json:"nextAirDate,omitempty"`

	// Watched: a film was watched in any of its versions; a series, in all of its episodes. A
	// series card also carries episodeCount, watchedEpisodes and inProgress (a film's has none).
	Watched bool `json:"watched"`
	*EpisodeCounts
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
	TVDBID        int64          `json:"tvdbId,omitempty"`
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
	Source        string         `json:"source"` // tvdb | tmdb | nfo | mixed
	// EpisodeOrder is how a show's files map to its episodes: aired, dvd or absolute. Only a
	// show with source "tvdb" has one to change (PATCH /api/titles/{id}); the field is not there
	// for a movie or a show from anywhere else.
	EpisodeOrder string `json:"episodeOrder,omitempty"`
	// Ratings are every rating the title has, in the fixed order: IMDb, Rotten Tomatoes critics
	// and audience, Metacritic, Letterboxd, Trakt, TMDB. [] when none.
	Ratings []RatingEntry `json:"ratings"`
	// Unmapped is in the answer to a PATCH that changed the order: how many of the show's episode
	// files have no episode in it (their season and number are not TVDB's in that order).
	Unmapped    *int   `json:"unmapped,omitempty"`
	Locked      bool   `json:"locked"`
	MatchState  string `json:"matchState"`
	NeedsReview bool   `json:"needsReview"`
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
	TMDBID       int64          `json:"tmdbId,omitempty"` // a person both sites know has both, and one page
	TVDBID       int64          `json:"tvdbId,omitempty"`
	Credits      []PersonCredit `json:"credits"`
}

// Trailer is an entry of GET /api/titles/{id}/trailers: a video at a site that plays it, by its
// key there (a client builds the player's address, or opens the site's app).
type Trailer struct {
	Site        string `json:"site"` // youtube | vimeo
	Key         string `json:"key"`
	Name        string `json:"name,omitempty"`
	Type        string `json:"type,omitempty"` // trailer | teaser
	Language    string `json:"language,omitempty"`
	Official    bool   `json:"official"`
	PublishedAt string `json:"publishedAt,omitempty"`
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
	Rating   *float64 `json:"rating,omitempty"` // the episode's TMDB score out of 10, as before M10
	// Ratings (M10) are the episode's IMDb rating, when it has one: [] when none.
	Ratings []RatingEntry `json:"ratings"`
}

// MetaFields are the metadata fields that list rows (items, shows) carry.
type MetaFields struct {
	TitleID *int64 `json:"titleId,omitempty"`
	// SortTitle is what a movie or a show is sorted by, as TitleCard.SortTitle is (not there for an
	// episode: it is sorted by its show, season and number).
	SortTitle     string   `json:"sortTitle,omitempty"`
	Poster        string   `json:"poster,omitempty"`
	Backdrop      string   `json:"backdrop,omitempty"`
	Logo          string   `json:"logo,omitempty"`
	Certification string   `json:"certification,omitempty"`
	Genres        []string `json:"genres,omitempty"`
	MatchState    string   `json:"matchState"`
	NeedsReview   bool     `json:"needsReview"`
	// Rating (M10): as TitleCard.Rating; null for a row with no title or no rating.
	Rating *RatingEntry `json:"rating"`
}

// SeasonInfo is a season of GET /api/shows/{name}.
type SeasonInfo struct {
	Season        int    `json:"season"`
	Name          string `json:"name"`
	Overview      string `json:"overview,omitempty"`
	Poster        string `json:"poster,omitempty"`
	AirDate       string `json:"airDate,omitempty"`
	EpisodeCounts        // of the season's episodes in the library
}

// ShowDetail is GET /api/shows/{name}.
type ShowDetail struct {
	Show string `json:"show"`
	EpisodeCounts
	Year     *int   `json:"year,omitempty"`
	Overview string `json:"overview,omitempty"`
	MetaFields
	Meta    *TitleDetail `json:"meta,omitempty"`
	Seasons []SeasonInfo `json:"seasons"`
}

// Candidate is an entry of GET /api/metadata/search. Source says where it came from
// ("tmdb" or "tvdb"), ID is its id there, and the one of tmdbId and tvdbId that is its
// own is set: that is what PUT .../match takes to apply it.
type Candidate struct {
	Source        string  `json:"source"`
	ID            int64   `json:"id"`
	TMDBID        int64   `json:"tmdbId,omitempty"`
	TVDBID        int64   `json:"tvdbId,omitempty"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"originalTitle,omitempty"`
	Year          int     `json:"year"`
	Overview      string  `json:"overview,omitempty"`
	Poster        string  `json:"poster,omitempty"`
	Score         float64 `json:"score"`
}

// ReviewEpisode is an episode of a show in the review list that the scanner flagged
// (its file name has no episode number): the item's id and its file.
type ReviewEpisode struct {
	ID   int64  `json:"id"`
	Path string `json:"path"`
}

// ReviewEntry is an entry of GET /api/metadata/review.
type ReviewEntry struct {
	Kind        string          `json:"kind"` // movie | series
	ItemID      *int64          `json:"itemId,omitempty"`
	Show        string          `json:"show,omitempty"`
	Title       string          `json:"title"`
	Year        int             `json:"year"`
	MatchState  string          `json:"matchState"`
	NeedsReview bool            `json:"needsReview"`
	Score       float64         `json:"score"`
	Candidate   *TitleCard      `json:"candidate,omitempty"`
	Episodes    []ReviewEpisode `json:"episodes,omitempty"` // a show's flagged episodes
}

// SettingsView is GET /api/settings. The key itself is never in it.
type SettingsView struct {
	TMDBKeySet        bool   `json:"tmdbKeySet"`
	TMDBKeySource     string `json:"tmdbKeySource"` // flag | env | settings | default | ""
	MetadataEnabled   bool   `json:"metadataEnabled"`
	MetadataForcedOff bool   `json:"metadataForcedOff"` // -metadata=false: the switch cannot be turned on here
	Language          string `json:"language"`
	// The limits on plays (M9): all plays together, direct ones included, and the
	// transcodes among them. The source is flag (the command line or the
	// environment: cannot be changed here), settings or default.
	MaxStreams          int    `json:"maxStreams"`
	MaxTranscodes       int    `json:"maxTranscodes"`
	MaxStreamsSource    string `json:"maxStreamsSource"`
	MaxTranscodesSource string `json:"maxTranscodesSource"`
	// TMDBKeyChecked is in the answer to a PUT that stored a key: whether the key was
	// checked against TMDB. It was not with -metadata=false, when nothing may be sent.
	TMDBKeyChecked *bool `json:"tmdbKeyChecked,omitempty"`

	// TheTVDB (M8). The key and the PIN are never in a response: only whether each is set
	// and where the key came from (flag | env | settings | default | ""). Shows are TVDB's
	// when a key is set. A key or PIN from a flag or the environment cannot be changed here
	// (a PATCH for it stores one that the flag still beats). TVDBKeyChecked is, like
	// TMDBKeyChecked, in the answer to a PUT that stored a key or a PIN: whether it was
	// checked with a login at TVDB (not with -metadata=false).
	TVDBKeySet      bool   `json:"tvdbKeySet"`
	TVDBPINSet      bool   `json:"tvdbPinSet"`
	TVDBKeySource   string `json:"tvdbKeySource"`
	TVDBPINSource   string `json:"tvdbPinSource"` // flag | env | settings | "": a PIN from a flag or env cannot be removed here (409)
	TVDBKeyChecked  *bool  `json:"tvdbKeyChecked,omitempty"`
	TVDBAttribution string `json:"tvdbAttribution"`
	// TVDBAttributionURL is the link that goes with the attribution text (TVDB asks for a
	// direct link to thetvdb.com).
	TVDBAttributionURL string `json:"tvdbAttributionUrl"`

	// Ratings (M10). The MDBList key is never in a response, as the others are not: only whether
	// it is set and where it comes from (flag | env | settings | ""); MDBListKeyChecked is, like
	// TVDBKeyChecked, in the answer to a PUT or PATCH that stored a key. RatingsPrimary is the source
	// whose rating cards show; RatingsShown the sources to show, in order (both can be changed).
	// The rest is read only: RatingsAvailable are the sources this install can produce a rating
	// from, given its keys and the privacy switch; the attributions are the notices IMDb and
	// MDBList ask for; IMDbDatasetUpdatedAt (RFC 3339) is null until the first download.
	MDBListKeySet         bool     `json:"mdblistKeySet"`
	MDBListKeySource      string   `json:"mdblistKeySource"`
	MDBListKeyChecked     *bool    `json:"mdblistKeyChecked,omitempty"`
	RatingsPrimary        string   `json:"ratingsPrimary"`
	RatingsShown          []string `json:"ratingsShown"`
	RatingsAvailable      []string `json:"ratingsAvailable"`
	IMDbAttribution       string   `json:"imdbAttribution"`
	IMDbAttributionURL    string   `json:"imdbAttributionUrl"`
	MDBListAttribution    string   `json:"mdblistAttribution"`
	MDBListAttributionURL string   `json:"mdblistAttributionUrl"`
	IMDbDatasetUpdatedAt  *string  `json:"imdbDatasetUpdatedAt"`

	// The last test of the key in use for each provider (POST /api/settings/test, or a save that
	// checked the key). KeyTestedAt is in unix seconds; 0 means the key was never tested, or it
	// changed since (stored, removed, or now shadowed by a flag or the environment). KeyTestOk is
	// whether the test passed, false when there is none. TVDB's logs in with the key and the PIN,
	// so its test is stale when either changes.
	TMDBKeyTestedAt    int64 `json:"tmdbKeyTestedAt"`
	TMDBKeyTestOK      bool  `json:"tmdbKeyTestOk"`
	TVDBKeyTestedAt    int64 `json:"tvdbKeyTestedAt"`
	TVDBKeyTestOK      bool  `json:"tvdbKeyTestOk"`
	MDBListKeyTestedAt int64 `json:"mdblistKeyTestedAt"`
	MDBListKeyTestOK   bool  `json:"mdblistKeyTestOk"`
}

// TVDBAttribution is the notice TheTVDB asks of an application that shows its metadata
// (https://thetvdb.com/api-information: a direct link to TheTVDB.com, with this sample
// wording).
const (
	tvdbAttribution    = "Metadata provided by TheTVDB. Please consider adding missing information or subscribing."
	tvdbAttributionURL = "https://thetvdb.com"
)

// imageURL turns an image ref ("tmdb:/abc.jpg", "tvdb:/banners/posters/1-1.jpg",
// "local:item:12:poster") into a relative URL with the token, or "" for a ref it does
// not understand. size is used for TMDB and TVDB images.
func (s *Server) imageURL(ref, size string) string {
	switch {
	case strings.HasPrefix(ref, "tmdb:/"):
		file := strings.TrimPrefix(ref, "tmdb:/")
		return s.withToken(fmt.Sprintf("/api/images/tmdb/%s/%s", size, file))
	case strings.HasPrefix(ref, "tvdb:/"):
		p, ok := metadata.TVDBFile(ref)
		if !ok {
			return ""
		}
		return s.withToken(fmt.Sprintf("/api/images/tvdb/%s%s", size, p))
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

// sortTitleOf is the name a movie or a show is sorted by in a list. When it is matched to a title
// (matched, with its brief b) it is the one GET /api/titles is ordered by: the title's sort title,
// which a person can edit, or else its title. Without a title it is the name from the files, with
// a leading article moved to the end the way the sort title of a matched title is made: the one
// function (metadata.SortTitle), not a copy of it.
func sortTitleOf(b store.TitleBrief, matched bool, name string) string {
	if matched && b.SortTitle != "" {
		return b.SortTitle
	}
	return metadata.SortTitle(name)
}

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

// titleCard builds a card; primary is the source whose rating it shows (see primaryRating).
func (s *Server) titleCard(c store.TitleCard, primary string) TitleCard {
	out := TitleCard{ID: c.ID, Kind: c.Kind, Title: c.Title, SortTitle: c.SortTitle, Year: c.Year, Poster: s.imageURL(c.Poster, posterSize),
		Backdrop: s.imageURL(c.Backdrop, backdropSize), Rating: primaryRating(c.Ratings, primary), Show: c.Show,
		NextAirDate: c.NextAirDate, Watched: c.Watched}
	if c.ItemID != 0 {
		out.ItemID = ptr(c.ItemID)
	}
	if c.Episodes != nil {
		out.EpisodeCounts = ptr(episodeCounts(*c.Episodes))
	}
	return out
}

func (s *Server) titleCards(cards []store.TitleCard, primary string) []TitleCard {
	out := make([]TitleCard, len(cards))
	for i, c := range cards {
		out[i] = s.titleCard(c, primary)
	}
	return out
}

func (s *Server) titleDetail(d store.TitleDetail, primary string) *TitleDetail {
	out := &TitleDetail{
		TitleCard: s.titleCard(d.TitleCard, primary), Ratings: ratingEntries(d.Ratings),
		TMDBID: d.TMDBID, TVDBID: d.TVDBID, IMDbID: d.IMDbID, OriginalTitle: d.OriginalTitle, SortTitle: d.SortTitle,
		Overview: d.Overview, Tagline: d.Tagline, ReleaseDate: d.ReleaseDate, EndDate: d.EndDate,
		Status: d.Status, RuntimeMin: optInt(d.RuntimeMin), VoteCount: optInt(d.VoteCount), Certification: d.Certification,
		Logo:   s.imageURL(d.Logo, logoSize),
		Genres: []Genre{}, Cast: []CastMember{}, Crew: []CrewMember{},
		Source: d.Source, Locked: d.Locked, MatchState: d.MatchState, NeedsReview: d.NeedsReview,
	}
	if d.Kind == store.KindSeries && d.Source == store.SourceTVDB {
		out.EpisodeOrder = d.EpisodeOrder
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
