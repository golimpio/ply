package stream

import (
	"context"
	"encoding/binary"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// encodeFloats packs keyframe times the way FuzzNewPlan reads them.
func encodeFloats(v ...float64) []byte {
	b := make([]byte, 8*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint64(b[i*8:], math.Float64bits(f))
	}
	return b
}

// bounded keeps a finite value inside +-1e7 s (115 days: longer than any
// media file, and where float64 still resolves milliseconds) and leaves NaN
// and Inf alone, so the plan's handling of those is exercised too.
func bounded(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	return math.Mod(v, 1e7)
}

// FuzzNewPlan builds plans and playlists from arbitrary keyframe arrays. Every
// plan must tile the file: contiguous GOP ranges, strictly increasing starts,
// positive durations that add up to the total.
func FuzzNewPlan(f *testing.F) {
	nan, inf := math.NaN(), math.Inf(1)
	f.Add(encodeFloats(evenKeyframes(15, 2)...), 30.0, 6.0)
	f.Add(encodeFloats(0, 2, 4, 6.5, 8, 12.4, 14, 18.6, 20, 25, 28), 30.0, 6.0)
	f.Add(encodeFloats(4, 0, 2, 2.0004, -0.0003, 6, 8, 10, 12), 14.0, 6.0) // unsorted, duplicated, negative
	f.Add(encodeFloats(1.4, 3.4, 5.4, 7.4, 9.4, 11.4, 13.4), 15.0, 6.0)    // first keyframe after 0
	f.Add(encodeFloats(0, 10, 20), 30.0, 6.0)                              // GOPs longer than a segment
	f.Add(encodeFloats(0, 5.5, 11, 16.5, 22), 30.0, 6.0)                   // GOPs just under the minimum
	f.Add(encodeFloats(evenKeyframes(10, 2)...), 0.0, 6.0)                 // duration missing
	f.Add(encodeFloats(0), 100.0, 6.0)
	f.Add(encodeFloats(0), 0.0004, 6.0) // a single keyframe and almost no duration
	f.Add(encodeFloats(0, 20), 20.0004, 6.0)
	f.Add(encodeFloats(0, 7, 13.0004), 13.0005, 6.0) // the tail would be a sliver
	f.Add(encodeFloats(nan, inf, -inf, 0, 6, 12), 18.0, 6.0)
	f.Add(encodeFloats(nan, inf), 10.0, 6.0) // nothing usable
	f.Add(encodeFloats(), 10.0, 6.0)
	f.Add(encodeFloats(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10), 11.0, 0.0)  // default minimum
	f.Add(encodeFloats(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10), 11.0, -3.0) // ditto
	f.Add(encodeFloats(0, 0.001, 0.002, 0.0031), 1.0, 1e-300)
	f.Add(encodeFloats(0, 3600, 7200, 1e7-1), 1e7, 6.0)
	f.Add([]byte{1, 2, 3}, 10.0, 6.0) // not even one float

	f.Fuzz(func(t *testing.T, data []byte, duration, minSeg float64) {
		if math.IsNaN(duration) || math.IsInf(duration, 0) || math.IsNaN(minSeg) || math.IsInf(minSeg, 0) {
			t.Skip("durations come from the database and the configuration: always finite")
		}
		duration, minSeg = bounded(duration), bounded(minSeg)
		var kf []float64
		usable := 0
		for i := 0; i+8 <= len(data) && len(kf) < 4096; i += 8 {
			v := bounded(math.Float64frombits(binary.LittleEndian.Uint64(data[i:])))
			kf = append(kf, v)
			if !math.IsNaN(v) && !math.IsInf(v, 0) {
				usable++
			}
		}

		p, err := NewPlan(kf, duration, minSeg)
		if usable == 0 {
			if err == nil {
				t.Fatalf("NewPlan(%v) succeeded without a usable keyframe", kf)
			}
			return
		}
		if err != nil {
			t.Fatalf("NewPlan(%v, %v, %v): %v", kf, duration, minSeg, err)
		}
		checkPlanInvariants(t, p, minSeg)
		checkPlaylist(t, p)
	})
}

func checkPlanInvariants(t *testing.T, p Plan, minSeg float64) {
	t.Helper()
	if minSeg <= 0 {
		minSeg = DefaultSegmentSec
	}
	tol := 1e-9 * math.Max(1, p.Duration)

	for i, k := range p.Keyframes {
		if math.IsNaN(k) || math.IsInf(k, 0) || k < 0 {
			t.Fatalf("keyframe %d = %v", i, k)
		}
		if i > 0 && k-p.Keyframes[i-1] < 0.001 {
			t.Fatalf("keyframes %d and %d are %v apart", i-1, i, k-p.Keyframes[i-1])
		}
	}
	if last := p.Keyframes[len(p.Keyframes)-1]; !(p.Duration > last) {
		t.Fatalf("duration %v does not extend past the last keyframe %v", p.Duration, last)
	}
	if len(p.Segments) == 0 {
		t.Fatal("no segments")
	}

	next, total := 0, 0.0
	for i, s := range p.Segments {
		if s.First != next || s.End <= s.First || s.End > len(p.Keyframes) {
			t.Fatalf("segment %d covers GOPs [%d,%d), want to start at %d (of %d)", i, s.First, s.End, next, len(p.Keyframes))
		}
		next = s.End
		if !(s.Duration > 0) {
			t.Fatalf("segment %d has duration %v (%+v)", i, s.Duration, s)
		}
		switch {
		case i == 0 && s.Start != 0:
			t.Fatalf("the first segment starts at %v", s.Start)
		case i > 0 && s.Start != p.Keyframes[s.First]:
			t.Fatalf("segment %d starts at %v, not on its keyframe %v", i, s.Start, p.Keyframes[s.First])
		case i > 0 && !(s.Start > p.Segments[i-1].Start):
			t.Fatalf("segment starts do not increase: %v then %v", p.Segments[i-1].Start, s.Start)
		}
		if i > 0 && math.Abs(p.Segments[i-1].Start+p.Segments[i-1].Duration-s.Start) > tol {
			t.Fatalf("gap or overlap before segment %d", i)
		}
		if i < len(p.Segments)-1 && s.Duration < minSeg-0.0005-tol {
			t.Fatalf("segment %d is %v s, shorter than %v", i, s.Duration, minSeg)
		}
		total += s.Duration
	}
	if next != len(p.Keyframes) {
		t.Fatalf("segments cover %d of %d GOPs", next, len(p.Keyframes))
	}
	if math.Abs(total-p.Duration) > tol {
		t.Fatalf("durations add up to %v, want %v", total, p.Duration)
	}
	if n := len(p.Segments); n > 1 && p.Segments[n-1].Duration < minSeg/2-tol {
		t.Fatalf("the last segment is a %v s sliver (minimum %v)", p.Segments[n-1].Duration, minSeg)
	}

	// The lookups agree with the layout.
	for i, s := range p.Segments {
		if got := p.SegmentAt(s.Start); got != i {
			t.Fatalf("SegmentAt(%v) = %d, want %d", s.Start, got, i)
		}
		if p.SegmentOfGOP(s.First) != i || p.SegmentOfGOP(s.End-1) != i {
			t.Fatalf("SegmentOfGOP disagrees with segment %d %+v", i, s)
		}
	}
	prev := -1.0
	for g := 0; g <= len(p.Keyframes); g++ {
		if tm := p.TimeOf(g); tm < prev {
			t.Fatalf("TimeOf(%d) = %v after %v", g, tm, prev)
		} else {
			prev = tm
		}
	}
	if p.TimeOf(len(p.Keyframes)) != p.Duration {
		t.Fatalf("TimeOf(end) = %v, want the duration %v", p.TimeOf(len(p.Keyframes)), p.Duration)
	}
}

var (
	fuzzTarget  = regexp.MustCompile(`#EXT-X-TARGETDURATION:(\d+)\n`)
	fuzzExtinf  = regexp.MustCompile(`#EXTINF:(\d+\.\d{3}),\n(\S+)\n`)
	fuzzMapLine = regexp.MustCompile(`#EXT-X-MAP:URI="([^"]+)"\n`)
)

// checkPlaylist parses the rendered playlist back and compares it with the plan.
func checkPlaylist(t *testing.T, p Plan) {
	t.Helper()
	const suffix = "?token=a%26b"
	pl := p.Playlist(suffix)
	if !strings.HasPrefix(pl, "#EXTM3U\n") || !strings.HasSuffix(pl, "#EXT-X-ENDLIST\n") ||
		!strings.Contains(pl, "#EXT-X-PLAYLIST-TYPE:VOD\n") {
		t.Fatalf("not a complete VOD playlist:\n%.300s", pl)
	}
	if m := fuzzMapLine.FindStringSubmatch(pl); m == nil || m[1] != InitName+suffix {
		t.Fatalf("init map: %v", m)
	}
	target, _ := strconv.Atoi(fuzzTarget.FindStringSubmatch(pl)[1])

	entries := fuzzExtinf.FindAllStringSubmatch(pl, -1)
	if len(entries) != len(p.Segments) {
		t.Fatalf("%d EXTINF entries for %d segments", len(entries), len(p.Segments))
	}
	var sum float64
	for i, e := range entries {
		d, _ := strconv.ParseFloat(e[1], 64)
		if !(d > 0) {
			t.Fatalf("segment %d is listed with duration %s (planned %v)", i, e[1], p.Segments[i].Duration)
		}
		if math.Round(d) > float64(target) {
			t.Fatalf("segment %d lasts %s, past the target duration %d", i, e[1], target)
		}
		if e[2] != SegmentName(i)+suffix || !ValidName(SegmentName(i)) {
			t.Fatalf("entry %d names %q", i, e[2])
		}
		sum += d
	}
	// Each entry is rounded to a millisecond.
	if math.Abs(sum-p.Duration) > 0.0005*float64(len(entries))+1e-9*math.Max(1, p.Duration) {
		t.Fatalf("EXTINF values add up to %v, plan says %v", sum, p.Duration)
	}
	if strings.Count(pl, suffix) != len(entries)+1 {
		t.Fatalf("token suffix appears %d times for %d URIs", strings.Count(pl, suffix), len(entries)+1)
	}
}

// FuzzValidName: the only names a session serves are init.mp4 and segNNNNN.m4s
// (ASCII digits). Nothing with a path separator, a dot-dot, whitespace or a
// control character may pass, and what passes must parse as what it claims.
func FuzzValidName(f *testing.F) {
	for _, s := range []string{
		"init.mp4", "seg00001.m4s", "seg99999.m4s", "../x", "seg1.m4s", "", "..", ".", "/", "init.mp4/",
		"seg00000.m4s/../init.mp4", "..\\init.mp4", "init.mp4\n", "init.mp4\x00", "seg0000\u0660.m4s", // Arabic-Indic zero
		"initXmp4", "seg00000Xm4s", "segX0000.m4s", "seg00000.m4sX",
		"seg00000.m4s ", " init.mp4", "INIT.MP4", "seg00000.M4S", "seg000000.m4s", "init.mp4.mp4", "%2e%2e%2finit.mp4",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !ValidName(s) {
			if _, ok := ParseSegmentName(s); ok {
				t.Fatalf("ParseSegmentName accepted %q, which ValidName rejects", s)
			}
			return
		}
		if strings.ContainsAny(s, "/\\\x00\n\r \t%?#") || strings.Contains(s, "..") || strings.Count(s, ".") != 1 {
			t.Fatalf("ValidName accepted %q", s)
		}
		if s == InitName {
			return
		}
		n, ok := ParseSegmentName(s)
		if !ok || SegmentName(n) != s || len(s) != len("seg00000.m4s") {
			t.Fatalf("ValidName accepted %q but it parses as %d, %v", s, n, ok)
		}
	})
}

