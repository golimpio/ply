package main

import (
	"net"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReachableURLs(t *testing.T) {
	// 1. Wildcard bind (like default :8420): discovery.URLs omits loopback, so
	// reachableURLs must append 127.0.0.1.
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	urls := reachableURLs(ln.Addr())
	if len(urls) == 0 {
		t.Fatal("expected at least 1 reachable URL")
	}
	has127 := false
	for _, u := range urls {
		if strings.HasPrefix(u, "http://127.0.0.1:") {
			has127 = true
			break
		}
	}
	if !has127 {
		t.Errorf("reachableURLs on wildcard did not include loopback: %v", urls)
	}

	// 2. Explicit loopback bind: discovery.URLs already has 127.0.0.1, so
	// reachableURLs must not duplicate it.
	lnLoop, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lnLoop.Close()

	urlsLoop := reachableURLs(lnLoop.Addr())
	count127 := 0
	for _, u := range urlsLoop {
		if strings.HasPrefix(u, "http://127.0.0.1:") {
			count127++
		}
	}
	if count127 != 1 {
		t.Errorf("reachableURLs duplicated loopback on explicit loopback listener: count=%d, urls=%v", count127, urlsLoop)
	}
}

func TestBannerFormattingAndAlignment(t *testing.T) {
	urls := []string{
		"http://192.168.1.10:8420",
		"http://127.0.0.1:8420",
	}
	b := banner("TestServer", "0.1.0", "/path/to/ply.log", urls)

	// Verify required content
	for _, want := range []string{
		"╭", "╮", "╰", "╯", "│", "─",
		"█▀█ █   █ █     local media server",
		"█▀▀ █▄▄ ▀█▀     v0.1.0",
		"Server:    TestServer",
		"Web UI:    http://192.168.1.10:8420",
		"http://127.0.0.1:8420",
		"Log file:  /path/to/ply.log",
		"Press Ctrl-C to stop (twice to quit at once)",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("banner missing %q:\n%s", want, b)
		}
	}

	// Verify every line has the exact same visual rune width
	lines := strings.Split(strings.TrimSuffix(b, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("banner has too few lines: %d", len(lines))
	}
	expectedWidth := utf8.RuneCountInString(lines[0])
	for i, l := range lines {
		w := utf8.RuneCountInString(l)
		if w != expectedWidth {
			t.Errorf("line %d width = %d, want %d: %q", i, w, expectedWidth, l)
		}
	}
}

func TestBannerWidePathExpandsCleanly(t *testing.T) {
	longPath := "/Users/someone/Very/Deep/Path/To/A/Super/Long/Directory/Structure/That/Exceeds/Normal/Width/ply.log"
	urls := []string{"http://127.0.0.1:8420"}
	b := banner("Server", "0.1.0", longPath, urls)

	lines := strings.Split(strings.TrimSuffix(b, "\n"), "\n")
	expectedWidth := utf8.RuneCountInString(lines[0])
	if expectedWidth <= 51 {
		t.Errorf("expected banner width > 51 for long path, got %d", expectedWidth)
	}
	for i, l := range lines {
		w := utf8.RuneCountInString(l)
		if w != expectedWidth {
			t.Errorf("line %d width = %d, want %d: %q", i, w, expectedWidth, l)
		}
	}
}
