package stream

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// ---- byte-level ISO BMFF builders ----------------------------------------

func u16(v uint16) []byte { return []byte{byte(v >> 8), byte(v)} }
func u32(v uint32) []byte { return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)} }
func i32(v int32) []byte  { return u32(uint32(v)) }
func u64(v uint64) []byte { return append(u32(uint32(v>>32)), u32(uint32(v))...) }

func isoBox(typ string, body ...[]byte) []byte {
	b := cat(body...)
	return cat(u32(uint32(8+len(b))), []byte(typ), b)
}

// box64 writes a box with the 64-bit size field.
func isoBox64(typ string, body ...[]byte) []byte {
	b := cat(body...)
	return cat(u32(1), []byte(typ), u64(uint64(16+len(b))), b)
}

func isoFull(typ string, version byte, body ...[]byte) []byte {
	return isoBox(typ, append([][]byte{{version, 0, 0, 0}}, body...)...)
}

type mp4Edit struct {
	duration uint64 // in movie ticks
	media    int64  // -1: an empty edit
	rate     uint32 // 0: normal speed
}

type mp4Trak struct {
	handler string
	scale   uint32
	stts    [][2]uint32 // count, delta
	ctts    [][2]int32  // count, offset
	cttsVer byte
	stss    []uint32
	noStss  bool
	co64    bool
	edits   []mp4Edit
	editVer byte
	mdhdVer byte
	table   int // bytes of stsz, as large as a real one

	// Damage: payloads used as they are, count included.
	sttsRaw, stssRaw, cttsRaw []byte
}

func (t mp4Trak) bytes() []byte {
	table := func(typ string, ver byte, raw []byte, count int, entries func(i int) []byte) []byte {
		if raw != nil {
			return isoFull(typ, ver, raw)
		}
		parts := [][]byte{u32(uint32(count))}
		for i := range count {
			parts = append(parts, entries(i))
		}
		return isoFull(typ, ver, parts...)
	}
	stbl := [][]byte{
		isoFull("stsd", 0, u32(0)),
		table("stts", 0, t.sttsRaw, len(t.stts), func(i int) []byte { return cat(u32(t.stts[i][0]), u32(t.stts[i][1])) }),
	}
	if !t.noStss {
		stbl = append(stbl, table("stss", 0, t.stssRaw, len(t.stss), func(i int) []byte { return u32(t.stss[i]) }))
	}
	if t.ctts != nil || t.cttsRaw != nil {
		stbl = append(stbl, table("ctts", t.cttsVer, t.cttsRaw, len(t.ctts), func(i int) []byte { return cat(u32(uint32(t.ctts[i][0])), i32(t.ctts[i][1])) }))
	}
	stbl = append(stbl, isoFull("stsc", 0, u32(0)), isoFull("stsz", 0, u32(0), u32(uint32(t.table/4)), make([]byte, t.table)))
	if t.co64 {
		stbl = append(stbl, isoFull("co64", 0, u32(1), u64(1<<40)))
	} else {
		stbl = append(stbl, isoFull("stco", 0, u32(1), u32(48)))
	}

	mdhd := isoFull("mdhd", 0, u32(0), u32(0), u32(t.scale), u32(0), u16(0), u16(0))
	if t.mdhdVer == 1 {
		mdhd = isoFull("mdhd", 1, u64(0), u64(0), u32(t.scale), u64(0), u16(0), u16(0))
	}
	hdlr := isoFull("hdlr", 0, u32(0), []byte(t.handler), make([]byte, 12), []byte{0})
	minf := isoBox("minf", isoFull("vmhd", 0, make([]byte, 8)), isoBox("dinf"), isoBox("stbl", stbl...))
	parts := [][]byte{isoFull("tkhd", 0, make([]byte, 80))}
	if len(t.edits) > 0 {
		entries := [][]byte{u32(uint32(len(t.edits)))}
		for _, e := range t.edits {
			rate := e.rate
			if rate == 0 {
				rate = 1 << 16
			}
			if t.editVer == 1 {
				entries = append(entries, u64(e.duration), u64(uint64(e.media)), u32(rate))
			} else {
				entries = append(entries, u32(uint32(e.duration)), i32(int32(e.media)), u32(rate))
			}
		}
		parts = append(parts, isoBox("edts", isoFull("elst", t.editVer, entries...)))
	}
	return isoBox("trak", append(parts, isoBox("mdia", mdhd, hdlr, minf))...)
}

