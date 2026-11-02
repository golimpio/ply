package library

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFingerprint(t *testing.T) {
	dir := t.TempDir()
	big := bytes.Repeat([]byte("0123456789abcdef"), 3*fingerprintChunk/16) // 192 KiB
	mk := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	fp := func(p string) string {
		t.Helper()
		s, err := fingerprint(p)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	orig := mk("orig.mkv", big)
	copied := mk("copy.mkv", append([]byte(nil), big...))
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(copied, old, old); err != nil {
		t.Fatal(err)
	}
	if fp(orig) != fp(copied) {
		t.Error("the same content under another name and mtime has another fingerprint")
	}

	changed := func(name string, at int) string {
		b := append([]byte(nil), big...)
		b[at] ^= 1
		return mk(name, b)
	}
	if fp(changed("head.mkv", 10)) == fp(orig) {
		t.Error("a change in the first 64 KiB is not seen")
	}
	if fp(changed("tail.mkv", len(big)-10)) == fp(orig) {
		t.Error("a change in the last 64 KiB is not seen")
	}
	if fp(mk("longer.mkv", append(append([]byte(nil), big...), big[:fingerprintChunk]...))) == fp(orig) {
		t.Error("a longer file with the same head and tail pattern has the same fingerprint")
	}

	small := mk("small.mkv", []byte("tiny"))
	if fp(small) == fp(mk("small2.mkv", []byte("tinz"))) {
		t.Error("small files that differ have the same fingerprint")
	}
	if fp(small) != fp(mk("small3.mkv", []byte("tiny"))) {
		t.Error("equal small files differ")
	}
	if _, err := fingerprint(filepath.Join(dir, "missing.mkv")); err == nil {
		t.Error("a missing file has a fingerprint")
	}
}
