package metadata

// The shapes of TMDB v3 responses, with just the fields ply reads. Absent and
// null fields decode to zero values.

// GenreRef is a genre as TMDB names it.
type GenreRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Configuration is /configuration.
type Configuration struct {
	Images struct {
		BaseURL       string   `json:"base_url"`
		SecureBaseURL string   `json:"secure_base_url"`
		PosterSizes   []string `json:"poster_sizes"`
		BackdropSizes []string `json:"backdrop_sizes"`
		ProfileSizes  []string `json:"profile_sizes"`
		LogoSizes     []string `json:"logo_sizes"`
		StillSizes    []string `json:"still_sizes"`
	} `json:"images"`
	ChangeKeys []string `json:"change_keys"`
}

// MovieResult is one hit of /search/movie, /find or a collection's parts.
type MovieResult struct {
	ID               int64   `json:"id"`
	Title            string  `json:"title"`
	OriginalTitle    string  `json:"original_title"`
	OriginalLanguage string  `json:"original_language"`
	Overview         string  `json:"overview"`
	ReleaseDate      string  `json:"release_date"`
	PosterPath       string  `json:"poster_path"`
	BackdropPath     string  `json:"backdrop_path"`
	Popularity       float64 `json:"popularity"`
	VoteAverage      float64 `json:"vote_average"`
	VoteCount        int     `json:"vote_count"`
}

// TVResult is one hit of /search/tv or /find.
type TVResult struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	OriginalName     string  `json:"original_name"`
	OriginalLanguage string  `json:"original_language"`
	Overview         string  `json:"overview"`
	FirstAirDate     string  `json:"first_air_date"`
	PosterPath       string  `json:"poster_path"`
	BackdropPath     string  `json:"backdrop_path"`
	Popularity       float64 `json:"popularity"`
	VoteAverage      float64 `json:"vote_average"`
	VoteCount        int     `json:"vote_count"`
}

type searchMovies struct {
	Results []MovieResult `json:"results"`
}

type searchTV struct {
	Results []TVResult `json:"results"`
}

// FindResult is /find/{external id}.
type FindResult struct {
	MovieResults []MovieResult `json:"movie_results"`
	TVResults    []TVResult    `json:"tv_results"`
}

// CastCredit and CrewCredit are the entries of a movie's credits.
type CastCredit struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Character   string `json:"character"`
	ProfilePath string `json:"profile_path"`
	Order       int    `json:"order"`
}

type CrewCredit struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Job         string `json:"job"`
	Department  string `json:"department"`
	ProfilePath string `json:"profile_path"`
}

type Credits struct {
	Cast []CastCredit `json:"cast"`
	Crew []CrewCredit `json:"crew"`
}

// AggregateCast and AggregateCrew are the entries of a show's aggregate
// credits: one per person, with all their roles.
type AggregateCast struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	ProfilePath       string `json:"profile_path"`
	Order             int    `json:"order"`
	TotalEpisodeCount int    `json:"total_episode_count"`
	Roles             []struct {
		Character    string `json:"character"`
		EpisodeCount int    `json:"episode_count"`
	} `json:"roles"`
}

type AggregateCrew struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	ProfilePath       string `json:"profile_path"`
	Department        string `json:"department"`
	TotalEpisodeCount int    `json:"total_episode_count"`
	Jobs              []struct {
		Job          string `json:"job"`
		EpisodeCount int    `json:"episode_count"`
	} `json:"jobs"`
}

type AggregateCredits struct {
	Cast []AggregateCast `json:"cast"`
	Crew []AggregateCrew `json:"crew"`
}

// ReleaseDates is a movie's release_dates append: certifications by country.
type ReleaseDates struct {
	Results []struct {
		Country      string `json:"iso_3166_1"`
		ReleaseDates []struct {
			Certification string `json:"certification"`
			Type          int    `json:"type"`
			ReleaseDate   string `json:"release_date"`
		} `json:"release_dates"`
	} `json:"results"`
}

// ContentRatings is a show's content_ratings append.
type ContentRatings struct {
	Results []struct {
		Country string `json:"iso_3166_1"`
		Rating  string `json:"rating"`
	} `json:"results"`
}

// ExternalIDs is the external_ids append.
type ExternalIDs struct {
	IMDbID string `json:"imdb_id"`
	TVDBID int64  `json:"tvdb_id"`
}

// Image is an entry of the images append.
type Image struct {
	FilePath    string  `json:"file_path"`
	Language    *string `json:"iso_639_1"` // null: no text on it
	VoteAverage float64 `json:"vote_average"`
	VoteCount   int     `json:"vote_count"`
	Width       int     `json:"width"`
}

type Images struct {
	Posters   []Image `json:"posters"`
	Backdrops []Image `json:"backdrops"`
	Logos     []Image `json:"logos"`
}

// CollectionRef is a movie's belongs_to_collection.
type CollectionRef struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	PosterPath   string `json:"poster_path"`
	BackdropPath string `json:"backdrop_path"`
}

