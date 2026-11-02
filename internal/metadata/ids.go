// Package metadata finds, scores and stores descriptions, artwork and credits
// for the library: local NFO files and artwork first, TMDB for the rest.
package metadata

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Hints are ids that name a title outright: found in a file or folder name or
// in an NFO. Zero values are unknown.
type Hints struct {
	TMDB int64
	IMDb string // "tt0133093"
	TVDB int64
}

// IsZero reports whether no id is known.
func (h Hints) IsZero() bool { return h == Hints{} }

// fill returns h with its unknown ids taken from o.
func (h Hints) fill(o Hints) Hints {
	if h.TMDB == 0 {
		h.TMDB = o.TMDB
	}
	if h.IMDb == "" {
		h.IMDb = o.IMDb
	}
	if h.TVDB == 0 {
		h.TVDB = o.TVDB
	}
	return h
}

var (
	// "[tmdbid-603]", "{tmdb-603}", "[tmdbid=603]" (Jellyfin, Plex, Emby).
	reTMDBTag = regexp.MustCompile(`(?i)[\[{(]\s*tmdb(?:id)?\s*[-=_ ]\s*(\d{1,10})\s*[\]})]`)
	reTVDBTag = regexp.MustCompile(`(?i)[\[{(]\s*tvdb(?:id)?\s*[-=_ ]\s*(\d{1,10})\s*[\]})]`)
	// An IMDb id, tagged or bare ("[imdbid-tt0133093]", "{imdb-tt0133093}",
	// "Movie.tt0133093.mkv"): tt and 7-8 digits between non-alphanumerics.
	reIMDb    = regexp.MustCompile(`(?i)(^|[^a-z0-9])(tt\d{7,8})([^a-z0-9]|$)`)
	reIMDbTag = regexp.MustCompile(`(?i)[\[{(]\s*imdb(?:id)?\s*[-=_ ]\s*tt\d{7,8}\s*[\]})]`)
)

// ParseIDs reads the ids in a file or folder name.
func ParseIDs(name string) Hints {
	var h Hints
	if m := reTMDBTag.FindStringSubmatch(name); m != nil {
		h.TMDB, _ = strconv.ParseInt(m[1], 10, 64)
	}
	if m := reTVDBTag.FindStringSubmatch(name); m != nil {
		h.TVDB, _ = strconv.ParseInt(m[1], 10, 64)
	}
	if m := reIMDb.FindStringSubmatch(name); m != nil {
		h.IMDb = strings.ToLower(m[2])
	}
	return h
}

// StripIDs removes id tags and bare IMDb ids from a name, so that the rest can
// be read as a title.
func StripIDs(name string) string {
	name = reTMDBTag.ReplaceAllString(name, " ")
	name = reTVDBTag.ReplaceAllString(name, " ")
	name = reIMDbTag.ReplaceAllString(name, " ")
	// An IMDb id is matched with the characters around it; give them back.
	for {
		stripped := reIMDb.ReplaceAllString(name, "$1$3")
		if stripped == name {
			break
		}
		name = stripped
	}
	return strings.Join(strings.Fields(name), " ")
}

// PathHints reads the ids in a media file's name and, nearest first, in the
// names of up to parents folders above it. A file's own id beats a folder's.
func PathHints(path string, parents int) Hints {
	h := ParseIDs(filepath.Base(path))
	dir := filepath.Dir(path)
	for range parents {
		if h.TMDB != 0 && h.IMDb != "" && h.TVDB != 0 {
			break
		}
		name := filepath.Base(dir)
		if name == "." || name == string(filepath.Separator) {
			break
		}
		h = h.fill(ParseIDs(name))
		dir = filepath.Dir(dir)
	}
	return h
}
