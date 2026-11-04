package stream

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"sort"
)

// Matroska/WebM element IDs, with their length marker as written in the file.
const (
	idEBML               = 0x1A45DFA3
	idSegment            = 0x18538067
	idSeekHead           = 0x114D9B74
	idSeek               = 0x4DBB
	idSeekID             = 0x53AB
	idSeekPosition       = 0x53AC
	idInfo               = 0x1549A966
	idTimestampScale     = 0x2AD7B1
	idDuration           = 0x4489
	idTracks             = 0x1654AE6B
	idTrackEntry         = 0xAE
	idTrackNumber        = 0xD7
	idTrackType          = 0x83
	idCluster            = 0x1F43B675
	idTimestamp          = 0xE7
	idSimpleBlock        = 0xA3
	idBlockGroup         = 0xA0
	idBlock              = 0xA1
	idCues               = 0x1C53BB6B
	idCuePoint           = 0xBB
	idCueTime            = 0xB3
	idCueTrackPositions  = 0xB7
	idCueTrack           = 0xF7
	idCueClusterPosition = 0xF1
	idReferenceBlock     = 0xFB
	idVoid               = 0xEC
	idCRC32              = 0xBF
	idTags               = 0x1254C367
	idChapters           = 0x1043A770
	idAttachments        = 0x1941A469

	trackTypeVideo    = 1
	trackTypeAudio    = 2
	trackTypeSubtitle = 17
	trackTypeMetadata = 33

	// mkvDefaultScale is TimestampScale when Info does not say: nanoseconds
	// per timestamp tick.
	mkvDefaultScale = 1_000_000
)

// Limits specific to Matroska.
const (
	mkvMaxCuePoint  = 64 << 10 // a CuePoint is a few dozen bytes
	mkvMaxHead      = 4 << 20  // SeekHead, Info or Tracks
	mkvMaxSeekHeads = 16       // SeekHeads followed from other SeekHeads
	mkvCueBuffer    = 64 << 10

	// What is read of clusters, for the start of the file: what the window of
	// the headers holds of the first one and, while it reaches, of the next
	// ones (at most mkvFreeClusters) for no read at all; and if an audio or
	// video track has no block in them, the head of the next cluster, which with
	// its header is a window: one read.
	mkvHeadBytes    = indexWindow - 64
	mkvFreeClusters = 4

	// Cues are read whole, in one read, up to this size (a cue point is a few
	// dozen bytes: a 3-hour film is some 300 kB); mkvCuesAhead is what the
	// first read takes before the size is known.
	mkvWholeCues = 8 << 20
	mkvCuesAhead = 256 << 10

	// Cues sparser than this are not a keyframe index: a median gap over
	// maxMedianGap seconds, or fewer than one cue per minCueSpacing seconds.
	// The median gap is above x264 and x265's default longest GOP, 250
	// frames: 10.43 s at 23.976 fps.
	maxMedianGap  = 12.0
	minCueSpacing = 30.0
)

// mkvTopLevel are the IDs that end an unknown-size Cluster.
var mkvTopLevel = map[uint32]bool{
	idCluster: true, idCues: true, idSeekHead: true, idInfo: true, idTracks: true,
	idTags: true, idChapters: true, idAttachments: true,
}

var errMalformedEBML = errors.New("matroska: malformed element")

// ebmlID decodes an element ID; n is its length, 0 when b does not start with
// a valid one.
func ebmlID(b []byte) (id uint32, n int) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0
	}
	n = bits.LeadingZeros8(b[0]) + 1
	if n > 4 || len(b) < n {
		return 0, 0
	}
	for _, c := range b[:n] {
		id = id<<8 | uint32(c)
	}
	return id, n
}

