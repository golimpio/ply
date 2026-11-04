package stream

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

const (
	// mp4MaxEdits is the most edit list entries a "simple" edit list has: an
	// optional empty edit (a delay), then the edit that plays the media.
	mp4MaxEdits = 2

	// A moov box up to this size is read whole, in one read: what is wanted
	// from it is scattered between sample tables of megabytes, and a seek on a
	// network share costs about as much as a megabyte.
	mp4WholeMoov = 8 << 20
)

var errMalformedMP4 = errors.New("mp4: malformed box")

// mp4Box is one ISO BMFF box; a zero typ means it was not found.
type mp4Box struct {
	typ  string
	off  int64 // first byte of the box
	body int64 // first byte of the payload
	end  int64 // one past the last byte of the box
}

func (b mp4Box) size() int64 { return b.end - b.body }

// mp4Track holds the boxes of one trak that matter for timing.
type mp4Track struct {
	hdlr, mdhd, elst, stts, ctts, stss mp4Box
}

// mp4Timing is when a track's samples start, in the movie's timeline.
type mp4Timing struct {
	scale   int64   // media timescale
	shift   float64 // seconds, from a leading empty edit
	media   int64   // media time the edit starts at, in media ticks
	segment int64   // duration of that edit in movie ticks (0: not stated)
	movie   int64   // movie timescale
	cttsAt0 int64   // composition offset of the first sample
}

type mp4Parser struct {
	x     *indexReader
	boxes int
}