func moovOf(movieScale uint32, extra [][]byte, traks ...mp4Trak) []byte {
	parts := [][]byte{isoFull("mvhd", 0, u32(0), u32(0), u32(movieScale), u32(0), make([]byte, 80))}
	for _, t := range traks {
		parts = append(parts, t.bytes())
	}
	return isoBox("moov", append(parts, extra...)...)
}

var ftyp = isoBox("ftyp", []byte("isom"), u32(512), []byte("isomiso2avc1mp41"))

// a video track with 60 s at 24 fps and a keyframe every 2 s (sample 1, 49,
// ...) in a 12288 Hz timescale, and a matching audio track.
func goodVideo() mp4Trak {
	var syncs []uint32
	for k := range 30 {
		syncs = append(syncs, uint32(k*48+1))
	}
	return mp4Trak{handler: "vide", scale: 12288, stts: [][2]uint32{{1440, 512}}, stss: syncs, table: 4 * 1440}
}

func goodAudio() mp4Trak {
	return mp4Trak{handler: "soun", scale: 48000, stts: [][2]uint32{{2813, 1024}}, noStss: true, table: 4 * 2813}
}

// goodMP4 is faststart: ftyp, moov, mdat.
func goodMP4(mod func(v, a *mp4Trak)) []byte {
	v, a := goodVideo(), goodAudio()
	if mod != nil {
		mod(&v, &a)
	}
	return cat(ftyp, moovOf(1000, nil, v, a), isoBox("mdat", make([]byte, 1000)))
}

// bframes is the composition offsets x264 gives a GOP of 48 with B-frames: the
// keyframe is shown two frames after it is decoded.
func bframes(v *mp4Trak) {
	v.ctts = nil
	for range 30 {
		v.ctts = append(v.ctts, [2]int32{1, 1024}, [2]int32{47, 512})
	}
}

