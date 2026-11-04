package library

import (
	"path/filepath"
	"regexp"
	"slices"
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

	// NeedsReview is set by ParseNameIn for an episode of a shows folder whose
	// name has no episode number: it is episode 0, and a person should look.
	NeedsReview bool
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
	stem := fileStem(path)

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

// fileStem is the file name without its video extension, NFC, with dots and
// underscores turned into spaces.
func fileStem(path string) string {
	base := filepath.Base(path)
	if IsVideoFile(base) {
		base = strings.TrimSuffix(base, filepath.Ext(base))
	}
	return dirStem(base)
}

// dirStem is fileStem for a directory name, which has no extension to cut.
func dirStem(name string) string {
	return strings.NewReplacer(".", " ", "_", " ").Replace(norm.NFC.String(name))
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

var (
	// Episode markers other than SxxEyy and 1x02, for a shows folder. Group 1 is
	// the text in front of the marker, the last group the episode number.
	reEOnly      = regexp.MustCompile(`(?i)^(.*?)[\s._-]*\be(\d{1,3})\b`)
	reEpisode    = regexp.MustCompile(`(?i)^(.*?)[\s._-]*\bep(?:isode)?[\s._-]*(\d{1,3})\b`)
	reLeadingNum = regexp.MustCompile(`^()[\s._-]*(\d{1,3})\b`)

	// Names that say nothing about the title: a movie in a folder of its own is
	// called after the folder.
	reNoTitle = regexp.MustCompile(`(?i)^(?:movie|film|video|video ts|vts \d+ \d+|bdmv|stream|certificate|main|` +
		`main movie|full movie|feature|index|sample|title\d*|disc ?\d+|cd ?\d+|dvd|blu ?ray|\d{5,})$`)
)

// episodeMarkers are tried in the order of docs/M7-FOLDERS.md. season says
// whether group 2 is the season (and group 3 the episode).
var episodeMarkers = []struct {
	re     *regexp.Regexp
	season bool
}{
	{reSxxExx, true}, {reNxNN, true}, {reEOnly, false}, {reEpisode, false}, {reLeadingNum, false},
}

// ParseNameIn is ParseName for a file under a media folder whose type is known
// (docs/M7-FOLDERS.md). In a movies folder a file is always a movie; in a shows
// folder always an episode. A root with no type (a -media folder that has not
// been guessed yet) parses as ParseName does.
func ParseNameIn(r Root, path string) Parsed {
	switch r.Kind {
	case KindMovies:
		return parseMovieIn(r.Path, path)
	case KindShows:
		return parseEpisodeIn(r.Path, path)
	}
	return ParseName(path)
}

// GuessKind is the type of a media folder whose type nobody chose, from the
// paths of its files: shows when more than half are named like episodes
// (S01E02, 1x02), else movies.
func GuessKind(paths []string) string {
	n := 0
	for _, p := range paths {
		if ParseName(p).Kind == store.KindEpisode {
			n++
		}
	}
	if n*2 > len(paths) {
		return KindShows
	}
	return KindMovies
}

// dirsBelow returns the names of the directories between root and the file at
// path, outermost first. ok is false when path is not under root; the names are
// then all of its parent directories.
func dirsBelow(root, path string) (dirs []string, ok bool) {
	dir := filepath.Dir(path)
	if rel, err := filepath.Rel(root, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		if rel == "." {
			return nil, true
		}
		return strings.Split(rel, string(filepath.Separator)), true
	}
	for _, d := range strings.Split(dir, string(filepath.Separator)) {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs, false
}

// parseMovieIn names a movie after its file, or after the nearest folder with a
// name of its own when the file name is no title (movie.mkv, VIDEO_TS/...).
func parseMovieIn(root, path string) Parsed {
	title, year := parseMovie(fileStem(path))
	if title != "" && !reNoTitle.MatchString(clean(fileStem(path))) {
		return Parsed{Kind: store.KindMovie, Title: title, Year: year}
	}
	dirs, _ := dirsBelow(root, path)
	for i := len(dirs) - 1; i >= 0; i-- {
		stem := dirStem(dirs[i])
		if clean(stem) == "" || reNoTitle.MatchString(clean(stem)) {
			continue
		}
		if t, y := parseMovie(stem); t != "" {
			return Parsed{Kind: store.KindMovie, Title: t, Year: y}
		}
	}
	return Parsed{Kind: store.KindMovie, Title: title, Year: year}
}

// episodeMatch is what the name of an episode file says.
type episodeMatch struct {
	prefix    string // the text in front of the marker: maybe the show
	title     string
	season    int
	hasSeason bool
	episode   int
}

// matchEpisode tries the episode markers in order; ok is false when the name
// has no episode number.
func matchEpisode(stem string) (em episodeMatch, ok bool) {
	for _, mk := range episodeMarkers {
		m := mk.re.FindStringSubmatchIndex(stem)
		if m == nil {
			continue
		}
		em.prefix = clean(stem[m[2]:m[3]])
		if mk.season {
			em.season, _ = strconv.Atoi(stem[m[4]:m[5]])
			em.hasSeason = true
		}
		em.episode, _ = strconv.Atoi(stem[m[len(m)-2]:m[len(m)-1]])
		em.title = clean(cutJunk(stem[m[1]:]))
		return em, true
	}
	return episodeMatch{title: clean(cutJunk(stem))}, false
}

// parseEpisodeIn reads an episode of a shows folder: the show is the first
// folder below the root, the season comes from the name or from a Season folder
// (else it is 1), the episode number from the name. A name with no number is
// episode 0, for a person to review.
func parseEpisodeIn(root, path string) Parsed {
	dirs, under := dirsBelow(root, path)
	em, found := matchEpisode(fileStem(path))

	// An episode with no number is for a person to look at, unless it is in a folder
	// of extras: those files are not episodes, and have no number to find.
	p := Parsed{Kind: store.KindEpisode, Title: em.title, Season: em.season, Episode: em.episode,
		NeedsReview: !found && !slices.ContainsFunc(dirs, isExtrasDir)}
	if !em.hasSeason {
		p.Season = 1
		for i := len(dirs) - 1; i >= 0; i-- {
			if n, ok := seasonOfDir(dirs[i]); ok {
				p.Season = n
				break
			}
		}
	}

	switch {
	case under && len(dirs) > 0 && !isSeasonDir(dirs[0]):
		p.Show = clean(dirStem(dirs[0]))
	case em.prefix != "":
		p.Show = em.prefix
	case under: // a Season folder, or the file itself, straight below the root: the root is the show's own folder
		p.Show = rootShow(root)
	case !under:
		p.Show = showFromParents(path)
	}
	return p
}

// rootShow is the show a root names when it is the show's folder itself.
func rootShow(root string) string {
	base := filepath.Base(root)
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return clean(dirStem(base))
}

func isSeasonDir(name string) bool { return reSeasonDir.MatchString(clean(dirStem(name))) }

// reExtrasDir names the folders that hold what is not an episode of the show:
// extras, featurettes, behind the scenes.
var reExtrasDir = regexp.MustCompile(`(?i)^(?:extras?|featurettes?|behind the scenes)$`)

func isExtrasDir(name string) bool { return reExtrasDir.MatchString(clean(dirStem(name))) }

// seasonOfDir reads the season from a "Season 2", "S2" or "Specials" folder
// name; Specials is season 0.
func seasonOfDir(name string) (season int, ok bool) {
	c := clean(dirStem(name))
	if !reSeasonDir.MatchString(c) {
		return 0, false
	}
	if n := reDigits.FindString(c); n != "" {
		season, _ = strconv.Atoi(n)
	}
	return season, true
}
