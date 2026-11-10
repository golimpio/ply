package main

import (
	"fmt"
	"net"
	"slices"
	"strings"
	"unicode/utf8"

	"ply/internal/discovery"
)

// reachableURLs returns the URLs clients can use to access ply.
// It includes LAN URLs from discovery.URLs, and appends loopback (127.0.0.1)
// if it is not already included.
func reachableURLs(addr net.Addr) []string {
	urls := discovery.URLs(addr)
	if tcp, ok := addr.(*net.TCPAddr); ok {
		local := fmt.Sprintf("http://127.0.0.1:%d", tcp.Port)
		if !slices.Contains(urls, local) {
			urls = append(urls, local)
		}
	}
	return urls
}

// banner formats a clean terminal banner with the ply logo and connection details.
func banner(name, ver, logPath string, urls []string) string {
	const minWidth = 51

	var lines []string
	lines = append(lines, "")
	lines = append(lines, "   █▀█ █   █ █     local media server")
	lines = append(lines, "   █▀▀ █▄▄ ▀█▀     v"+ver)
	lines = append(lines, "")
	lines = append(lines, "   Server:    "+name)
	if len(urls) > 0 {
		lines = append(lines, "   Web UI:    "+urls[0])
		for _, u := range urls[1:] {
			lines = append(lines, "              "+u)
		}
	}
	lines = append(lines, "")
	lines = append(lines, "   Log file:  "+logPath)
	lines = append(lines, "")
	lines = append(lines, "   Press Ctrl-C to stop (twice to quit at once)")

	width := minWidth
	for _, l := range lines {
		if w := utf8.RuneCountInString(l) + 4; w > width {
			width = w
		}
	}

	var sb strings.Builder
	sb.WriteString("  ╭" + strings.Repeat("─", width) + "╮\n")
	for _, l := range lines {
		pad := width - utf8.RuneCountInString(l)
		if pad < 0 {
			pad = 0
		}
		sb.WriteString("  │" + l + strings.Repeat(" ", pad) + "│\n")
	}
	sb.WriteString("  ╰" + strings.Repeat("─", width) + "╯\n")
	return sb.String()
}
