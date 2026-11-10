package api

// GET /api/items?kind=movie&fields=card answers the movies as the grid of posters needs them, and
// no more: a row is about two thirds of the weight of the full ItemSummary (581 bytes against 884 for
// 5,000 films; PLY_LISTPERF=1 go test -run TestListPayload -v ./internal/api/ measures it), and that
// much less for the browser to parse and keep. The fields are the ones below, exactly, each with the
// name and the shape it has on the full row; everything else (the frame-grab backdrop, the codecs,
// when it was watched, the certification, the logo, the links in the rating) is on GET /api/items/{id}.
// It answers episodes too, in their own shape (EpisodeCard): an episode is a card as well, and the
// episode list is the biggest one a browsing client asks for.

// fieldsCard is the value of the fields query parameter that asks for the card projection.
const fieldsCard = "card"

// projectionEpisodeCard is the projection the episode cards are kept under: their ETag is not the
// movie cards' one. Two shapes of list never share a validator, just as a card list never shares the
// full list's. It is a key for the ETag only: no `fields` value spells this, so it cannot be asked
// for by mistake.
const projectionEpisodeCard = "episode-card"

// ItemCard is a movie as the grid shows it. The order, the filters and the sortTitle are those of
// the full list; the fields are the full row's, each with the name, the shape and the omitempty it has
// there (but for sortTitle, always sent so that the grid can sort on it), with the artwork reduced to
// its frame grab and its colour, and the rating to its number and state. They are cut from the full
// row (itemCard takes an ItemSummary), so that a link is built in one place and the two can never
// differ.
type ItemCard struct {
	ID          int64       `json:"id"`
	Kind        string      `json:"kind"`
	Title       string      `json:"title"`
	SortTitle   string      `json:"sortTitle"`
	Year        *int        `json:"year,omitempty"`
	TitleID     *int64      `json:"titleId,omitempty"`
	AddedAt     int64       `json:"addedAt"`
	Poster      string      `json:"poster,omitempty"`
	Backdrop    string      `json:"backdrop,omitempty"`
	Art         *CardArt    `json:"art"`    // null when there is no artwork
	Rating      *CardRating `json:"rating"` // null when it has none
	Watched     bool        `json:"watched"`
	Progress    *Progress   `json:"progress"` // null when the file has not been started or watched
	DurationSec float64     `json:"durationSec"`
	Container   string      `json:"container"`
	Width       int         `json:"width"`
	Height      int         `json:"height"`
	HDR         string      `json:"hdr,omitempty"`
	Genres      []string    `json:"genres,omitempty"` // names
	MatchState  string      `json:"matchState"`
	NeedsReview bool        `json:"needsReview"`
}

// CardArt is an item's frame-grab artwork without its backdrop: the thumb link (it carries the
// file's ?v=<mtime>, as on the full row) and the colour ("#rrggbb", once the picture exists) a tile
// is tinted with while the picture loads.
type CardArt struct {
	Thumb string `json:"thumb"`
	Color string `json:"color,omitempty"`
}

// CardRating is a rating without the link to its page, the votes and the time it was fetched. State
// is what Rotten Tomatoes' glyph (certified fresh, verified hot) is drawn from.
type CardRating struct {
	Source   string  `json:"source"`
	Value    float64 `json:"value"`
	Scale    float64 `json:"scale"`
	Score100 int     `json:"score100"`
	State    string  `json:"state,omitempty"`
}

func itemCard(it ItemSummary) ItemCard {
	c := ItemCard{
		ID: it.ID, Kind: it.Kind, Title: it.Title, SortTitle: it.SortTitle, Year: it.Year, TitleID: it.TitleID, AddedAt: it.AddedAt,
		Poster: it.Poster, Backdrop: it.Backdrop, Watched: it.WatchState.Watched, Progress: it.Progress, DurationSec: it.DurationSec,
		Container: it.Container, Width: it.Width, Height: it.Height, HDR: it.HDR, Genres: it.Genres, MatchState: it.MatchState, NeedsReview: it.NeedsReview,
	}
	if a := it.Art; a != nil {
		c.Art = &CardArt{Thumb: a.Thumb, Color: a.Color}
	}
	if r := it.Rating; r != nil {
		c.Rating = &CardRating{Source: r.Source, Value: r.Value, Scale: r.Scale, Score100: r.Score100, State: r.State}
	}
	return c
}

