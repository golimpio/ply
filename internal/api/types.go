package api

import (
	"fmt"
	"path/filepath"
	"strconv"

	"ply/internal/playback"
	"ply/internal/probe"
	"ply/internal/store"
	"ply/internal/subs"
)

// Progress is the resume state of an item.
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
	Progress     *Progress `json:"progress"` // null when the item has not been started
	Art          *ArtURLs  `json:"art"`      // null when there is no artwork

	// Metadata (M6): poster, backdrop and logo are image URLs, the frame-grab
	// Art above stays the fallback. Overview and Still are an episode's.
	MetaFields
	Overview string `json:"overview,omitempty"`
	Still    string `json:"still,omitempty"`
}

// Subtitle describes a selectable subtitle track. Key is the ffprobe stream
// index for an embedded track and ext<N> for a sidecar file. URL (WebVTT) is
// only set for text tracks; an Image track can only be burned into the video
// (POST /playback with subtitleIndex = the stream index).
type Subtitle struct {
	Key      string `json:"key"`
	Language string `json:"language"`
	Title    string `json:"title"`
	Format   string `json:"format"`
	Image    bool   `json:"image"`
	Forced   bool   `json:"forced"`
	Default  bool   `json:"default"`
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

	// Meta is the movie's title, or the show's for an episode; EpisodeInfo is the
	// episode's own metadata. Both are absent when there is none. (The spec calls
	// the second one "episode", which an item's episode number already is.)
	Meta        *TitleDetail `json:"meta,omitempty"`
	EpisodeInfo *EpisodeInfo `json:"episodeInfo,omitempty"`
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

	Year     *int   `json:"year,omitempty"`
	Overview string `json:"overview,omitempty"`
	MetaFields
}

func summarize(e store.Entry) ItemSummary {
	s := ItemSummary{
		ID:          e.ID,
		Kind:        e.Kind,
		Title:       e.Title,
		DurationSec: e.DurationSec,
		Container:   e.Container,
		AddedAt:     e.AddedAt,
		MetaFields:  MetaFields{MatchState: e.MatchState, NeedsReview: e.NeedsReview},
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
	if p := e.Progress; p != nil {
		s.Progress = &Progress{PositionSec: p.PositionSec, Watched: p.Watched}
	}
	return s
}

// subtitlesOf lists the embedded subtitle streams, then the sidecar files.
func (s *Server) subtitlesOf(e store.Entry) []Subtitle {
	list := []Subtitle{}
	for _, st := range e.Streams {
		if st.Type != "subtitle" {
			continue
		}
		sub := Subtitle{
			Key:      strconv.Itoa(st.Index),
			Language: st.Language,
			Title:    st.Title,
			Format:   st.Codec,
			Image:    playback.IsImageSubtitle(st.Codec),
			Forced:   st.Forced,
			Default:  st.Default,
		}
		if s.Subs != nil && subs.IsText(st.Codec) {
			sub.URL = s.subtitleURL(e.ID, sub.Key)
		}
		list = append(list, sub)
	}
	for i, x := range e.External {
		sub := Subtitle{Key: extKey(i), Language: x.Language, Format: x.Format, Forced: x.Forced}
		if s.Subs != nil {
			sub.URL = s.subtitleURL(e.ID, sub.Key)
		}
		list = append(list, sub)
	}
	return list
}

func (s *Server) detail(e store.Entry, pb Playback) ItemDetail {
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
		Subtitles:   s.subtitlesOf(e),
		Playback:    pb,
	}
}
