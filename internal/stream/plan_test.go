package stream

import (
	"math"
	"strings"
	"testing"
)

func evenKeyframes(n int, gop float64) []float64 {
	kf := make([]float64, n)
	for i := range kf {
		kf[i] = float64(i) * gop
	}
	return kf
}

// checkInvariants verifies what every plan must satisfy.
func checkInvariants(t *testing.T, p Plan, minSeg float64) {
	t.Helper()
	next, total := 0, 0.0
	for i, s := range p.Segments {
		if s.First != next || s.End <= s.First {
			t.Errorf("segment %d covers GOPs [%d,%d), want to start at %d", i, s.First, s.End, next)
		}
		next = s.End
		if i > 0 && math.Abs(s.Start-p.Keyframes[s.First]) > 1e-9 {
			t.Errorf("segment %d starts at %.3f, not on its keyframe %.3f", i, s.Start, p.Keyframes[s.First])
		}
		if i < len(p.Segments)-1 && s.Duration < minSeg-0.001 {
			t.Errorf("segment %d is %.3f s, want >= %.1f", i, s.Duration, minSeg)
		}
		if i > 0 && math.Abs(p.Segments[i-1].Start+p.Segments[i-1].Duration-s.Start) > 1e-9 {
			t.Errorf("gap before segment %d", i)
		}
		total += s.Duration
	}
	if next != len(p.Keyframes) {
		t.Errorf("segments cover %d of %d GOPs", next, len(p.Keyframes))
	}
	if math.Abs(total-p.Duration) > 1e-6 {
		t.Errorf("durations sum to %.3f, want %.3f", total, p.Duration)
	}
}

func TestNewPlan(t *testing.T) {
	tests := []struct {
		name     string
		kf       []float64
		duration float64
		starts   []float64 // expected segment starts
		firstEnd []int     // expected End of each segment
	}{
		{"even 2 s GOPs", evenKeyframes(15, 2), 30, []float64{0, 6, 12, 18, 24}, []int{3, 6, 9, 12, 15}},
		{"irregular GOPs", []float64{0, 2, 4, 6.5, 8, 12.4, 14, 18.6, 20, 25, 28}, 30,
			[]float64{0, 6.5, 14, 20}, []int{3, 6, 8, 11}}, // the 2 s tail joins the last segment
		{"tail long enough stays", evenKeyframes(7, 2), 16, []float64{0, 6, 12}, []int{3, 6, 7}},
		{"first keyframe after 0", []float64{1.4, 3.4, 5.4, 7.4, 9.4, 11.4, 13.4}, 15,
			[]float64{0, 7.4}, []int{3, 7}}, // the segment starts at 0 although the first keyframe is at 1.4
		{"one keyframe", []float64{0}, 100, []float64{0}, []int{1}},
		{"long GOPs", []float64{0, 10, 20}, 30, []float64{0, 10, 20}, []int{1, 2, 3}},
		{"unsorted, duplicated, negative", []float64{4, 0, 2, 2.0004, -0.0003, 6, 8, 10, 12}, 14,
			[]float64{0, 6}, []int{3, 7}}, // -0.0003 and 0 merge, 2.0004 and 2 merge
		{"duration missing", evenKeyframes(10, 2), 0, []float64{0, 6, 12}, []int{3, 6, 10}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewPlan(tt.kf, tt.duration, 6)
			if err != nil {
				t.Fatal(err)
			}
			checkInvariants(t, p, 6)
			if len(p.Segments) != len(tt.starts) {
				t.Fatalf("segments = %+v, want starts %v", p.Segments, tt.starts)
			}
			for i, s := range p.Segments {
				if math.Abs(s.Start-tt.starts[i]) > 1e-9 || s.End != tt.firstEnd[i] {
					t.Errorf("segment %d = %+v, want start %v end %d", i, s, tt.starts[i], tt.firstEnd[i])
				}
			}
		})
	}
}

// FuzzNewPlan found this: with one keyframe and a container duration under a
// millisecond the only segment was listed as EXTINF:0.000.
func TestPlanNeverListsAZeroLengthSegment(t *testing.T) {
	for _, duration := range []float64{0, 0.0004, 0.0009, 0.001, 0.0011, 1} {
		p, err := NewPlan([]float64{0}, duration, 6)
		if err != nil {
			t.Fatal(err)
		}
		if pl := p.Playlist(""); strings.Contains(pl, "EXTINF:0.000,") {
			t.Errorf("duration %v: playlist lists a zero-length segment:\n%s", duration, pl)
		}
		if d := p.Segments[0].Duration; d < 0.001 {
			t.Errorf("duration %v: the segment lasts %v", duration, d)
		}
	}
}