// MovieDetails is /movie/{id} with credits, release_dates, external_ids and
// images appended.
type MovieDetails struct {
	ID                  int64          `json:"id"`
	IMDbID              string         `json:"imdb_id"`
	Title               string         `json:"title"`
	OriginalTitle       string         `json:"original_title"`
	Overview            string         `json:"overview"`
	Tagline             string         `json:"tagline"`
	ReleaseDate         string         `json:"release_date"`
	Runtime             int            `json:"runtime"`
	Status              string         `json:"status"`
	VoteAverage         float64        `json:"vote_average"`
	VoteCount           int            `json:"vote_count"`
	Genres              []GenreRef     `json:"genres"`
	PosterPath          string         `json:"poster_path"`
	BackdropPath        string         `json:"backdrop_path"`
	BelongsToCollection *CollectionRef `json:"belongs_to_collection"`
	Credits             Credits        `json:"credits"`
	ReleaseDates        ReleaseDates   `json:"release_dates"`
	ExternalIDs         ExternalIDs    `json:"external_ids"`
	Images              Images         `json:"images"`
}

// EpisodeRef is a show's last or next episode to air.
type EpisodeRef struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	AirDate       string `json:"air_date"`
	SeasonNumber  int    `json:"season_number"`
	EpisodeNumber int    `json:"episode_number"`
}

// SeasonRef is a season in a show's details.
type SeasonRef struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	SeasonNumber int    `json:"season_number"`
	EpisodeCount int    `json:"episode_count"`
	AirDate      string `json:"air_date"`
	PosterPath   string `json:"poster_path"`
}

// TVDetails is /tv/{id} with aggregate_credits, content_ratings, external_ids
// and images appended.
type TVDetails struct {
	ID              int64       `json:"id"`
	Name            string      `json:"name"`
	OriginalName    string      `json:"original_name"`
	Overview        string      `json:"overview"`
	Tagline         string      `json:"tagline"`
	FirstAirDate    string      `json:"first_air_date"`
	LastAirDate     string      `json:"last_air_date"`
	Status          string      `json:"status"`
	InProduction    bool        `json:"in_production"`
	EpisodeRunTime  []int       `json:"episode_run_time"`
	VoteAverage     float64     `json:"vote_average"`
	VoteCount       int         `json:"vote_count"`
	Genres          []GenreRef  `json:"genres"`
	PosterPath      string      `json:"poster_path"`
	BackdropPath    string      `json:"backdrop_path"`
	NumberOfSeasons int         `json:"number_of_seasons"`
	Seasons         []SeasonRef `json:"seasons"`
	Networks        []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"networks"`
	CreatedBy []struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		ProfilePath string `json:"profile_path"`
	} `json:"created_by"`
	NextEpisodeToAir *EpisodeRef      `json:"next_episode_to_air"`
	LastEpisodeToAir *EpisodeRef      `json:"last_episode_to_air"`
	AggregateCredits AggregateCredits `json:"aggregate_credits"`
	ContentRatings   ContentRatings   `json:"content_ratings"`
	ExternalIDs      ExternalIDs      `json:"external_ids"`
	Images           Images           `json:"images"`
}

// EpisodeDetails is an episode of a season response.
type EpisodeDetails struct {
	EpisodeNumber int     `json:"episode_number"`
	SeasonNumber  int     `json:"season_number"`
	Name          string  `json:"name"`
	Overview      string  `json:"overview"`
	AirDate       string  `json:"air_date"`
	Runtime       int     `json:"runtime"`
	StillPath     string  `json:"still_path"`
	VoteAverage   float64 `json:"vote_average"`
}

// SeasonDetails is /tv/{id}/season/{n}.
type SeasonDetails struct {
	ID           int64            `json:"id"`
	Name         string           `json:"name"`
	Overview     string           `json:"overview"`
	AirDate      string           `json:"air_date"`
	PosterPath   string           `json:"poster_path"`
	SeasonNumber int              `json:"season_number"`
	Episodes     []EpisodeDetails `json:"episodes"`
}

// PersonDetails is /person/{id}.
type PersonDetails struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Biography          string `json:"biography"`
	Birthday           string `json:"birthday"`
	Deathday           string `json:"deathday"`
	PlaceOfBirth       string `json:"place_of_birth"`
	KnownForDepartment string `json:"known_for_department"`
	ProfilePath        string `json:"profile_path"`
	IMDbID             string `json:"imdb_id"`
}

// CollectionDetails is /collection/{id}.
type CollectionDetails struct {
	ID           int64         `json:"id"`
	Name         string        `json:"name"`
	Overview     string        `json:"overview"`
	PosterPath   string        `json:"poster_path"`
	BackdropPath string        `json:"backdrop_path"`
	Parts        []MovieResult `json:"parts"`
}

// Video is an entry of /movie/{id}/videos or /tv/{id}/videos.
type Video struct {
	Language    string `json:"iso_639_1"`
	Name        string `json:"name"`
	Key         string `json:"key"`
	Site        string `json:"site"` // "YouTube", "Vimeo"
	Type        string `json:"type"` // "Trailer", "Teaser", "Clip", "Featurette", ...
	Official    bool   `json:"official"`
	PublishedAt string `json:"published_at"` // "2021-09-09T15:00:20.000Z"
}

type videoList struct {
	Results []Video `json:"results"`
}

// changesPage is a page of /movie/changes or /tv/changes.
type changesPage struct {
	Results []struct {
		ID int64 `json:"id"`
	} `json:"results"`
	Page         int `json:"page"`
	TotalPages   int `json:"total_pages"`
	TotalResults int `json:"total_results"`
}