// ebmlSize decodes a data size; n is its length (0 when invalid) and size is
// -1 for the reserved "unknown" value.
func ebmlSize(b []byte) (size int64, n int) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0
	}
	n = bits.LeadingZeros8(b[0]) + 1
	if len(b) < n {
		return 0, 0
	}
	mask := byte(1)<<(8-n) - 1 // zero for n == 8
	size = int64(b[0] & mask)
	unknown := b[0]&mask == mask
	for _, c := range b[1:n] {
		size = size<<8 | int64(c)
		unknown = unknown && c == 0xFF
	}
	if unknown {
		return -1, n
	}
	return size, n
}

// parseEBMLHeader decodes the ID and data size at the start of b; n is the
// length of both.
func parseEBMLHeader(b []byte) (id uint32, size int64, n int, err error) {
	id, k := ebmlID(b)
	if k == 0 {
		return 0, 0, 0, errMalformedEBML
	}
	size, m := ebmlSize(b[k:])
	if m == 0 {
		return 0, 0, 0, errMalformedEBML
	}
	return id, size, k + m, nil
}

func ebmlUint(b []byte) (uint64, bool) {
	if len(b) > 8 {
		return 0, false
	}
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v, true
}

func ebmlFloat(b []byte) (float64, bool) {
	switch len(b) {
	case 0:
		return 0, true
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b))), true
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(b)), true
	}
	return 0, false
}

// mkvLoc is where an element lives; off < 0 means it has not been seen.
type mkvLoc struct {
	off, body, size int64 // size is -1 for unknown
}

type mkvTrack struct{ number, typ uint64 }

type mkvParser struct {
	x               *indexReader
	segData, segEnd int64
	info, tracks    mkvLoc
	cues, cluster   mkvLoc           // cluster is the first one
	seek            map[uint32]int64 // element ID -> offset, from the SeekHeads
	pending         []int64          // SeekHeads named by SeekHeads, still to read
	read            map[int64]bool   // SeekHeads read
	elements        int
}

