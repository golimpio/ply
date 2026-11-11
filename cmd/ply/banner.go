package main

import (
	"fmt"
	"net"
	"os"
	"slices"
	"strings"

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

// banner formats the terminal banner: the ply logo and the connection details.
func banner(name, ver, logPath string, urls []string) string {
	useColor := isTerminal(os.Stderr) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	return formatBanner(name, ver, logPath, urls, useColor)
}

// formatBanner formats the banner, with ANSI styles when useColor.
func formatBanner(name, ver, logPath string, urls []string, useColor bool) string {
	const (
		ansiReset    = "\033[0m"
		ansiBold     = "\033[1m"
		ansiDim      = "\033[2m"
		ansiBoldCyan = "\033[1;36m"
	)
	style := func(code, s string) string {
		if !useColor {
			return s
		}
		return code + s + ansiReset
	}

	lines := []string{
		"",
		style(ansiBoldCyan, "█▀█ █   █ █"),
		style(ansiBoldCyan, "█▀▀ █▄▄ ▀█▀"),
		"",
		style(ansiBold, "Local Media Server") + " " + style(ansiDim, "(v"+ver+")"),
		"",
		style(ansiBold, name),
	}
	for _, u := range urls {
		lines = append(lines, style(ansiBoldCyan, u))
	}
	lines = append(lines,
		"",
		style(ansiBold, "Log file:")+" "+logPath,
		style(ansiDim, "Press Ctrl-C to stop (twice to quit at once)"),
		"",
	)
	return strings.Join(lines, "\n") + "\n"
}
