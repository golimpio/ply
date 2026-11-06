package api

// GET /api/items?kind=movie&fields=card answers the movies as the grid of posters needs them, and
// no more: a row is about a third of the weight of the full ItemSummary (308 bytes against 884 for
// 5,000 films), which is a third of what the browser has to parse and keep. The fields are the ones
// below, exactly; everything else (the other pictures, the codecs, the container, when it was
// watched, the certification, the logo, the links in the rating) is on GET /api/items/{id}.
// It is for movies only: an episode is not a card.

// fieldsCard is the value of the fields query parameter that asks for the card projection.
const fieldsCard = "card"

// ItemCard is a movie as the grid shows it. The order, the filters and the sortTitle are those of
// the full list; the fields are the full row's, with the artwork reduced to its colour and the
// rating to its number.
type ItemCard struct {
	ID          int64       `json:"id"`
	Kind        string      `json:"kind"`
	Title       string      `json:"title"`
	SortTitle   string      `json:"sortTitle"`
	Year        *int        `json:"year,omitempty"`
	TitleID     *int64      `json:"titleId,omitempty"`
	AddedAt     int64       `json:"addedAt"`
	Poster      string      `json:"poster,omitempty"`
	Art         *CardArt    `json:"art,omitempty"` // only once a picture of the file has a colour
	Rating      *CardRating `json:"rating"`        // null when it has none
	Watched     bool        `json:"watched"`
	Progress    *Progress   `json:"progress"` // null when the file has not been started or watched
	DurationSec float64     `json:"durationSec"`
	Width       int         `json:"width"`
	Height      int         `json:"height"`
	HDR         string      `json:"hdr,omitempty"`
	Genres      []string    `json:"genres,omitempty"` // names
	MatchState  string      `json:"matchState"`
	NeedsReview bool        `json:"needsReview"`
}

// CardArt is the colour ("#rrggbb") of an item's artwork, which the grid tints a tile with while
// the picture loads.
type CardArt struct {
	Color string `json:"color"`
}

// CardRating is a rating without the link to its page and the time it was fetched.
type CardRating struct {
	Source   string  `json:"source"`
	Value    float64 `json:"value"`
	Scale    float64 `json:"scale"`
	Score100 int     `json:"score100"`
}

func itemCard(it ItemSummary) ItemCard {
	c := ItemCard{
		ID: it.ID, Kind: it.Kind, Title: it.Title, SortTitle: it.SortTitle, Year: it.Year, TitleID: it.TitleID, AddedAt: it.AddedAt,
		Poster: it.Poster, Watched: it.WatchState.Watched, Progress: it.Progress, DurationSec: it.DurationSec,
		Width: it.Width, Height: it.Height, HDR: it.HDR, Genres: it.Genres, MatchState: it.MatchState, NeedsReview: it.NeedsReview,
	}
	if it.Art != nil && it.Art.Color != "" {
		c.Art = &CardArt{Color: it.Art.Color}
	}
	if r := it.Rating; r != nil {
		c.Rating = &CardRating{Source: r.Source, Value: r.Value, Scale: r.Scale, Score100: r.Score100}
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