// tinyMKV is a small Matroska file for fuzz seeds.
func tinyMKV(mod func(*mkvFile)) []byte {
	f := goodMKV(nil)
	f.clusters, f.cues = f.clusters[:5], f.cues[:5]
	for i := range f.clusters {
		for j := range f.clusters[i].blocks {
			f.clusters[i].blocks[j].size = 20
		}
	}
	f.duration = 10000
	if mod != nil {
		mod(&f)
	}
	return f.build()
}

// tinyMP4 is a small MP4 file for fuzz seeds.
func tinyMP4(mod func(v, a *mp4Trak)) []byte {
	return goodMP4(func(v, a *mp4Trak) {
		v.stts, v.stss, v.table = [][2]uint32{{240, 512}}, []uint32{1, 49, 97, 145, 193}, 0
		a.stts, a.table = [][2]uint32{{470, 1024}}, 0
		if mod != nil {
			mod(v, a)
		}
	})
}

// fuzzIndexParser runs a keyframe index parser over data, then over a file of
// pad*4096 more bytes that repeat it (a file the size of a film, that is only
// ever read through the counting reader). Whatever the bytes, it must return,
// stay inside the read limits, and give either an error or a usable index.
func fuzzIndexParser(t *testing.T, data []byte, pad uint32, parse func(context.Context, *indexReader, int) ([]float64, error)) {
	t.Helper()
	for _, size := range []int64{int64(len(data)), int64(len(data)) + int64(pad)<<12} {
		cr := &countingReaderAt{r: patternReader{prefix: data, pattern: data, size: size}}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		kf, err := parse(ctx, &indexReader{r: cr, size: size}, -1)
		cancel()
		if cr.bytes > indexByteBudget || cr.reads > indexReadBudget {
			t.Fatalf("read %d bytes in %d reads (limits %d, %d)", cr.bytes, cr.reads, indexByteBudget, indexReadBudget)
		}
		if err != nil {
			continue
		}
		if len(kf) == 0 || len(kf) > maxIndexKeyframes {
			t.Fatalf("%d keyframes without an error", len(kf))
		}
		for i, k := range kf {
			if math.IsNaN(k) || math.IsInf(k, 0) || k < 0 || i > 0 && k-kf[i-1] < 0.001 {
				t.Fatalf("keyframe %d = %v after %v: not clean, ascending times", i, k, head(kf[max(i-1, 0):]))
			}
		}
	}
}

