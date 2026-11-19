// Package migrate joins a Plex library to a ply library by exact file path and
// moves the chosen artwork (as sidecars), watch state and title matches across.
// It only reads Plex and the ply database; the only writes are sidecar files next
// to the media and ply API calls (match, progress, watched).
package migrate

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// PlyItem is a ply library item, read for the path join.
type PlyItem struct {
	ID          int64
	Kind        string // "movie" | "episode"
	Path        string
	Show        string
	Season      int
	Episode     int
	TitleID     int64 // 0 = not matched
	DurationSec float64
}

// ReadPlyItems reads ply's items read-only from <dataDir>/ply.db. It never writes
// the ply database; matching and watch state go through the ply API.
func ReadPlyItems(ctx context.Context, dataDir string) ([]PlyItem, error) {
	dbPath := filepath.Join(dataDir, "ply.db")
	dsn := (&url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx,
		`SELECT id, kind, path, show, season, episode, title_id, duration_sec FROM items`)
	if err != nil {
		return nil, fmt.Errorf("read ply items (is %s a ply data dir?): %w", dbPath, err)
	}
	defer rows.Close()
	var out []PlyItem
	for rows.Next() {
		var it PlyItem
		var titleID sql.NullInt64
		if err := rows.Scan(&it.ID, &it.Kind, &it.Path, &it.Show, &it.Season, &it.Episode, &titleID, &it.DurationSec); err != nil {
			return nil, err
		}
		it.TitleID = titleID.Int64
		out = append(out, it)
	}
	return out, rows.Err()
}

// PlyClient calls a running ply server's API (matching, progress, watched).
type PlyClient struct {
	base  string
	token string
	http  *http.Client
}

func NewPlyClient(base, token string) *PlyClient {
	return &PlyClient{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http:  &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *PlyClient) do(ctx context.Context, method, path string, body any) error {
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ply %s %s: %s", method, path, resp.Status)
	}
	return nil
}

// matchBody prefers a TMDB id, else an IMDb id (the ply match API takes those).
func matchBody(tmdb int64, imdb string) map[string]any {
	if tmdb > 0 {
		return map[string]any{"tmdbId": tmdb}
	}
	return map[string]any{"imdbId": imdb}
}

// MatchMovie links a ply movie item to a title by id (a manual, pinned match).
func (c *PlyClient) MatchMovie(ctx context.Context, id int64, tmdb int64, imdb string) error {
	return c.do(ctx, http.MethodPut, fmt.Sprintf("/api/items/%d/match", id), matchBody(tmdb, imdb))
}

// MatchShow links a whole ply show (by its ply name) to a title by id.
func (c *PlyClient) MatchShow(ctx context.Context, show string, tmdb int64, imdb string) error {
	return c.do(ctx, http.MethodPut, "/api/shows/"+url.PathEscape(show)+"/match", matchBody(tmdb, imdb))
}

// SetProgress sets an item's resume position (ply marks watched at >= 90%).
func (c *PlyClient) SetProgress(ctx context.Context, id int64, positionSec, durationSec float64) error {
	return c.do(ctx, http.MethodPut, fmt.Sprintf("/api/items/%d/progress", id),
		map[string]any{"positionSec": positionSec, "durationSec": durationSec})
}

// SetWatched sets an item's watched flag.
func (c *PlyClient) SetWatched(ctx context.Context, id int64, watched bool) error {
	return c.do(ctx, http.MethodPut, fmt.Sprintf("/api/items/%d/watched", id),
		map[string]any{"watched": watched})
}