// matroskaKeyframes reads the keyframes of a video track from the Cues of the
// Matroska/WebM file behind x, without reading any cluster payload. The track
// is the videoIndex-th (ffprobe stream index), or the first video track if
// videoIndex is negative.
func matroskaKeyframes(ctx context.Context, x *indexReader, videoIndex int) ([]float64, error) {
	p := &mkvParser{
		x: x, seek: map[uint32]int64{}, read: map[int64]bool{},
		info: mkvLoc{off: -1}, tracks: mkvLoc{off: -1}, cues: mkvLoc{off: -1}, cluster: mkvLoc{off: -1},
	}

	id, size, n, err := p.header(0, false)
	if err != nil {
		return nil, err
	}
	if id != idEBML || size < 0 || size > x.size-int64(n) {
		return nil, errors.New("matroska: no EBML header")
	}
	off := int64(n) + size
	if id, size, n, err = p.header(off, false); err != nil {
		return nil, err
	}
	if id != idSegment {
		return nil, errors.New("matroska: no Segment")
	}
	p.segData, p.segEnd = off+int64(n), x.size
	if size >= 0 && size < p.segEnd-p.segData {
		p.segEnd = p.segData + size
	}

	if err := p.scan(ctx); err != nil {
		return nil, err
	}
	if err := p.find(&p.info, idInfo, 0); err != nil {
		return nil, fmt.Errorf("matroska: no Info: %w", err)
	}
	if err := p.find(&p.tracks, idTracks, 0); err != nil {
		return nil, fmt.Errorf("matroska: no Tracks: %w", err)
	}
	if p.cluster.off < 0 {
		return nil, errors.New("matroska: no Cluster")
	}

	scale, duration, err := p.readInfo()
	if err != nil {
		return nil, err
	}
	tracks, err := p.readTracks()
	if err != nil {
		return nil, err
	}
	video := uint64(0)
	for i, t := range tracks {
		if t.typ == trackTypeVideo && t.number != 0 && (videoIndex < 0 || videoIndex == i) {
			video = t.number
			break
		}
	}
	if video == 0 {
		return nil, errors.New("matroska: no such video track")
	}

	// The first cluster is read for the start of the file: its header, and what
	// the window already holds of its content, with no read of its own. So are
	// the clusters after it, as far as the window reaches (small ones, or small
	// blocks at their start). If that showed fewer than two video keyframes (a
	// cluster of one GOP, of more than a window, shows one), the head of the next
	// cluster is read: one read. The second keyframe is where cues that skip every
	// other one show. The blocks that are there also give the start of the file
	// (whatever track comes first in them) and check the cues; a check that took
	// more reads is left to the first fragments (planguard.go).
	first, err := p.readCluster(ctx, p.cluster.off, clusterHere)
	if err != nil {
		return nil, err
	}
	loaded := []mkvCluster{first}
	more := func(mode clusterMode) (bool, error) {
		next := loaded[len(loaded)-1].next
		if next < 0 || next >= p.segEnd {
			return false, nil
		}
		c, err := p.readCluster(ctx, next, mode)
		if errors.Is(err, errNotCluster) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		loaded = append(loaded, c)
		return true, nil
	}
	for len(loaded) < mkvFreeClusters {
		if ok, err := more(clusterFree); err != nil {
			return nil, err
		} else if !ok {
			break
		}
	}
	if keyframesOf(loaded, video) < 2 {
		if _, err := more(clusterHop); err != nil {
			return nil, err
		}
	}
	start, err := startOf(loaded, tracks, scale)
	if err != nil {
		return nil, err
	}

	if err := p.find(&p.cues, idCues, mkvCuesAhead); err != nil {
		return nil, fmt.Errorf("matroska: no Cues: %w", err)
	}
	cues, err := p.readCues(ctx, video)
	if err != nil {
		return nil, err
	}
	if len(cues) == 0 {
		return nil, errors.New("matroska: no cue points for the video track")
	}
	sort.SliceStable(cues, func(i, j int) bool { return cues[i].time < cues[j].time })

	// Do the cues list the keyframes that are there? Compare them with the
	// blocks of the clusters read, which are the first keyframe or two of the
	// file, whatever the muxer wrote.
	for _, c := range loaded {
		if err := crossCheck(c, video, cues); err != nil {
			return nil, err
		}
	}

	kf := make([]float64, len(cues))
	for i, c := range cues {
		kf[i] = float64(c.time-start) * scale / 1e9
	}
	kf = cleanKeyframes(kf)
	if err := checkDense(kf, duration*scale/1e9, float64(start)*scale/1e9); err != nil {
		return nil, err
	}
	return kf, nil
}

// checkDense rejects cue lists too sparse to be a keyframe index: some muxers
// write one cue per few clusters, not one per keyframe. The scan then finds
// the real ones. kf are the keyframe times relative to the start of the file,
// start is where that is (seconds) and duration the Info Duration in seconds
// (0 when it has none).
func checkDense(kf []float64, duration, start float64) error {
	if len(kf) == 0 {
		return errors.New("matroska: no cue points for the video track")
	}
	// A stretch of video at either end with no cue is keyframes missing, not a
	// long GOP.
	if kf[0] > 2*maxMedianGap {
		return fmt.Errorf("matroska: the first cue is at %.1f s: the keyframes before it are missing", kf[0])
	}
	last := kf[len(kf)-1]
	if duration > 0 && duration-(last+start) > 2*maxMedianGap {
		return fmt.Errorf("matroska: the last cue is %.1f s before the end: the keyframes after it are missing", duration-(last+start))
	}
	if duration < last {
		duration = last
	}
	if float64(len(kf)) < duration/minCueSpacing {
		return fmt.Errorf("matroska: %d cue points in %.0f s are too sparse for a keyframe index", len(kf), duration)
	}
	gaps := make([]float64, len(kf)-1)
	for i := range gaps {
		gaps[i] = kf[i+1] - kf[i]
	}
	sort.Float64s(gaps)
	if len(gaps) > 0 && gaps[len(gaps)/2] > maxMedianGap {
		return fmt.Errorf("matroska: median cue gap %.1f s is too sparse for a keyframe index", gaps[len(gaps)/2])
	}
	return nil
}