// FuzzMatroskaKeyframes: no input makes the Matroska index reader panic, hang,
// or read past its limits, however large the file claims to be.
func FuzzMatroskaKeyframes(f *testing.F) {
	for _, mod := range []func(*mkvFile){
		nil,
		func(f *mkvFile) { f.seek = false },
		func(f *mkvFile) { f.seek, f.cuesFirst = false, true },
		func(f *mkvFile) { f.tail = true },
		func(f *mkvFile) { f.seek2 = true },
		func(f *mkvFile) { f.unknownSegment, f.unknownCues, f.tagsAfterCues = true, true, true },
		func(f *mkvFile) { f.clusters[0].unknown = true },
		func(f *mkvFile) { f.clusters[0].blocks[0].group = true; shifted(1400)(f) },
		ticks(500_000),
		func(f *mkvFile) { f.segSize = []byte{0x01, 0x80, 0, 0, 0, 0, 0, 0} },
		func(f *mkvFile) { f.cuesSize = []byte{0x01, 0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFE} },
		func(f *mkvFile) { f.cuesRaw = cat(ebmlIDBytes(idCuePoint), ebmlSizeBytes(1<<40)) },
		func(f *mkvFile) { f.infoBody = elUint(idTimestampScale, 0) },
	} {
		data := tinyMKV(mod)
		f.Add(data, uint32(0))
		f.Add(data, uint32(1<<20)) // a file of 4 GiB made of it
	}
	f.Add(tinyMKV(nil)[:60], uint32(0))
	f.Add([]byte{0x1A, 0x45, 0xDF, 0xA3, 0x80}, uint32(7))
	f.Add([]byte{}, uint32(0))
	f.Fuzz(func(t *testing.T, data []byte, pad uint32) {
		fuzzIndexParser(t, data, pad, matroskaKeyframes)
	})
}

