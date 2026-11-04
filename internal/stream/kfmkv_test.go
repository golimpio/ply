package stream

import (
	"bytes"
	"context"
	"errors"
	"math"
	"math/bits"
	"sort"
	"strings"
	"testing"
)

// ---- byte-level EBML builders ------------------------------------------

func ebmlIDBytes(id uint32) []byte {
	n := (bits.Len32(id) + 7) / 8
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(id >> (8 * (n - 1 - i)))
	}
	return b
}

// ebmlSizeBytes is n as a data size in the fewest bytes.
func ebmlSizeBytes(n int64) []byte {
	for l := 1; l <= 8; l++ {
		if n < int64(1)<<(7*l)-1 {
			v := uint64(n) | 1<<(7*l)
			b := make([]byte, l)
			for i := range b {
				b[i] = byte(v >> (8 * (l - 1 - i)))
			}
			return b
		}
	}
	panic("size too big")
}

var ebmlUnknown = []byte{0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}

func el(id uint32, body ...[]byte) []byte {
	b := cat(body...)
	return cat(ebmlIDBytes(id), ebmlSizeBytes(int64(len(b))), b)
}

func elUnknown(id uint32, body ...[]byte) []byte {
	return cat(ebmlIDBytes(id), ebmlUnknown, cat(body...))
}

// elRaw writes an element with the given size bytes, true or not.
func elRaw(id uint32, size []byte, body ...[]byte) []byte {
	return cat(ebmlIDBytes(id), size, cat(body...))
}

func elUintN(id uint32, v uint64, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(v >> (8 * (n - 1 - i)))
	}
	return el(id, b)
}

func elUint(id uint32, v uint64) []byte { return elUintN(id, v, max(1, (bits.Len64(v)+7)/8)) }

func elFloat(id uint32, f float64) []byte {
	var b [8]byte
	u := math.Float64bits(f)
	for i := range b {
		b[i] = byte(u >> (8 * (7 - i)))
	}
	return el(id, b[:])
}

// ---- a Matroska file, as the real muxers lay one out --------------------

type mkvBlk struct {
	track uint64
	rel   int16
	key   bool
	group bool // in a BlockGroup, like a block with a reference
	size  int  // payload bytes
}

type mkvClu struct {
	ts      uint64
	blocks  []mkvBlk
	unknown bool // an unknown-size Cluster (live recordings)
}

type mkvCue struct{ time, track uint64 }

type mkvFile struct {
	scale    uint64  // TimestampScale; 0 leaves it out (the default, 1 ms)
	duration float64 // in ticks; 0 leaves it out
	tracks   []mkvTrack
	clusters []mkvClu
	cues     []mkvCue

	seek           bool // a SeekHead first, then a Void, like mkvmerge
	seek2          bool // the first SeekHead names a second one at the end, which has the Cues
	chain          int  // the first SeekHead names a chain of this many at the end, each naming the next; the last has the Cues
	tail           bool // Info and Tracks after the clusters
	cuesFirst      bool
	unknownSegment bool
	unknownCues    bool
	tagsAfterCues  bool

	// Lies and damage.
	segSize, cuesSize, infoSize, tracksSize []byte
	cuesRaw, infoBody                       []byte
	seekPos                                 map[uint32]uint64
	cueCluster                              map[int]uint64 // the position cue i gives for its cluster
}

func (c mkvClu) bytes() []byte {
	b, _ := c.encode()
	return b
}

// encode returns the cluster, and the offset of each of its blocks from the
// start of its content.
func (c mkvClu) encode() ([]byte, []uint64) {
	parts := [][]byte{elUint(idTimestamp, c.ts)}
	offs := make([]uint64, len(c.blocks))
	pos := uint64(len(parts[0]))
	for i, b := range c.blocks {
		flags := byte(0)
		if b.key {
			flags = 0x80
		}
		blk := cat([]byte{0x80 | byte(b.track), byte(uint16(b.rel) >> 8), byte(b.rel), flags}, make([]byte, b.size))
		var e []byte
		if b.group {
			parts2 := [][]byte{el(idBlock, blk)}
			if !b.key {
				parts2 = append(parts2, elUint(idReferenceBlock, 1)) // refers to a block before it
			}
			e = el(idBlockGroup, append(parts2, elUint(0x9B, 42))...)
		} else {
			e = el(idSimpleBlock, blk)
		}
		offs[i] = pos
		pos += uint64(len(e))
		parts = append(parts, e)
	}
	if c.unknown {
		return elUnknown(idCluster, parts...), offs
	}
	return el(idCluster, parts...), offs
}

func (f mkvFile) infoEl() []byte {
	parts := [][]byte{el(0x4D80, []byte("test"))}
	if f.scale != 0 {
		parts = append(parts, elUint(idTimestampScale, f.scale))
	}
	if f.duration != 0 {
		parts = append(parts, elFloat(idDuration, f.duration))
	}
	if f.infoBody != nil {
		parts = [][]byte{f.infoBody}
	}
	if f.infoSize != nil {
		return elRaw(idInfo, f.infoSize, parts...)
	}
	return el(idInfo, parts...)
}

func (f mkvFile) tracksEl() []byte {
	var parts [][]byte
	for _, t := range f.tracks {
		parts = append(parts, el(idTrackEntry, elUint(idTrackNumber, t.number), elUint(idTrackType, t.typ), el(0x86, []byte("V_TEST"))))
	}
	if f.tracksSize != nil {
		return elRaw(idTracks, f.tracksSize, parts...)
	}
	return el(idTracks, parts...)
}