// header reads the ID and size of the element at off. hop says the next read
// will be somewhere else (stepping from cluster to cluster), so no read-ahead.
func (p *mkvParser) header(off int64, hop bool) (id uint32, size int64, n int, err error) {
	var b [12]byte
	m := min(int64(len(b)), p.x.size-off)
	if m < 2 {
		return 0, 0, 0, io.ErrUnexpectedEOF
	}
	read := p.x.readAt
	if hop {
		read = p.x.readSmall
	}
	if err := read(b[:m], off); err != nil {
		return 0, 0, 0, err
	}
	return parseEBMLHeader(b[:m])
}

// count charges one element against the budget, and checks for cancellation.
func (p *mkvParser) count(ctx context.Context) error {
	if p.elements++; p.elements > maxIndexElements {
		return limitErr("elements")
	}
	return ctx.Err()
}

// scan walks the top-level elements of the Segment, stepping over clusters by
// their size, until it knows where Info, Tracks and Cues are.
func (p *mkvParser) scan(ctx context.Context) error {
	for pos := p.segData; pos < p.segEnd; {
		if err := p.count(ctx); err != nil {
			return err
		}
		// Past the first cluster the elements are far apart.
		id, size, n, err := p.header(pos, p.cluster.off >= 0)
		if err != nil {
			if errors.Is(err, errIndexLimit) {
				return err
			}
			return nil // garbage or the end of a truncated file: work with what was found
		}
		body := pos + int64(n)
		loc := mkvLoc{off: pos, body: body, size: size}
		switch id {
		case idSeekHead:
			if err := p.readSeekHead(pos); err != nil {
				return err
			}
		case idInfo:
			setLoc(&p.info, loc)
		case idTracks:
			setLoc(&p.tracks, loc)
		case idCues:
			setLoc(&p.cues, loc)
		case idCluster:
			setLoc(&p.cluster, loc)
			// A SeekHead at the end of the file is a jump away; stepping over
			// every cluster to the Cues is not.
			if err := p.drainSeekHeads(); err != nil {
				return err
			}
			if p.located() {
				return nil
			}
		}
		if size < 0 {
			return nil // an unknown-size element cannot be stepped over
		}
		pos = body + size
	}
	return nil
}

func setLoc(dst *mkvLoc, l mkvLoc) {
	if dst.off < 0 {
		*dst = l
	}
}

// known reports whether the element is located, or a SeekHead says where.
func (p *mkvParser) known(l *mkvLoc, id uint32) bool {
	_, ok := p.seek[id]
	return l.off >= 0 || ok
}

// located reports whether Info, Tracks and Cues are all known.
func (p *mkvParser) located() bool {
	return p.known(&p.info, idInfo) && p.known(&p.tracks, idTracks) && p.known(&p.cues, idCues)
}

// drainSeekHeads reads the SeekHeads that other SeekHeads named, until
// everything is located or none are left.
func (p *mkvParser) drainSeekHeads() error {
	for !p.located() && len(p.pending) > 0 {
		next := p.pending[0]
		p.pending = p.pending[1:]
		if err := p.readSeekHead(next); err != nil {
			return err
		}
	}
	return nil
}

// find locates the element, following SeekHeads that name other SeekHeads.
// ahead is how much to read at the element where a SeekHead says it is: the
// element is going to be read, and its header should not cost a read of its own.
func (p *mkvParser) find(l *mkvLoc, id uint32, ahead int64) error {
	for l.off < 0 {
		if off, ok := p.seek[id]; ok {
			if ahead > 0 {
				if err := p.x.cover(off, min(ahead, p.x.size-off)); err != nil {
					return err
				}
			}
			got, size, n, err := p.header(off, false)
			if err != nil {
				return err
			}
			if got != id {
				return errors.New("SeekHead points at another element")
			}
			*l = mkvLoc{off: off, body: off + int64(n), size: size}
			break
		}
		if len(p.pending) == 0 {
			return errors.New("not in the file or its SeekHeads")
		}
		next := p.pending[0]
		p.pending = p.pending[1:]
		if err := p.readSeekHead(next); err != nil {
			return err
		}
	}
	return nil
}