func TestMP4SampleTables(t *testing.T) {
	const frame = 512.0 / 12288
	for _, c := range []struct {
		name string
		mod  func(v, a *mp4Trak)
		want []float64
	}{
		{"stss and stts", nil, every(30, 0, 2)},
		{"sync samples count from 1", func(v, a *mp4Trak) {
			v.scale, v.stts, v.stss = 1000, [][2]uint32{{12, 1000}}, []uint32{1, 5, 9}
		}, []float64{0, 4, 8}},
		{"durations change between runs", func(v, a *mp4Trak) {
			v.scale, v.stts, v.stss = 100, [][2]uint32{{3, 100}, {2, 300}, {5, 200}}, []uint32{1, 4, 5, 6, 10}
		}, []float64{0, 3, 6, 9, 17}},
		{"no stss: every sample is a sync sample", func(v, a *mp4Trak) {
			v.noStss, v.stts = true, [][2]uint32{{10, 512}}
		}, every(10, 0, frame)},
		{"co64", func(v, a *mp4Trak) { v.co64, a.co64 = true, true }, every(30, 0, 2)},
		{"64-bit media header", func(v, a *mp4Trak) { v.mdhdVer = 1 }, every(30, 0, 2)},

		// Composition offsets: a keyframe is shown after it is decoded.
		{"ctts, no edit list: shown two frames late", func(v, a *mp4Trak) { bframes(v) }, every(30, 2*frame, 2)},
		{"ctts of the first sample is the start when the others begin later", func(v, a *mp4Trak) {
			bframes(v)
			a.edits = []mp4Edit{{1000, -1, 0}, {60000, 0, 0}}
		}, every(30, 0, 2)},
		{"ctts and the edit list that undoes it", func(v, a *mp4Trak) {
			bframes(v)
			v.edits = []mp4Edit{{60000, 1024, 0}}
		}, every(30, 0, 2)},
		{"ctts version 1, negative offsets elsewhere", func(v, a *mp4Trak) {
			v.cttsVer = 1
			v.ctts = nil
			for range 30 {
				v.ctts = append(v.ctts, [2]int32{1, 1024}, [2]int32{47, -512})
			}
		}, every(30, 2*frame, 2)},

		// Edit lists: delays move the video against the other tracks.
		{"video delayed by 0.5 s", func(v, a *mp4Trak) {
			v.edits = []mp4Edit{{500, -1, 0}, {60000, 0, 0}}
		}, every(30, 0.5, 2)},
		{"both delayed by 0.5 s", func(v, a *mp4Trak) {
			v.edits = []mp4Edit{{500, -1, 0}, {60000, 0, 0}}
			a.edits = []mp4Edit{{500, -1, 0}, {60000, 0, 0}}
		}, every(30, 0, 2)},
		{"audio priming alone does not move the start", func(v, a *mp4Trak) {
			a.edits = []mp4Edit{{60000, 1024, 0}}
		}, every(30, 0, 2)},
		{"audio priming after a delay does (ffmpeg -output_ts_offset 1.4)", func(v, a *mp4Trak) {
			v.edits = []mp4Edit{{1400, -1, 0}, {60000, 0, 0}}
			a.edits = []mp4Edit{{1400, -1, 0}, {60000, 1024, 0}}
		}, every(30, 1024.0/48000, 2)},
		{"version 1 edit list", func(v, a *mp4Trak) {
			v.editVer = 1
			v.edits = []mp4Edit{{500, -1, 0}, {60000, 0, 0}}
		}, every(30, 0.5, 2)},
		{"edit of unstated duration", func(v, a *mp4Trak) { v.edits = []mp4Edit{{0, 0, 0}} }, every(30, 0, 2)},

		// An edit that ends before the media does: the keyframes it still covers
		// are all there is to list (the last is at 58 s).
		{"an edit that ends after the last keyframe", func(v, a *mp4Trak) { v.edits = []mp4Edit{{58500, 0, 0}} }, every(30, 0, 2)},
		{"an edit that ends just after the last keyframe", func(v, a *mp4Trak) { v.edits = []mp4Edit{{58002, 0, 0}} }, every(30, 0, 2)},
		{"the same after a delay", func(v, a *mp4Trak) { v.edits = []mp4Edit{{500, -1, 0}, {58500, 0, 0}} }, every(30, 0.5, 2)},
		{"a track without samples does not set the start", func(v, a *mp4Trak) {
			v.edits = []mp4Edit{{500, -1, 0}, {60000, 0, 0}}
			a.stts = nil // were it counted, it would start at 0 and the video at 0.5
		}, every(30, 0, 2)},
	} {
		t.Run(c.name, func(t *testing.T) {
			kf, source, err, _ := indexOf(goodMP4(c.mod), -1)
			if err != nil {
				t.Fatal(err)
			}
			if source != sourceSampleTable {
				t.Errorf("source %q", source)
			}
			wantTimes(t, kf, c.want...)
		})
	}
}

