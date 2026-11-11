package stream

import (
	"context"
	"encoding/binary"
	"testing"
)

func hlsDescriptor(tag byte, payload []byte) []byte {
	var length [4]byte
	n := len(payload)
	i := 3
	length[i] = byte(n & 0x7f)
	for n >>= 7; n > 0; n >>= 7 {
		i--
		length[i] = byte(n&0x7f) | 0x80
	}
	return cat([]byte{tag}, length[i:], payload)
}

func hlsAVC(width, height uint16, config []byte) []byte {
	header := make([]byte, 78)
	binary.BigEndian.PutUint16(header[24:], width)
	binary.BigEndian.PutUint16(header[26:], height)
	return isoBox("avc1", header, isoBox("avcC", config))
}

func hlsAAC(asc []byte) []byte {
	config := cat([]byte{0x40}, make([]byte, 12), hlsDescriptor(5, asc))
	es := hlsDescriptor(3, cat([]byte{0, 1, 0}, hlsDescriptor(4, config)))
	return isoBox("mp4a", make([]byte, 28), isoFull("esds", 0, es))
}

func hlsInit(entries ...[]byte) []byte {
	var tracks [][]byte
	for _, entry := range entries {
		tracks = append(tracks, isoBox("trak", isoBox("mdia", isoBox("minf",
			isoBox("stbl", isoFull("stsd", 0, u32(1), entry))))))
	}
	return cat(ftyp, isoBox("moov", tracks...))
}

func TestHLSInfoUsesOutputConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config []byte
		audio  []byte
		width  uint16
		height uint16
		codecs string
	}{
		{"high level 4.1 with AAC LC", []byte{1, 0x64, 0, 0x29}, hlsAAC([]byte{0x12, 0x10}), 1920, 1080, "avc1.640029,mp4a.40.2"},
		{"scaled baseline with HE AAC", []byte{1, 0x42, 0xc0, 0x1e}, hlsAAC([]byte{0x2b, 0x92}), 640, 360, "avc1.42C01E,mp4a.40.5"},
		{"silent movie", []byte{1, 0x4d, 0x40, 0x28}, nil, 1280, 720, "avc1.4D4028"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := [][]byte{hlsAVC(tc.width, tc.height, tc.config)}
			if tc.audio != nil {
				entries = append(entries, tc.audio)
			}
			got, err := readHLSInfo(t.Context(), hlsInit(entries...))
			if err != nil {
				t.Fatal(err)
			}
			if got.Codecs != tc.codecs || got.Width != int(tc.width) || got.Height != int(tc.height) {
				t.Fatalf("metadata = %+v; want codecs %s, %dx%d", got, tc.codecs, tc.width, tc.height)
			}
		})
	}
}

func TestHLSInfoRejectsMalformedConfiguration(t *testing.T) {
	good := hlsInit(hlsAVC(640, 360, []byte{1, 0x64, 0, 0x28}), hlsAAC([]byte{0x12, 0x10}))
	if _, err := readHLSInfo(t.Context(), good); err != nil {
		t.Fatal("positive control:", err)
	}
	for name, data := range map[string][]byte{
		"truncated box":        good[:len(good)-1],
		"missing video":        hlsInit(hlsAAC([]byte{0x12, 0x10})),
		"missing avc config":   hlsInit(isoBox("avc1", make([]byte, 78))),
		"short avc config":     hlsInit(hlsAVC(640, 360, []byte{1, 0x64, 0})),
		"zero dimensions":      hlsInit(hlsAVC(0, 360, []byte{1, 0x64, 0, 0x28})),
		"missing audio config": hlsInit(hlsAVC(640, 360, []byte{1, 0x64, 0, 0x28}), isoBox("mp4a", make([]byte, 28))),
		"invalid audio config": hlsInit(hlsAVC(640, 360, []byte{1, 0x64, 0, 0x28}), hlsAAC([]byte{0, 0})),
		"unsupported codec":    hlsInit(isoBox("hvc1", make([]byte, 78))),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readHLSInfo(t.Context(), data); err == nil {
				t.Fatal("malformed init accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readHLSInfo(ctx, good); err == nil {
		t.Fatal("cancelled parser succeeded")
	}
}

func TestHLSAACDescriptorBoundaries(t *testing.T) {
	// Optional ES descriptor fields can contain bytes that look like descriptors.
	// They must be skipped, never scanned for the first accidental 0x04/0x05.
	config := cat([]byte{0x40}, make([]byte, 12), hlsDescriptor(5, []byte{0x12, 0x10}))
	payload := cat([]byte{0, 1, 0xe0, 0, 4, 3, 4, 5, 6, 0, 5}, hlsDescriptor(4, config))
	good := hlsDescriptor(3, payload)
	if codec, err := aacHLSCodec(good); err != nil || codec != "mp4a.40.2" {
		t.Fatalf("optional ES fields: %s, %v", codec, err)
	}
	// Four-byte descriptor sizes are emitted by ffmpeg even for short payloads.
	long := cat([]byte{3, 0x80, 0x80, 0x80, byte(len(payload))}, payload)
	if codec, err := aacHLSCodec(long); err != nil || codec != "mp4a.40.2" {
		t.Fatalf("four-byte length: %s, %v", codec, err)
	}
	for _, bad := range [][]byte{
		good[:2], {3, 0x80, 0x80, 0x80, 0x80, 0}, {3, 127, 0},
		hlsDescriptor(3, []byte{0, 1, 0x40, 0xff}),
		hlsDescriptor(3, cat([]byte{0, 1, 0}, hlsDescriptor(4, []byte{0x40}))),
	} {
		if _, err := aacHLSCodec(bad); err == nil {
			t.Fatalf("invalid descriptor accepted: %x", bad)
		}
	}
}
