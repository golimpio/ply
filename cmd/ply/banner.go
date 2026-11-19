package main

import (
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"

	"ply/internal/discovery"
)

// reachableURLs returns the URLs clients can use to access ply.
// It includes LAN URLs from discovery.URLs, and appends loopback (127.0.0.1)
// if it is not already included. With HTTPS it is the certificate's name alone:
// an address is not one the certificate is for.
func reachableURLs(addr net.Addr, tlsHost string) []string {
	tcp, isTCP := addr.(*net.TCPAddr)
	if tlsHost != "" && isTCP {
		return []string{"https://" + net.JoinHostPort(tlsHost, strconv.Itoa(tcp.Port))}
	}
	urls := discovery.URLs(addr)
	if isTCP {
		local := fmt.Sprintf("http://127.0.0.1:%d", tcp.Port)
		if !slices.Contains(urls, local) {
			urls = append(urls, local)
		}
	}
	return urls
}

// The ANSI styles of everything ply writes to a terminal.
const (
	ansiReset    = "\033[0m"
	ansiBold     = "\033[1m"
	ansiDim      = "\033[2m"
	ansiBoldCyan = "\033[1;36m"
)

// style wraps s in code when the terminal can show it.
func style(useColor bool, code, s string) string {
	if !useColor {
		return s
	}
	return code + s + ansiReset
}

// colorTerminal says whether f takes ANSI styles: it is a terminal, it is not a dumb
// one, and NO_COLOR is not set.
func colorTerminal(f *os.File) bool {
	return isTerminal(f) && os.Getenv("TERM") != "dumb" && os.Getenv("NO_COLOR") == ""
}

// logo is the ply wordmark: a blank line (it stands apart from the command typed),
// the two lines of the mark and a blank line. Every command-line run opens with it.
func logo(useColor bool) string {
	return "\n" + style(useColor, ansiBoldCyan, "█▀█ █   █ █") + "\n" +
		style(useColor, ansiBoldCyan, "█▀▀ █▄▄ ▀█▀") + "\n\n"
}

// versionText is what -version prints: the version alone, so a script that reads it
// gets no logo and no colors.
func versionText(ver string) string {
	return "ply v" + ver + "\n"
}

// banner formats what follows the logo when the server starts: the program and
// version, the server's name, every URL clients can use, and the log file.
func banner(name, ver, logPath string, urls []string) string {
	return formatBanner(name, ver, logPath, urls, colorTerminal(os.Stderr))
}

// formatBanner formats the banner, with ANSI styles when useColor.
func formatBanner(name, ver, logPath string, urls []string, useColor bool) string {
	lines := []string{
		style(useColor, ansiBold, "Local Media Server") + " " + style(useColor, ansiDim, "(v"+ver+")"),
		"",
		style(useColor, ansiBold, name),
	}
	for _, u := range urls {
		lines = append(lines, style(useColor, ansiBoldCyan, u))
	}
	lines = append(lines,
		"",
		style(useColor, ansiBold, "Log file:")+" "+logPath,
		style(useColor, ansiDim, "Press Ctrl-C to stop (twice to quit at once)"),
		"",
	)
	return strings.Join(lines, "\n") + "\n"
}
