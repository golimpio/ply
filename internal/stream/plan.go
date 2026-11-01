// Package stream turns library files into HLS (fMP4) sessions by driving
// ffmpeg. The playlist is generated up front from the file's keyframe index,
// so players can seek anywhere; ffmpeg is (re)started on demand and only ever
// produces what the player is about to ask for.
package stream

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DefaultSegmentSec is the minimum duration of a playlist segment.
const DefaultSegmentSec = 6.0

// Segment is one playlist entry: the keyframes [First, End) of the plan.
type Segment struct {
	Start    float64 // seconds from the start of the file
	Duration float64
	First    int // index of the first keyframe (GOP) in the segment
	End      int // one past the last GOP
}

// Plan is the segment layout of one file. A GOP is the run of frames from one
// keyframe to the next; ffmpeg writes one fragment per GOP and a segment is
// served as the concatenation of its GOPs, so playlist boundaries never
// depend on how ffmpeg would have cut the stream itself.
type Plan struct {
	Keyframes []float64 // ascending, seconds from the start of the file
	Duration  float64
	Segments  []Segment
}

// NewPlan groups keyframes into segments of at least minSeg seconds, each
// starting on a keyframe: a segment ends at the first keyframe that is at
// least minSeg after its own first one. The first segment starts at 0 and the
// last ends at duration; a trailing sliver shorter than half of minSeg is
// merged into its predecessor.
func NewPlan(keyframes []float64, duration, minSeg float64) (Plan, error) {
	if minSeg <= 0 {
		minSeg = DefaultSegmentSec
	}
	kf := cleanKeyframes(keyframes)
	if len(kf) == 0 {
		return Plan{}, fmt.Errorf("no keyframes")
	}
	// A container duration that is missing or ends within a millisecond of the
	// last keyframe would make the last segment zero-length (EXTINF:0.000).
	if last := kf[len(kf)-1]; duration < last+0.001 {
		duration = last + 0.04
	}
	p := Plan{Keyframes: kf, Duration: duration}
	for i := 0; i < len(kf); {
		j := i + 1
		for j < len(kf) && kf[j]-kf[i] < minSeg-0.0005 {
			j++
		}
		start, end := kf[i], duration
		if i == 0 {
			start = 0
		}
		if j < len(kf) {
			end = kf[j]
		}
		p.Segments = append(p.Segments, Segment{Start: start, Duration: end - start, First: i, End: j})
		i = j
	}
	if n := len(p.Segments); n > 1 && p.Segments[n-1].Duration < minSeg/2 {
		last := p.Segments[n-1]
		prev := &p.Segments[n-2]
		prev.Duration += last.Duration
		prev.End = last.End
		p.Segments = p.Segments[:n-1]
	}
	return p, nil
}

func cleanKeyframes(in []float64) []float64 {
	kf := make([]float64, 0, len(in))
	for _, t := range in {
		if math.IsNaN(t) || math.IsInf(t, 0) {
			continue
		}
		kf = append(kf, math.Max(t, 0))
	}
	sort.Float64s(kf)
	out := kf[:0]
	for _, t := range kf {
		if len(out) == 0 || t-out[len(out)-1] >= 0.001 {
			out = append(out, t)
		}
	}
	return out
}

// TimeOf is the start time of keyframe g; past the last one it is the
// duration of the file.
func (p Plan) TimeOf(g int) float64 {
	if g < 0 {
		return 0
	}
	if g >= len(p.Keyframes) {
		return p.Duration
	}
	return p.Keyframes[g]
}

// SegmentAt returns the index of the segment containing time t (clamped).
func (p Plan) SegmentAt(t float64) int {
	i := sort.Search(len(p.Segments), func(i int) bool { return p.Segments[i].Start > t })
	return max(i-1, 0)
}

// SegmentOfGOP returns the segment containing keyframe g (clamped).
func (p Plan) SegmentOfGOP(g int) int {
	i := sort.Search(len(p.Segments), func(i int) bool { return p.Segments[i].First > g })
	return max(i-1, 0)
}

// Playlist renders the complete VOD playlist. suffix (for example
// "?token=abc") is appended to every URI so players that cannot send headers
// stay authenticated.
func (p Plan) Playlist(suffix string) string {
	target := 1.0
	for _, s := range p.Segments {
		target = math.Max(target, math.Ceil(s.Duration))
	}
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", int(target))
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	fmt.Fprintf(&b, "#EXT-X-MAP:URI=\"%s%s\"\n", InitName, suffix)
	for i, s := range p.Segments {
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n%s%s\n", s.Duration, SegmentName(i), suffix)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// InitName is the name of the fMP4 initialisation segment.
const InitName = "init.mp4"

var nameRE = regexp.MustCompile(`^(init\.mp4|seg\d{5}\.m4s)$`)

// ValidName reports whether name is a servable session file: the init
// segment or a media segment.
func ValidName(name string) bool { return nameRE.MatchString(name) }

// SegmentName is the file name of segment i.
func SegmentName(i int) string { return fmt.Sprintf("seg%05d.m4s", i) }

// ParseSegmentName returns the segment index of a name such as seg00042.m4s.
func ParseSegmentName(name string) (int, bool) {
	if !ValidName(name) || name == InitName {
		return 0, false
	}
	n, err := strconv.Atoi(name[3:8])
	return n, err == nil
}
