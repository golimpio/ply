package subs

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// FNORDAHEAD, the flag F_RDAHEAD sets on an open file, which F_GETFL reports.
const fNoReadAhead = 0x80000

// The kernel's reading ahead, which keeps seconds of a slow share's link in flight
// where nothing can take them back, is off on the file the feed reads. (What it cost a
// play was measured on the NAS: docs/M9-ARCHITECTURE.md, item 4; this is the switch.)
func TestTheFeedReadsWithoutTheKernelReadingAhead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.mkv")
	if err := os.WriteFile(path, film(1000), 0o644); err != nil {
		t.Fatal(err)
	}
	plain, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if flags, err := unix.FcntlInt(plain.Fd(), unix.F_GETFL, 0); err != nil || flags&fNoReadAhead != 0 { // positive control
		t.Fatalf("a file as os.Open gives it: flags %#x, %v; want reading ahead on", flags, err)
	}

	fd := openFeed(path)
	if fd == nil {
		t.Fatal("a Matroska file is not fed")
	}
	defer fd.Close()
	if flags, err := unix.FcntlInt(fd.f.Fd(), unix.F_GETFL, 0); err != nil || flags&fNoReadAhead == 0 {
		t.Errorf("the fed file: flags %#x, %v; want reading ahead off", flags, err)
	}
}