// readSeekHead reads the SeekHead at off: where other top-level elements are.
func (p *mkvParser) readSeekHead(off int64) error {
	if p.read[off] {
		return nil
	}
	if len(p.read) >= mkvMaxSeekHeads {
		return limitErr("seekheads")
	}
	p.read[off] = true
	id, size, n, err := p.header(off, false)
	if err != nil {
		return err
	}
	if id != idSeekHead {
		return errors.New("SeekHead points at another element")
	}
	body, err := p.x.readBody(off+int64(n), size, mkvMaxHead)
	if err != nil {
		return err
	}
	return p.walk(body, func(id uint32, b []byte) error {
		if id != idSeek {
			return nil
		}
		var target uint32
		pos, havePos := int64(0), false
		err := p.walk(b, func(id uint32, b []byte) error {
			switch id {
			case idSeekID:
				if len(b) == 0 || len(b) > 4 {
					return errMalformedEBML
				}
				target = 0
				for _, c := range b {
					target = target<<8 | uint32(c)
				}
			case idSeekPosition:
				v, ok := ebmlUint(b)
				if !ok {
					return errMalformedEBML
				}
				// A position nowhere near the file is ignored, like one past its end.
				pos, havePos = int64(v), v <= math.MaxInt64>>1
			}
			return nil
		})
		if err != nil || target == 0 || !havePos {
			return err
		}
		abs := p.segData + pos
		if abs >= p.x.size {
			return nil // points outside the file; maybe a damaged copy
		}
		if target == idSeekHead {
			p.pending = append(p.pending, abs)
		} else if _, ok := p.seek[target]; !ok {
			p.seek[target] = abs
		}
		return nil
	})
}

// walk calls fn for each child element in b; an unknown-size child runs to the
// end of b.
func (p *mkvParser) walk(b []byte, fn func(id uint32, body []byte) error) error {
	for len(b) > 0 {
		if p.elements++; p.elements > maxIndexElements {
			return limitErr("elements")
		}
		id, size, n, err := parseEBMLHeader(b)
		if err != nil {
			return err
		}
		b = b[n:]
		var body []byte
		switch {
		case size < 0:
			body, b = b, nil
		case size > int64(len(b)):
			return errMalformedEBML
		default:
			body, b = b[:size], b[size:]
		}
		if err := fn(id, body); err != nil {
			return err
		}
	}
	return nil
}

func (p *mkvParser) body(l mkvLoc) ([]byte, error) {
	if l.size < 0 {
		return nil, errors.New("matroska: unknown-size element where a known size is needed")
	}
	return p.x.readBody(l.body, l.size, mkvMaxHead)
}

// readInfo returns the TimestampScale (nanoseconds per tick) and the Duration
// (ticks, 0 if absent).
func (p *mkvParser) readInfo() (scale, duration float64, err error) {
	b, err := p.body(p.info)
	if err != nil {
		return 0, 0, err
	}
	scale = mkvDefaultScale
	err = p.walk(b, func(id uint32, b []byte) error {
		switch id {
		case idTimestampScale:
			v, ok := ebmlUint(b)
			if !ok || v == 0 {
				return errors.New("matroska: bad TimestampScale")
			}
			scale = float64(v)
		case idDuration:
			v, ok := ebmlFloat(b)
			if !ok {
				return errMalformedEBML
			}
			if v > 0 && !math.IsInf(v, 0) {
				duration = v
			}
		}
		return nil
	})
	return scale, duration, err
}