// cuesEl writes the cue points. Each points at the cluster that holds its
// time and at the block there that has it, like the muxers' do.
func (f mkvFile) cuesEl(cl clusterMap) []byte {
	var points [][]byte
	for i, c := range f.cues {
		ci := sort.Search(len(f.clusters), func(i int) bool { return f.clusters[i].ts > c.time }) - 1
		ci = max(ci, 0)
		var cluster, rel uint64
		if ci < len(cl.pos) {
			cluster = cl.pos[ci]
			for j, b := range f.clusters[ci].blocks {
				if b.track == c.track && int64(f.clusters[ci].ts)+int64(b.rel) == int64(c.time) {
					rel = cl.blocks[ci][j]
					break
				}
			}
		}
		if v, ok := f.cueCluster[i]; ok {
			cluster = v
		}
		points = append(points, el(idCuePoint, elUint(idCueTime, c.time),
			el(idCueTrackPositions, elUint(idCueTrack, c.track), elUintN(idCueClusterPosition, cluster, 8), elUintN(0xF0, rel, 8))))
	}
	switch {
	case f.cuesRaw != nil:
		return elRaw(idCues, ebmlSizeBytes(int64(len(f.cuesRaw))), f.cuesRaw)
	case f.unknownCues:
		return elUnknown(idCues, points...)
	case f.cuesSize != nil:
		return elRaw(idCues, f.cuesSize, points...)
	}
	return el(idCues, points...)
}

// chainKey is where the keys of the SeekHeads of a chain start: above any ID.
const chainKey = 0x7F000000

// clusterMap says where the clusters of a layout are: their offset in the
// Segment and, for each block, its offset from the start of the cluster's
// content. The cues carry them.
type clusterMap struct {
	pos    []uint64
	blocks [][]uint64
}

// layout assembles the file; pos holds the SeekHead targets (offsets in the
// Segment) and cl the clusters' places, both found by an earlier pass (the sizes
// of what they go in do not depend on them), and those of this pass come back.
func (f mkvFile) layout(pos map[uint32]uint64, cl clusterMap) ([]byte, map[uint32]uint64, clusterMap) {
	var body []byte
	offs := map[uint32]uint64{}
	var placed clusterMap
	add := func(id uint32, b []byte) {
		offs[id] = uint64(len(body))
		body = append(body, b...)
	}
	// seekHead lists elements by the key their position is kept under; the
	// SeekHeads of a chain are all idSeekHead, and have keys of their own.
	seekHead := func(keys ...uint32) []byte {
		var entries [][]byte
		for _, key := range keys {
			id := key
			if key >= chainKey {
				id = idSeekHead
			}
			p := pos[key]
			if v, ok := f.seekPos[key]; ok {
				p = v
			}
			entries = append(entries, el(idSeek, el(idSeekID, ebmlIDBytes(id)), elUintN(idSeekPosition, p, 8)))
		}
		return el(idSeekHead, entries...)
	}

	if f.seek {
		switch {
		case f.chain > 0:
			body = append(body, seekHead(idInfo, idTracks, chainKey)...)
		case f.seek2:
			body = append(body, seekHead(idInfo, idTracks, idSeekHead)...)
		default:
			body = append(body, seekHead(idInfo, idTracks, idCues)...)
		}
		body = append(body, el(idVoid, make([]byte, 40))...)
	}
	if !f.tail {
		add(idInfo, f.infoEl())
		add(idTracks, f.tracksEl())
	}
	if f.cuesFirst {
		add(idCues, f.cuesEl(cl))
	}
	for _, c := range f.clusters {
		b, blocks := c.encode()
		placed.pos = append(placed.pos, uint64(len(body)))
		placed.blocks = append(placed.blocks, blocks)
		body = append(body, b...)
	}
	if f.tail {
		add(idInfo, f.infoEl())
		add(idTracks, f.tracksEl())
	}
	if !f.cuesFirst {
		add(idCues, f.cuesEl(cl))
		if f.tagsAfterCues {
			body = append(body, el(idTags)...)
		}
	}
	if f.seek2 {
		add(idSeekHead, seekHead(idCues))
	}
	for i := 0; i < f.chain; i++ {
		if i == f.chain-1 {
			add(chainKey+uint32(i), seekHead(idCues))
		} else {
			add(chainKey+uint32(i), seekHead(chainKey+uint32(i+1)))
		}
	}

	size := ebmlSizeBytes(int64(len(body)))
	if f.unknownSegment {
		size = ebmlUnknown
	}
	if f.segSize != nil {
		size = f.segSize
	}
	return cat(el(idEBML, el(0x4282, []byte("matroska"))), ebmlIDBytes(idSegment), size, body), offs, placed
}

func (f mkvFile) build() []byte {
	_, pos, cl := f.layout(nil, clusterMap{})
	out, _, _ := f.layout(pos, cl)
	return out
}

// goodMKV is two minutes with a keyframe every 2 s: an audio track (1) and a
// video track (2) in that order, one cluster per keyframe, and a decoy audio
// cue every third keyframe. Each video keyframe is 300 kB, so the index is a
// small part of the file.
func goodMKV(mod func(*mkvFile)) mkvFile { return gopsMKV(1, mod) }

// gopsMKV is goodMKV with perCluster keyframes to a cluster, as ffmpeg's
// clusters of 5 s hold two or three of a 2 s GOP: what a cluster shows of the
// cues is only more than one keyframe if it holds more than one.
func gopsMKV(perCluster int, mod func(*mkvFile)) mkvFile {
	f := mkvFile{duration: 120000, seek: true, tracks: []mkvTrack{{1, trackTypeAudio}, {2, trackTypeVideo}}}
	for k := 0; k < 60; k += perCluster {
		ts := uint64(k) * 2000
		var blocks []mkvBlk
		for g := 0; g < perCluster && k+g < 60; g++ {
			rel := int16(g * 2000)
			blocks = append(blocks,
				mkvBlk{track: 2, rel: rel, key: true, size: 300000}, mkvBlk{track: 1, rel: rel, size: 300},
				mkvBlk{track: 2, rel: rel + 42, size: 5000}, mkvBlk{track: 1, rel: rel + 21, size: 300},
				mkvBlk{track: 2, rel: rel + 1900, size: 5000}) // the rest of the GOP, in short
			f.cues = append(f.cues, mkvCue{ts + uint64(rel), 2})
			if (k+g)%3 == 0 {
				f.cues = append(f.cues, mkvCue{ts + uint64(rel) + 7, 1})
			}
		}
		f.clusters = append(f.clusters, mkvClu{ts: ts, blocks: blocks})
	}
	if mod != nil {
		mod(&f)
	}
	return f
}

