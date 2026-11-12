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
	for line := range strings.SplitSeq(strings.TrimSpace(withToken), "\n") {
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

// A session asked to start at a time says so in its playlist, for the players that start where it says.
func TestPlaylistFromSaysWhereToStart(t *testing.T) {
	p, _ := NewPlan(evenKeyframes(15, 2), 30, 6)
	startTag := func(pl string) []string {
		var tags []string
		for line := range strings.SplitSeq(pl, "\n") {
			if strings.HasPrefix(line, "#EXT-X-START") {
				tags = append(tags, line)
			}
		}
		return tags
	}

	pl := p.PlaylistFrom("?token=a", 13.25)
	if got := startTag(pl); len(got) != 1 || got[0] != "#EXT-X-START:TIME-OFFSET=13.250,PRECISE=YES" {
		t.Fatalf("start tags = %q", got)
	}
	// The rest is the playlist there is without it, in the same order, with the tag after the header tags
	// and before the first URI.
	without := p.Playlist("?token=a")
	if strings.Replace(pl, "#EXT-X-START:TIME-OFFSET=13.250,PRECISE=YES\n", "", 1) != without {
		t.Errorf("playlist with a start:\n%s\nis not the one without:\n%s", pl, without)
	}
	if i, j := strings.Index(pl, "#EXT-X-START"), strings.Index(pl, "#EXT-X-MAP"); i < strings.Index(pl, "#EXT-X-INDEPENDENT-SEGMENTS") || i > j {
		t.Errorf("the start tag is at %d, the header ends at %d and the first URI is at %d", i, strings.Index(pl, "#EXT-X-INDEPENDENT-SEGMENTS"), j)
	}
	if strings.Contains(startTag(pl)[0], "token") {
		t.Error("token in the start tag")
	}

	// Positive control for the cases below: Playlist never has it.
	if got := startTag(without); len(got) != 0 {
		t.Errorf("Playlist has a start tag: %q", got)
	}
	for name, at := range map[string]float64{"the beginning": 0, "before it": -3, "not a number": math.NaN(), "infinity": math.Inf(1),
		"minus infinity": math.Inf(-1), "the end": 30, "past the end": 31} {
		if got := startTag(p.PlaylistFrom("", at)); len(got) != 0 {
			t.Errorf("a start at %s (%v) has a tag: %q", name, at, got)
		}
	}
	if got := startTag(p.PlaylistFrom("", 29.999)); len(got) != 1 {
		t.Errorf("a start just inside the file has tags %q, want one", got)
	}
}

// A seek lands inside the segment that covers it, on a keyframe at or before the target: the segment starts
// up to one segment-length before the time asked for, never after it, and a player that seeks to the time
// has the data it needs. (How far the server cuts in before the target is this, and no more.)
func TestSeekTargetsAreCoveredByTheSegmentThatStartsBeforeThem(t *testing.T) {
	for name, kf := range map[string][]float64{
		"even 2 s GOPs":  evenKeyframes(3600, 2),
		"10 s GOPs":      evenKeyframes(720, 10),
		"irregular GOPs": {0, 1.4, 5.1, 9.9, 10.4, 18, 19.5, 30, 41.7, 42, 60, 83.2, 90},
	} {
		p, err := NewPlan(kf, kf[len(kf)-1]+4, 6)
		if err != nil {
			t.Fatal(err)
		}
		worst, at := 0.0, 0.0
		for target := 0.0; target < p.Duration; target += 0.37 {
			s := p.Segments[p.SegmentAt(target)]
			left := target - s.Start
			if left < 0 || left >= s.Duration+1e-9 {
				t.Fatalf("%s: %.2f s is in the segment %+v, which does not cover it", name, target, s)
			}
			if k := p.TimeOf(s.First); s.Start != 0 && k != s.Start {
				t.Fatalf("%s: the segment %+v does not start on its keyframe %.3f", name, s, k)
			}
			if left > worst {
				worst, at = left, target
			}
		}
		longest := 0.0
		for _, s := range p.Segments {
			longest = max(longest, s.Duration)
		}
		t.Logf("%s: a seek lands at most %.2f s into its segment (at %.2f s); the longest segment is %.2f s", name, worst, at, longest)
		if worst > longest {
			t.Errorf("%s: the segment starts %.2f s before the target, more than the longest segment (%.2f s)", name, worst, longest)
		}
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
