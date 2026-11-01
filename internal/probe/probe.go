// Package probe wraps ffprobe and normalises its output.
package probe

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Stream is one audio, video or subtitle stream. The JSON form is the API's
// Stream shape.
type Stream struct {
	Index         int    `json:"index"` // ffprobe stream index
	Type          string `json:"type"`  // video | audio | subtitle
	Codec         string `json:"codec"`
	CodecTag      string `json:"codecTag"`
	Profile       string `json:"profile"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	PixFmt        string `json:"pixFmt"`
	ColorTransfer string `json:"colorTransfer"`
	Channels      int    `json:"channels"`
	Language      string `json:"language"`
	Title         string `json:"title"`
	Default       bool   `json:"default"`
	Forced        bool   `json:"forced"`
	DolbyVision   bool   `json:"dolbyVision,omitempty"`
}

// HDR classifies the stream's dynamic range: "dv", "hdr10", "hlg" or "".
func (s Stream) HDR() string {
	switch {
	case s.DolbyVision:
		return "dv"
	case s.ColorTransfer == "smpte2084":
		return "hdr10"
	case s.ColorTransfer == "arib-std-b67":
		return "hlg"
	}
	return ""
}

// BitDepth is the bits per colour component implied by the pixel format
// (yuv420p10le is 10, nv12 is 8); 0 when the format is unknown.
func (s Stream) BitDepth() int {
	pf := s.PixFmt
	switch {
	case pf == "":
		return 0
	case strings.HasPrefix(pf, "p010"):
		return 10
	case strings.HasPrefix(pf, "p012"):
		return 12
	case strings.HasPrefix(pf, "p016"):
		return 16
	}
	for _, d := range []int{16, 14, 12, 10, 9} {
		n := strconv.Itoa(d)
		if strings.HasSuffix(pf, n+"le") || strings.HasSuffix(pf, n+"be") {
			return d
		}
	}
	return 8
}

// Result is the normalised ffprobe output for one file.
type Result struct {
	Container   string // mp4 mov m4v mkv webm ts avi, or ffprobe's first format name
	DurationSec float64
	Bitrate     int64
	Streams     []Stream // video, audio and subtitle streams only; cover art excluded
}

type ffprobeOutput struct {
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
	Streams []struct {
		Index          int               `json:"index"`
		CodecType      string            `json:"codec_type"`
		CodecName      string            `json:"codec_name"`
		CodecTagString string            `json:"codec_tag_string"`
		Profile        string            `json:"profile"`
		Width          int               `json:"width"`
		Height         int               `json:"height"`
		PixFmt         string            `json:"pix_fmt"`
		ColorTransfer  string            `json:"color_transfer"`
		Channels       int               `json:"channels"`
		Tags           map[string]string `json:"tags"`
		Disposition    map[string]int    `json:"disposition"`
		SideDataList   []struct {
			Type string `json:"side_data_type"`
		} `json:"side_data_list"`
	} `json:"streams"`
}

// Parse converts ffprobe JSON for the file at path into a Result. The path's
// extension disambiguates containers that share an ffprobe format name.
func Parse(data []byte, path string) (Result, error) {
	var out ffprobeOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return Result{}, fmt.Errorf("parse ffprobe output: %w", err)
	}
	res := Result{
		Container:   container(out.Format.FormatName, filepath.Ext(path)),
		DurationSec: parseFloat(out.Format.Duration),
		Bitrate:     int64(parseFloat(out.Format.BitRate)),
	}
	for _, s := range out.Streams {
		switch s.CodecType {
		case "video", "audio", "subtitle":
		default:
			continue
		}
		if s.Disposition["attached_pic"] == 1 {
			continue
		}
		st := Stream{
			Index:         s.Index,
			Type:          s.CodecType,
			Codec:         s.CodecName,
			CodecTag:      cleanTag(s.CodecTagString),
			Profile:       s.Profile,
			Width:         s.Width,
			Height:        s.Height,
			PixFmt:        s.PixFmt,
			ColorTransfer: s.ColorTransfer,
			Channels:      s.Channels,
			Language:      tag(s.Tags, "language"),
			Title:         tag(s.Tags, "title"),
			Default:       s.Disposition["default"] == 1,
			Forced:        s.Disposition["forced"] == 1,
		}
		for _, sd := range s.SideDataList {
			if strings.Contains(strings.ToLower(sd.Type), "dovi configuration record") {
				st.DolbyVision = true
			}
		}
		res.Streams = append(res.Streams, st)
	}
	return res, nil
}

func container(formatName, ext string) string {
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	names := strings.Split(formatName, ",")
	switch {
	case slices.Contains(names, "matroska") || slices.Contains(names, "webm"):
		if ext == "webm" {
			return "webm"
		}
		return "mkv"
	case slices.Contains(names, "mov") || slices.Contains(names, "mp4"):
		if ext == "mov" || ext == "m4v" {
			return ext
		}
		return "mp4"
	case slices.Contains(names, "mpegts"):
		return "ts"
	case slices.Contains(names, "avi"):
		return "avi"
	}
	return names[0]
}

// cleanTag drops the "[0][0][0][0]" placeholder ffprobe prints for unknown tags.
func cleanTag(s string) string {
	if strings.ContainsAny(s, "[]") {
		return ""
	}
	return s
}

// tag looks up a metadata tag case-insensitively (ffprobe emits LANGUAGE or
// language depending on the muxer).
func tag(tags map[string]string, key string) string {
	for k, v := range tags {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

func parseFloat(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0
	}
	return f
}
