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

func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\033' {
			inEsc = true
			continue
		}
		if inEsc {
			if (s[i] >= 'a' && s[i] <= 'z') || (s[i] >= 'A' && s[i] <= 'Z') {
				inEsc = false
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func TestFormatBannerColor(t *testing.T) {
	urls := []string{"http://192.168.1.100:8420"}

	// Uncolored: no ANSI escape codes
	uncolored := formatBanner("Server", "0.1.0", "/path/to/ply.log", urls, false)
	if strings.Contains(uncolored, "\033[") {
		t.Errorf("uncolored banner should not contain ANSI escape codes:\n%s", uncolored)
	}

	// Colored: positive control — MUST contain ANSI escape codes
	colored := formatBanner("Server", "0.1.0", "/path/to/ply.log", urls, true)
	if !strings.Contains(colored, "\033[") {
		t.Fatalf("positive control: colored banner MUST contain ANSI escape codes:\n%s", colored)
	}
	for _, code := range []string{
		"\033[1;36m", // bold cyan (logo, URLs)
		"\033[1m",    // bold (labels)
		"\033[2m",    // dim (hints, box borders)
		"\033[0m",    // reset
	} {
		if !strings.Contains(colored, code) {
			t.Errorf("colored banner missing ANSI code %q", code)
		}
	}
	if !strings.Contains(colored, "Server") || !strings.Contains(colored, "http://192.168.1.100:8420") {
		t.Errorf("colored banner missing content:\n%s", colored)
	}

	// Stripped colored banner should be identical to uncolored banner
	stripped := stripANSI(colored)
	if strings.Contains(stripped, "\033[") {
		t.Error("stripANSI failed to remove all escape sequences")
	}
	if stripped != uncolored {
		t.Errorf("stripped colored banner differs from uncolored banner:\nGot:\n%s\nWant:\n%s", stripped, uncolored)
	}

	// Alignment: every line in the visual display must have identical rune width
	lines := strings.Split(strings.TrimSuffix(stripped, "\n"), "\n")
	expectedWidth := utf8.RuneCountInString(lines[0])
	for i, l := range lines {
		w := utf8.RuneCountInString(l)
		if w != expectedWidth {
			t.Errorf("line %d visual width = %d, want %d: %q", i, w, expectedWidth, l)
		}
	}
}
