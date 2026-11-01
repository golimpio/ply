package stream

import (
	"encoding/binary"
	"io"
	"os"
)

type box struct {
	typ  string
	off  int64
	size int64
}

// topBoxes lists the top-level ISO BMFF boxes of f. complete is true only
// when the boxes tile the whole file exactly, which a half-written file
// never does.
func topBoxes(f io.ReaderAt, size int64) (boxes []box, complete bool) {
	var hdr [16]byte
	for off := int64(0); off < size; {
		if size-off < 8 {
			return boxes, false
		}
		if _, err := f.ReadAt(hdr[:8], off); err != nil {
			return boxes, false
		}
		n := int64(binary.BigEndian.Uint32(hdr[:4]))
		switch n {
		case 0: // extends to the end of the file
			n = size - off
		case 1:
			if _, err := f.ReadAt(hdr[8:16], off+8); err != nil {
				return boxes, false
			}
			n = int64(binary.BigEndian.Uint64(hdr[8:16]))
		}
		if n < 8 || off+n > size {
			return boxes, false
		}
		boxes = append(boxes, box{typ: string(hdr[4:8]), off: off, size: n})
		off += n
	}
	return boxes, true
}

func hasBox(boxes []box, typ string) (box, bool) {
	for _, b := range boxes {
		if b.typ == typ {
			return b, true
		}
	}
	return box{}, false
}

// fileInfo is a validated fMP4 file on disk.
type fileInfo struct {
	path string
	size int64
	// mediaOff is where the media data starts: the first moof of a GOP
	// fragment (so a styp can be dropped), 0 for an init segment.
	mediaOff int64
}

// checkFile validates the file at path: kind "init" needs ftyp and moov,
// "gop" needs moof and mdat, and the boxes must cover the file exactly.
func checkFile(path, kind string) (fileInfo, bool) {
	f, err := os.Open(path)
	if err != nil {
		return fileInfo{}, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() < 16 {
		return fileInfo{}, false
	}
	boxes, ok := topBoxes(f, fi.Size())
	if !ok {
		return fileInfo{}, false
	}
	info := fileInfo{path: path, size: fi.Size()}
	switch kind {
	case "init":
		if _, ok := hasBox(boxes, "ftyp"); !ok {
			return fileInfo{}, false
		}
		if _, ok := hasBox(boxes, "moov"); !ok {
			return fileInfo{}, false
		}
	default:
		moof, ok := hasBox(boxes, "moof")
		if !ok {
			return fileInfo{}, false
		}
		if _, ok := hasBox(boxes, "mdat"); !ok {
			return fileInfo{}, false
		}
		info.mediaOff = moof.off
	}
	return info, true
}

// pieces is an io.ReadSeekCloser over the concatenation of file sections.
type pieces struct {
	files []*os.File
	secs  []*io.SectionReader
	size  int64
	pos   int64
}

func (p *pieces) add(f *os.File, off, n int64) {
	p.files = append(p.files, f)
	p.secs = append(p.secs, io.NewSectionReader(f, off, n))
	p.size += n
}

func (p *pieces) Size() int64 { return p.size }

func (p *pieces) Read(b []byte) (int, error) {
	if p.pos >= p.size {
		return 0, io.EOF
	}
	// Find the section containing pos. Segments hold a handful of pieces.
	rel := p.pos
	for _, s := range p.secs {
		if rel < s.Size() {
			n, err := s.ReadAt(b, rel)
			p.pos += int64(n)
			if err == io.EOF && n > 0 {
				err = nil
			}
			return n, err
		}
		rel -= s.Size()
	}
	return 0, io.EOF
}

func (p *pieces) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = p.pos + offset
	case io.SeekEnd:
		abs = p.size + offset
	default:
		return 0, os.ErrInvalid
	}
	if abs < 0 {
		return 0, os.ErrInvalid
	}
	p.pos = abs
	return abs, nil
}

func (p *pieces) Close() error {
	var first error
	for _, f := range p.files {
		if err := f.Close(); err != nil && first == nil {
			first = err
		}
	}
	p.files = nil
	return first
}
