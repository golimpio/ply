package plex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// APISource reads a running Plex Media Server over its HTTP API with an
// X-Plex-Token. It returns the exact selected artwork (thumb/art ?download=1)
// and the external ids from the Guid array, so it is the preferred source for
// artwork fidelity. The token is never logged.
type APISource struct {
	base  string
	token string
	http  *http.Client
}

// NewAPI builds an API source. base is the server root (e.g. http://host:32400).
func NewAPI(base, token string) *APISource {
	return &APISource{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http:  &http.Client{Timeout: 60 * time.Second},
	}
}

func (s *APISource) Close() error { return nil }

// getJSON fetches path (relative to base) and decodes the MediaContainer envelope.
func (s *APISource) getJSON(ctx context.Context, path string, query url.Values) (*mediaContainer, error) {
	u := s.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", s.token)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("plex %s: %s", path, resp.Status)
	}
	var env struct {
		MediaContainer mediaContainer `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("plex %s: %w", path, err)
	}
	return &env.MediaContainer, nil
}

type mediaContainer struct {
	Directory []apiSection `json:"Directory"`
	Metadata  []apiMeta    `json:"Metadata"`
}

type apiSection struct {
	Key      string `json:"key"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Location []struct {
		Path string `json:"path"`
	} `json:"Location"`
}

type apiMeta struct {
	RatingKey string `json:"ratingKey"`
	GUID      string `json:"guid"`
	Guid      []struct {
		ID string `json:"id"`
	} `json:"Guid"`
	Type             string  `json:"type"`
	Title            string  `json:"title"`
	Year             int     `json:"year"`
	Thumb            string  `json:"thumb"`
	Art              string  `json:"art"`
	ViewOffset       int64   `json:"viewOffset"`
	ViewCount        int64   `json:"viewCount"`
	LastViewedAt     int64   `json:"lastViewedAt"`
	UserRating       float64 `json:"userRating"`
	Index            int     `json:"index"`
	ParentIndex      int     `json:"parentIndex"`
	ParentTitle      string  `json:"parentTitle"`
	GrandparentTitle string  `json:"grandparentTitle"`
	Media            []struct {
		Part []struct {
			File string `json:"file"`
		} `json:"Part"`
	} `json:"Media"`
}

func (s *APISource) Libraries(ctx context.Context) ([]Library, error) {
	c, err := s.getJSON(ctx, "/library/sections", nil)
	if err != nil {
		return nil, err
	}
	var out []Library
	for _, d := range c.Directory {
		var kind Kind
		switch d.Type {
		case "movie":
			kind = Movie
		case "show":
			kind = Show
		default:
			continue // artist/photo: out of scope
		}
		lib := Library{Key: d.Key, Title: d.Title, Kind: kind}
		for _, l := range d.Location {
			if l.Path != "" {
				lib.Paths = append(lib.Paths, l.Path)
			}
		}
		out = append(out, lib)
	}
	return out, nil
}

func (s *APISource) Items(ctx context.Context) ([]Item, error) {
	libs, err := s.Libraries(ctx)
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, lib := range libs {
		types := []string{"1"} // movie
		if lib.Kind == Show {
			types = []string{"2", "3", "4"} // show, season, episode
		}
		for _, typ := range types {
			q := url.Values{"type": {typ}}
			c, err := s.getJSON(ctx, "/library/sections/"+lib.Key+"/all", q)
			if err != nil {
				return nil, err
			}
			for _, m := range c.Metadata {
				out = append(out, apiItem(m, lib.Kind, typ))
			}
		}
	}
	return out, nil
}

func apiItem(m apiMeta, libKind Kind, typ string) Item {
	var guidArray []string
	for _, g := range m.Guid {
		guidArray = append(guidArray, g.ID)
	}
	it := Item{
		RatingKey: m.RatingKey,
		Title:     m.Title,
		Year:      m.Year,
		GUID:      m.GUID,
		IDs:       ParseIDs(m.GUID, guidArray),
		PosterURL: m.Thumb,
		ArtURL:    m.Art,
		Watch: Watch{
			OffsetMS:   m.ViewOffset,
			ViewCount:  m.ViewCount,
			LastViewed: m.LastViewedAt,
			UserRating: m.UserRating,
		},
	}
	for _, media := range m.Media {
		for _, p := range media.Part {
			if p.File != "" {
				it.Files = append(it.Files, p.File)
			}
		}
	}
	switch typ {
	case "1":
		it.Kind = Movie
	case "2":
		it.Kind = Show
	case "3":
		it.Kind = Season
		it.Show = m.ParentTitle
		it.Season = m.Index
	case "4":
		it.Kind = Episode
		it.Show = m.GrandparentTitle
		it.Season = m.ParentIndex
		it.Episode = m.Index
	}
	return it
}

// Art fetches the exact selected image: slot "poster" → thumb, "art" → art.
func (s *APISource) Art(ctx context.Context, it Item, slot string) ([]byte, error) {
	endpoint := "thumb"
	if slot == "art" {
		endpoint = "art"
	}
	if (slot == "poster" && it.PosterURL == "") || (slot == "art" && it.ArtURL == "") {
		return nil, ErrNoArt
	}
	u := s.base + "/library/metadata/" + it.RatingKey + "/" + endpoint + "?download=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Plex-Token", s.token)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNoArt
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("plex artwork %s: %s", it.RatingKey, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}