// FuzzMP4Keyframes: the same for the MP4/MOV index reader.
func FuzzMP4Keyframes(f *testing.F) {
	big := make([]byte, 600)
	for _, data := range [][]byte{
		tinyMP4(nil),
		tinyMP4(func(v, a *mp4Trak) { v.noStss = true }),
		tinyMP4(func(v, a *mp4Trak) { v.co64 = true }),
		tinyMP4(func(v, a *mp4Trak) { bframes(v); v.edits = []mp4Edit{{10000, 1024, 0}} }),
		tinyMP4(func(v, a *mp4Trak) { v.edits = []mp4Edit{{500, -1, 0}, {10000, 0, 0}}; v.editVer, v.mdhdVer = 1, 1 }),
		tinyMP4(func(v, a *mp4Trak) { v.sttsRaw = cat(u32(0xFFFFFFFF), u32(1), u32(512)) }),
		tinyMP4(func(v, a *mp4Trak) { v.stssRaw = cat(u32(0xFFFFFFFF), u32(1)) }),
		cat(ftyp, isoBox64("mdat", big), isoBox64("moov", moovOf(1000, nil, goodVideo())[8:])),
		cat(ftyp, isoBox("moof"), isoBox("free")),
		cat(ftyp, u32(1), []byte("moov"), u64(1<<62)),
		cat(ftyp, u32(0xFFFFFFFF), []byte("moov")),
	} {
		f.Add(data, uint32(0))
		f.Add(data, uint32(1<<20))
	}
	f.Add(tinyMP4(nil)[:100], uint32(0))
	f.Add([]byte("\x00\x00\x00\x08ftyp"), uint32(3))
	f.Fuzz(func(t *testing.T, data []byte, pad uint32) {
		fuzzIndexParser(t, data, pad, mp4Keyframes)
	})
}
