package main

import (
	"net"
	"strings"
	"testing"
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

// The banner is the logo, the name and version, the server's name and every URL,
// one per line, then the log file and the hint, with no box and no indent.
func TestFormatBannerLayout(t *testing.T) {
	urls := []string{"http://192.168.1.10:8420", "http://127.0.0.1:8420"}
	got := formatBanner("TestServer", "0.1.0", "/path/to/ply.log", urls, false)
	want := `█▀█ █   █ █
█▀▀ █▄▄ ▀█▀

Local Media Server (v0.1.0)

TestServer
http://192.168.1.10:8420
http://127.0.0.1:8420

Log file: /path/to/ply.log
Press Ctrl-C to stop (twice to quit at once)

`
	if got != want {
		t.Fatalf("banner:\n%s\nwant:\n%s", got, want)
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
		"\033[2m",    // dim (version, hint)
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
}
