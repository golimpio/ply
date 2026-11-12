//go:build integration

package stream

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ply/internal/hwaccel"
)

func TestHLSInfoReadsActualFFmpegInit(t *testing.T) {
	src := generate(t, "source.mkv",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "6", "-c:v", "libx264", "-preset", "ultrafast", "-g", "48",
		"-pix_fmt", "yuv420p", "-c:a", "aac")
	for _, scaled := range []bool{false, true} {
		name := "remux"
		if scaled {
			name = "scaled transcode"
		}
		t.Run(name, func(t *testing.T) {
			m := newManager(t, nil)
			req := Request{Path: src, Container: "mkv", DurationSec: 6, VideoIndex: 0, VideoCodec: "h264",
				AudioIndex: 1, AudioCodec: "aac", AudioCopy: true}
			wantWidth, wantHeight := 320, 180
			if scaled {
				plan := (hwaccel.Info{}).SoftwareOnly().Plan(hwaccel.Request{
					Codec: "h264", BitDepth: 8, Width: 320, Height: 180, VideoIndex: 0, BurnSubtitle: -1, MaxHeight: 90,
				})
				req.Video = &VideoSpec{Codec: "h264", Encode: plan.Encode, Pre: plan.Pre, Filter: plan.Filter}
				wantWidth, wantHeight = 160, 90
			}
			s, err := m.Create(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			got, err := s.HLSInfo(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got.Width != wantWidth || got.Height != wantHeight || !strings.HasPrefix(got.Codecs, "avc1.") ||
				!strings.HasSuffix(got.Codecs, ",mp4a.40.2") {
				t.Fatalf("generated metadata = %+v, want %dx%d AVC + AAC LC", got, wantWidth, wantHeight)
			}
		})
	}
}

func TestMPEGTSMetadataReadsGeneratedMediaAndDoesNotRewind(t *testing.T) {
	src := generate(t, "transport-source.mkv",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "30", "-c:v", "libx264", "-preset", "ultrafast", "-g", "48",
		"-pix_fmt", "yuv420p", "-c:a", "aac")
	transcodeVariants(t, func(t *testing.T, encoder hwaccel.Info) {
		m := newManager(t, nil)
		plan := encoder.Plan(hwaccel.Request{
			Codec: "h264", BitDepth: 8, Width: 320, Height: 180, VideoIndex: 0, BurnSubtitle: -1, MaxHeight: 90,
		})
		req := Request{Path: src, Container: "mkv", DurationSec: 30, VideoIndex: 0, VideoCodec: "h264",
			AudioIndex: 1, AudioCodec: "aac", MPEGTS: true, StartSec: 12.5,
			Video: &VideoSpec{Codec: "h264", Encode: plan.Encode, Pre: plan.Pre, Filter: plan.Filter}}
		s, err := m.Create(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		got, err := s.HLSInfo(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got.Codecs != "avc1.640028,mp4a.40.2" || got.Width != 160 || got.Height != 90 {
			t.Fatalf("generated TS metadata = %+v, want High4.0/AAC-LC and scaled 160x90", got)
		}
		if s.hlsInfo.Load() == nil {
			t.Fatal("successful output metadata was not cached")
		}
		media, err := s.OpenSegment(ctx, s.Plan().SegmentAt(24))
		if err != nil {
			t.Fatal(err)
		}
		media.Close()
		s.mu.Lock()
		run, segment := s.runSeq, s.lastSeg
		s.mu.Unlock()
		again, err := s.HLSInfo(ctx)
		if err != nil || again != got {
			t.Fatalf("cached metadata changed: %+v, %v", again, err)
		}
		s.mu.Lock()
		afterRun, afterSegment := s.runSeq, s.lastSeg
		s.mu.Unlock()
		if afterRun != run || afterSegment != segment {
			t.Fatalf("metadata refresh rewound playback: run/segment %d/%d -> %d/%d", run, segment, afterRun, afterSegment)
		}
		cancelled, stop := context.WithCancel(t.Context())
		stop()
		if _, err := s.HLSInfo(cancelled); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled request read cached metadata: %v", err)
		}
	})
}
