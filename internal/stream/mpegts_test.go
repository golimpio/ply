package stream

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMPEGTSArgsKeepLegacyEnvelopeAndModernDefault(t *testing.T) {
	spec := baseSpec()
	spec.Video = VideoSpec{Encode: []string{"-c:v", "libx264", "-preset", "fast"}}
	spec.Audio = AudioSpec{Codec: "aac", Copy: true}
	spec.SegmentSec = 6
	modern := BuildArgs(spec)
	if !has(modern, "-hls_segment_type", "fmp4") || !has(modern, "-fps_mode", "passthrough") || !has(modern, "-bsf:a", "aac_adtstoasc") {
		t.Fatalf("modern positive control: %v", modern)
	}
	spec.MPEGTS = true
	args := BuildArgs(spec)
	for _, want := range [][]string{
		{"-profile:v", "high", "-level:v", "4.0", "-fps_mode", "passthrough"}, {"-vf", "fps=30"},
		{"-c:a", "aac", "-b:a", "192k", "-ac", "2", "-profile:a", "aac_low", "-ar", "48000"},
		{"-hls_segment_type", "mpegts"}, {"-hls_segment_filename", "gop%05d.ts"},
		{"-copyts", "-start_at_zero", "-avoid_negative_ts", "disabled", "-output_ts_offset", "2.000"},
		{"-hls_segment_options", "mpegts_copyts=1:mpegts_flags=+initial_discontinuity"},
		{"-hls_flags", "temp_file"},
	} {
		if !has(args, want...) {
			t.Errorf("legacy args lack %v: %v", want, args)
		}
	}
	for _, forbidden := range []string{"-hls_fmp4_init_filename", "-bsf:a", "-r"} {
		if slices.Contains(args, forbidden) {
			t.Errorf("modern-only option %s in TS args", forbidden)
		}
	}
	spec.Video.Complex = "[0:0]null[v]"
	args = BuildArgs(spec)
	if !has(args, "-filter_complex", "[0:0]null[v];[v]fps=30[legacy]", "-map", "[legacy]") || slices.Contains(args, "-vf") {
		t.Fatalf("complex output lost fps stage or mixed simple/complex filters: %v", args)
	}
	spec.AudioIndex = -1
	args = BuildArgs(spec)
	if !slices.Contains(args, "-an") || slices.Contains(args, "-profile:a") {
		t.Fatalf("silent TS: %v", args)
	}
}

func TestMPEGTSPlanHasOneGOPPerSegmentIncludingShortTail(t *testing.T) {
	kf, err := uniformKeyframes(31, 6)
	if err != nil {
		t.Fatal(err)
	}
	modern, err := NewPlan(kf, 31, 6)
	if err != nil || len(modern.Segments) != 5 || modern.Segments[4].End-modern.Segments[4].First != 2 {
		t.Fatalf("modern tail-merging positive control: %+v, %v", modern, err)
	}
	legacy, err := newPlan(kf, 31, 6, false)
	if err != nil {
		t.Fatal(err)
	}
	for i, seg := range legacy.Segments {
		if seg.First != i || seg.End != i+1 {
			t.Fatalf("TS segment %d splices GOPs: %+v", i, seg)
		}
	}
	playlist := legacy.playlistFrom("?token=a%26b", 13.25, true)
	for _, want := range []string{"#EXT-X-VERSION:3", "#EXT-X-START:TIME-OFFSET=13.250,PRECISE=YES", "seg00005.ts?token=a%26b", "#EXTINF:1.000,", "#EXT-X-ENDLIST"} {
		if !strings.Contains(playlist, want) {
			t.Errorf("TS playlist lacks %q:\n%s", want, playlist)
		}
	}
	if !strings.Contains(modern.Playlist(""), "#EXT-X-MAP") {
		t.Fatal("modern init-map positive control failed")
	}
	if strings.Contains(playlist, "#EXT-X-MAP") || strings.Contains(playlist, ".m4s") {
		t.Fatalf("TS requires an MP4 init: %s", playlist)
	}
	for _, name := range []string{"seg00000.ts", "seg00042.m4s"} {
		if n, ok := ParseSegmentName(name); !ok || (n != 0 && n != 42) {
			t.Fatalf("segment name %s = %d, %v", name, n, ok)
		}
	}
	for _, name := range []string{"seg0000.ts", "seg00000.ts.tmp", "../seg00000.ts", "gop00000.ts"} {
		if ValidName(name) {
			t.Fatalf("invalid segment name accepted: %s", name)
		}
	}
}

func TestMPEGTSRejectsVideoCopyAndHasNoInit(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, nil)
	request := fakeRequest()
	// The same request remains valid for normal fMP4 remuxing.
	modern, err := m.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := modern.OpenInit(t.Context()); err != nil {
		t.Fatal("modern init positive control:", err)
	}
	request.MPEGTS = true
	if _, err := m.Create(t.Context(), request); err == nil {
		t.Fatal("TS accepted copy video with variable source GOPs")
	}
	request.Video = &VideoSpec{} // non-nil does not imply encoding
	if _, err := m.Create(t.Context(), request); err == nil {
		t.Fatal("TS accepted a zero-value copy spec")
	}
	kf, err := uniformKeyframes(31, 6)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := newPlan(kf, 31, 6, false)
	if err != nil {
		t.Fatal(err)
	}
	s := newSession(m, "legacy", t.TempDir(), request, plan)
	if !s.MPEGTS() {
		t.Fatal("transport getter lost TS")
	}
	playlist, err := s.Playlist(t.Context(), "?token=a")
	if err != nil || !strings.Contains(playlist, "seg00000.ts?token=a") || strings.Contains(playlist, "#EXT-X-MAP") {
		t.Fatalf("TS session lost its transport: %v %s", err, playlist)
	}
	if _, err := s.OpenInit(context.Background()); !errors.Is(err, ErrNoSegment) {
		t.Fatalf("TS init = %v; want no init segment", err)
	}
}

func TestMPEGTSCompletionRejectsPartialOrCorruptPackets(t *testing.T) {
	packet := bytes.Repeat([]byte{0xff}, tsPacketSize)
	packet[0], packet[1], packet[2], packet[3] = 0x47, 0x1f, 0xff, 0x10
	good := bytes.Repeat(packet, 70) // crosses the validator's buffer boundary
	path := filepath.Join(t.TempDir(), "gop00000.ts")
	write := func(data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(good)
	if info, ok := checkTSFile(path); !ok || info.size != int64(len(good)) || info.mediaOff != 0 {
		t.Fatalf("valid TS control: %+v, %v", info, ok)
	}
	corrupt := append([]byte{}, good...)
	corrupt[tsPacketSize*65] = 0
	errored := append([]byte{}, good...)
	errored[1] |= 0x80
	badAdaptation := append([]byte{}, good...)
	badAdaptation[3], badAdaptation[4] = 0x30, 183
	for name, data := range map[string][]byte{
		"partial final packet": good[:len(good)-1], "sync byte": corrupt,
		"transport error": errored, "adaptation length": badAdaptation,
	} {
		write(data)
		if _, ok := checkTSFile(path); ok {
			t.Errorf("accepted %s", name)
		}
	}
	for _, name := range []string{"gop00042.ts", "gop00042.ts.tmp", "gop00042.m4s.tmp"} {
		if n, ok := gopIndex(name); !ok || n != 42 {
			t.Errorf("cleanup missed %q: %d, %v", name, n, ok)
		}
	}
}