func TestMP4Layouts(t *testing.T) {
	v, a := goodVideo(), goodAudio()
	moov := moovOf(1000, nil, v, a)
	mdat := make([]byte, 8<<20)
	for _, c := range []struct {
		name string
		file []byte
	}{
		{"faststart", cat(ftyp, moov, isoBox("mdat", mdat))},
		{"moov at the end", cat(ftyp, isoBox("mdat", mdat), moov)},
		{"moov at the end, 64-bit sizes", cat(ftyp, isoBox64("mdat", mdat), isoBox64("moov", moov[8:]))},
		{"free space around", cat(ftyp, isoBox("free", make([]byte, 5000)), isoBox("wide"), isoBox64("mdat", mdat), isoBox("free"), moov)},
		{"audio first", cat(ftyp, moovOf(1000, nil, a, v), isoBox("mdat", mdat))},
		{"moov is the last thing, mdat runs to it", cat(ftyp, isoBox("mdat", mdat), isoBox("skip", make([]byte, 100)), moov)},
	} {
		t.Run(c.name, func(t *testing.T) {
			index := -1
			if c.name == "audio first" {
				index = 1
			}
			kf, _, err, cr := indexOf(c.file, index)
			if err != nil {
				t.Fatal(err)
			}
			wantTimes(t, kf, every(30, 0, 2)...)
			if cr.bytes*20 > int64(len(c.file)) || cr.reads > 6 {
				t.Errorf("read %d bytes in %d reads of a %d byte file, want under 5%% and 6 reads", cr.bytes, cr.reads, len(c.file))
			}
		})
	}

	// The stream index selects the trak.
	two := cat(ftyp, moovOf(1000, nil, a, v), isoBox("mdat", mdat[:100]))
	for index, ok := range map[int]bool{-1: true, 0: false, 1: true, 2: false, 7: false} {
		kf, _, err, _ := indexOf(two, index)
		if ok != (err == nil) {
			t.Errorf("stream %d: err = %v, want an index: %v", index, err, ok)
		} else if ok {
			wantTimes(t, kf, every(30, 0, 2)...)
		}
	}
}

func TestMP4CannotBeIndexed(t *testing.T) {
	bad := func(name string, file []byte, index ...int) {
		t.Helper()
		i := -1
		if len(index) > 0 {
			i = index[0]
		}
		kf, _, err, _ := indexOf(file, i)
		if err == nil {
			t.Errorf("%s: got an index %v", name, head(kf))
		}
	}
	mdat := isoBox("mdat", make([]byte, 100))
	if _, _, err, _ := indexOf(goodMP4(nil), -1); err != nil { // positive control
		t.Fatal(err)
	}

	bad("edit list of three", goodMP4(func(v, a *mp4Trak) { v.edits = []mp4Edit{{500, -1, 0}, {1000, 0, 0}, {58500, 1000, 0}} }))
	bad("edit at double speed", goodMP4(func(v, a *mp4Trak) { v.edits = []mp4Edit{{60000, 0, 2 << 16}} }))
	bad("an empty edit only", goodMP4(func(v, a *mp4Trak) { v.edits = []mp4Edit{{60000, -1, 0}} }))
	bad("an empty edit second", goodMP4(func(v, a *mp4Trak) { v.edits = []mp4Edit{{500, 0, 0}, {500, -1, 0}} }))
	bad("two edits, none empty", goodMP4(func(v, a *mp4Trak) { v.edits = []mp4Edit{{30000, 0, 0}, {30000, 0, 0}} }))
	bad("an edit that stops at 10 s of 60", goodMP4(func(v, a *mp4Trak) { v.edits = []mp4Edit{{10000, 0, 0}} }))
	// ffprobe lists a couple of keyframes at or after the end of an edit and
	// not the rest: when one is there, it is for the scan to say which.
	bad("an edit that ends at the last keyframe", goodMP4(func(v, a *mp4Trak) { v.edits = []mp4Edit{{58000, 0, 0}} }))
	bad("an edit that ends just before the last keyframe", goodMP4(func(v, a *mp4Trak) { v.edits = []mp4Edit{{57999, 0, 0}} }))
	bad("an edit that ends at the last keyframe, after a delay", goodMP4(func(v, a *mp4Trak) { v.edits = []mp4Edit{{500, -1, 0}, {58000, 0, 0}} }))
	bad("a complex edit list on the audio track", goodMP4(func(v, a *mp4Trak) { a.edits = []mp4Edit{{500, 0, 0}, {500, 0, 0}, {500, 0, 0}} }))
	bad("a keyframe before the start of the edit", goodMP4(func(v, a *mp4Trak) {
		bframes(v)
		v.edits = []mp4Edit{{60000, 2048, 0}}
	}))
	bad("no samples in the video track", goodMP4(func(v, a *mp4Trak) { v.stts = nil }))
	bad("no video track", cat(ftyp, moovOf(1000, nil, goodAudio()), mdat))
	bad("zero timescale", goodMP4(func(v, a *mp4Trak) { v.scale = 0 }))

	// Fragmented files carry their samples in moof boxes.
	frag := moovOf(1000, [][]byte{isoBox("mvex", isoFull("trex", 0, make([]byte, 20)))}, func() mp4Trak { v := goodVideo(); v.stts = nil; return v }())
	bad("fragmented, mvex", cat(ftyp, frag))
	bad("fragmented, mvex but samples in the moov", cat(ftyp, moovOf(1000, [][]byte{isoBox("mvex")}, goodVideo()), mdat))
	bad("fragmented, moof first", cat(ftyp, isoBox("moof", isoFull("mfhd", 0, u32(1))), mdat, moovOf(1000, nil, goodVideo())))

	// Sync samples that cannot be.
	for name, syncs := range map[string][]uint32{
		"descending": {5, 3}, "duplicate": {1, 1}, "zero": {0, 1}, "past the last sample": {1, 1441}, "far past": {1, 1 << 31},
	} {
		bad("stss "+name, goodMP4(func(v, a *mp4Trak) { v.stss = syncs }))
	}

	bad("no moov", cat(ftyp, mdat))
	bad("mdat to the end of the file", cat(ftyp, u32(0), []byte("mdat"), make([]byte, 100)))
	bad("not mp4", bytes.Repeat([]byte("ftypmoov"), 100))
}

