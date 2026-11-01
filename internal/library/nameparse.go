package library

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"ply/internal/store"
)

// Parsed is what can be inferred from a file's name and location.
type Parsed struct {
	Kind    string // store.KindMovie | store.KindEpisode
	Title   string // movie title; for episodes the episode title (may be empty)
	Year    int    // movies only; 0 = unknown
	Show    string // episodes only
	Season  int
	Episode int
}

var (
	reSxxExx    = regexp.MustCompile(`(?i)^(.*?)[\s._-]*\bs(\d{1,2})[\s._-]*e(\d{1,3})`)
	reNxNN      = regexp.MustCompile(`(?i)^(.*?)[\s._-]*\b(\d{1,2})x(\d{2,3})\b`)
	reSeasonDir = regexp.MustCompile(`(?i)^(season[\s._-]*\d+|s\d{1,2}|specials?)$`)
	reDigits    = regexp.MustCompile(`\d+`)
	// Release-name noise. Matched on the already de-dotted name.
	reJunk = regexp.MustCompile(`(?i)\b(?:2160p|1080[pi]|720p|576p|480p|4k|uhd|blu[ -]?ray|bdrip|brrip|` +
		`web[ -]?dl|web[ -]?rip|hdtv|dvdrip|remux|x26[45]|h[ -]?26[45]|hevc|xvid|hdr10?|10bit|aac|ac3|eac3|dts)\b`)
)

// ParseName infers kind, title, year and episode numbers from a media file
// path. Only the file name and its parent directories are used.
func ParseName(path string) Parsed {
	base := filepath.Base(path)
	if IsVideoFile(base) {
		base = strings.TrimSuffix(base, filepath.Ext(base))
	}
	stem := strings.NewReplacer(".", " ", "_", " ").Replace(norm.NFC.String(base))

	for _, re := range []*regexp.Regexp{reSxxExx, reNxNN} {
		m := re.FindStringSubmatchIndex(stem)
		if m == nil {
			continue
		}
		season, _ := strconv.Atoi(stem[m[4]:m[5]])
		episode, _ := strconv.Atoi(stem[m[6]:m[7]])
		show := clean(stem[m[2]:m[3]])
		if show == "" {
			show = showFromParents(path)
		}
		return Parsed{
			Kind:    store.KindEpisode,
			Title:   clean(cutJunk(stem[m[1]:])),
			Show:    show,
			Season:  season,
			Episode: episode,
		}
	}

	title, year := parseMovie(stem)
	return Parsed{Kind: store.KindMovie, Title: title, Year: year}
}

// parseMovie finds the last plausible release year that has a title in front
// of it ("1917" alone is a title, not a year).
func parseMovie(stem string) (title string, year int) {
	runs := reDigits.FindAllStringIndex(stem, -1)
	for i := len(runs) - 1; i >= 0; i-- {
		start, end := runs[i][0], runs[i][1]
		if end-start != 4 {
			continue
		}
		y, _ := strconv.Atoi(stem[start:end])
		if y < 1900 || y > 2099 {
			continue
		}
		if letterBefore(stem, start) || letterAfter(stem, end) { // 1920x1080, x2000
			continue
		}
		if t := clean(stem[:start]); t != "" {
			return t, y
		}
	}
	if t := clean(cutJunk(stem)); t != "" {
		return t, 0
	}
	return clean(stem), 0 // the name starts with a junk-like word, e.g. "4K Beauty"
}

func letterBefore(s string, i int) bool {
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return unicode.IsLetter(r)
}

func letterAfter(s string, i int) bool {
	r, _ := utf8.DecodeRuneInString(s[i:])
	return unicode.IsLetter(r)
}

// showFromParents returns the nearest parent directory name that is not a
// "Season N" / "Specials" directory.
func showFromParents(path string) string {
	dir := filepath.Dir(path)
	for {
		name := filepath.Base(dir)
		if name == "." || name == string(filepath.Separator) || name == "" {
			return ""
		}
		if cleaned := clean(norm.NFC.String(name)); !reSeasonDir.MatchString(cleaned) {
			return cleaned
		}
		dir = filepath.Dir(dir)
	}
}

// cutJunk truncates s at the first release-name token (1080p, BluRay, x264...).
func cutJunk(s string) string {
	if loc := reJunk.FindStringIndex(s); loc != nil {
		return s[:loc[0]]
	}
	return s
}

// clean turns separators into spaces, collapses whitespace and trims dangling
// dashes and brackets.
func clean(s string) string {
	s = strings.NewReplacer(".", " ", "_", " ").Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	return strings.Trim(s, " -([")
}
