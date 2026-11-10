package main

import (
	"fmt"
	"net"
	"os"
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
	useColor := isTerminal(os.Stderr) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	return formatBanner(name, ver, logPath, urls, useColor)
}

type bannerLine struct {
	plain  string
	styled string
}

// formatBanner formats the banner with optional ANSI styling.
func formatBanner(name, ver, logPath string, urls []string, useColor bool) string {
	const minWidth = 51

	const (
		ansiReset    = "\033[0m"
		ansiBold     = "\033[1m"
		ansiDim      = "\033[2m"
		ansiBoldCyan = "\033[1;36m"
	)

	var lines []bannerLine
	add := func(plain, styled string) {
		if !useColor {
			styled = plain
		}
		lines = append(lines, bannerLine{plain: plain, styled: styled})
	}

	add("", "")
	add(
		"   █▀█ █   █ █     local media server",
		"   "+ansiBoldCyan+"█▀█ █   █ █"+ansiReset+"     "+ansiDim+"local media server"+ansiReset,
	)
	add(
		"   █▀▀ █▄▄ ▀█▀     v"+ver,
		"   "+ansiBoldCyan+"█▀▀ █▄▄ ▀█▀"+ansiReset+"     "+ansiDim+"v"+ver+ansiReset,
	)
	add("", "")
	add(
		"   Server:    "+name,
		"   "+ansiBold+"Server:"+ansiReset+"    "+name,
	)
	if len(urls) > 0 {
		add(
			"   Web UI:    "+urls[0],
			"   "+ansiBold+"Web UI:"+ansiReset+"    "+ansiBoldCyan+urls[0]+ansiReset,
		)
		for _, u := range urls[1:] {
			add(
				"              "+u,
				"              "+ansiBoldCyan+u+ansiReset,
			)
		}
	}
	add("", "")
	add(
		"   Log file:  "+logPath,
		"   "+ansiBold+"Log file:"+ansiReset+"  "+logPath,
	)
	add("", "")
	add(
		"   Press Ctrl-C to stop (twice to quit at once)",
		"   "+ansiDim+"Press Ctrl-C to stop (twice to quit at once)"+ansiReset,
	)

	width := minWidth
	for _, l := range lines {
		if w := utf8.RuneCountInString(l.plain) + 4; w > width {
			width = w
		}
	}

	border := func(s string) string {
		if useColor {
			return ansiDim + s + ansiReset
		}
		return s
	}

	var sb strings.Builder
	sb.WriteString("  " + border("╭"+strings.Repeat("─", width)+"╮") + "\n")
	for _, l := range lines {
		pad := width - utf8.RuneCountInString(l.plain)
		if pad < 0 {
			pad = 0
		}
		sb.WriteString("  " + border("│") + l.styled + strings.Repeat(" ", pad) + border("│") + "\n")
	}
	sb.WriteString("  " + border("╰"+strings.Repeat("─", width)+"╯") + "\n")
	return sb.String()
}
