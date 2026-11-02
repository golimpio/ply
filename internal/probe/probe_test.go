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

func TestStreamBitDepth(t *testing.T) {
	for pf, want := range map[string]int{
		"": 0, "yuv420p": 8, "nv12": 8, "yuvj420p": 8, "rgb24": 8,
		"yuv420p10le": 10, "yuv422p10le": 10, "p010le": 10, "gbrp12le": 12, "yuv420p12be": 12, "p016le": 16, "yuv420p9le": 9,
	} {
		if got := (Stream{PixFmt: pf}).BitDepth(); got != want {
			t.Errorf("BitDepth(%q) = %d, want %d", pf, got, want)
		}
	}
}

const taggedJSON = `{
  "streams": [
    {"index": 0, "codec_type": "video", "codec_name": "h264", "disposition": {"default": 1}},
    {"index": 1, "codec_type": "audio", "codec_name": "aac", "disposition": {"default": 1}},
    {"index": 2, "codec_type": "video", "codec_name": "mjpeg", "disposition": {"attached_pic": 1}},
    {"index": 3, "codec_type": "video", "codec_name": "png", "disposition": {"attached_pic": 1}}
  ],
  "format": {"format_name": "mov,mp4,m4a,3gp,3g2,mj2", "duration": "60", "tags": {
    "TITLE": "Pilot", "Date": "2019-12-25T10:00:00Z", "SYNOPSIS": "A synopsis.", "show": "The Show",
    "season_number": "2", "episode_sort": "5", "episode_id": "S02E05", "encoder": "Lavf"}}
}`

func TestParseTagsAndCover(t *testing.T) {
	res, err := Parse([]byte(taggedJSON), "/media/x.mp4")
	if err != nil {
		t.Fatal(err)
	}
	want := Tags{Title: "Pilot", Year: 2019, Date: "2019-12-25T10:00:00Z", Description: "A synopsis.", Show: "The Show",
		Season: 2, Episode: 5, EpisodeID: "S02E05"}
	if res.Tags != want {
		t.Errorf("tags = %+v, want %+v", res.Tags, want)
	}
	if !res.HasCover || res.Cover != 2 {
		t.Errorf("cover = %d (%v), want the first attached picture, stream 2", res.Cover, res.HasCover)
	}
	if len(res.Streams) != 2 {
		t.Errorf("streams = %d, want video and audio only", len(res.Streams))
	}
}

func TestParseTagEdgeCases(t *testing.T) {
	for name, tc := range map[string]struct {
		json string
		want Tags
	}{
		"none":               {`{"format":{"format_name":"mpegts"},"streams":[]}`, Tags{}},
		"description wins":   {`{"format":{"format_name":"mpegts","tags":{"description":"d","synopsis":"s"}}}`, Tags{Description: "d"}},
		"year tag":           {`{"format":{"format_name":"mpegts","tags":{"year":"1999"}}}`, Tags{Date: "1999", Year: 1999}},
		"implausible year":   {`{"format":{"format_name":"mpegts","tags":{"date":"1066-10-14"}}}`, Tags{Date: "1066-10-14"}},
		"bad numbers":        {`{"format":{"format_name":"mpegts","tags":{"season_number":"two","episode_sort":"-3"}}}`, Tags{}},
		"blank values":       {`{"format":{"format_name":"mpegts","tags":{"title":"  ","show":""}}}`, Tags{}},
		"whitespace trimmed": {`{"format":{"format_name":"mpegts","tags":{"title":"  Pilot \n"}}}`, Tags{Title: "Pilot"}},
	} {
		res, err := Parse([]byte(tc.json), "x.ts")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.Tags != tc.want {
			t.Errorf("%s: tags = %+v, want %+v", name, res.Tags, tc.want)
		}
		if res.HasCover {
			t.Errorf("%s: cover reported without an attached picture", name)
		}
	}
	if !(Tags{}).Empty() || (Tags{Title: "x"}).Empty() {
		t.Error("Tags.Empty")
	}

	// A huge description is cut, and stays valid UTF-8.
	huge := make([]rune, 20000)
	for i := range huge {
		huge[i] = 'é'
	}
	res, err := Parse([]byte(`{"format":{"format_name":"mpegts","tags":{"description":"`+string(huge)+`"}}}`), "x.ts")
	if err != nil || len(res.Tags.Description) == 0 || len(res.Tags.Description) > maxTagText {
		t.Errorf("huge description: %d bytes, %v", len(res.Tags.Description), err)
	}
	for _, r := range res.Tags.Description {
		if r == 0xFFFD {
			t.Fatal("the description was cut in the middle of a character")
		}
	}
}

func TestCoverIsOnlyTheFirstVideoAttachedPicture(t *testing.T) {
	// A subtitle or audio stream flagged attached_pic is not a cover.
	res, err := Parse([]byte(`{"streams":[{"index":5,"codec_type":"audio","disposition":{"attached_pic":1}}],"format":{"format_name":"mpegts"}}`), "x.ts")
	if err != nil || res.HasCover {
		t.Errorf("audio flagged as a picture: %+v, %v", res, err)
	}
}
