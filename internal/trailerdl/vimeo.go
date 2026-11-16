package trailerdl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Vimeo resolves a Vimeo video to its streams, from its player page's config.
type Vimeo struct {
	HTTP *http.Client
	Base string // "" = https://player.vimeo.com (tests: a fake's address)
}

// Resolve reads the player page of video key: the best progressive MP4 up to MaxHeight,
// else the HLS master playlist.
func (v Vimeo) Resolve(ctx context.Context, key string) (Source, error) {
	base := v.Base
	if base == "" {
		base = "https://player.vimeo.com"
	}
	page := base + "/video/" + url.PathEscape(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, page, nil)
	if err != nil {
		return Source{}, err
	}
	req.Header.Set("User-Agent", desktopUA)
	req.Header.Set("Referer", page)
	c := v.HTTP
	if c == nil {
		c = http.DefaultClient
	}
	data, err := fetch(c, req)
	if se := (*StatusError)(nil); errors.As(err, &se) && bytes.Contains(se.Body, []byte("Because of its privacy settings")) {
		return Source{}, fmt.Errorf("Vimeo: the video's privacy settings keep it to some sites: %w", ErrUnavailable)
	}
	if err != nil {
		return Source{}, fmt.Errorf("Vimeo player: %w", err)
	}
	cfg, err := playerConfig(data)
	if err != nil {
		return Source{}, err
	}
	files := cfg.Request.Files
	var progressive []Stream
	for _, p := range files.Progressive {
		// Vimeo's progressive files are H.264 with AAC.
		progressive = append(progressive, Stream{URL: p.URL, MIME: `video/mp4; codecs="avc1"`, Height: p.Height})
	}
	if best, ok := pickVideo(progressive, MaxHeight); ok {
		return Source{Video: best, Header: http.Header{"User-Agent": {desktopUA}, "Referer": {page}}}, nil
	}
	cdn, ok := files.HLS.CDNs[files.HLS.DefaultCDN]
	if !ok && len(files.HLS.CDNs) > 0 {
		names := make([]string, 0, len(files.HLS.CDNs))
		for name := range files.HLS.CDNs {
			names = append(names, name)
		}
		slices.Sort(names)
		cdn, ok = files.HLS.CDNs[names[0]], true
	}
	if !ok || cdn.URL == "" {
		return Source{}, fmt.Errorf("Vimeo gave no stream: %w", ErrUnavailable)
	}
	return Source{HLS: cdn.URL, Header: http.Header{"User-Agent": {desktopUA}, "Referer": {page}}}, nil
}

type vimeoConfig struct {
	Request struct {
		Files struct {
			Progressive []struct {
				URL    string `json:"url"`
				Height int    `json:"height"`
			} `json:"progressive"`
			HLS struct {
				DefaultCDN string `json:"default_cdn"`
				CDNs       map[string]struct {
					URL string `json:"url"`
				} `json:"cdns"`
			} `json:"hls"`
		} `json:"files"`
	} `json:"request"`
}

// playerConfig reads the object assigned to playerConfig in the player page: the decoder
// stops at its end, whatever follows it.
func playerConfig(page []byte) (*vimeoConfig, error) {
	_, rest, ok := bytes.Cut(page, []byte("playerConfig = "))
	if !ok {
		return nil, fmt.Errorf("Vimeo: the player page has no config: %w", ErrUnavailable)
	}
	var cfg vimeoConfig
	if err := json.NewDecoder(strings.NewReader(string(rest))).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("Vimeo player config: %w", err)
	}
	return &cfg, nil
}
