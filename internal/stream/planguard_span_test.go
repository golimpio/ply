package stream

import (
	"os"
	"path/filepath"
	"testing"
)

// The start of a fragment is held to 0.25 s of where the plan has its GOP, and to
// a third of the GOP for one shorter than 0.75 s.
func TestStartDisagrees(t *testing.T) {
	long := fragWant{start: 10, span: 4, haveSpan: true}
	short := fragWant{start: 10, span: 0.6, haveSpan: true} // a third of it is 0.2 s
	last := fragWant{start: 10}                             // the last GOP: nothing after it to measure it by

	for _, c := range []struct {
		name  string
		start float64
		want  fragWant
		bad   bool
	}{
		{"on time", 10, long, false},
		{"0.24 s late", 10.24, long, false},
		{"0.24 s early", 9.76, long, false},
		{"0.26 s late", 10.26, long, true},
		{"0.26 s early", 9.74, long, true},
		{"a long way off", 14, long, true},

		{"short GOP, 0.19 s late", 10.19, short, false},
		{"short GOP, 0.19 s early", 9.81, short, false},
		{"short GOP, 0.21 s late", 10.21, short, true},
		{"short GOP, 0.21 s early", 9.79, short, true},

		{"last GOP, 0.24 s late", 10.24, last, false},
		{"last GOP, 0.26 s late", 10.26, last, true},
		{"last GOP, 0.26 s early", 9.74, last, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if why := startDisagrees(c.start, c.want); (why != "") != c.bad {
				t.Errorf("startDisagrees(%v, %+v) = %q, want disagreement: %v", c.start, c.want, why, c.bad)
			}
		})
	}
}

// A duration can only confirm: it agrees when it is the plan's GOP within the
// tolerance, and says nothing otherwise, or where either side has none.
func TestDurationAgrees(t *testing.T) {
	long := fragWant{start: 10, span: 4, haveSpan: true}
	short := fragWant{start: 10, span: 0.6, haveSpan: true}
	last := fragWant{start: 10}
	lasting := func(dur float64) fragSpan { return fragSpan{start: 10, dur: dur, haveDur: true} }

	for _, c := range []struct {
		name string
		got  fragSpan
		want fragWant
		ok   bool
	}{
		{"the GOP", lasting(4), long, true},
		{"0.24 s over", lasting(4.24), long, true},
		{"0.24 s under", lasting(3.76), long, true},
		{"0.26 s over", lasting(4.26), long, false},
		{"0.26 s under", lasting(3.74), long, false},
		{"half", lasting(2), long, false},
		{"twice", lasting(8), long, false},
		{"short GOP, 0.19 s over", lasting(0.79), short, true},
		{"short GOP, 0.21 s over", lasting(0.81), short, false},
		{"short GOP, 0.21 s under", lasting(0.39), short, false},
		{"no duration in the fragment", fragSpan{start: 10, dur: 4}, long, false},
		{"no GOP after it", lasting(4), last, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := durationAgrees(c.got, c.want); got != c.ok {
				t.Errorf("durationAgrees(%+v, %+v) = %v, want %v", c.got, c.want, got, c.ok)
			}
		})
	}
}

// What the fragment file says of the video: when it starts and how long it
// lasts, from the trun or the default of the tfhd.
func TestFragmentVideoSpan(t *testing.T) {
	video := fragVideo{track: 2, scale: 1000}
	tfhd := func(flags uint32, fields ...[]byte) []byte {
		return isoBox("tfhd", append([][]byte{{0, byte(flags >> 16), byte(flags >> 8), byte(flags)}, u32(2)}, fields...)...)
	}
	trun := func(flags, count uint32, fields ...[]byte) []byte {
		return isoBox("trun", append([][]byte{{0, byte(flags >> 16), byte(flags >> 8), byte(flags)}, u32(count)}, fields...)...)
	}
	file := func(head, run []byte) []byte {
		return cat(isoBox("moof", isoFull("mfhd", 0, u32(1)),
			isoBox("traf", isoFull("tfhd", 0, u32(1)), isoFull("tfdt", 1, u64(999999)), isoFull("trun", 0, u32(1))), // audio
			isoBox("traf", head, isoFull("tfdt", 1, u64(4000)), run)), isoBox("mdat", make([]byte, 50)))
	}
	for _, c := range []struct {
		name    string
		file    []byte
		start   float64
		dur     float64
		haveDur bool
	}{
		{"durations of the samples", file(tfhd(0), trun(0x100, 3, u32(500), u32(500), u32(1000))), 4, 2, true},
		{"durations among other fields", file(tfhd(0), trun(0x100|0x200|0x800, 2, u32(100), u32(9999), u32(40), u32(200), u32(9999), u32(0))), 4.04, 0.3, true},
		{"the default duration", file(tfhd(0x8, u32(40)), trun(0, 5)), 4, 0.2, true},
		{"the default after a base offset and a description", file(tfhd(0x1|0x2|0x8, u64(0), u32(1), u32(40)), trun(0, 5)), 4, 0.2, true},
		{"durations win over the default", file(tfhd(0x8, u32(40)), trun(0x100, 2, u32(100), u32(300))), 4, 0.4, true},
		{"after the data offset and first sample flags", file(tfhd(0), trun(0x1|0x4|0x100, 2, u32(0), u32(0), u32(250), u32(250))), 4, 0.5, true},
		{"samples cut off", file(tfhd(0), trun(0x100, 3, u32(500), u32(500))), 4, 0, false},
		{"no duration anywhere", file(tfhd(0), trun(0, 3)), 4, 0, false},
		{"no samples", file(tfhd(0x8, u32(40)), trun(0x100, 0)), 4, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "gop.m4s")
			if err := os.WriteFile(p, c.file, 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := fragmentVideoSpan(p, video)
			if err != nil {
				t.Fatal(err)
			}
			if d := got.start - c.start; d > 1e-9 || d < -1e-9 {
				t.Errorf("starts at %v, want %v", got.start, c.start)
			}
			if got.haveDur != c.haveDur {
				t.Fatalf("haveDur = %v, want %v", got.haveDur, c.haveDur)
			}
			if d := got.dur - c.dur; c.haveDur && (d > 1e-9 || d < -1e-9) {
				t.Errorf("lasts %v, want %v", got.dur, c.dur)
			}
		})
	}
}