// shifted moves every timestamp by ms milliseconds.
func shifted(ms uint64) func(*mkvFile) {
	return func(f *mkvFile) {
		for i := range f.clusters {
			f.clusters[i].ts += ms
		}
		for i := range f.cues {
			f.cues[i].time += ms
		}
	}
}

// ticks re-expresses every timestamp in units of scale nanoseconds.
func ticks(scale uint64) func(*mkvFile) {
	return func(f *mkvFile) {
		conv := func(ms uint64) uint64 { return ms * 1_000_000 / scale }
		f.scale = scale
		f.duration = float64(conv(uint64(f.duration)))
		for i := range f.clusters {
			f.clusters[i].ts = conv(f.clusters[i].ts)
			for j := range f.clusters[i].blocks {
				f.clusters[i].blocks[j].rel = int16(conv(uint64(f.clusters[i].blocks[j].rel)))
			}
		}
		for i := range f.cues {
			f.cues[i].time = conv(f.cues[i].time)
		}
	}
}

// smallKeys makes the video keyframes 2 kB: the window the headers are read in
// holds several clusters.
func smallKeys(f *mkvFile) {
	for i := range f.clusters {
		for j := range f.clusters[i].blocks {
			if f.clusters[i].blocks[j].key {
				f.clusters[i].blocks[j].size = 2000
			}
		}
	}
}

// audioFirst puts the audio block of each GOP before its video keyframe: the
// window then shows both tracks, and one keyframe.
func audioFirst(f *mkvFile) {
	for i := range f.clusters {
		b := f.clusters[i].blocks
		for j := 0; j+1 < len(b); j += 5 {
			b[j], b[j+1] = b[j+1], b[j]
		}
	}
}

// pFrames makes the video frames after the keyframes n bytes: with 40 kB a
// cluster is more than a window of them, though the keyframe at its head is
// small; with 16 kB the window holds two clusters and no more.
func pFrames(n int) func(*mkvFile) {
	return func(f *mkvFile) {
		for i := range f.clusters {
			for j := range f.clusters[i].blocks {
				if b := &f.clusters[i].blocks[j]; b.track == 2 && !b.key {
					b.size = n
				}
			}
		}
	}
}

// videoCues keeps the cues that pick which of the video keyframes (k = 0, 1,
// ...) to list, and the audio decoys.
func videoCues(keep func(k int) bool) func(*mkvFile) {
	return func(f *mkvFile) {
		var cues []mkvCue
		for _, c := range f.cues {
			if c.track != 2 || keep(int(c.time/2000)) {
				cues = append(cues, c)
			}
		}
		f.cues = cues
	}
}

// ---- the tests ----------------------------------------------------------

func TestMatroskaCuesLayouts(t *testing.T) {
	for _, c := range []struct {
		name  string
		mod   func(*mkvFile)
		reads int // what the layout costs: a SeekHead saves hopping over clusters
	}{
		// The usual layout is three reads: the window at the start (the headers and
		// the start of the first cluster), the head of the second cluster (for what
		// the first leaves out of the start of the file and the cues), the Cues.
		{"seekhead first", nil, 3},
		// What the window holds already is not read again: keyframes of 2 kB show
		// several clusters, so no hop; with the audio first the window shows both
		// tracks but only one keyframe, and the head of the next cluster is read.
		{"keyframes of 2 kB", smallKeys, 2},
		{"audio first", audioFirst, 3},
		// Two keyframes in the window are all the cues are checked against: no hop.
		{"two keyframes in the window", func(f *mkvFile) { smallKeys(f); pFrames(16000)(f) }, 2},
		{"no seekhead, cues last", func(f *mkvFile) { f.seek = false }, 63},
		{"no seekhead, cues before the clusters", func(f *mkvFile) { f.seek, f.cuesFirst = false, true }, 3},
		{"info and tracks after the clusters", func(f *mkvFile) { f.tail = true }, 5},
		{"second seekhead at the end", func(f *mkvFile) { f.seek2 = true }, 5},
		{"a chain of seekheads", func(f *mkvFile) { f.chain = 3 }, 5},
		{"unknown-size segment", func(f *mkvFile) { f.unknownSegment = true }, 3},
		{"unknown-size cues, tags after", func(f *mkvFile) { f.unknownCues, f.tagsAfterCues = true, true }, 3},
		{"unknown-size cues at the end of the file", func(f *mkvFile) { f.unknownCues = true }, 3},
		{"unknown-size first cluster, seekhead", func(f *mkvFile) { f.clusters[0].unknown = true }, 2},
		{"seekhead position out of the file: scanned", func(f *mkvFile) { f.seekPos = map[uint32]uint64{idCues: 1 << 60} }, 63},
		{"scale 2 ms", ticks(2_000_000), 3},
		{"scale 0.5 ms", ticks(500_000), 3},
		{"scale 0.25 ms", ticks(250_000), 3},
		{"explicit default scale", func(f *mkvFile) { f.scale = 1_000_000 }, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := goodMKV(c.mod).build()
			kf, source, err, cr := indexOf(data, -1)
			if err != nil {
				t.Fatal(err)
			}
			if source != sourceCues {
				t.Errorf("source %q", source)
			}
			wantTimes(t, kf, every(60, 0, 2)...)
			max := int64(200 << 10) // of a file of 18 MB
			t.Logf("%d reads, %d bytes of a %d byte file", cr.reads, cr.bytes, len(data))
			if cr.bytes > max || cr.reads > c.reads {
				t.Errorf("read %d bytes in %d reads of a %d byte file, want under %d bytes and %d reads", cr.bytes, cr.reads, len(data), max, c.reads)
			}
		})
	}
}

