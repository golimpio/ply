package stream

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func mkBox(typ string, payload ...byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b, uint32(len(b)))
	copy(b[4:], typ)
	copy(b[8:], payload)
	return b
}

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func writeExecutable(path, body string) error { return os.WriteFile(path, []byte(body), 0o755) }

func TestCheckFile(t *testing.T) {
	styp, moof, mdat := mkBox("styp", 1, 2, 3, 4), mkBox("moof", 9, 9, 9), mkBox("mdat", make([]byte, 50)...)
	good := cat(styp, moof, mdat)
	large := cat(styp, moof, []byte{0, 0, 0, 1}, []byte("mdat"), []byte{0, 0, 0, 0, 0, 0, 0, 24}, make([]byte, 8)) // 64-bit size

	tests := []struct {
		name string
		kind string
		data []byte
		ok   bool
		off  int64
	}{
		{"complete fragment", "gop", good, true, int64(len(styp))},
		{"64-bit mdat size", "gop", large, true, int64(len(styp))},
		{"without styp", "gop", cat(moof, mdat), true, 0},
		{"truncated mdat", "gop", good[:len(good)-1], false, 0},
		{"only header of mdat", "gop", good[:len(styp)+len(moof)+8], false, 0},
		{"missing mdat", "gop", cat(styp, moof), false, 0},
		{"missing moof", "gop", cat(styp, mdat), false, 0},
		{"garbage", "gop", bytes.Repeat([]byte{0xff}, 64), false, 0},
		{"box claims more than there is", "gop", cat(styp, moof, mdat[:4], []byte{0xff, 0xff}), false, 0},
		{"empty", "gop", nil, false, 0},
		{"init", "init", cat(mkBox("ftyp", 1, 2, 3, 4), mkBox("moov", 5, 6, 7, 8)), true, 0},
		{"init without moov", "init", cat(mkBox("ftyp", 1, 2, 3, 4), mkBox("free", 5, 6, 7, 8)), false, 0},
		{"init truncated", "init", cat(mkBox("ftyp", 1, 2, 3, 4), mkBox("moov", 5, 6, 7, 8))[:20], false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "f")
			if err := os.WriteFile(p, tt.data, 0o644); err != nil {
				t.Fatal(err)
			}
			info, ok := checkFile(p, tt.kind)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && (info.mediaOff != tt.off || info.size != int64(len(tt.data))) {
				t.Errorf("info = %+v, want mediaOff %d size %d", info, tt.off, len(tt.data))
			}
		})
	}
	if _, ok := checkFile(filepath.Join(t.TempDir(), "missing"), "gop"); ok {
		t.Error("missing file accepted")
	}
	if _, ok := checkFile(t.TempDir(), "gop"); ok {
		t.Error("directory accepted")
	}
}

func TestPiecesReadSeek(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) *os.File {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	p := &pieces{}
	p.add(write("a", "AAAA0123456789"), 4, 10) // "0123456789"
	p.add(write("b", "xxBBBBB"), 2, 5)         // "BBBBB"
	p.add(write("c", "CC"), 0, 2)              // "CC"
	defer p.Close()

	const all = "0123456789BBBBBCC"
	if p.Size() != int64(len(all)) {
		t.Fatalf("Size = %d, want %d", p.Size(), len(all))
	}
	got, err := io.ReadAll(p)
	if err != nil || string(got) != all {
		t.Fatalf("ReadAll = %q, %v", got, err)
	}
	// Every (offset, length) window reads back the right bytes.
	for off := 0; off <= len(all); off++ {
		for n := 0; off+n <= len(all); n++ {
			if _, err := p.Seek(int64(off), io.SeekStart); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, n)
			if _, err := io.ReadFull(p, buf); err != nil || string(buf) != all[off:off+n] {
				t.Fatalf("window [%d,%d) = %q, %v", off, off+n, buf, err)
			}
		}
	}
	if pos, _ := p.Seek(-2, io.SeekEnd); pos != int64(len(all))-2 {
		t.Errorf("SeekEnd -2 = %d", pos)
	}
	if rest, _ := io.ReadAll(p); string(rest) != "CC" {
		t.Errorf("tail = %q", rest)
	}
	if _, err := p.Seek(-1, io.SeekStart); err == nil {
		t.Error("negative seek accepted")
	}
}
