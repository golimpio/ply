package library

import (
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"

	"ply/internal/store"
)

// Trailers and other extras, as Plex lays them out (docs/M13-TRAILERS.md, "Plex layouts").
var (
	// reTrailerName is a trailer named inline: Plex's "<name>-trailer", and the ".trailer"
	// and " - trailer" Jellyfin and Kodi write. Group 1 is the name.
	reTrailerName = regexp.MustCompile(`(?i)^(.*?)(?:-| - |\.)trailer$`)
	// reTrailerDir is the folder a film or a show keeps its trailers in.
	reTrailerDir = regexp.MustCompile(`(?i)^trailers?$`)
	// Plex's other extras of a film: by folder, or by the suffix of an inline name.
	reExtraDir  = regexp.MustCompile(`(?i)^(?:behind the scenes|deleted scenes|featurettes|interviews|scenes|shorts|other)$`)
	reExtraName = regexp.MustCompile(`(?i)-(?:behindthescenes|deleted|featurette|interview|scene|short|other)$`)
)

// baseStem is a file's name without its extension, as the OS spells it (no dots replaced).
func baseStem(path string) string {
	base := norm.NFC.String(filepath.Base(path))
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// trailerOf reads path as a trailer, of a film or a show of root (ok false: it is not one).
// A film's Title and Year are what its name says (the folder's, for a trailer in a Trailers
// folder) and OwnerDir is the film's folder; a show's Show and OwnerDir are the show's. A
// trailer of a Trailers folder straight in a films root is nobody's: OwnerDir "".
func trailerOf(r Root, path string) (Parsed, bool) {
	dir := filepath.Dir(path)
	inline := reTrailerName.FindStringSubmatch(baseStem(path))
	inFolder := reTrailerDir.MatchString(filepath.Base(dir))
	if inline == nil && !inFolder {
		return Parsed{}, false
	}
	p := Parsed{Kind: store.KindTrailer}
	if r.Kind == KindShows {
		p.OwnerDir = ShowDir(r.Path, path)
		p.Show = showOf(r.Path, p.OwnerDir)
		return p, true
	}
	name := ""
	if inline != nil && !inFolder {
		name = inline[1]
	} else {
		dir = filepath.Dir(dir) // the folder that holds Trailers
		if filepath.Clean(dir) == filepath.Clean(r.Path) {
			return p, true // Plex does not look there either
		}
		name = filepath.Base(dir)
	}
	p.OwnerDir = dir
	p.Title, p.Year = parseMovie(dirStem(name))
	return p, true
}

// ShowDir is the folder of the show a file of a shows root belongs to: the first folder
// below the root that is not a Season folder, or the root itself when it is the show's
// own folder (as parseEpisodeIn decides the show).
func ShowDir(root, path string) string {
	dirs, under := dirsBelow(root, path)
	switch {
	case under && len(dirs) > 0 && !isSeasonDir(dirs[0]):
		return filepath.Join(root, dirs[0])
	case under:
		return root
	}
	return filepath.Dir(path)
}

// showOf is the show a show's folder names (as parseEpisodeIn names it).
func showOf(root, showDir string) string {
	if filepath.Clean(showDir) == filepath.Clean(root) {
		return rootShow(root)
	}
	return clean(dirStem(filepath.Base(showDir)))
}

// isPlexExtra reports whether path is one of a film's other extras (a featurette, a deleted
// scene, ...) in a films root: those are not films, and are not indexed. In a shows root they
// stay episodes 0 (reExtrasDir).
func isPlexExtra(r Root, path string) bool {
	if r.Kind != KindMovies {
		return false
	}
	if reExtraName.MatchString(baseStem(path)) {
		return true
	}
	dir := filepath.Dir(path)
	return reExtraDir.MatchString(filepath.Base(dir)) && filepath.Clean(filepath.Dir(dir)) != filepath.Clean(r.Path)
}