// Times are relative to where ffprobe puts the start of the file: the earliest
// of the first blocks of the audio and video tracks.
func TestMatroskaStartTime(t *testing.T) {
	t.Run("everything starts at 1.4 s", func(t *testing.T) {
		kf, _, err, _ := indexOf(goodMKV(shifted(1400)).build(), -1)
		if err != nil {
			t.Fatal(err)
		}
		wantTimes(t, kf, every(60, 0, 2)...)
	})
	t.Run("audio starts first", func(t *testing.T) {
		data := goodMKV(func(f *mkvFile) {
			shifted(1500)(f)
			f.clusters[0].ts = 1000
			f.clusters[0].blocks = []mkvBlk{{track: 2, rel: 500, key: true, size: 100}, {track: 1, rel: 0, size: 100}}
		}).build()
		kf, _, err, _ := indexOf(data, -1)
		if err != nil {
			t.Fatal(err)
		}
		wantTimes(t, kf, every(60, 0.5, 2)...)
	})
	t.Run("video starts first, audio is late", func(t *testing.T) {
		data := goodMKV(func(f *mkvFile) {
			f.clusters[0].blocks = []mkvBlk{{track: 2, key: true, size: 100}, {track: 1, rel: 32, size: 100}}
		}).build()
		kf, _, err, _ := indexOf(data, -1)
		if err != nil {
			t.Fatal(err)
		}
		wantTimes(t, kf, every(60, 0, 2)...)
	})
	t.Run("first blocks are in BlockGroups", func(t *testing.T) {
		data := goodMKV(func(f *mkvFile) {
			shifted(1400)(f)
			for i := range f.clusters[0].blocks {
				f.clusters[0].blocks[i].group = true
			}
		}).build()
		kf, _, err, _ := indexOf(data, -1)
		if err != nil {
			t.Fatal(err)
		}
		wantTimes(t, kf, every(60, 0, 2)...)
	})
	t.Run("a track with no block in the first cluster costs few reads", func(t *testing.T) {
		data := goodMKV(func(f *mkvFile) {
			f.tracks = append(f.tracks, mkvTrack{3, trackTypeAudio}) // a second audio track, silent at first
			blocks := []mkvBlk{{track: 2, key: true, size: 100000}, {track: 1, size: 100}}
			for i := 1; i <= 40; i++ {
				blocks = append(blocks, mkvBlk{track: 2, rel: int16(i), size: 100000})
			}
			f.clusters[0].blocks = blocks
		}).build()
		kf, _, err, cr := indexOf(data, -1)
		if err != nil {
			t.Fatal(err)
		}
		wantTimes(t, kf, every(60, 0, 2)...)
		if cr.reads > 9 { // the hops to later clusters are one read each
			t.Errorf("%d reads", cr.reads)
		}
	})

	// A subtitle or data track counts only if it starts less than a second
	// before the media; one that starts earlier is an outlier (libavformat's
	// update_stream_timings), and not part of where the file starts.
	text := func(typ uint64, rel int16) func(*mkvFile) {
		return func(f *mkvFile) {
			shifted(1400)(f)
			f.tracks = append(f.tracks, mkvTrack{3, typ})
			f.clusters[0].blocks = append([]mkvBlk{{track: 3, rel: rel, size: 20}}, f.clusters[0].blocks...)
		}
	}
	for _, c := range []struct {
		name  string
		mod   func(*mkvFile)
		first float64 // the first keyframe, relative to the start
	}{
		{"a subtitle 0.4 s before the media counts", text(trackTypeSubtitle, -400), 0.4},
		{"a subtitle just under 1 s before counts", text(trackTypeSubtitle, -999), 0.999},
		{"a subtitle 1 s before is an outlier", text(trackTypeSubtitle, -1000), 0},
		{"a subtitle 1.4 s before is an outlier", text(trackTypeSubtitle, -1400), 0},
		{"a subtitle after the media changes nothing", text(trackTypeSubtitle, 300), 0},
		{"a metadata track 0.4 s before counts", text(trackTypeMetadata, -400), 0.4},
		{"a metadata track 1.2 s before is an outlier", text(trackTypeMetadata, -1200), 0},
		{"a track of another type is not part of it", text(18, -400), 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			kf, _, err, _ := indexOf(goodMKV(c.mod).build(), -1)
			if err != nil {
				t.Fatal(err)
			}
			wantTimes(t, kf, every(60, c.first, 2)...)
		})
	}
	t.Run("a subtitle at 0 and the media at 3 s, in clusters of their own", func(t *testing.T) {
		// The first cluster has the subtitle only: the audio and video are
		// found in the next.
		data := goodMKV(func(f *mkvFile) {
			shifted(3000)(f)
			f.tracks = append(f.tracks, mkvTrack{3, trackTypeSubtitle})
			f.clusters = append([]mkvClu{{ts: 0, blocks: []mkvBlk{{track: 3, size: 20}}}}, f.clusters...)
		}).build()
		kf, _, err, _ := indexOf(data, -1)
		if err != nil {
			t.Fatal(err)
		}
		wantTimes(t, kf, every(60, 0, 2)...)
	})
}

