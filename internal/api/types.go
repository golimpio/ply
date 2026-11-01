package api

import (
	"fmt"
	"path/filepath"

	"ply/internal/playback"
	"ply/internal/probe"
	"ply/internal/store"
)

// Progress is the resume state of an item (filled in by M5).
type Progress struct {
	PositionSec float64 `json:"positionSec"`
	Watched     bool    `json:"watched"`
}

// ItemSummary is the list-view form of an item. addedAt is unix seconds; hdr
// is "hdr10", "hlg", "dv" or absent.
type ItemSummary struct {
	ID           int64     `json:"id"`
	Kind         string    `json:"kind"`
	Title        string    `json:"title"`
	Year         *int      `json:"year,omitempty"`
	Show         string    `json:"show,omitempty"`
	Season       *int      `json:"season,omitempty"`
	Episode      *int      `json:"episode,omitempty"`
	EpisodeTitle string    `json:"episodeTitle,omitempty"`
	DurationSec  float64   `json:"durationSec"`
	Container    string    `json:"container"`
	VideoCodec   string    `json:"videoCodec"`
	AudioCodec   string    `json:"audioCodec"`
	Width        int       `json:"width"`
	Height       int       `json:"height"`
	HDR          string    `json:"hdr,omitempty"`
	AddedAt      int64     `json:"addedAt"`
	Progress     *Progress `json:"progress,omitempty"`
}

// Subtitle describes a selectable subtitle track.
type Subtitle struct {
	Key      string `json:"key"` // stream index, or ext<N> for sidecars (M5)
	Language string `json:"language"`
	Title    string `json:"title"`
	Format   string `json:"format"`
	Image    bool   `json:"image"`
	URL      string `json:"url,omitempty"`
}

// ItemDetail is the full item view.
type ItemDetail struct {
	ItemSummary
	Path      string         `json:"path"` // base name only
	Size      int64          `json:"size"`
	Bitrate   int64          `json:"bitrate"`
	Streams   []probe.Stream `json:"streams"`
	Subtitles []Subtitle     `json:"subtitles"`
	Playback  Playback       `json:"playback"`
}

// Playback is a delivery decision plus the URL to play.
type Playback struct {
	Method      string   `json:"method"`
	VideoAction string   `json:"videoAction"`
	AudioAction string   `json:"audioAction"`
	AudioIndex  int      `json:"audioIndex"`
	Reasons     []string `json:"reasons"`
	URL         string   `json:"url"` // empty until an HLS session is available (M3)
	SessionID   string   `json:"sessionId,omitempty"`
	StartSec    float64  `json:"startSec"`
}

// ShowSummary is one entry of GET /api/shows.
type ShowSummary struct {
	Show         string `json:"show"`
	Seasons      []int  `json:"seasons"`
	EpisodeCount int    `json:"episodeCount"`
}

func summarize(e store.Entry) ItemSummary {
	s := ItemSummary{
		ID:          e.ID,
		Kind:        e.Kind,
		Title:       e.Title,
		DurationSec: e.DurationSec,
		Container:   e.Container,
		AddedAt:     e.AddedAt,
	}
	if e.Year > 0 {
		s.Year = &e.Year
	}
	if e.Kind == store.KindEpisode {
		s.Show = e.Show
		s.Season = &e.Season
		s.Episode = &e.Episode
		s.EpisodeTitle = e.Title
		if s.Title == "" {
			s.Title = fmt.Sprintf("S%02dE%02d", e.Season, e.Episode)
		}
	}
	if v, ok := playback.VideoStream(e.Streams); ok {
		s.VideoCodec, s.Width, s.Height, s.HDR = v.Codec, v.Width, v.Height, v.HDR()
	}
	if a, ok := playback.AudioStream(e.Streams, -1); ok {
		s.AudioCodec = a.Codec
	}
	return s
}

func subtitlesOf(streams []probe.Stream) []Subtitle {
	subs := []Subtitle{}
	for _, st := range streams {
		if st.Type != "subtitle" {
			continue
		}
		subs = append(subs, Subtitle{
			Key:      fmt.Sprint(st.Index),
			Language: st.Language,
			Title:    st.Title,
			Format:   st.Codec,
			Image:    playback.IsImageSubtitle(st.Codec),
		})
	}
	return subs
}

func detail(e store.Entry, pb Playback) ItemDetail {
	streams := e.Streams
	if streams == nil {
		streams = []probe.Stream{}
	}
	return ItemDetail{
		ItemSummary: summarize(e),
		Path:        filepath.Base(e.Path),
		Size:        e.Size,
		Bitrate:     e.Bitrate,
		Streams:     streams,
		Subtitles:   subtitlesOf(e.Streams),
		Playback:    pb,
	}
}