func TestMP4Truncated(t *testing.T) {
	v, a := goodVideo(), goodAudio()
	moov := moovOf(1000, nil, v, a)
	for _, c := range []struct {
		name  string
		file  []byte
		whole int // the index is complete from this many bytes on
	}{
		{"faststart", cat(ftyp, moov, isoBox("mdat", make([]byte, 3000))), len(ftyp) + len(moov)},
		{"moov at the end", cat(ftyp, isoBox("mdat", make([]byte, 3000)), moov), 0},
	} {
		whole := c.whole
		if whole == 0 {
			whole = len(c.file)
		}
		for n := 0; n <= len(c.file); n++ {
			// Every length near the ends and near the point where the index
			// is complete, one in 31 elsewhere.
			if n > 400 && n < len(c.file)-400 && n%31 != 0 && (n < whole-400 || n > whole+400) {
				continue
			}
			kf, _, err, _ := indexOf(c.file[:n], -1)
			if n < whole && err == nil {
				t.Fatalf("%s: cut at %d of %d bytes gave an index %v", c.name, n, len(c.file), head(kf))
			}
			if n >= whole && err != nil {
				t.Fatalf("%s: cut at %d of %d bytes (the index is complete): %v", c.name, n, len(c.file), err)
			}
		}
	}
}