func TestMatroskaVideoTrackSelection(t *testing.T) {
	// Tracks: video 5, audio 6, video 9 (a 6 s GOP with cues of its own).
	data := goodMKV(func(f *mkvFile) {
		f.tracks = []mkvTrack{{5, trackTypeVideo}, {6, trackTypeAudio}, {9, trackTypeVideo}}
		f.cues = nil
		for k := 0; k < 60; k++ {
			ts := uint64(k) * 2000
			f.clusters[k].blocks = []mkvBlk{{track: 5, key: true, size: 100}, {track: 6, size: 100}, {track: 9, key: k%3 == 0, size: 100}}
			f.cues = append(f.cues, mkvCue{ts, 5})
			if k%3 == 0 {
				f.cues = append(f.cues, mkvCue{ts, 9}, mkvCue{ts, 6})
			}
		}
	}).build()
	for _, c := range []struct {
		index int
		want  []float64 // nil: no index
	}{
		{-1, every(60, 0, 2)}, {0, every(60, 0, 2)}, {2, every(20, 0, 6)}, // the second video stream
		{1, nil}, {3, nil}, {5, nil}, {9, nil}, // audio, past the tracks, and track numbers are not stream indexes
	} {
		kf, _, err, _ := indexOf(data, c.index)
		if c.want == nil {
			if err == nil {
				t.Errorf("stream %d: got an index %v, want none", c.index, head(kf))
			}
			continue
		}
		if err != nil {
			t.Errorf("stream %d: %v", c.index, err)
			continue
		}
		wantTimes(t, kf, c.want...)
	}

	// No video at all, or no cue for the video.
	if _, _, err, _ := indexOf(goodMKV(func(f *mkvFile) { f.tracks = f.tracks[:1] }).build(), -1); err == nil {
		t.Error("an index without a video track")
	}
	if _, _, err, _ := indexOf(goodMKV(func(f *mkvFile) {
		f.cues = []mkvCue{{0, 1}, {2000, 1}, {4000, 1}}
	}).build(), -1); err == nil {
		t.Error("an index from cues of the audio track")
	}
}

// The cues have to list the keyframes there are, no more and no fewer. Cues
// that skip some would give a plan with fewer GOPs than ffmpeg writes
// fragments (the segments after the first would hold the wrong content), and
// cues that add some, a plan with more (ffmpeg would be asked for fragments
// that never come). What can be told from the file at no cost is compared: the
// blocks that the start of the file already holds (the window the headers were
// read in, and the head of the next cluster, which is read for the start time
// anyway). The rest is for the first fragments ffmpeg writes (planguard.go).
func TestMatroskaCuesAgainstBlocks(t *testing.T) {
	ok := func(t *testing.T, name string, data []byte, want ...float64) {
		t.Helper()
		kf, _, err, _ := indexOf(data, -1)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if want != nil {
			wantTimes(t, kf, want...)
		}
	}
	bad := func(t *testing.T, name string, data []byte, msg string) {
		t.Helper()
		kf, _, err, _ := indexOf(data, -1)
		if err == nil {
			t.Fatalf("%s: got an index %v", name, head(kf))
		}
		if !strings.Contains(err.Error(), msg) {
			t.Fatalf("%s: err = %v, want it to say %q", name, err, msg)
		}
	}

	ok(t, "all the keyframes", gopsMKV(3, nil).build(), every(60, 0, 2)...) // positive control

	// With the audio first the window shows both tracks and one keyframe: the
	// second is what shows the cues that skip every other one.
	ok(t, "audio first", goodMKV(audioFirst).build(), every(60, 0, 2)...)
	bad(t, "audio first, every other cue removed", goodMKV(func(f *mkvFile) {
		audioFirst(f)
		videoCues(func(k int) bool { return k%2 == 0 })(f)
	}).build(), "has no cue")

	// ffmpeg writes a cluster to a GOP: the first cluster shows one keyframe, and
	// only the head of the next shows that the cues skip every other.
	bad(t, "one-keyframe clusters, every other cue removed", goodMKV(videoCues(func(k int) bool { return k%2 == 0 })).build(), "has no cue")
	bad(t, "one-keyframe clusters, every fourth cue kept", goodMKV(videoCues(func(k int) bool { return k%4 == 0 })).build(), "has no cue")
	bad(t, "one-keyframe clusters, the second keyframe has no cue", goodMKV(videoCues(func(k int) bool { return k != 1 })).build(), "has no cue")
	// What neither the start nor the end of the file shows, the first fragments
	// of a run that starts there do (planguard.go).
	ok(t, "one-keyframe clusters, a hole in the middle", goodMKV(videoCues(func(k int) bool { return k < 8 || k%2 == 0 || k >= 24 })).build())

	// E1: every other cue is gone.
	bad(t, "every other cue removed", gopsMKV(3, videoCues(func(k int) bool { return k%2 == 0 })).build(), "has no cue")
	// A quarter of the keyframes (the cues are 8 s apart: the count and the
	// median gap would pass).
	bad(t, "every fourth cue kept", gopsMKV(3, videoCues(func(k int) bool { return k%4 == 0 })).build(), "has no cue")
	// A hole at the start, then dense: the median would not see it.
	bad(t, "a hole after the first cue", gopsMKV(3, func(f *mkvFile) {
		f.cues = append([]mkvCue{{0, 2}}, f.cues[20:]...)
	}).build(), "has no cue")
	// With keyframes of 2 kB the window holds a whole cluster, and the start of the
	// next: more of the file than one keyframe's worth to check the cues against.
	small := smallKeys
	with := func(mods ...func(*mkvFile)) func(*mkvFile) {
		return func(f *mkvFile) {
			for _, m := range mods {
				m(f)
			}
		}
	}
	// The window holds the first four clusters: all of them are checked, for no read.
	bad(t, "small clusters, the fourth keyframe has no cue", goodMKV(with(small, videoCues(func(k int) bool { return k != 3 }))).build(), "has no cue")
	ok(t, "small clusters, the fifth keyframe has no cue", goodMKV(with(small, videoCues(func(k int) bool { return k != 4 }))).build())
	// A cluster of one keyframe and frames of 40 kB shows its first frames, which
	// are not keyframes: the keyframe count is of keyframes, and the next cluster
	// is read.
	bad(t, "one keyframe and heavy frames, every other cue removed", goodMKV(with(small, pFrames(40000), videoCues(func(k int) bool { return k%2 == 0 }))).build(), "has no cue")
	halves := func(f *mkvFile) { // E1b: a cue every half second
		for ms := uint64(0); ms < 120000; ms += 500 {
			if ms%2000 != 0 {
				f.cues = append(f.cues, mkvCue{ms, 2})
			}
		}
	}
	bad(t, "a cue every half second", gopsMKV(3, with(small, halves)).build(), "not on a keyframe")
	// Where the keyframes are 300 kB the window holds the first of them and no more:
	// the extra cues are not seen. The first fragment is longer than the plan's
	// GOP, which is what shows (TestFirstFragmentSettlesThePlanByItsDuration).
	ok(t, "a cue every half second, beyond the window", gopsMKV(3, halves).build())

	// One cue between two keyframes, where the blocks that were read show it.
	extra := func(f *mkvFile) { f.cues = append(f.cues, mkvCue{1000 + 42, 2}) }
	bad(t, "an extra cue on a frame", gopsMKV(3, with(small, extra)).build(), "not on a keyframe")
	bad(t, "an extra cue, in one-keyframe clusters", goodMKV(with(small, func(f *mkvFile) { f.cues = append(f.cues, mkvCue{42, 2}) })).build(), "not on a keyframe")
	// Cues that do not hit the keyframe, but are within a tick of it, are the same cue.
	ok(t, "a tick early", gopsMKV(3, func(f *mkvFile) {
		for i := range f.cues {
			if f.cues[i].track == 2 && f.cues[i].time > 0 {
				f.cues[i].time--
			}
		}
	}).build())
	ok(t, "a tick late", gopsMKV(3, func(f *mkvFile) {
		for i := range f.cues {
			if f.cues[i].track == 2 {
				f.cues[i].time++
			}
		}
	}).build())
	bad(t, "two ticks late", gopsMKV(3, func(f *mkvFile) {
		for i := range f.cues {
			if f.cues[i].track == 2 {
				f.cues[i].time += 2
			}
		}
	}).build(), "")

	// The end of the file is not read: a keyframe without a cue there, or a cue
	// that points nowhere (the positions are not looked at), is for the fragments.
	ok(t, "a keyframe without a cue in the last cluster", gopsMKV(3, videoCues(func(k int) bool { return k != 58 })).build())
	ok(t, "the last cue points nowhere", gopsMKV(3, func(f *mkvFile) {
		f.cueCluster = map[int]uint64{len(f.cues) - 1: 0}
	}).build())

	// A keyframe is a SimpleBlock with the flag, or a BlockGroup that refers to
	// no other block.
	groups := func(f *mkvFile) {
		for i := range f.clusters {
			for j := range f.clusters[i].blocks {
				f.clusters[i].blocks[j].group = true
			}
		}
	}
	ok(t, "block groups", gopsMKV(3, groups).build(), every(60, 0, 2)...)
	bad(t, "a cue on a block group that refers to another", gopsMKV(3, with(small, groups, func(f *mkvFile) {
		f.cues = append(f.cues, mkvCue{42, 2})
	})).build(), "not on a keyframe")
	bad(t, "a block group keyframe without a cue", gopsMKV(3, with(small, groups, videoCues(func(k int) bool { return k != 1 }))).build(), "has no cue")
	// A block group cut off by the end of the window shows its track and time, not
	// whether it refers to another block: it is not judged (here the first
	// keyframe is 300 kB, in a group, and the cue for it is there anyway).
	ok(t, "a block group cut off", gopsMKV(3, groups).build())

	// Only what was read is judged: a cluster of keyframes of 800 kB each shows
	// the first one and the start of the next, and no cue further on.
	big := func(f *mkvFile) {
		for i := range f.clusters {
			for j := range f.clusters[i].blocks {
				if f.clusters[i].blocks[j].key {
					f.clusters[i].blocks[j].size = 800000
				}
			}
		}
	}
	ok(t, "keyframes of 800 kB", gopsMKV(3, big).build(), every(60, 0, 2)...)
	// The second keyframe is 800 kB into the cluster; the first of the next is at its head.
	ok(t, "keyframes of 800 kB, the second has no cue", gopsMKV(3, with(big, videoCues(func(k int) bool { return k != 1 }))).build())
	bad(t, "keyframes of 800 kB, the first of the next cluster has no cue", gopsMKV(3, with(big, videoCues(func(k int) bool { return k != 3 }))).build(), "has no cue")
}