func itemCards(items []ItemSummary) []ItemCard {
	out := make([]ItemCard, len(items))
	for i, it := range items {
		out[i] = itemCard(it)
	}
	return out
}

// EpisodeCard is an episode as a list of them needs it: a show's episodes, the home page's rails and
// the search. It is cut from the full row (episodeCard takes an ItemSummary) as ItemCard is, with each
// field's name, shape and omitempty as the full row has it, so that the two cannot drift.
//
// What it keeps is what a page draws: the show, the season and the number, the episode's own still
// (lib/meta.ts paints an episode from it, and falls back to the frame grab without it), its own
// overview (the season list prints it under the episode), the frame grab and its colour, the episode's
// own ratings, the watch state the client patches after a mark, and the width, height and HDR that
// MediaCard's badge reads. Everything else -- the backdrop, the poster, the logo, the codecs, the
// container, the genres, the certification, the match state, the sort title, and rating (the show's:
// an episode's own are in ratings) -- is in GET /api/items/{id}.
// Deliberately not sent: a field no page draws is weight on every row of the largest list there is.
type EpisodeCard struct {
	ID           int64        `json:"id"`
	Kind         string       `json:"kind"`
	Title        string       `json:"title"`
	Year         *int         `json:"year,omitempty"`
	Show         string       `json:"show,omitempty"`
	Season       *int         `json:"season,omitempty"`
	Episode      *int         `json:"episode,omitempty"`
	EpisodeTitle string       `json:"episodeTitle,omitempty"`
	AddedAt      int64        `json:"addedAt"`
	DurationSec  float64      `json:"durationSec"`
	Still        string       `json:"still,omitempty"`
	Overview     string       `json:"overview,omitempty"` // the episode's own: the season list prints it
	Art          *CardArt     `json:"art"`                // null when there is no artwork
	Ratings      []CardRating `json:"ratings,omitzero"`
	Watched      bool         `json:"watched"`
	WatchedAt    *int64       `json:"watchedAt"` // null when it was not watched
	PlayCount    int          `json:"playCount"`
	Progress     *Progress    `json:"progress"` // null when the file has not been started or watched
	Width        int          `json:"width"`
	Height       int          `json:"height"`
	HDR          string       `json:"hdr,omitempty"`
}

func episodeCard(it ItemSummary) EpisodeCard {
	c := EpisodeCard{
		ID: it.ID, Kind: it.Kind, Title: it.Title, Year: it.Year, Show: it.Show, Season: it.Season, Episode: it.Episode,
		EpisodeTitle: it.EpisodeTitle, AddedAt: it.AddedAt, DurationSec: it.DurationSec, Still: it.Still, Overview: it.Overview,
		Watched: it.WatchState.Watched, WatchedAt: it.WatchState.WatchedAt, PlayCount: it.WatchState.PlayCount,
		Progress: it.Progress, Width: it.Width, Height: it.Height, HDR: it.HDR,
	}
	if a := it.Art; a != nil {
		c.Art = &CardArt{Thumb: a.Thumb, Color: a.Color}
	}
	for _, r := range it.Ratings {
		c.Ratings = append(c.Ratings, CardRating{Source: r.Source, Value: r.Value, Scale: r.Scale, Score100: r.Score100, State: r.State})
	}
	return c
}

func episodeCards(items []ItemSummary) []EpisodeCard {
	out := make([]EpisodeCard, len(items))
	for i, it := range items {
		out[i] = episodeCard(it)
	}
	return out
}
