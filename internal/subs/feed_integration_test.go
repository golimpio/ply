//go:build integration && unix

package subs

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// relayout writes the Matroska file in with its top-level elements in the order of the IDs
// given (Info, Tracks, Cluster for every Cluster, Cues...), under a SeekHead that points at
// them: the file an editor or a tool that appends leaves, which ffmpeg reads when it can seek
// to where the SeekHead says.
func relayout(t *testing.T, in []byte, order ...uint32) []byte {
	t.Helper()
	head := func(b []byte) (id uint32, size, n int) {
		id, k := ebmlID(b)
		sz, m := ebmlSize(b[k:])
		if k == 0 || m == 0 || sz < 0 {
			t.Fatalf("relayout: no element at %x", b[:8])
		}
		return id, int(sz), k + m
	}
	_, size, n := head(in)
	ebml := in[:n+size]
	_, _, n = head(in[len(ebml):])
	body := in[len(ebml)+n:]
	byID := map[uint32][][]byte{}
	for len(body) > 0 {
		id, size, n := head(body)
		byID[id] = append(byID[id], body[:n+size])
		body = body[n+size:]
	}
	seekHead := func(pos map[uint32]int) []byte {
		var entries []byte
		for _, id := range []uint32{idInfo, idTracks, 0x1C53BB6B} {
			if _, ok := byID[id]; !ok {
				continue
			}
			idb := []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
			p := pos[id]
			inner := append([]byte{0x53, 0xAB, 0x80 | 4}, idb...)
			inner = append(inner, 0x53, 0xAC, 0x80|4, byte(p>>24), byte(p>>16), byte(p>>8), byte(p))
			entries = append(entries, 0x4D, 0xBB, 0x80|byte(len(inner)))
			entries = append(entries, inner...)
		}
		return append([]byte{0x11, 0x4D, 0x9B, 0x74, 0x01, 0, 0, 0, 0, byte(len(entries) >> 16), byte(len(entries) >> 8), byte(len(entries))}, entries...)
	}
	pos, at := map[uint32]int{}, len(seekHead(nil))
	var elems []byte
	for _, id := range order {
		for _, raw := range byID[id] {
			if _, ok := pos[id]; !ok {
				pos[id] = at
			}
			elems = append(elems, raw...)
			at += len(raw)
		}
	}
	seg := append(seekHead(pos), elems...)
	out := append(append([]byte{}, ebml...), 0x18, 0x53, 0x80, 0x67, 0x01)
	return append(out, append(sizeBytes(len(seg)), seg...)...)
}

// sizeBytes is the last seven bytes of an eight-byte size.
func sizeBytes(n int) []byte {
	return []byte{byte(n >> 48), byte(n >> 40), byte(n >> 32), byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
}

// A Matroska file whose Tracks come after a Cluster is read by ffmpeg through the SeekHead
// and cannot be read from a pipe ("Stream map ” matches no streams"). With the real ffmpeg:
// the fixture is that file (a control: through a pipe it does fail, and directly it works),
// and the Generator, which must not feed it, gets the subtitles in one run of ffmpeg.
func TestAFileWithItsTracksAfterAClusterIsReadByRealFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	var srt strings.Builder
	for i := range 20 {
		fmt.Fprintf(&srt, "%d\n00:00:%02d,100 --> 00:00:%02d,600\ncue %d\n\n", i+1, i, i, i+1)
	}
	sub := filepath.Join(dir, "a.srt")
	if err := os.WriteFile(sub, []byte(srt.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := filepath.Join(dir, "orig.mkv")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=10:duration=20",
		"-i", sub, "-map", "0", "-map", "1", "-c:v", "libx264", "-preset", "ultrafast", "-g", "10", "-c:s", "srt", orig).CombinedOutput(); err != nil {
		t.Fatalf("making the fixture: %v\n%s", err, out)
	}
	b, err := os.ReadFile(orig)
	if err != nil {
		t.Fatal(err)
	}
	late := filepath.Join(dir, "late.mkv")
	if err := os.WriteFile(late, relayout(t, b, idInfo, idCluster, idTracks, 0x1C53BB6B), 0o644); err != nil {
		t.Fatal(err)
	}

	read := func(in string, pipe bool) error {
		args := []string{"-nostdin", "-v", "error", "-y", "-i", in, "-map", "0:1", "-c:s", "webvtt", "-f", "webvtt", filepath.Join(dir, "x.vtt")}
		cmd := exec.Command(ffmpeg, args...)
		if pipe {
			args[5] = "pipe:0"
			cmd = exec.Command(ffmpeg, args...)
			cmd.Stdin = bytes.NewReader(mustRead(t, in))
		}
		return cmd.Run()
	}
	if err := read(orig, true); err != nil { // control: a muxer's file goes through a pipe
		t.Fatalf("the original through a pipe: %v", err)
	}
	if err := read(late, false); err != nil { // control: the late file is a good one
		t.Fatalf("the late file read directly: %v", err)
	}
	if err := read(late, true); err == nil { // control: and a pipe does not do for it
		t.Fatal("the late file went through a pipe: the fixture is not the file this test is about")
	}

	wrapper, log := fakeFFmpeg(t, "exec "+ffmpeg+" \"$@\"\n")
	g := newFakeGen(t, wrapper)
	for i, c := range []struct{ name, in string }{{"orig", orig}, {"late", late}} {
		p, err := g.Path(t.Context(), Source{ItemID: int64(i + 1), Key: "1", Input: c.in, Stream: 1, MTime: 1})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got, _ := os.ReadFile(p); !bytes.Contains(got, []byte("cue 15")) {
			t.Errorf("%s: the subtitles lack their cues: %q", c.name, got)
		}
	}
	r := runs(t, log)
	if len(r) != 2 || !strings.Contains(r[0], "-i pipe:0 ") || !strings.Contains(r[1], "-i "+late+" ") {
		t.Errorf("ffmpeg was run as %q: want the original on a pipe, and the late file (once) opened by ffmpeg", r)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
