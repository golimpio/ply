package api

import (
	"context"
	"errors"
	"math"
	"net/url"
	"slices"
	"strconv"
	"time"

	"ply/internal/metadata"
	"ply/internal/store"
)

// The notices M10 shows beside the numbers it brings (docs/M10-RATINGS.md).
const (
	// IMDb's terms for its datasets ask for exactly this text: help.imdb.com, "Can I use IMDb
	// data in my software?" (read 2026-11-05).
	imdbAttribution    = "Information courtesy of IMDb (https://www.imdb.com). Used with permission."
	imdbAttributionURL = "https://www.imdb.com"
	// MDBList is credited, with a link, wherever its numbers are shown.
	mdblistAttribution    = "Ratings from Rotten Tomatoes, Metacritic, Letterboxd and Trakt provided by MDBList."
	mdblistAttributionURL = "https://mdblist.com"
)

// ratingsPrimary is the source whose rating a card shows (Settings → Ratings).
func (s *Server) ratingsPrimary(ctx context.Context) string {
	if s.Settings == nil {
		return metadata.DefaultRatingsPrimary
	}
	primary, _ := s.Settings.RatingsPrefs(ctx)
	return primary
}

// ratingsSettings fills the ratings part of the settings view. The date of the IMDb dataset is
// the service's to say (it keeps the file), so it is there only when the server has one.
func (s *Server) ratingsSettings(ctx context.Context, v *SettingsView) {
	v.IMDbAttribution, v.IMDbAttributionURL = imdbAttribution, imdbAttributionURL
	v.MDBListAttribution, v.MDBListAttributionURL = mdblistAttribution, mdblistAttributionURL
	v.RatingsPrimary, v.RatingsShown, v.RatingsAvailable = metadata.DefaultRatingsPrimary, slices.Clone(store.RatingSources), []string{}
	if s.Settings == nil {
		return
	}
	v.RatingsPrimary, v.RatingsShown = s.Settings.RatingsPrefs(ctx)
	v.RatingsAvailable = s.Settings.RatingsAvailable(ctx)
	if s.Ratings != nil {
		if t, ok := s.Ratings.IMDbDatasetUpdatedAt(ctx); ok {
			v.IMDbDatasetUpdatedAt = ptr(t.UTC().Format(time.RFC3339))
		}
	}
}

// RatingEntry is a rating from one source. Value is on the source's own Scale (IMDb 10, Metacritic
// 100, Letterboxd 5); Score100 is Value / Scale × 100, rounded, which is what sorting and
// filtering go by, so that a client never needs a source's scale. State is Rotten Tomatoes' word
// for the score (fresh, rotten, certified on rt_critics; upright, spilled, verified_hot on
// rt_audience). URL is the page of the title at the source, "" when it is not known. FetchedAt is
// RFC 3339.
type RatingEntry struct {
	Source    string  `json:"source"`
	Value     float64 `json:"value"`
	Scale     float64 `json:"scale"`
	Score100  int     `json:"score100"`
	Votes     *int    `json:"votes,omitempty"`
	State     string  `json:"state,omitempty"`
	URL       string  `json:"url"`
	FetchedAt string  `json:"fetchedAt"`
}

func ratingEntry(r store.Rating) RatingEntry {
	e := RatingEntry{Source: r.Source, Value: r.Value, Scale: r.Scale, Score100: r.Score100(), State: r.State, URL: r.URL}
	if r.Votes > 0 {
		e.Votes = ptr(r.Votes)
	}
	if r.FetchedAt > 0 {
		e.FetchedAt = time.Unix(r.FetchedAt, 0).UTC().Format(time.RFC3339)
	}
	return e
}

// ratingEntries are a title's (or an episode's) ratings, in the fixed order; never nil.
func ratingEntries(rs []store.Rating) []RatingEntry {
	out := make([]RatingEntry, len(rs))
	for i, r := range rs {
		out[i] = ratingEntry(r)
	}
	return out
}

// episodeRatings are an episode's ratings: its IMDb rating, when it has one; never nil.
func episodeRatings(ep store.Episode) []RatingEntry {
	if ep.IMDbRating == nil {
		return []RatingEntry{}
	}
	return []RatingEntry{ratingEntry(*ep.IMDbRating)}
}

// primaryRating is the rating a card shows: the primary source (or Rotten Tomatoes audience
// if critics was chosen but missing), else the first available in RatingSources.
func primaryRating(rs []store.Rating, primary string) *RatingEntry {
	if len(rs) == 0 {
		return nil
	}
	pick := rs[0]
	if primary != "" {
		if i := slices.IndexFunc(rs, func(r store.Rating) bool { return r.Source == primary }); i >= 0 {
			pick = rs[i]
		} else if primary == store.RatingRTCritics {
			// When primary is Rotten Tomatoes critics and missing, fall back to audience before other sources.
			if i := slices.IndexFunc(rs, func(r store.Rating) bool { return r.Source == store.RatingRTAudience }); i >= 0 {
				pick = rs[i]
			}
		}
	}
	e := ratingEntry(pick)
	return &e
}

// ratingQuery is the `sort=rating` and `minRating=N` of a list.
type ratingQuery struct {
	byRating bool
	min      float64
	hasMin   bool
}

// parseRatingQuery reads sort and minRating. sort can only be "rating"; minRating is a number from
// 0 to 100, on score100.
func parseRatingQuery(q url.Values) (ratingQuery, error) {
	var rq ratingQuery
	switch v := q.Get("sort"); v {
	case "":
	case "rating":
		rq.byRating = true
	default:
		return rq, errors.New(`sort must be "rating"`)
	}
	if v := q.Get("minRating"); v != "" {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(n) || n < 0 || n > 100 {
			return rq, errors.New("minRating must be a number from 0 to 100")
		}
		rq.min, rq.hasMin = n, true
	}
	return rq, nil
}

func (rq ratingQuery) active() bool { return rq.byRating || rq.hasMin }

// apply keeps the entries with a rating of at least minRating (those with none are left out when a
// minimum is given), and with sort=rating puts the best first, those with none last, ties in the
// order they came in.
func applyRatingQuery[T any](items []T, rq ratingQuery, rating func(T) *RatingEntry) []T {
	if !rq.active() {
		return items
	}
	out := items
	if rq.hasMin {
		out = make([]T, 0, len(items))
		for _, it := range items {
			if r := rating(it); r != nil && float64(r.Score100) >= rq.min {
				out = append(out, it)
			}
		}
	}
	if rq.byRating {
		out = slices.Clone(out)
		slices.SortStableFunc(out, func(a, b T) int {
			ra, rb := rating(a), rating(b)
			switch {
			case ra == nil && rb == nil:
				return 0
			case ra == nil:
				return 1
			case rb == nil:
				return -1
			}
			return rb.Score100 - ra.Score100
		})
	}
	return out
}
