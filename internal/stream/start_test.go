package stream

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A session asked to start at a time cuts in at the keyframe before it (the first segment ffmpeg writes is the one
// that covers the time), and its playlist says the time itself, so that a player that honours the tag starts at the
// second asked for, not at the keyframe.
func TestSessionStartsAtTheGOPBeforeTheTargetAndItsPlaylistSaysTheTarget(t *testing.T) {
	const item, target = 9, 61.5
	for name, tc := range map[string]struct {
		start     float64
		wantStart string // the -start_number of ffmpeg: the first GOP of the segment that covers the start
		wantTag   bool
	}{
		"a resume position": {start: target, wantStart: "30", wantTag: true}, // 60 s is the 30th 2 s GOP, and a segment start
		"no start":          {start: 0, wantStart: "0", wantTag: false},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newGuardRig(t)
			cache := &recCache{kf: map[int64][]float64{item: every(60, 0, 2)}} // two minutes, a keyframe every 2 s
			m, _ := rig.manager(t, cache, "/nonexistent/ffprobe", nil)

			req := indexedRequest("/media/film.mkv", item)
			req.StartSec = tc.start
			s, err := m.Create(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if s.guard != nil {
				t.Fatal("a plan from the cache has a guard")
			}

			// The segment that covers the target starts before it, by less than a segment.
			seg := s.Plan().Segments[s.Plan().SegmentAt(tc.start)]
			if tc.start > 0 {
				t.Logf("asked to start at %.1f s: ffmpeg starts at the keyframe at %.1f s (%.1f s before)", tc.start, seg.Start, tc.start-seg.Start)
			}
			if lead := tc.start - seg.Start; lead < 0 || lead >= seg.Duration {
				t.Errorf("segment %+v does not cover %.1f s", seg, tc.start)
			}
			// The fake logs its arguments when it starts, which is when Create returns or a moment after.
			var args string
			for deadline := time.Now().Add(10 * time.Second); args == "" && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
				args = rig.starts()[0]
			}
			if !strings.Contains(args, "-start_number "+tc.wantStart+" ") {
				t.Errorf("ffmpeg ran with %q, want -start_number %s", args, tc.wantStart)
			}

			pl, err := s.Playlist(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(pl, "#EXT-X-START:TIME-OFFSET=61.500,PRECISE=YES\n"); got != tc.wantTag {
				t.Errorf("playlist has the start tag = %v, want %v:\n%s", got, tc.wantTag, pl)
			}
			if !tc.wantTag && strings.Contains(pl, "EXT-X-START") {
				t.Errorf("a session with no start has a start tag:\n%s", pl)
			}
		})
	}
}