func TestMP4Hostile(t *testing.T) {
	mdat := isoBox("mdat", make([]byte, 100))
	good := goodMP4(nil)
	if _, _, err, _ := indexOf(good, -1); err != nil {
		t.Fatal(err)
	}
	tableAt := func(f func(v *mp4Trak)) []byte { return goodMP4(func(v, a *mp4Trak) { f(v) }) }

	for _, c := range []struct {
		name string
		file []byte
	}{
		{"box of 4 GiB", cat(ftyp, u32(0xFFFFFFFF), []byte("moov"), make([]byte, 100))},
		{"box of 2^62 bytes", cat(ftyp, u32(1), []byte("moov"), u64(1<<62), make([]byte, 100))},
		{"box of 2^64-1 bytes", cat(ftyp, u32(1), []byte("moov"), u64(1<<64-1), make([]byte, 100))},
		{"64-bit box smaller than its header", cat(ftyp, u32(1), []byte("moov"), u64(12), make([]byte, 100))},
		{"box of 4 bytes", cat(ftyp, u32(4), []byte("moov"), make([]byte, 100))},
		{"box of 0 bytes inside moov", cat(ftyp, isoBox("moov", u32(0), []byte("trak"), make([]byte, 100)))},
		{"trak larger than its moov", cat(ftyp, isoBox("moov", u32(1<<20), []byte("trak"), make([]byte, 100)), mdat)},
		{"stts of 2^32-1 entries", tableAt(func(v *mp4Trak) { v.sttsRaw = cat(u32(0xFFFFFFFF), u32(1), u32(512)) })},
		{"stss of 2^32-1 entries", tableAt(func(v *mp4Trak) { v.stssRaw = cat(u32(0xFFFFFFFF), u32(1)) })},
		{"ctts of 2^32-1 entries", tableAt(func(v *mp4Trak) { v.cttsRaw = cat(u32(0xFFFFFFFF), u32(1), u32(0)) })},
		{"stts of 33 MB", tableAt(func(v *mp4Trak) { v.sttsRaw = cat(u32(1), u32(1440), u32(512), make([]byte, 33<<20)) })},
		{"stss of 33 MB", tableAt(func(v *mp4Trak) { v.stssRaw = cat(u32(1), u32(1), make([]byte, 33<<20)) })},
		{"stts runs add up to 2^40 samples", tableAt(func(v *mp4Trak) {
			v.sttsRaw = cat(u32(3), u32(0x7FFFFFFF), u32(1), u32(0x7FFFFFFF), u32(1), u32(0x7FFFFFFF), u32(1))
		})},
		{"stts with huge deltas", tableAt(func(v *mp4Trak) { v.sttsRaw = cat(u32(1), u32(1440), u32(0xFFFFFFFF)) })},
		{"stts cut short", tableAt(func(v *mp4Trak) { v.sttsRaw = []byte{1} })},
		{"stss cut short", tableAt(func(v *mp4Trak) { v.stssRaw = []byte{0, 0} })},
		{"ctts cut short", tableAt(func(v *mp4Trak) { v.cttsRaw = []byte{0, 0, 0} })},
		{"elst of three entries", goodMP4(func(v, a *mp4Trak) { v.edits = make([]mp4Edit, 3) })},
		{"garbage", bytes.Repeat([]byte{0xFF, 'f', 't', 'y', 'p'}, 1000)},
		{"ftyp only", ftyp},
	} {
		t.Run(c.name, func(t *testing.T) {
			var err error
			var cr *countingReaderAt
			a := allocated(func() { _, _, err, cr = indexOf(c.file, -1) })
			if err == nil {
				t.Error("got an index")
			}
			if a > 1<<20 {
				t.Errorf("allocated %d bytes", a)
			}
			if cr.bytes > 40<<20 {
				t.Errorf("read %d bytes", cr.bytes)
			}
		})
	}
}

