package library

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"ply/internal/store"
)

var subtitleExts = map[string]string{".srt": "srt", ".vtt": "vtt", ".ass": "ass"}

// langToken matches the language part of a sidecar name: "en", "eng",
// "pt-BR", "zh_Hant".
var langToken = regexp.MustCompile(`^[a-zA-Z]{2,3}([-_][a-zA-Z0-9]{2,4})?$`)

// flagTokens are name parts that describe a subtitle without naming its
// language. Only "forced" changes behaviour. ("hi" is not here: it is Hindi.)
var flagTokens = map[string]bool{"forced": true, "sdh": true, "cc": true, "default": true, "full": true}

// sidecars picks, from the file names of one directory, the subtitles that
// belong to the video at videoPath: "<base>.srt|vtt|ass" and
// "<base>.<lang>[.forced].srt|vtt|ass" (base compared case-insensitively).
// The result is ordered by path.
func sidecars(videoPath string, names []string) []store.ExternalSubtitle {
	dir := filepath.Dir(videoPath)
	base := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	var out []store.ExternalSubtitle
	for _, name := range names {
		format, ok := subtitleExts[strings.ToLower(filepath.Ext(name))]
		if !ok || len(name) <= len(base) || !strings.EqualFold(name[:len(base)], base) {
			continue
		}
		// What sits between "<base>." and ".<ext>" (possibly nothing).
		mid := name[len(base) : len(name)-len(filepath.Ext(name))]
		if mid != "" && mid[0] != '.' {
			continue // "Movie 2.srt" is not a sidecar of "Movie.mkv"
		}
		lang, forced, ok := parseSidecarTokens(strings.TrimPrefix(mid, "."))
		if !ok {
			continue
		}
		out = append(out, store.ExternalSubtitle{
			Path: filepath.Join(dir, name), Language: lang, Format: format, Forced: forced,
		})
	}
	slices.SortFunc(out, func(a, b store.ExternalSubtitle) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// parseSidecarTokens reads "en", "en.forced", "forced", "pt-BR.sdh" or "".
// ok is false for anything else: a second language, an unknown word.
func parseSidecarTokens(mid string) (lang string, forced, ok bool) {
	if mid == "" {
		return "", false, true
	}
	tokens := strings.Split(mid, ".")
	if len(tokens) > 3 {
		return "", false, false
	}
	for _, tok := range tokens {
		switch low := strings.ToLower(tok); {
		case flagTokens[low]:
			forced = forced || low == "forced"
		case lang == "" && langToken.MatchString(tok):
			lang = strings.ToLower(strings.ReplaceAll(tok, "_", "-"))
		default:
			return "", false, false
		}
	}
	return lang, forced, true
}

// dirNames lists file names, caching the last directory: the videos of one
// folder are visited one after another.
type dirNames struct {
	dir   string
	names []string
}

func (d *dirNames) list(dir string) []string {
	if d.dir != dir || d.names == nil {
		entries, _ := os.ReadDir(dir) // an unreadable directory has no sidecars
		d.dir, d.names = dir, make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() {
				d.names = append(d.names, e.Name())
			}
		}
	}
	return d.names
}
