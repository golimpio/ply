package metadata

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Local is what sits next to a media file: its NFO and artwork, as absolute
// paths ("" = none).
type Local struct {
	NFO      string
	Poster   string
	Backdrop string
	Thumb    string
}

var (
	imageExts = []string{".jpg", ".jpeg", ".png", ".webp"}
	// The same extensions the scanner indexes (internal/library).
	videoExts = []string{".mp4", ".m4v", ".mov", ".mkv", ".webm", ".avi", ".ts", ".m2ts"}
)

// MovieLocal finds the local files of the movie at videoPath among names, the
// entries of its directory (any order, files only). <base>-poster and
// <base>-fanart always apply; the folder-level names (poster, folder, cover,
// fanart, backdrop, background, movie.nfo) only when the directory holds one
// video file, so that a folder of several movies does not give them all the
// same poster.
func MovieLocal(videoPath string, names []string) Local {
	dir, base := splitVideo(videoPath)
	ix := newIndex(dir, names)
	alone := countVideos(names) <= 1

	posters := []string{base + "-poster"}
	backdrops := []string{base + "-fanart"}
	nfos := []string{base + ".nfo"}
	if alone {
		posters = append(posters, "poster", "folder", "cover")
		backdrops = append(backdrops, "fanart", "backdrop", "background")
		nfos = append(nfos, "movie.nfo")
	}
	return Local{
		NFO:      ix.find(nfos, []string{""}),
		Poster:   ix.find(posters, imageExts),
		Backdrop: ix.find(backdrops, imageExts),
	}
}

// EpisodeLocal finds an episode's NFO (<base>.nfo) and thumbnail (<base>-thumb
// or <base>.jpg).
func EpisodeLocal(videoPath string, names []string) Local {
	dir, base := splitVideo(videoPath)
	ix := newIndex(dir, names)
	return Local{
		NFO:   ix.find([]string{base + ".nfo"}, []string{""}),
		Thumb: ix.find([]string{base + "-thumb", base}, imageExts),
	}
}

// ShowLocal finds a show's tvshow.nfo, poster and fanart among the entries of
// its folder.
func ShowLocal(showDir string, names []string) Local {
	ix := newIndex(showDir, names)
	return Local{
		NFO:      ix.find([]string{"tvshow.nfo"}, []string{""}),
		Poster:   ix.find([]string{"poster", "folder"}, imageExts),
		Backdrop: ix.find([]string{"fanart", "backdrop"}, imageExts),
	}
}

// SeasonPoster finds a season's poster in the show's folder: seasonNN-poster
// (season01-poster.jpg; season-specials-poster.jpg for season 0).
func SeasonPoster(showDir string, names []string, season int) string {
	bases := []string{fmt.Sprintf("season%02d-poster", season)}
	if season < 10 {
		bases = append(bases, fmt.Sprintf("season%d-poster", season))
	}
	if season == 0 {
		bases = append(bases, "season-specials-poster")
	}
	return newIndex(showDir, names).find(bases, imageExts)
}

// FolderPoster finds poster.* or folder.* among the entries of dir: a season's
// own folder ("Season 01/poster.jpg").
func FolderPoster(dir string, names []string) string {
	return newIndex(dir, names).find([]string{"poster", "folder"}, imageExts)
}

// splitVideo returns the directory and the file name without its extension.
func splitVideo(videoPath string) (dir, base string) {
	dir = filepath.Dir(videoPath)
	base = filepath.Base(videoPath)
	return dir, strings.TrimSuffix(base, filepath.Ext(base))
}

func countVideos(names []string) int {
	n := 0
	for _, name := range names {
		if slices.Contains(videoExts, strings.ToLower(filepath.Ext(name))) {
			n++
		}
	}
	return n
}

// index maps the folded names of one directory to the names as they are on disk.
type index struct {
	dir   string
	names map[string]string
}

func fold(s string) string { return strings.ToLower(norm.NFC.String(s)) }

func newIndex(dir string, names []string) index {
	ix := index{dir: dir, names: make(map[string]string, len(names))}
	// Sorted, so that two names that differ only in case give the same answer
	// whatever order the directory was listed in.
	for _, name := range slices.Sorted(slices.Values(names)) {
		if _, dup := ix.names[fold(name)]; !dup {
			ix.names[fold(name)] = name
		}
	}
	return ix
}

// find returns the path of the first name+ext, trying bases in order and, for
// each, the extensions in order. Case and Unicode normalisation are ignored.
func (ix index) find(bases, exts []string) string {
	for _, base := range bases {
		for _, ext := range exts {
			if name, ok := ix.names[fold(base+ext)]; ok {
				return filepath.Join(ix.dir, name)
			}
		}
	}
	return ""
}