func (p *mkvParser) readTracks() ([]mkvTrack, error) {
	b, err := p.body(p.tracks)
	if err != nil {
		return nil, err
	}
	var tracks []mkvTrack
	err = p.walk(b, func(id uint32, b []byte) error {
		if id != idTrackEntry {
			return nil
		}
		var t mkvTrack
		err := p.walk(b, func(id uint32, b []byte) error {
			switch id {
			case idTrackNumber:
				t.number, _ = ebmlUint(b)
			case idTrackType:
				t.typ, _ = ebmlUint(b)
			}
			return nil
		})
		tracks = append(tracks, t) // one per stream, so the index is ffprobe's stream index
		return err
	})
	return tracks, err
}

// cueReader streams the body of the Cues element through the metered reader.
type cueReader struct {
	x        *indexReader
	off, end int64
}

func (c *cueReader) Read(b []byte) (int, error) {
	if c.off >= c.end {
		return 0, io.EOF
	}
	b = b[:min(int64(len(b)), c.end-c.off)]
	if err := c.x.readAt(b, c.off); err != nil {
		return 0, err
	}
	c.off += int64(len(b))
	return len(b), nil
}

// cueEntry is a cue point of the video track.
type cueEntry struct {
	time int64 // ticks
}

// readCues returns the cue points of the track, in file order.
func (p *mkvParser) readCues(ctx context.Context, track uint64) ([]cueEntry, error) {
	l := p.cues
	end := p.segEnd
	if l.size >= 0 {
		if l.size > p.x.size-l.body {
			return nil, errors.New("matroska: Cues are cut off")
		}
		end = l.body + l.size
	}
	whole := int64(mkvWholeCues)
	if l.size < 0 {
		whole = mkvCuesAhead // no telling how far they go
	}
	if err := p.x.cover(l.body, min(whole, end-l.body)); err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(&cueReader{x: p.x, off: l.body, end: end}, mkvCueBuffer)
	var cues []cueEntry
	for {
		if err := p.count(ctx); err != nil {
			return nil, err
		}
		head, err := br.Peek(12)
		if len(head) == 0 {
			if err == io.EOF {
				return cues, nil
			}
			return nil, err
		}
		id, size, n, herr := parseEBMLHeader(head)
		if l.size < 0 && (herr != nil || id != idCuePoint && id != idVoid && id != idCRC32) {
			return cues, nil // unknown-size Cues end at the first element that is not theirs
		}
		if herr != nil {
			return nil, herr
		}
		_, _ = br.Discard(n)
		if size < 0 || size > math.MaxInt32 {
			return nil, errMalformedEBML
		}
		if id != idCuePoint {
			if _, err := br.Discard(int(size)); err != nil {
				return nil, noEOF(err)
			}
			continue
		}
		if size > mkvMaxCuePoint {
			return nil, errors.New("matroska: oversized CuePoint")
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(br, body); err != nil {
			return nil, noEOF(err)
		}
		c, ok, err := p.cuePoint(body, track)
		if err != nil {
			return nil, err
		}
		if ok {
			cues = append(cues, c)
		}
	}
}

// noEOF turns the end of the stream inside an element into an error.
func noEOF(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// cuePoint returns the cue in body if it indexes track.
func (p *mkvParser) cuePoint(body []byte, track uint64) (cue cueEntry, ok bool, err error) {
	var t uint64
	haveTime, onTrack := false, false
	err = p.walk(body, func(id uint32, b []byte) error {
		switch id {
		case idCueTime:
			t, haveTime = ebmlUint(b)
		case idCueTrackPositions:
			return p.walk(b, func(id uint32, b []byte) error {
				if v, ok := ebmlUint(b); id == idCueTrack && ok && v == track {
					onTrack = true
				}
				return nil
			})
		}
		return nil
	})
	if t > math.MaxInt64>>2 {
		return cue, false, errMalformedEBML
	}
	cue.time = int64(t)
	return cue, haveTime && onTrack, err
}

// mkvBlock is a block of a track, with its absolute timestamp.
type mkvBlock struct {
	track uint64
	time  int64 // ticks
	key   bool
	known bool // whether key is: a BlockGroup cut off before it says is not
}

// mkvCluster is what was read of one cluster: its blocks, in file order.
type mkvCluster struct {
	off    int64
	ts     int64 // the Cluster's Timestamp
	blocks []mkvBlock
	next   int64 // where the element after it starts; -1 if the size is unknown
}

var errNotCluster = errors.New("matroska: not a Cluster")

// clusterMode says how much readCluster may read.
type clusterMode int

const (
	clusterHere clusterMode = iota // the header (a read, if the window is elsewhere) and the content the window holds
	clusterFree                    // only what the window holds already: no read
	clusterHop                     // the header and the first mkvHeadBytes of the content: one read
)

// readCluster reads the Cluster at off and lists the blocks of what it got of
// it. A block that the end of what was read cuts short counts if its header is
// there: that has the track, the timestamp and, for a SimpleBlock, the keyframe
// flag.
func (p *mkvParser) readCluster(ctx context.Context, off int64, mode clusterMode) (mkvCluster, error) {
	c := mkvCluster{off: off, next: -1}
	if mode == clusterFree && !p.x.windowed(off, 12) {
		return c, errNotCluster // not for nothing
	}
	id, size, hn, err := p.header(off, false)
	if errors.Is(err, errMalformedEBML) || err == nil && id != idCluster {
		return c, errNotCluster
	}
	if err != nil {
		return c, err
	}
	end := p.segEnd
	if size >= 0 {
		c.next = off + int64(hn) + size
		end = min(end, c.next)
	}
	content := off + int64(hn)
	var n int64
	if mode == clusterHop {
		n = min(mkvHeadBytes, end-content)
		if err := p.x.cover(off, int64(hn)+n); err != nil {
			return c, err
		}
	} else {
		n = min(end-content, p.x.windowEnd()-content)
	}
	region, err := p.x.readBody(content, max(n, 0), indexWindow)
	if err != nil {
		return c, err
	}

	haveTS := false
parse:
	for len(region) > 0 {
		if err := p.count(ctx); err != nil {
			return c, err
		}
		cid, csize, chn, err := parseEBMLHeader(region)
		if err != nil || csize < 0 || mkvTopLevel[cid] {
			break // cut inside a header, or the end of an unknown-size cluster
		}
		body := region[chn:]
		whole := csize <= int64(len(body))
		if whole {
			body = body[:csize]
		}
		switch cid {
		case idTimestamp:
			v, ok := ebmlUint(body)
			if !whole || !ok || v > math.MaxInt64>>2 {
				return c, errMalformedEBML
			}
			c.ts, haveTS = int64(v), true
		case idSimpleBlock:
			track, rel, flags, ok := blockOf(body)
			if !ok {
				break parse
			}
			if !haveTS {
				return c, errors.New("matroska: block before the Cluster timestamp")
			}
			c.blocks = append(c.blocks, mkvBlock{track, c.ts + int64(rel), flags&0x80 != 0, true})
		case idBlockGroup:
			if !whole {
				// Whether it refers to another block is behind the cut, but its Block,
				// the first child, may be here: that has the track and the time.
				if bid, _, bn, err := parseEBMLHeader(body); err == nil && bid == idBlock {
					if track, rel, _, ok := blockOf(body[bn:]); ok && haveTS {
						c.blocks = append(c.blocks, mkvBlock{track: track, time: c.ts + int64(rel)})
					}
				}
				break parse
			}
			var (
				track     uint64
				rel       int16
				have, ref bool
			)
			if err := p.walk(body, func(id uint32, b []byte) error {
				switch id {
				case idBlock:
					if t, r, _, ok := blockOf(b); ok {
						track, rel, have = t, r, true
					}
				case idReferenceBlock:
					ref = true
				}
				return nil
			}); err != nil {
				return c, err
			}
			if have {
				if !haveTS {
					return c, errors.New("matroska: block before the Cluster timestamp")
				}
				c.blocks = append(c.blocks, mkvBlock{track, c.ts + int64(rel), !ref, true}) // a keyframe refers to nothing
			}
		}
		if !whole {
			break
		}
		region = region[chn+int(csize):]
	}
	return c, nil
}

// blockOf decodes the track number, the timestamp relative to the cluster and
// the flags at the start of a Block or SimpleBlock body.
func blockOf(b []byte) (track uint64, rel int16, flags byte, ok bool) {
	t, n := ebmlSize(b)
	if n == 0 || t <= 0 || len(b) < n+3 {
		return 0, 0, 0, false
	}
	return uint64(t), int16(binary.BigEndian.Uint16(b[n:])), b[n+2], true
}

// keyframesOf counts the keyframes of the video track in the blocks read.
func keyframesOf(cs []mkvCluster, video uint64) int {
	n := 0
	for _, c := range cs {
		for _, b := range c.blocks {
			if b.track == video && b.known && b.key {
				n++
			}
		}
	}
	return n
}

// startOf is where ffprobe puts the start of the file, which keyframe times
// are relative to: the earliest block of the audio and video tracks, in ticks.
// A subtitle or data track counts only when it starts less than a second
// before them; one that starts earlier is an outlier and is ignored (the
// rule of libavformat's update_stream_timings).
func startOf(cs []mkvCluster, tracks []mkvTrack, scale float64) (int64, error) {
	typ := map[uint64]uint64{}
	left := map[uint64]bool{} // audio and video tracks with no block yet
	for _, t := range tracks {
		typ[t.number] = t.typ
		if t.typ == trackTypeVideo || t.typ == trackTypeAudio {
			left[t.number] = true
		}
	}
	av, text := int64(math.MaxInt64), int64(math.MaxInt64)
blocks:
	for _, c := range cs {
		for _, b := range c.blocks {
			switch typ[b.track] {
			case trackTypeVideo, trackTypeAudio:
				av = min(av, b.time)
				delete(left, b.track)
			case trackTypeSubtitle, trackTypeMetadata:
				text = min(text, b.time)
			}
			if len(left) == 0 {
				break blocks
			}
		}
	}
	hasAV, hasText := av != math.MaxInt64, text != math.MaxInt64
	switch {
	case !hasAV && !hasText:
		return 0, errors.New("matroska: no block to take the start of the file from")
	case !hasAV:
		return text, nil
	case hasText && av > text && float64(av-text)*scale < 1e9:
		return text, nil
	}
	return av, nil
}

// crossCheck compares the cues with the video blocks that were read of one
// cluster. Every keyframe block must have a cue, and every cue within the
// stretch of video that was read must be on a keyframe block: cues that skip
// keyframes, or add others, would give a plan that disagrees with the
// fragments ffmpeg writes. Cues beyond what was read are not judged.
func crossCheck(c mkvCluster, video uint64, cues []cueEntry) error {
	var keys []int64
	latest, any := int64(math.MinInt64), false
	for _, b := range c.blocks {
		if b.track != video || !b.known {
			continue
		}
		any = true
		latest = max(latest, b.time)
		if b.key {
			keys = append(keys, b.time)
		}
	}
	if !any {
		return nil
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys {
		if i := sort.Search(len(cues), func(i int) bool { return cues[i].time >= k-1 }); i == len(cues) || cues[i].time > k+1 {
			return fmt.Errorf("matroska: the keyframe at tick %d has no cue", k)
		}
	}
	for i := sort.Search(len(cues), func(i int) bool { return cues[i].time >= c.ts }); i < len(cues) && cues[i].time <= latest; i++ {
		t := cues[i].time
		if j := sort.Search(len(keys), func(j int) bool { return keys[j] >= t-1 }); j == len(keys) || keys[j] > t+1 {
			return fmt.Errorf("matroska: the cue at tick %d is not on a keyframe", t)
		}
	}
	return nil
}