func TestCheckDense(t *testing.T) {
	gaps := func(first float64, gap ...float64) []float64 {
		kf := []float64{first}
		for _, g := range gap {
			kf = append(kf, kf[len(kf)-1]+g)
		}
		return kf
	}
	rep := func(n int, g float64) []float64 {
		s := make([]float64, n)
		for i := range s {
			s[i] = g
		}
		return s
	}
	for _, c := range []struct {
		name     string
		kf       []float64
		duration float64
		start    float64
		bad      string // what the error says; "" for none
	}{
		{"dense", every(60, 0, 2), 120, 0, ""},
		{"nothing", nil, 120, 0, "no cue points"},
		{"one cue in a short clip", []float64{0}, 20, 0, ""},

		// The median gap: 12 s is the limit (x264's default longest GOP is 10.4 s),
		// and it is not over it. The numbers are written out.
		{"median gap of 10.5 s", every(10, 0, 10.5), 0, 0, ""},
		{"median gap of exactly the limit", every(10, 0, 12), 0, 0, ""},
		{"median gap just over", every(10, 0, 12.001), 0, 0, "median cue gap"},
		{"fewer than half the gaps over", gaps(0, append(rep(6, 2), rep(5, 20)...)...), 0, 0, ""},
		{"half the gaps over", gaps(0, append(rep(5, 2), rep(6, 20)...)...), 0, 0, "median cue gap"},

		// The count: one cue per 30 s at least, judged on the duration or, with
		// none, on where the last cue is. Dense at the start, nothing for ten
		// minutes, one at the end: the median is 2 s.
		{"count: no duration, a hole before the last cue", append(every(5, 0, 2), 600), 0, 0, "too sparse"},
		{"count: the last cue past the duration", append(every(5, 0, 2), 600), 100, 0, "too sparse"},
		{"count: enough in the dense part and the hole", gaps(0, append(rep(60, 2), rep(20, 100)...)...), 2120, 0, ""},
		{"count: not enough", gaps(0, append(rep(60, 2), rep(30, 100)...)...), 3120, 0, "too sparse"},

		// The ends: a stretch of more than two median gaps of video at the start
		// or the end with no cue is missing keyframes.
		{"first cue at twice the limit", every(30, 2*maxMedianGap, 2), 0, 0, ""},
		{"first cue past twice the limit", every(30, 2*maxMedianGap+0.001, 2), 0, 0, "first cue"},
		{"last cue twice the limit before the end", every(30, 0, 2), 58 + 2*maxMedianGap, 0, ""},
		{"last cue past twice the limit before the end", every(30, 0, 2), 58 + 2*maxMedianGap + 0.001, 0, "last cue"},
		{"the end is measured from where the media starts", every(30, 0, 2), 58 + 2*maxMedianGap + 1.3, 1.4, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := checkDense(c.kf, c.duration, c.start)
			switch {
			case c.bad == "" && err != nil:
				t.Fatalf("rejected: %v", err)
			case c.bad != "" && err == nil:
				t.Fatalf("accepted, want %q", c.bad)
			case c.bad != "" && !strings.Contains(err.Error(), c.bad):
				t.Fatalf("err = %v, want %q", err, c.bad)
			}
		})
	}
}

