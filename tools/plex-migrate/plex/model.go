// Package plex reads a Plex Media Server library, either from its HTTP API
// (a running server plus an X-Plex-Token) or from a read-only copy of its
// SQLite database and Metadata bundles. It normalises both into []Item so the
// migration logic does not care which source was used.
//
// It only ever READS Plex: the DB source opens a copy, never the live database.
package plex

// Kind is a Plex metadata type the migration handles. Music, photos and DVR
// are out of scope.
type Kind string

const (
	Movie   Kind = "movie"
	Show    Kind = "show"
	Season  Kind = "season"
	Episode Kind = "episode"
)

// Plex metadata_items.type values (verify against the live DB; stable for these).
const (
	typeMovie   = 1
	typeShow    = 2
	typeSeason  = 3
	typeEpisode = 4
)

// KindOfType maps a Plex metadata_items.type to a Kind, ok=false for types the
// migration does not handle.
func KindOfType(t int) (Kind, bool) {
	switch t {
	case typeMovie:
		return Movie, true
	case typeShow:
		return Show, true
	case typeSeason:
		return Season, true
	case typeEpisode:
		return Episode, true
	}
	return "", false
}

// IDs are the external identifiers a Plex item carries, used to match ply titles.
type IDs struct {
	IMDb string // "tt0107302", "" when unknown
	TMDB int64  // 0 when unknown
	TVDB int64  // 0 when unknown
}

// Empty reports whether no external id was found.
func (id IDs) Empty() bool { return id.IMDb == "" && id.TMDB == 0 && id.TVDB == 0 }

// Watch is one account's view state for an item.
type Watch struct {
	OffsetMS   int64   // resume position in milliseconds (Plex view_offset)
	ViewCount  int64   // times watched to completion
	LastViewed int64   // unix seconds of the last view (0 = never)
	UserRating float64 // the person's own rating (0 = none); scale verified per source
}

// Started reports whether there is any watch state worth importing.
func (w Watch) Started() bool { return w.OffsetMS > 0 || w.ViewCount > 0 || w.LastViewed > 0 }

// Item is a normalised Plex metadata item from either source. Movies are flat;
// a show's seasons and episodes are returned as their own Items carrying Show,
// Season and Episode so the migration can group them.
type Item struct {
	RatingKey string // Plex metadata_items.id / API ratingKey
	Kind      Kind
	Title     string
	Year      int
	Show      string // season/episode: the show's title
	Season    int    // season/episode: the season number (0 = specials)
	Episode   int    // episode: the episode number
	GUID      string // raw agent guid (legacy) — kept for reporting
	Hash      string // metadata_items.hash: the artwork bundle name (DB source)
	IDs       IDs
	Files     []string // absolute media paths (media_parts.file / Media.Part.file)
	PosterURL string   // selected poster: API path or bundle reference ("" = none)
	ArtURL    string   // selected backdrop
	Custom    bool     // the poster is a deliberate user pick (locked/uploaded)
	Watch     Watch
}

// Library is a Plex library section and the folders it scans.
type Library struct {
	Key   string // section id
	Title string
	Kind  Kind // movie | show
	Paths []string
}
