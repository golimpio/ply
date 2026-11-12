package stream

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// HLSInfo describes the generated stream, which may differ from the source after
// scaling or transcoding. Its codec values are RFC 6381 identifiers.
type HLSInfo struct {
	Codecs        string
	Width, Height int
}

// HLSInfo reads generated media metadata, never the source film.
func (s *Session) HLSInfo(ctx context.Context) (HLSInfo, error) {
	if s.MPEGTS() {
		return s.mpegTSHLSInfo(ctx)
	}
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

func (s *Session) mpegTSHLSInfo(ctx context.Context) (HLSInfo, error) {
	if err := ctx.Err(); err != nil {
		return HLSInfo{}, err
	}
	if s.stopping() {
		return HLSInfo{}, ErrClosed
	}
	if cached := s.hlsInfo.Load(); cached != nil {
		return *cached, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-s.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	release := s.Hold()
	defer release()
	if _, err := s.Playlist(ctx, ""); err != nil {
		return HLSInfo{}, err
	}
	media, err := s.OpenSegment(ctx, s.Plan().SegmentAt(s.req.StartSec))
	if err != nil {
		return HLSInfo{}, err
	}
	defer media.Close()
	info, err := probeMPEGTSHLSInfo(ctx, s.m.o.FFprobe, media)
	if s.stopping() {
		return HLSInfo{}, ErrClosed
	}
	if err == nil && s.req.AudioIndex >= 0 && !strings.Contains(info.Codecs, "mp4a.40.2") {
		return HLSInfo{}, errors.New("HLS metadata probe did not find the selected audio")
	}
	if err == nil {
		// Later master requests must not rewind an active producer to the resume segment.
		s.hlsInfo.CompareAndSwap(nil, &info)
	}
	return info, err
}

const (
	metadataInputLimit  = 4 << 20
	metadataOutputLimit = 64 << 10
)

// Only a bounded prefix of a completed generated segment is inspected. The
// open Media retains its files across a concurrent seek or cache pruning.
func probeMPEGTSHLSInfo(ctx context.Context, ffprobe string, media io.Reader) (HLSInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-f", "mpegts",
		"-probesize", strconv.Itoa(metadataInputLimit), "-analyzeduration", "1000000",
		"-show_entries", "stream=codec_type,codec_name,width,height,profile,extradata",
		"-show_data", "-of", "json", "-i", "pipe:0")
	cmd.Stdin = io.LimitReader(media, metadataInputLimit)
	var stderr tailBuffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return HLSInfo{}, err
	}
	if err := cmd.Start(); err != nil {
		return HLSInfo{}, fmt.Errorf("start HLS metadata probe: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, metadataOutputLimit+1))
	overflow := len(data) > metadataOutputLimit
	if overflow || readErr != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	switch {
	case overflow:
		return HLSInfo{}, errors.New("HLS metadata probe output is too large")
	case ctx.Err() != nil:
		return HLSInfo{}, ctx.Err()
	case readErr != nil:
		return HLSInfo{}, readErr
	case waitErr != nil:
		return HLSInfo{}, fmt.Errorf("HLS metadata probe: %w: %s", waitErr, stderr.String())
	}
	return parseMPEGTSHLSInfo(data)
}

func parseMPEGTSHLSInfo(data []byte) (HLSInfo, error) {
	var result struct {
		Streams []struct {
			Type      string `json:"codec_type"`
			Codec     string `json:"codec_name"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			Profile   string `json:"profile"`
			ExtraData string `json:"extradata"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return HLSInfo{}, fmt.Errorf("HLS metadata JSON: %w", err)
	}
	var info HLSInfo
	videoCodec, audioCodec := "", ""
	for _, track := range result.Streams {
		switch track.Type {
		case "video":
			if videoCodec != "" || track.Codec != "h264" || track.Width <= 0 || track.Height <= 0 {
				return HLSInfo{}, errors.New("HLS transport stream has invalid AVC video")
			}
			config, err := probeHexData(track.ExtraData)
			if err != nil {
				return HLSInfo{}, err
			}
			videoCodec, err = annexBAVCCodec(config)
			if err != nil {
				return HLSInfo{}, err
			}
			info.Width, info.Height = track.Width, track.Height
		case "audio":
			if audioCodec != "" || track.Codec != "aac" || track.Profile != "LC" {
				return HLSInfo{}, fmt.Errorf("HLS transport stream has invalid AAC-LC audio: codec %q, profile %q", track.Codec, track.Profile)
			}
			audioCodec = "mp4a.40.2"
		}
	}
	if videoCodec == "" {
		return HLSInfo{}, errors.New("HLS transport stream has no AVC video")
	}
	info.Codecs = videoCodec
	if audioCodec != "" {
		info.Codecs += "," + audioCodec
	}
	return info, nil
}

// ffprobe prints extradata as offset-prefixed hex groups followed by ASCII.
func probeHexData(dump string) ([]byte, error) {
	var data []byte
	for line := range strings.SplitSeq(strings.TrimSpace(dump), "\n") {
		offset, body, ok := strings.Cut(line, ":")
		n, err := strconv.ParseUint(strings.TrimSpace(offset), 16, 32)
		if !ok || err != nil || n != uint64(len(data)) {
			return nil, errors.New("invalid HLS codec hex data")
		}
		body = strings.TrimLeft(body, " ")
		hexPart, _, _ := strings.Cut(body, "  ")
		part, err := hex.DecodeString(strings.Join(strings.Fields(hexPart), ""))
		if err != nil || len(part) == 0 {
			return nil, errors.New("invalid HLS codec hex data")
		}
		data = append(data, part...)
	}
	return data, nil
}

func annexBAVCCodec(data []byte) (string, error) {
	for i := 0; i+5 < len(data); i++ {
		if data[i] != 0 || data[i+1] != 0 {
			continue
		}
		start := i + 2
		if data[start] == 0 {
			start++
		}
		if start+4 >= len(data) || data[start] != 1 || data[start+1]&0x9f != 7 {
			continue
		}
		return fmt.Sprintf("avc1.%02X%02X%02X", data[start+2], data[start+3], data[start+4]), nil
	}
	return "", errors.New("HLS transport stream has no AVC SPS")
}