func TestMatroskaMalformed(t *testing.T) {
	good := goodMKV(nil).build()
	if _, _, err, _ := indexOf(good, -1); err != nil {
		t.Fatalf("positive control: %v", err)
	}

	t.Run("truncated", func(t *testing.T) {
		for _, name := range []string{"seekhead", "scanned"} {
			data := good
			if name == "scanned" {
				data = goodMKV(func(f *mkvFile) { f.seek = false }).build()
			}
			cut := func(n int) {
				if _, _, err, _ := indexOf(data[:n], -1); err == nil {
					t.Fatalf("%s: a file cut at %d of %d bytes gave an index", name, n, len(data))
				}
			}
			for n := 0; n < 3000; n++ {
				cut(n)
			}
			for n := 3000; n < len(data)-700; n += 200011 {
				cut(n)
			}
			for n := len(data) - 700; n < len(data); n++ {
				cut(n)
			}
		}
	})

	huge := []byte{0x01, 0x80, 0, 0, 0, 0, 0, 0}                           // 2^55
	almost := []byte{0x01, 0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFE}       // 2^48 - 2
	badPoint := cat(ebmlIDBytes(idCuePoint), ebmlSizeBytes(1<<40))         // a CuePoint that is not
	unknownPoint := cat(ebmlIDBytes(idCuePoint), ebmlUnknown)              // ... or has no size
	deepVoid := cat(ebmlIDBytes(idVoid), ebmlSizeBytes(1<<40))             // a Void that is not
	garbage := bytes.Repeat([]byte{0xFF, 0x00, 0x13}, 50)                  // not elements at all
	cutPoint := cat(ebmlIDBytes(idCuePoint), ebmlSizeBytes(40), []byte{1}) // a CuePoint cut short
	for _, c := range []struct {
		name string
		mod  func(*mkvFile)
		ok   bool // still gives the index
	}{
		{"segment of 2^55 bytes is the rest of the file", func(f *mkvFile) { f.segSize = huge }, true},
		{"cues of 2^55 bytes", func(f *mkvFile) { f.cuesSize = huge }, false},
		{"cues of 2^48 bytes", func(f *mkvFile) { f.cuesSize = almost }, false},
		{"info of 2^55 bytes", func(f *mkvFile) { f.infoSize = huge }, false},
		{"tracks of 2^55 bytes", func(f *mkvFile) { f.tracksSize = huge }, false},
		{"cue point of 2^40 bytes", func(f *mkvFile) { f.cuesRaw = badPoint }, false},
		{"cue point of unknown size", func(f *mkvFile) { f.cuesRaw = unknownPoint }, false},
		{"void of 2^40 bytes in the cues", func(f *mkvFile) { f.cuesRaw = deepVoid }, false},
		{"garbage in the cues", func(f *mkvFile) { f.cuesRaw = garbage }, false},
		{"cue point cut short", func(f *mkvFile) { f.cuesRaw = cutPoint }, false},
		{"no cue points", func(f *mkvFile) { f.cuesRaw = el(idVoid, make([]byte, 100)) }, false},
		{"timestamp scale 0", func(f *mkvFile) { f.scale = 0; f.infoBody = elUint(idTimestampScale, 0) }, false},
		{"info is garbage", func(f *mkvFile) { f.infoBody = garbage }, false},
		{"seekhead points at a cluster", func(f *mkvFile) { f.seekPos = map[uint32]uint64{idCues: 0} }, false},
		{"seekhead points inside the file at nothing", func(f *mkvFile) { f.seekPos = map[uint32]uint64{idCues: 5} }, false},
		{"seekhead points at the end", func(f *mkvFile) { f.seekPos = map[uint32]uint64{idCues: 1 << 62} }, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := goodMKV(c.mod).build()
			var err error
			var cr *countingReaderAt
			a := allocated(func() { _, _, err, cr = indexOf(data, -1) })
			if c.ok != (err == nil) {
				t.Errorf("err = %v, want success: %v", err, c.ok)
			}
			// What a file may make it hold: a couple of megabytes of cluster,
			// whatever sizes it claims.
			if a > 8<<20 || cr.bytes > 3<<20 {
				t.Errorf("allocated %d bytes and read %d for a %d byte file", a, cr.bytes, len(data))
			}
		})
	}

	t.Run("not matroska", func(t *testing.T) {
		for _, data := range [][]byte{
			nil, {0x1A}, {0x1A, 0x45, 0xDF, 0xA3}, {0x1A, 0x45, 0xDF, 0xA3, 0x80},
			cat(ebmlIDBytes(idEBML), ebmlSizeBytes(1<<40)),
			cat(el(idEBML), ebmlIDBytes(idCluster), ebmlUnknown), // no Segment
			cat(el(idEBML), ebmlIDBytes(idSegment), ebmlUnknown), // an empty one
			cat(el(idEBML), el(idSegment, el(idVoid))),
		} {
			if _, _, err, _ := indexOf(data, -1); err == nil {
				t.Errorf("% x: got an index", data)
			}
		}
	})
}

