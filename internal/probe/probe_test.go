package probe

import "testing"

const mkvJSON = `{
  "streams": [
    {"index": 0, "codec_type": "video", "codec_name": "hevc", "profile": "Main 10",
     "codec_tag_string": "[0][0][0][0]", "width": 3840, "height": 2160, "pix_fmt": "yuv420p10le",
     "color_transfer": "smpte2084",
     "disposition": {"default": 1, "forced": 0},
     "side_data_list": [{"side_data_type": "DOVI configuration record", "dv_profile": 8}]},
    {"index": 1, "codec_type": "audio", "codec_name": "eac3", "codec_tag_string": "[0][0][0][0]",
     "channels": 6, "tags": {"LANGUAGE": "eng", "Title": "Surround"},
     "disposition": {"default": 1, "forced": 0}},
    {"index": 2, "codec_type": "subtitle", "codec_name": "hdmv_pgs_subtitle",
     "tags": {"language": "por"}, "disposition": {"default": 0, "forced": 1}},
    {"index": 3, "codec_type": "attachment", "codec_name": "ttf"},
    {"index": 4, "codec_type": "video", "codec_name": "mjpeg", "disposition": {"attached_pic": 1}}
  ],
  "format": {"format_name": "matroska,webm", "duration": "7234.567000", "bit_rate": "12345678"}
}`

func TestParseMKV(t *testing.T) {
	res, err := Parse([]byte(mkvJSON), "/media/Movie (2020).mkv")
	if err != nil {
		t.Fatal(err)
	}
	if res.Container != "mkv" {
		t.Errorf("container = %q, want mkv", res.Container)
	}
	if res.DurationSec != 7234.567 || res.Bitrate != 12345678 {
		t.Errorf("duration/bitrate = %v/%v", res.DurationSec, res.Bitrate)
	}
	if len(res.Streams) != 3 {
		t.Fatalf("streams = %d, want 3 (attachment and cover art dropped): %+v", len(res.Streams), res.Streams)
	}

	v, a, s := res.Streams[0], res.Streams[1], res.Streams[2]
	if v.Codec != "hevc" || v.Width != 3840 || v.Height != 2160 || v.PixFmt != "yuv420p10le" || !v.Default {
		t.Errorf("video = %+v", v)
	}
	if v.CodecTag != "" {
		t.Errorf("placeholder codec tag kept: %q", v.CodecTag)
	}
	if !v.DolbyVision || v.HDR() != "dv" {
		t.Errorf("dolby vision not detected: %+v hdr=%q", v, v.HDR())
	}
	if a.Codec != "eac3" || a.Channels != 6 || a.Language != "eng" || a.Title != "Surround" {
		t.Errorf("audio = %+v (tags must be read case-insensitively)", a)
	}
	if s.Type != "subtitle" || s.Language != "por" || !s.Forced || s.Default {
		t.Errorf("subtitle = %+v", s)
	}
}

func TestParseKeepsRealCodecTag(t *testing.T) {
	const js = `{"streams":[{"index":0,"codec_type":"video","codec_name":"hevc","codec_tag_string":"hvc1"}],
		"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2"}}`
	res, err := Parse([]byte(js), "x.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Streams[0].CodecTag; got != "hvc1" {
		t.Errorf("codec tag = %q, want hvc1", got)
	}
}

func TestHDR(t *testing.T) {
	tests := []struct {
		s    Stream
		want string
	}{
		{Stream{ColorTransfer: "bt709"}, ""},
		{Stream{ColorTransfer: "smpte2084"}, "hdr10"},
		{Stream{ColorTransfer: "arib-std-b67"}, "hlg"},
		{Stream{ColorTransfer: "smpte2084", DolbyVision: true}, "dv"},
		{Stream{}, ""},
	}
	for _, tt := range tests {
		if got := tt.s.HDR(); got != tt.want {
			t.Errorf("HDR(%+v) = %q, want %q", tt.s, got, tt.want)
		}
	}
}

func TestContainer(t *testing.T) {
	const mp4Fmt = "mov,mp4,m4a,3gp,3g2,mj2"
	tests := []struct {
		format, ext, want string
	}{
		{mp4Fmt, ".mp4", "mp4"},
		{mp4Fmt, ".MP4", "mp4"},
		{mp4Fmt, ".mov", "mov"},
		{mp4Fmt, ".m4v", "m4v"},
		{mp4Fmt, "", "mp4"},
		{"matroska,webm", ".mkv", "mkv"},
		{"matroska,webm", ".webm", "webm"},
		{"matroska,webm", ".MKV", "mkv"},
		{"mpegts", ".ts", "ts"},
		{"mpegts", ".m2ts", "ts"},
		{"avi", ".avi", "avi"},
		{"asf", ".wmv", "asf"},
		{"", ".mp4", ""},
	}
	for _, tt := range tests {
		if got := container(tt.format, tt.ext); got != tt.want {
			t.Errorf("container(%q, %q) = %q, want %q", tt.format, tt.ext, got, tt.want)
		}
	}
}

func TestParseInvalidJSON(t *testing.T) {
	if _, err := Parse([]byte("not json"), "x.mkv"); err == nil {
		t.Error("expected an error for invalid JSON")
	}
}

func TestParseMissingDuration(t *testing.T) {
	res, err := Parse([]byte(`{"format":{"format_name":"avi","duration":"N/A"},"streams":[]}`), "x.avi")
	if err != nil {
		t.Fatal(err)
	}
	if res.DurationSec != 0 || res.Container != "avi" {
		t.Errorf("result = %+v", res)
	}
}
