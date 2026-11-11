package stream

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

// HLSInfo describes the generated stream, which may differ from the source after
// scaling or transcoding. Its codec values are RFC 6381 identifiers.
type HLSInfo struct {
	Codecs        string
	Width, Height int
}

// HLSInfo reads the generated init segment, never the source film.
func (s *Session) HLSInfo(ctx context.Context) (HLSInfo, error) {
	f, err := s.OpenInit(ctx)
	if err != nil {
		return HLSInfo{}, err
	}
	defer f.Close()
	const maxInit = 4 << 20
	data, err := io.ReadAll(io.LimitReader(f, maxInit+1))
	if err != nil {
		return HLSInfo{}, err
	}
	if len(data) > maxInit {
		return HLSInfo{}, errors.New("HLS init segment is too large")
	}
	return readHLSInfo(ctx, data)
}

func readHLSInfo(ctx context.Context, data []byte) (HLSInfo, error) {
	p := &mp4Parser{x: &indexReader{r: bytes.NewReader(data), size: int64(len(data))}}
	moov, err := p.findMoov(ctx)
	if err != nil {
		return HLSInfo{}, err
	}
	var info HLSInfo
	var codecs []string
	err = p.walk(ctx, moov, func(trak mp4Box) error {
		if trak.typ != "trak" {
			return nil
		}
		stsd, err := p.hlsSampleDescription(ctx, trak)
		if err != nil || stsd.typ == "" {
			return err
		}
		var head [8]byte
		if stsd.size() < 8 {
			return errMalformedMP4
		}
		if err := p.x.readAt(head[:], stsd.body); err != nil {
			return err
		}
		if binary.BigEndian.Uint32(head[4:]) != 1 {
			return errors.New("HLS init must have one sample description per track")
		}
		entry, err := p.box(stsd.body+8, stsd.end)
		if err != nil {
			return err
		}
		switch entry.typ {
		case "avc1", "avc3":
			if entry.size() < 78 || info.Width != 0 {
				return errMalformedMP4
			}
			var dimensions [4]byte
			if err := p.x.readAt(dimensions[:], entry.body+24); err != nil {
				return err
			}
			info.Width = int(binary.BigEndian.Uint16(dimensions[:2]))
			info.Height = int(binary.BigEndian.Uint16(dimensions[2:]))
			if info.Width == 0 || info.Height == 0 {
				return errMalformedMP4
			}
			var codec string
			err := p.walk(ctx, mp4Box{body: entry.body + 78, end: entry.end}, func(b mp4Box) error {
				if b.typ != "avcC" {
					return nil
				}
				var config [4]byte
				if b.size() < 4 {
					return errMalformedMP4
				}
				if err := p.x.readAt(config[:], b.body); err != nil {
					return err
				}
				if config[0] != 1 {
					return errMalformedMP4
				}
				codec = fmt.Sprintf("%s.%02X%02X%02X", entry.typ, config[1], config[2], config[3])
				return nil
			})
			if err != nil {
				return err
			}
			if codec == "" {
				return errors.New("HLS AVC configuration is missing")
			}
			codecs = append(codecs, codec)
		case "mp4a":
			if entry.size() < 28 {
				return errMalformedMP4
			}
			audioFound := false
			err := p.walk(ctx, mp4Box{body: entry.body + 28, end: entry.end}, func(b mp4Box) error {
				if b.typ != "esds" {
					return nil
				}
				if b.size() < 4 || b.size() > 64<<10 {
					return errMalformedMP4
				}
				config := make([]byte, b.size())
				if err := p.x.readAt(config, b.body); err != nil {
					return err
				}
				codec, err := aacHLSCodec(config[4:])
				if err != nil {
					return err
				}
				codecs = append(codecs, codec)
				audioFound = true
				return nil
			})
			if err != nil {
				return err
			}
			if !audioFound {
				return errors.New("HLS AAC configuration is missing")
			}
		default:
			return fmt.Errorf("unsupported HLS sample entry %q", entry.typ)
		}
		return nil
	})
	if err != nil {
		return HLSInfo{}, err
	}
	if info.Width == 0 {
		return HLSInfo{}, errors.New("HLS init has no AVC video")
	}
	info.Codecs = strings.Join(codecs, ",")
	return info, nil
}

func (p *mp4Parser) hlsSampleDescription(ctx context.Context, trak mp4Box) (mp4Box, error) {
	parent := trak
	for _, typ := range []string{"mdia", "minf", "stbl", "stsd"} {
		var found mp4Box
		if err := p.walk(ctx, parent, func(b mp4Box) error {
			if b.typ == typ {
				found = b
			}
			return nil
		}); err != nil {
			return mp4Box{}, err
		}
		if found.typ == "" {
			return mp4Box{}, nil
		}
		parent = found
	}
	return parent, nil
}

// descriptor consumes one MPEG-4 descriptor, whose length uses up to four
// continuation bytes. All reads remain inside the enclosing descriptor.
func descriptor(data []byte, want byte) ([]byte, error) {
	if len(data) < 2 || data[0] != want {
		return nil, errMalformedMP4
	}
	n := 0
	for i := 1; i <= 4 && i < len(data); i++ {
		n = n<<7 | int(data[i]&0x7f)
		if data[i]&0x80 == 0 {
			if n > len(data)-i-1 {
				return nil, errMalformedMP4
			}
			return data[i+1 : i+1+n], nil
		}
	}
	return nil, errMalformedMP4
}

func aacHLSCodec(data []byte) (string, error) {
	es, err := descriptor(data, 3)
	if err != nil || len(es) < 3 {
		return "", errMalformedMP4
	}
	flags, at := es[2], 3
	if flags&0x80 != 0 {
		at += 2
	}
	if flags&0x40 != 0 {
		if at >= len(es) {
			return "", errMalformedMP4
		}
		at += 1 + int(es[at])
	}
	if flags&0x20 != 0 {
		at += 2
	}
	if at > len(es) {
		return "", errMalformedMP4
	}
	decoder, err := descriptor(es[at:], 4)
	if err != nil || len(decoder) < 13 || decoder[0] != 0x40 {
		return "", errMalformedMP4
	}
	asc, err := descriptor(decoder[13:], 5)
	if err != nil || len(asc) < 2 {
		return "", errMalformedMP4
	}
	objectType := int(asc[0] >> 3)
	if objectType == 31 {
		objectType = 32 + (int(asc[0]&7) << 3) + int(asc[1]>>5)
	}
	if objectType == 0 {
		return "", errMalformedMP4
	}
	return fmt.Sprintf("mp4a.40.%d", objectType), nil
}