// mp4Keyframes reads the keyframes of a video track from the sample tables in
// the moov box of the MP4/MOV file behind x: the sync samples (stss), their
// decode times (stts) and composition offsets (ctts), moved by the edit list.
// The track is the videoIndex-th trak (ffprobe's stream index), or the first
// video one if videoIndex is negative. Only the boxes it needs are read: a
// faststart file and one with moov at the end cost the same, and mdat is
// stepped over.
func mp4Keyframes(ctx context.Context, x *indexReader, videoIndex int) ([]float64, error) {
	p := &mp4Parser{x: x}
	moov, err := p.findMoov(ctx)
	if err != nil {
		return nil, err
	}
	if size := moov.end - moov.off; size <= mp4WholeMoov {
		if err := x.cover(moov.off, size); err != nil {
			return nil, err
		}
	}

	var (
		tracks     []mp4Track
		movieScale int64
	)
	err = p.walk(ctx, moov, func(b mp4Box) error {
		switch b.typ {
		case "mvhd":
			var h [24]byte
			n := min(b.size(), int64(len(h)))
			if err := x.readAt(h[:n], b.body); err != nil {
				return err
			}
			ts, err := timescaleOf(h[:n])
			movieScale = ts
			return err
		case "mvex":
			return errors.New("mp4: fragmented file")
		case "trak":
			t, err := p.readTrack(ctx, b)
			if err != nil {
				return err
			}
			tracks = append(tracks, t)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// The times are relative to the start of the file as ffprobe takes it: the
	// earliest start of the video and audio tracks that have samples, unless a
	// text or data track starts less than a second before them.
	var (
		video    *mp4Track
		vtime    mp4Timing
		av, text = math.Inf(1), math.Inf(1)
	)
	for i := range tracks {
		t := &tracks[i]
		if t.stts.typ == "" || t.mdhd.typ == "" {
			continue
		}
		has, err := p.hasSamples(t.stts)
		if err != nil {
			return nil, err
		}
		if !has {
			continue
		}
		tm, err := p.timing(t, movieScale)
		if err != nil {
			return nil, err
		}
		h, err := p.handler(t.hdlr)
		if err != nil {
			return nil, err
		}
		// Samples before the start of the edit are cut off (an AAC priming
		// delay): they put the start at zero, not before it.
		start := math.Max(tm.start(), 0)
		if h == "vide" || h == "soun" {
			av = math.Min(av, start)
		} else {
			text = math.Min(text, start)
		}
		if video == nil && h == "vide" && (videoIndex < 0 || videoIndex == i) {
			video, vtime = t, tm
		}
	}
	if video == nil {
		return nil, errors.New("mp4: no video track with samples")
	}
	starts := av
	if text < av && av-text < 1 { // the rule of libavformat's update_stream_timings
		starts = text
	}
	kf, err := p.syncTimes(video, vtime)
	if err != nil {
		return nil, err
	}
	for i := range kf {
		kf[i] -= starts
	}
	kf = cleanKeyframes(kf)
	if len(kf) == 0 {
		return nil, errors.New("mp4: no sync samples")
	}
	return kf, nil
}

// start is the time of the first sample, in seconds.
func (m mp4Timing) start() float64 {
	return m.shift + float64(m.cttsAt0-m.media)/float64(m.scale)
}

// box decodes the header of the box at off, which must lie within limit.
func (p *mp4Parser) box(off, limit int64) (mp4Box, error) {
	var h [16]byte
	n := min(int64(len(h)), limit-off)
	if n < 8 {
		return mp4Box{}, io.ErrUnexpectedEOF
	}
	if err := p.x.readAt(h[:n], off); err != nil {
		return mp4Box{}, err
	}
	size, hdr := uint64(binary.BigEndian.Uint32(h[:4])), int64(8)
	switch size {
	case 0: // runs to the end of the enclosing box
		size = uint64(limit - off)
	case 1: // 64-bit size follows
		if n < 16 {
			return mp4Box{}, io.ErrUnexpectedEOF
		}
		size, hdr = binary.BigEndian.Uint64(h[8:16]), 16
	}
	if size < uint64(hdr) || size > uint64(limit-off) {
		return mp4Box{}, errMalformedMP4
	}
	return mp4Box{typ: string(h[4:8]), off: off, body: off + hdr, end: off + int64(size)}, nil
}

// walk calls fn for each child box of parent (a plain container).
func (p *mp4Parser) walk(ctx context.Context, parent mp4Box, fn func(mp4Box) error) error {
	for off := parent.body; off < parent.end; {
		if p.boxes++; p.boxes > maxIndexElements {
			return limitErr("elements")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if n := parent.end - off; n < 8 {
			// QuickTime ends a container with 32 zero bits.
			var tail [8]byte
			if err := p.x.readAt(tail[:n], off); err != nil {
				return err
			}
			if tail != ([8]byte{}) {
				return errMalformedMP4
			}
			return nil
		}
		b, err := p.box(off, parent.end)
		if err != nil {
			return err
		}
		if err := fn(b); err != nil {
			return err
		}
		off = b.end
	}
	return nil
}

// findMoov steps over the top-level boxes (mdat by its size) to moov.
func (p *mp4Parser) findMoov(ctx context.Context) (mp4Box, error) {
	file := mp4Box{end: p.x.size}
	var moov mp4Box
	errFound := errors.New("found")
	err := p.walk(ctx, file, func(b mp4Box) error {
		switch b.typ {
		case "moov":
			moov = b
			return errFound
		case "moof":
			return errors.New("mp4: fragmented file")
		}
		return nil
	})
	if err != nil && err != errFound {
		return mp4Box{}, err
	}
	if moov.typ == "" {
		return mp4Box{}, errors.New("mp4: no moov box")
	}
	return moov, nil
}

// readTrack finds the boxes of a trak without reading their content.
func (p *mp4Parser) readTrack(ctx context.Context, trak mp4Box) (mp4Track, error) {
	var t mp4Track
	err := p.walk(ctx, trak, func(b mp4Box) error {
		switch b.typ {
		case "edts":
			return p.walk(ctx, b, func(c mp4Box) error {
				if c.typ == "elst" {
					t.elst = c
				}
				return nil
			})
		case "mdia":
			return p.walk(ctx, b, func(c mp4Box) error {
				switch c.typ {
				case "mdhd":
					t.mdhd = c
				case "hdlr":
					t.hdlr = c
				case "minf":
					return p.walk(ctx, c, func(d mp4Box) error {
						if d.typ != "stbl" {
							return nil
						}
						return p.walk(ctx, d, func(e mp4Box) error {
							switch e.typ {
							case "stts":
								t.stts = e
							case "ctts":
								t.ctts = e
							case "stss":
								t.stss = e
							}
							return nil
						})
					})
				}
				return nil
			})
		}
		return nil
	})
	return t, err
}

// timescaleOf reads the timescale of an mvhd or mdhd payload.
func timescaleOf(b []byte) (int64, error) {
	var at int
	switch {
	case len(b) >= 4 && b[0] == 0:
		at = 12
	case len(b) >= 4 && b[0] == 1:
		at = 20
	default:
		return 0, errMalformedMP4
	}
	if len(b) < at+4 {
		return 0, errMalformedMP4
	}
	ts := int64(binary.BigEndian.Uint32(b[at:]))
	if ts == 0 {
		return 0, errors.New("mp4: zero timescale")
	}
	return ts, nil
}

func (p *mp4Parser) handler(hdlr mp4Box) (string, error) {
	if hdlr.typ == "" || hdlr.size() < 12 {
		return "", nil
	}
	var h [12]byte
	if err := p.x.readAt(h[:], hdlr.body); err != nil {
		return "", err
	}
	return string(h[8:12]), nil
}

// hasSamples reports whether a stts box describes at least one sample.
func (p *mp4Parser) hasSamples(stts mp4Box) (bool, error) {
	if stts.size() < 16 {
		return false, nil
	}
	var h [16]byte
	if err := p.x.readAt(h[:], stts.body); err != nil {
		return false, err
	}
	return binary.BigEndian.Uint32(h[4:8]) > 0 && binary.BigEndian.Uint32(h[8:12]) > 0, nil
}

// timing reads the media timescale, the edit list and the first composition
// offset of a track. Anything but a plain edit list is an error: the times
// would not be what ffprobe reports.
func (p *mp4Parser) timing(t *mp4Track, movieScale int64) (mp4Timing, error) {
	m := mp4Timing{movie: movieScale}
	var h [32]byte
	n := min(t.mdhd.size(), int64(len(h)))
	if err := p.x.readAt(h[:n], t.mdhd.body); err != nil {
		return m, err
	}
	var err error
	if m.scale, err = timescaleOf(h[:n]); err != nil {
		return m, err
	}

	if t.elst.typ != "" {
		if err := p.edits(t.elst, &m); err != nil {
			return m, err
		}
	}

	if t.ctts.typ != "" && t.ctts.size() >= 16 {
		var c [16]byte
		if err := p.x.readAt(c[:], t.ctts.body); err != nil {
			return m, err
		}
		if binary.BigEndian.Uint32(c[4:8]) > 0 {
			m.cttsAt0 = int64(int32(binary.BigEndian.Uint32(c[12:16])))
		}
	}
	return m, nil
}

// edits reads a simple edit list: [empty edit,] one edit at normal rate.
func (p *mp4Parser) edits(elst mp4Box, m *mp4Timing) error {
	var h [8]byte
	if elst.size() < int64(len(h)) {
		return errMalformedMP4
	}
	if err := p.x.readAt(h[:], elst.body); err != nil {
		return err
	}
	version, count := h[0], int64(binary.BigEndian.Uint32(h[4:8]))
	if count == 0 {
		return nil
	}
	if count > mp4MaxEdits || version > 1 {
		return errors.New("mp4: complex edit list")
	}
	entry := int64(12)
	if version == 1 {
		entry = 20
	}
	if elst.size() < 8+count*entry {
		return errMalformedMP4
	}
	b, err := p.x.readBody(elst.body+8, count*entry, 2*20)
	if err != nil {
		return err
	}
	type edit struct{ duration, media int64 }
	edits := make([]edit, count)
	for i := range edits {
		e := b[int64(i)*entry:]
		var rate uint32
		if version == 1 {
			edits[i] = edit{int64(binary.BigEndian.Uint64(e)), int64(binary.BigEndian.Uint64(e[8:]))}
			rate = binary.BigEndian.Uint32(e[16:])
		} else {
			edits[i] = edit{int64(binary.BigEndian.Uint32(e)), int64(int32(binary.BigEndian.Uint32(e[4:])))}
			rate = binary.BigEndian.Uint32(e[8:])
		}
		if rate != 1<<16 {
			return errors.New("mp4: edit list with a playback rate")
		}
	}
	if len(edits) == 2 {
		if edits[0].media != -1 || m.movie <= 0 || edits[0].duration < 0 {
			return errors.New("mp4: complex edit list")
		}
		m.shift = float64(edits[0].duration) / float64(m.movie)
		edits = edits[1:]
	}
	if edits[0].media < 0 || edits[0].duration < 0 {
		return errors.New("mp4: complex edit list")
	}
	m.media, m.segment = edits[0].media, edits[0].duration
	return nil
}

type mp4Run struct{ count, value int64 }

// readRuns reads the (count, value) pairs of a stts or ctts box. Offsets are
// signed: version 1 allows negative ones and version 0 never has 2^31.
func (p *mp4Parser) readRuns(b mp4Box) ([]mp4Run, error) {
	if b.typ == "" {
		return nil, nil
	}
	if b.size() < 8 {
		return nil, errMalformedMP4
	}
	data, err := p.x.readBody(b.body, b.size(), maxTableBytes)
	if err != nil {
		return nil, err
	}
	n := int64(binary.BigEndian.Uint32(data[4:8]))
	if n > (int64(len(data))-8)/8 {
		return nil, errMalformedMP4
	}
	if n > maxIndexElements {
		return nil, limitErr("elements")
	}
	runs := make([]mp4Run, n)
	for i := range runs {
		e := data[8+8*i:]
		runs[i] = mp4Run{int64(binary.BigEndian.Uint32(e)), int64(int32(binary.BigEndian.Uint32(e[4:])))}
	}
	return runs, nil
}

// runCursor walks forward through runs by sample number (1-based).
type runCursor struct {
	runs  []mp4Run
	i     int
	first int64 // sample number of the first sample of runs[i]
	acc   int64 // sum of count*value of the runs before runs[i]
}

// at moves to the run holding sample n, and returns its value and the sum of
// the values of the samples before n. ok is false past the last sample.
func (c *runCursor) at(n int64) (value, before int64, ok bool) {
	for c.i < len(c.runs) && n >= c.first+c.runs[c.i].count {
		c.first += c.runs[c.i].count
		c.acc += c.runs[c.i].count * c.runs[c.i].value
		c.i++
	}
	if c.i == len(c.runs) {
		return 0, 0, false
	}
	r := c.runs[c.i]
	return r.value, c.acc + (n-c.first)*r.value, true
}

// syncTimes returns the presentation time, in seconds in the movie timeline,
// of every sync sample of the track.
func (p *mp4Parser) syncTimes(t *mp4Track, m mp4Timing) ([]float64, error) {
	stts, err := p.readRuns(t.stts)
	if err != nil {
		return nil, err
	}
	ctts, err := p.readRuns(t.ctts)
	if err != nil {
		return nil, err
	}
	var total, duration int64
	for _, r := range stts {
		total += r.count
		duration += r.count * r.value
		if total > math.MaxInt32 || duration < 0 {
			return nil, errMalformedMP4
		}
	}
	if total == 0 {
		return nil, errors.New("mp4: no samples (fragmented file?)")
	}

	// ffprobe lists the keyframes inside the edit, and a couple past its end
	// that it flags as discarded (an edit of 50 s over a track of 60 s lists 27
	// of 30 keyframes: the last at 52 s). Whether a keyframe at or after the end
	// is listed depends on how far it is, so that is a case for the scan; the
	// edit of a muxer that writes one the length of the track never gets here.
	end := math.Inf(1)
	if m.segment > 0 && m.movie > 0 {
		end = m.shift + float64(m.segment)/float64(m.movie)
	}

	var syncs []int64
	if t.stss.typ == "" { // no table: every sample is a sync sample
		if total > maxIndexKeyframes {
			return nil, limitErr("keyframes")
		}
		syncs = make([]int64, total)
		for i := range syncs {
			syncs[i] = int64(i) + 1
		}
	} else {
		if t.stss.size() < 8 {
			return nil, errMalformedMP4
		}
		data, err := p.x.readBody(t.stss.body, t.stss.size(), maxTableBytes)
		if err != nil {
			return nil, err
		}
		n := int64(binary.BigEndian.Uint32(data[4:8]))
		if n > (int64(len(data))-8)/4 {
			return nil, errMalformedMP4
		}
		if n > maxIndexKeyframes {
			return nil, limitErr("keyframes")
		}
		syncs = make([]int64, n)
		prev := int64(0)
		for i := range syncs {
			syncs[i] = int64(binary.BigEndian.Uint32(data[8+4*i:]))
			if syncs[i] <= prev || syncs[i] > total {
				return nil, fmt.Errorf("mp4: sync sample %d out of order or out of range", syncs[i])
			}
			prev = syncs[i]
		}
	}

	out := make([]float64, 0, len(syncs))
	dts, offs := runCursor{runs: stts, first: 1}, runCursor{runs: ctts, first: 1}
	for _, s := range syncs {
		_, at, ok := dts.at(s)
		if !ok {
			return nil, errMalformedMP4
		}
		off, _, ok := offs.at(s)
		if !ok {
			off = 0
		}
		tm := m.shift + float64(at+off-m.media)/float64(m.scale)
		if tm < 0 {
			return nil, errors.New("mp4: keyframe before the start of the edit")
		}
		if tm > end-1e-6 {
			return nil, errors.New("mp4: edit list ends before a keyframe")
		}
		out = append(out, tm)
	}
	return out, nil
}
