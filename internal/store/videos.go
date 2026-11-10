package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
)

// The sites a trailer can be at, and the kinds of video kept.
const (
	SiteYouTube = "youtube"
	SiteVimeo   = "vimeo"

	VideoTrailer = "trailer"
	VideoTeaser  = "teaser"
)

// Video is a trailer or teaser of a title, at a site that plays it.
type Video struct {
	Site        string // SiteYouTube | SiteVimeo
	Key         string // the video's id at the site
	Name        string
	Type        string // VideoTrailer | VideoTeaser | "" (the provider does not say)
	Language    string // as the provider gives it ("en", "eng"), "" unknown
	Official    bool
	PublishedAt string // YYYY-MM-DD, "" unknown
}

// TitleVideos is what is stored of a title's videos: the videos, best first, what they were fetched
// for (the caller's ref, "tmdb:603@en-US"; "" never fetched) and when (unix seconds).
type TitleVideos struct {
	Videos    []Video
	Ref       string
	CheckedAt int64
}

var (
	reYouTubeKey = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	reVimeoKey   = regexp.MustCompile(`^[0-9]{1,12}$`)
)

// ValidVideo reports whether key is a video id of the site: a client builds the address of the
// player from these two alone, so nothing else may pass.
func ValidVideo(site, key string) bool {
	switch site {
	case SiteYouTube:
		return reYouTubeKey.MatchString(key)
	case SiteVimeo:
		return reVimeoKey.MatchString(key)
	}
	return false
}

// GetTitleVideos returns the videos stored for a title. ErrNotFound for no such title.
func (s *Store) GetTitleVideos(ctx context.Context, titleID int64) (TitleVideos, error) {
	var out TitleVideos
	err := s.snapshot(ctx, func(q queryer) error {
		err := q.QueryRowContext(ctx, `SELECT videos_ref, videos_checked FROM titles WHERE id = ?`, titleID).
			Scan(&out.Ref, &out.CheckedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		rows, err := q.QueryContext(ctx, `SELECT site, key, name, type, language, official, published_at
			FROM title_videos WHERE title_id = ? ORDER BY ord`, titleID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v Video
			if err := rows.Scan(&v.Site, &v.Key, &v.Name, &v.Type, &v.Language, &v.Official, &v.PublishedAt); err != nil {
				return err
			}
			out.Videos = append(out.Videos, v)
		}
		return rows.Err()
	})
	return out, err
}

// SetTitleVideos replaces the videos of a title with videos (best first; none is a title checked
// that has none) fetched for ref at checkedAt. ErrNotFound for a title that is gone. No list shows
// them: it leaves Generation alone. A person is waiting for the title's page: it goes ahead of the
// background writes.
func (s *Store) SetTitleVideos(ctx context.Context, titleID int64, ref string, videos []Video, checkedAt int64) error {
	for _, v := range videos {
		if !ValidVideo(v.Site, v.Key) {
			return fmt.Errorf("store: video %q at %q", v.Key, v.Site)
		}
	}
	return s.writeQuiet(ctx, prioHigh, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE titles SET videos_ref = ?, videos_checked = ? WHERE id = ?`, ref, checkedAt, titleID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM title_videos WHERE title_id = ?`, titleID); err != nil {
			return err
		}
		for i, v := range videos {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO title_videos (title_id, ord, site, key, name, type, language, official, published_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				titleID, i, v.Site, v.Key, v.Name, v.Type, v.Language, v.Official, v.PublishedAt); err != nil {
				return err
			}
		}
		return nil
	})
}