func TestMP4Limits(t *testing.T) {
	// A file of boxes that are each a megabyte: the walk reads a window of
	// each and stops at the byte limit.
	free := isoBox("free", make([]byte, 1<<20-8))
	const size = int64(100) << 30
	cr := &countingReaderAt{r: patternReader{prefix: ftyp, pattern: free, size: size}}
	_, _, err := indexKeyframes(context.Background(), cr, size, -1)
	wantLimit(t, err, "bytes")
	if cr.bytes > indexByteBudget {
		t.Errorf("read %d bytes, limit %d", cr.bytes, indexByteBudget)
	}

	// Thousands of boxes in one window cost one read, not thousands.
	many := isoBox("moov", bytes.Repeat(isoBox("free"), 8000))
	_, _, _, c2 := indexOf(cat(ftyp, many), -1)
	if c2.reads > 4 {
		t.Errorf("8000 small boxes took %d reads", c2.reads)
	}

	// Past the box limit the walk stops, however small the reads are.
	tiny := isoBox("moov", bytes.Repeat(isoBox("free"), maxIndexElements+1))
	_, _, err, _ = indexOf(cat(ftyp, tiny), -1)
	wantLimit(t, err, "elements")
}

// How many keyframes an index may hold, by the sync sample table and, when it
// has none, by the samples.
func TestMP4KeyframeLimits(t *testing.T) {
	entries := func(n int) []byte {
		b := make([]byte, 0, 4+4*n)
		b = append(b, u32(uint32(n))...)
		for i := 1; i <= n; i++ {
			b = append(b, u32(uint32(i))...)
		}
		return b
	}
	file := func(samples, syncs int) []byte {
		return goodMP4(func(v, a *mp4Trak) {
			v.scale = 1000
			v.sttsRaw = cat(u32(1), u32(uint32(samples)), u32(1000)) // a sample a second
			if syncs < 0 {
				v.noStss = true
			} else {
				v.stssRaw = entries(syncs)
			}
		})
	}

	for _, c := range []struct {
		name    string
		samples int
		syncs   int // -1: no table
	}{
		{"a sync sample table", maxIndexKeyframes, maxIndexKeyframes},
		{"no table, every sample", maxIndexKeyframes, -1},
	} {
		t.Run(c.name+", at the limit", func(t *testing.T) {
			kf, _, err, _ := indexOf(file(c.samples, c.syncs), -1)
			if err != nil {
				t.Fatal(err)
			}
			if len(kf) != maxIndexKeyframes || kf[len(kf)-1] != float64(maxIndexKeyframes-1) {
				t.Errorf("%d keyframes, the last at %v", len(kf), kf[len(kf)-1])
			}
		})
		t.Run(c.name+", one over", func(t *testing.T) {
			syncs := c.syncs
			if syncs > 0 {
				syncs++
			}
			_, _, err, _ := indexOf(file(c.samples+1, syncs), -1)
			wantLimit(t, err, "keyframes")
		})
	}
}

// ffprobe takes a text or data track into account only when it starts less
// than a second before the audio and video (libavformat's
// update_stream_timings); any other track is part of where the file starts.
func TestMP4TextTrackStart(t *testing.T) {
	// The video and the audio are delayed 2 s by an empty edit.
	delayed := func(v, a *mp4Trak) {
		v.edits = []mp4Edit{{2000, -1, 0}, {60000, 0, 0}}
		a.edits = []mp4Edit{{2000, -1, 0}, {60000, 0, 0}}
	}
	other := func(handler string, delay uint64) mp4Trak {
		t := mp4Trak{handler: handler, scale: 1000, stts: [][2]uint32{{2, 30000}}, noStss: true}
		if delay > 0 {
			t.edits = []mp4Edit{{delay, -1, 0}, {60000, 0, 0}}
		}
		return t
	}
	file := func(extra mp4Trak) []byte {
		v, a := goodVideo(), goodAudio()
		delayed(&v, &a)
		return cat(ftyp, moovOf(1000, nil, v, a, extra), isoBox("mdat", make([]byte, 100)))
	}
	for _, c := range []struct {
		name  string
		extra mp4Trak
		first float64 // the first keyframe, relative to the start
	}{
		{"a text track at 0, 2 s before: an outlier", other("text", 0), 0},
		{"a subtitle track 1.4 s before: an outlier", other("sbtl", 600), 0},
		{"a subtitle track exactly 1 s before: an outlier", other("sbtl", 1000), 0},
		{"a subtitle track 0.5 s before counts", other("sbtl", 1500), 0.5},
		{"a timecode track 0.5 s before counts", other("tmcd", 1500), 0.5},
		{"a metadata track 0.5 s before counts", other("meta", 1500), 0.5},
		{"a subtitle track after the media changes nothing", other("sbtl", 2500), 0},
		{"a second video track at 0 (cover art) is part of it", other("vide", 0), 2},
		{"a second audio track at 0 is part of it", other("soun", 0), 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			kf, _, err, _ := indexOf(file(c.extra), -1)
			if err != nil {
				t.Fatal(err)
			}
			wantTimes(t, kf, every(30, c.first, 2)...)
		})
	}
}

