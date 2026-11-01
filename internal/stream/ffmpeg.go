package stream

import "os"

// jellyfinFFmpeg are the places jellyfin-ffmpeg (a build with the hardware
// acceleration and filters Plex-like servers need) is usually installed.
var jellyfinFFmpeg = []string{
	"/usr/lib/jellyfin-ffmpeg/ffmpeg",
	"/opt/homebrew/opt/jellyfin-ffmpeg/bin/ffmpeg",
	"/usr/local/opt/jellyfin-ffmpeg/bin/ffmpeg",
}

// ResolveFFmpeg picks the ffmpeg binary. An explicit choice (anything but
// the default name) is used as given; otherwise jellyfin-ffmpeg if installed,
// else "ffmpeg" from PATH.
func ResolveFFmpeg(configured string) string {
	return resolveFFmpeg(configured, jellyfinFFmpeg)
}

func resolveFFmpeg(configured string, candidates []string) string {
	if configured != "" && configured != "ffmpeg" {
		return configured
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return c
		}
	}
	return "ffmpeg"
}