func TestNewPlanNeedsKeyframes(t *testing.T) {
	for _, kf := range [][]float64{nil, {}, {math.NaN()}, {math.Inf(1)}} {
		if _, err := NewPlan(kf, 10, 6); err == nil {
			t.Errorf("NewPlan(%v) succeeded", kf)
		}
	}
}

func TestPlanLookups(t *testing.T) {
	p, _ := NewPlan(evenKeyframes(15, 2), 30, 6)
	for _, tt := range []struct {
		t    float64
		want int
	}{{-5, 0}, {0, 0}, {5.99, 0}, {6, 1}, {17.9, 2}, {24, 4}, {29.9, 4}, {1000, 4}} {
		if got := p.SegmentAt(tt.t); got != tt.want {
			t.Errorf("SegmentAt(%v) = %d, want %d", tt.t, got, tt.want)
		}
	}
	for _, tt := range []struct{ g, want int }{{0, 0}, {2, 0}, {3, 1}, {14, 4}, {99, 4}} {
		if got := p.SegmentOfGOP(tt.g); got != tt.want {
			t.Errorf("SegmentOfGOP(%d) = %d, want %d", tt.g, got, tt.want)
		}
	}
	if p.TimeOf(3) != 6 || p.TimeOf(15) != 30 || p.TimeOf(-1) != 0 {
		t.Errorf("TimeOf: %v %v %v", p.TimeOf(3), p.TimeOf(15), p.TimeOf(-1))
	}
}

func TestPlaylist(t *testing.T) {
	p, _ := NewPlan([]float64{0, 2, 4, 6.5, 8, 12.4, 14, 18.6, 20, 25, 28}, 30, 6)

	pl := p.Playlist("")
	want := `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:10
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-PLAYLIST-TYPE:VOD
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-MAP:URI="init.mp4"
#EXTINF:6.500,
seg00000.m4s
#EXTINF:7.500,
seg00001.m4s
#EXTINF:6.000,
seg00002.m4s
#EXTINF:10.000,
seg00003.m4s
#EXT-X-ENDLIST
`
	if pl != want {
		t.Errorf("playlist:\n%s\nwant:\n%s", pl, want)
	}

	withToken := p.Playlist("?token=a%26b")
	for _, line := range strings.Split(strings.TrimSpace(withToken), "\n") {
		isURI := !strings.HasPrefix(line, "#") || strings.HasPrefix(line, "#EXT-X-MAP")
		if isURI && !strings.Contains(line, "?token=a%26b") {
			t.Errorf("URI line without token: %q", line)
		}
		if !isURI && strings.Contains(line, "token") {
			t.Errorf("token in a tag line: %q", line)
		}
	}
	if strings.Count(withToken, "?token=") != 5 { // init + 4 segments
		t.Errorf("token appears %d times, want 5", strings.Count(withToken, "?token="))
	}
}

func TestNames(t *testing.T) {
	valid := []string{"init.mp4", "seg00000.m4s", "seg00042.m4s", "seg99999.m4s"}
	invalid := []string{"", "index.m3u8", "ffmpeg.m3u8", "gop00000.m4s", "seg0.m4s", "seg0000.m4s", "seg000000.m4s",
		"seg0000a.m4s", "seg00000.m4s.tmp", "Seg00000.m4s", "init.MP4", "init1.mp4", "../init.mp4", "a/seg00000.m4s",
		"seg00000.m4s\n", "seg-0001.m4s", " seg00000.m4s", "init.mp4/"}
	for _, n := range valid {
		if !ValidName(n) {
			t.Errorf("ValidName(%q) = false", n)
		}
	}
	for _, n := range invalid {
		if ValidName(n) {
			t.Errorf("ValidName(%q) = true", n)
		}
	}
	if i, ok := ParseSegmentName("seg00042.m4s"); !ok || i != 42 {
		t.Errorf("ParseSegmentName = %d, %v", i, ok)
	}
	if _, ok := ParseSegmentName("init.mp4"); ok {
		t.Error("init.mp4 parsed as a segment")
	}
	if SegmentName(7) != "seg00007.m4s" || !ValidName(SegmentName(123)) {
		t.Errorf("SegmentName: %q", SegmentName(7))
	}
}
