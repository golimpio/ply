package stream

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

const tsMetadataFixture = `{"streams":[
	{"codec_type":"video","codec_name":"h264","width":640,"height":360,"extradata":"00000000: 0000 0001 6764 0028 aabb ccdd                 ....gd.(...."},
	{"codec_type":"audio","codec_name":"aac","profile":"LC"}
]}`

func TestMPEGTSMetadataUsesOutputSPS(t *testing.T) {
	got, err := parseMPEGTSHLSInfo([]byte(tsMetadataFixture))
	if err != nil || got.Codecs != "avc1.640028,mp4a.40.2" || got.Width != 640 || got.Height != 360 {
		t.Fatalf("output metadata = %+v, %v", got, err)
	}
	// SPS NAL data, rather than a named profile or an assumed level, determines
	// all three RFC 6381 bytes. Both Annex B start-code sizes are accepted.
	for _, tc := range []struct {
		data []byte
		want string
	}{
		{[]byte{0, 0, 1, 0x67, 0x42, 0xe0, 0x1e}, "avc1.42E01E"},
		{[]byte{0, 0, 0, 1, 0x68, 1, 2, 0, 0, 1, 0x67, 0x4d, 0x40, 0x29}, "avc1.4D4029"},
	} {
		codec, err := annexBAVCCodec(tc.data)
		if err != nil || codec != tc.want {
			t.Fatalf("SPS %x: codec %q, %v; want %s", tc.data, codec, err, tc.want)
		}
	}
	for _, bad := range []string{
		"{",
		strings.Replace(tsMetadataFixture, `"width":640`, `"width":0`, 1),
		strings.Replace(tsMetadataFixture, `"codec_name":"h264"`, `"codec_name":"hevc"`, 1),
		strings.Replace(tsMetadataFixture, "6764", "6864", 1),
		strings.Replace(tsMetadataFixture, "00000000:", "00000010:", 1),
		strings.Replace(tsMetadataFixture, "aabb", "zzzz", 1),
		strings.Replace(tsMetadataFixture, `"profile":"LC"`, `"profile":"HE-AAC"`, 1),
		`{"streams":[{"codec_type":"audio","codec_name":"aac","profile":"LC"}]}`,
	} {
		if info, err := parseMPEGTSHLSInfo([]byte(bad)); err == nil {
			t.Fatalf("invalid output accepted as %+v: %s", info, bad)
		}
	}
}

func metadataProbe(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffprobe")
	if err := writeExecutable(path, "#!/bin/sh\n"+body); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMPEGTSMetadataProbeIsConfiguredAndBounded(t *testing.T) {
	bin := metadataProbe(t, "cat > \"$0.input\"\nprintf '%s\\n' \"$@\" > \"$0.args\"\ncat <<'JSON'\n"+tsMetadataFixture+"\nJSON\n")
	input := bytes.Repeat([]byte{0x47}, metadataInputLimit+1024)
	got, err := probeMPEGTSHLSInfo(t.Context(), bin, bytes.NewReader(input))
	if err != nil || got.Codecs != "avc1.640028,mp4a.40.2" {
		t.Fatalf("configured metadata probe: %+v, %v", got, err)
	}
	read, err := os.ReadFile(bin + ".input")
	if err != nil || len(read) != metadataInputLimit || !bytes.Equal(read, input[:metadataInputLimit]) {
		t.Fatalf("metadata input length=%d, %v; want bounded generated media", len(read), err)
	}
	args, err := os.ReadFile(bin + ".args")
	if err != nil || !strings.Contains(string(args), "-show_data\n") || !strings.Contains(string(args), "-i\npipe:0\n") {
		t.Fatalf("metadata arguments %q, %v", args, err)
	}

	overflow := metadataProbe(t, fmt.Sprintf("printf '%%s' '%s'\n", strings.Repeat("x", metadataOutputLimit+1)))
	if _, err := probeMPEGTSHLSInfo(t.Context(), overflow, strings.NewReader("")); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("unbounded output was not rejected: %v", err)
	}
	failed := metadataProbe(t, "printf 'bad generated media' >&2\nexit 3\n")
	if _, err := probeMPEGTSHLSInfo(t.Context(), failed, strings.NewReader("")); err == nil || !strings.Contains(err.Error(), "bad generated media") {
		t.Fatalf("probe failure was ignored: %v", err)
	}
}

func TestMPEGTSMetadataProbeCancelsAndCanRetry(t *testing.T) {
	bin := metadataProbe(t, "exec sleep 1\n")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := probeMPEGTSHLSInfo(ctx, bin, strings.NewReader(""))
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("probe cancellation: %v after %s", err, time.Since(started))
	}
	// A cancelled request is not a cached media failure: another request can
	// probe the same configured binary and get the actual output metadata.
	if err := writeExecutable(bin, "#!/bin/sh\ncat <<'JSON'\n"+tsMetadataFixture+"\nJSON\n"); err != nil {
		t.Fatal(err)
	}
	got, err := probeMPEGTSHLSInfo(t.Context(), bin, strings.NewReader(""))
	if err != nil || got.Width != 640 {
		t.Fatalf("retry after cancellation: %+v, %v", got, err)
	}
}