// QuickTime ends a container with 32 zero bits.
func TestMP4ZeroTerminator(t *testing.T) {
	v, a := goodVideo(), goodAudio()
	mvhd := isoFull("mvhd", 0, u32(0), u32(0), u32(1000), u32(0), make([]byte, 80))
	file := func(tail []byte) []byte {
		return cat(ftyp, isoBox("moov", mvhd, v.bytes(), a.bytes(), tail), isoBox("mdat", make([]byte, 100)))
	}
	for _, tail := range [][]byte{{0, 0, 0, 0}, {0, 0, 0}, make([]byte, 7)} {
		kf, _, err, _ := indexOf(file(tail), -1)
		if err != nil {
			t.Fatalf("%d zero bytes: %v", len(tail), err)
		}
		wantTimes(t, kf, every(30, 0, 2)...)
	}
	for _, tail := range [][]byte{{0, 0, 0, 1}, {1}, {0, 0, 0, 0, 0, 0, 1}} {
		if _, _, err, _ := indexOf(file(tail), -1); err == nil {
			t.Errorf("a tail of % x was taken for a terminator", tail)
		}
	}
}

// The sample tables of a film's moov are megabytes and scattered through it, and
// the file may be on a network share where a read costs as much as a megabyte:
// a moov of a few MB is one read, one that is not worth that is walked.
func TestMP4BigMoovIsOneRead(t *testing.T) {
	big := func(table int) (moov []byte) {
		v := goodVideo()
		v.table = table // the sample sizes: the biggest table of a film
		return moovOf(1000, nil, v, goodAudio())
	}
	mdat := isoBox("mdat", make([]byte, 1<<20))

	for _, c := range []struct {
		name     string
		file     []byte
		moov     int
		reads    int
		maxBytes int // the moov, if it is read whole, and a window
	}{
		{"3 MB, faststart", nil, 3 << 20, 2, 3<<20 + 2*indexWindow},
		{"3 MB, at the end", nil, 3 << 20, 3, 3<<20 + 3*indexWindow},
		{"4 MB, at the end", nil, 4 << 20, 3, 4<<20 + 3*indexWindow},
		// Too big to be worth it: the boxes are found one by one and the tables
		// that are not wanted (the sample sizes) are never read.
		{"9 MB, faststart", nil, 9 << 20, 12, 1 << 20},
		{"9 MB, at the end", nil, 9 << 20, 12, 1 << 20},
	} {
		moov := big(c.moov)
		switch {
		case strings.HasSuffix(c.name, "faststart"):
			c.file = cat(ftyp, moov, mdat)
		default:
			c.file = cat(ftyp, mdat, moov)
		}
		t.Run(c.name, func(t *testing.T) {
			kf, _, err, cr := indexOf(c.file, -1)
			if err != nil {
				t.Fatal(err)
			}
			wantTimes(t, kf, every(30, 0, 2)...)
			if cr.reads > c.reads || cr.bytes > int64(c.maxBytes) {
				t.Errorf("moov of %d bytes: %d reads and %d bytes, want at most %d and %d", len(moov), cr.reads, cr.bytes, c.reads, c.maxBytes)
			}
		})
	}
}