// A hostile file stops at the limits, wherever they are: bytes read, reads
// made, elements parsed, SeekHeads followed.
func TestMatroskaLimits(t *testing.T) {
	f := goodMKV(func(f *mkvFile) { f.seek = false; f.clusters = f.clusters[:1] })
	prologue := cat(el(idEBML, el(0x4282, []byte("matroska"))), ebmlIDBytes(idSegment), ebmlUnknown, f.infoEl(), f.tracksEl())

	t.Run("a cues element full of padding", func(t *testing.T) {
		const gig = int64(1) << 30
		prefix := cat(prologue, f.clusters[0].bytes(), ebmlIDBytes(idCues), ebmlSizeBytes(gig), ebmlIDBytes(idVoid), ebmlSizeBytes(900<<20))
		cr := &countingReaderAt{r: patternReader{prefix: prefix, size: int64(len(prefix)) + gig}}
		_, _, err := indexKeyframes(context.Background(), cr, int64(len(prefix))+gig, -1)
		wantLimit(t, err, "bytes")
		if cr.bytes > indexByteBudget {
			t.Errorf("read %d bytes, limit %d", cr.bytes, indexByteBudget)
		}
	})

	t.Run("a million clusters", func(t *testing.T) {
		cluster := cat(ebmlIDBytes(idCluster), []byte{0x81, 0x00}) // 6 bytes
		size := int64(len(prologue)) + 100<<20
		cr := &countingReaderAt{r: patternReader{prefix: prologue, pattern: cluster, size: size}}
		_, _, err := indexKeyframes(context.Background(), cr, size, -1)
		wantLimit(t, err, "reads")
		if cr.reads > indexReadBudget {
			t.Errorf("made %d reads, limit %d", cr.reads, indexReadBudget)
		}
	})

	t.Run("two million elements in a header", func(t *testing.T) {
		voids := bytes.Repeat([]byte{0xEC, 0x80}, 2<<20) // 4 MiB: allowed in size, not in number
		data := goodMKV(func(f *mkvFile) { f.infoBody = voids }).build()
		_, _, err, _ := indexOf(data, -1)
		wantLimit(t, err, "elements")
		// positive control: a header just under the element limit is fine.
		ok := goodMKV(func(f *mkvFile) {
			f.infoBody = cat(voids[:2*(maxIndexElements/2)], elUint(idTimestampScale, 1_000_000))
		}).build()
		if _, _, err, _ := indexOf(ok, -1); errors.Is(err, errIndexLimit) {
			t.Errorf("a header with %d elements hit the limit: %v", maxIndexElements/2, err)
		}
	})

	t.Run("more cue points than a file can have", func(t *testing.T) {
		// Each cue point is several elements, so the element limit is the one
		// that stops a flood of them, a quarter of a million cues in.
		point := el(idCuePoint, elUint(idCueTime, 1), el(idCueTrackPositions, elUint(idCueTrack, 2)))
		data := goodMKV(func(f *mkvFile) { f.cuesRaw = bytes.Repeat(point, maxIndexKeyframes+1) }).build()
		_, _, err, _ := indexOf(data, -1)
		wantLimit(t, err, "elements")
	})

	t.Run("a chain of seekheads", func(t *testing.T) {
		// Sixteen SeekHeads are followed, the first and fifteen more; one more is
		// not. (The numbers are written out, not taken from the constant.)
		kf, _, err, _ := indexOf(goodMKV(func(f *mkvFile) { f.chain = 15 }).build(), -1)
		if err != nil {
			t.Fatalf("a chain of 16 seekheads: %v", err)
		}
		wantTimes(t, kf, every(60, 0, 2)...)
		_, _, err, _ = indexOf(goodMKV(func(f *mkvFile) { f.chain = 16 }).build(), -1)
		wantLimit(t, err, "seekheads")
	})
}

// Cues of a film are megabytes, and the file may be on a network share where a
// read costs as much as a megabyte: they are one read, not one per 64 kB.
func TestMatroskaBigCuesAreOneRead(t *testing.T) {
	// A cue per second for two days: 172800 points, over 5 MB.
	data := goodMKV(func(f *mkvFile) {
		f.cues = f.cues[:0]
		f.duration = 172800 * 1000
		for s := uint64(0); s < 172800; s++ {
			f.cues = append(f.cues, mkvCue{s * 1000, 2})
		}
		// One cluster with one video block, that all the cues point at: nothing of
		// the video after the first block to say the others are not keyframes.
		f.clusters = f.clusters[:1]
		f.clusters[0].blocks = []mkvBlk{{track: 2, key: true, size: 1000}, {track: 1, size: 100}}
	}).build()
	_, size, n, err := parseEBMLHeader(data[bytes.LastIndex(data, ebmlIDBytes(idCues)):])
	if err != nil {
		t.Fatal(err)
	}
	cuesSize := size + int64(n)
	if cuesSize < 4<<20 {
		t.Fatalf("the Cues are only %d bytes", cuesSize)
	}
	kf, _, err, cr := indexOf(data, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(kf) != 172800 {
		t.Fatalf("%d keyframes", len(kf))
	}
	if cr.reads > 6 {
		t.Errorf("%d reads for Cues of %d bytes", cr.reads, cuesSize)
	}
	if cr.bytes > cuesSize+2<<20 {
		t.Errorf("read %d bytes for Cues of %d", cr.bytes, cuesSize)
	}
}
