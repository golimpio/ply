package migrate

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"ply/internal/metadata"
)

// The write targets mirror the names internal/metadata/localart.go reads, so a
// sidecar this tool writes is exactly what ply serves (local beats TMDB, pinned).
// Movies use the per-file "<base>-poster" form, which works whether or not the
// folder holds one film; shows/seasons use the folder-level names.

func baseName(path string) string {
	b := filepath.Base(path)
	return strings.TrimSuffix(b, filepath.Ext(b))
}

func dirNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func moviePosterTarget(path string) string {
	return filepath.Join(filepath.Dir(path), baseName(path)+"-poster.jpg")
}
func movieBackdropTarget(path string) string {
	return filepath.Join(filepath.Dir(path), baseName(path)+"-fanart.jpg")
}
func episodeStillTarget(path string) string {
	return filepath.Join(filepath.Dir(path), baseName(path)+"-thumb.jpg")
}
func showPosterTarget(showFolder string) string {
	return filepath.Join(showFolder, "poster.jpg")
}
func seasonPosterTarget(showFolder string, season int) string {
	name := "season-specials-poster.jpg"
	if season > 0 {
		name = "season" + pad2(season) + "-poster.jpg"
	}
	return filepath.Join(showFolder, name)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// reSeasonDir mirrors internal/metadata/localart.go's season-folder pattern.
var reSeasonDir = regexp.MustCompile(`(?i)^(season[\s._-]*\d+|s\d{1,2}|specials?)$`)

func isSeasonDirName(name string) bool {
	cleaned := strings.Join(strings.Fields(strings.NewReplacer(".", " ", "_", " ").Replace(name)), " ")
	return reSeasonDir.MatchString(cleaned)
}

// showFolderOf derives the show's folder from an episode path: the parent of a
// "Season N"/"Specials" folder, else the episode's own folder.
func showFolderOf(episodePath string) string {
	dir := filepath.Dir(episodePath)
	if isSeasonDirName(filepath.Base(dir)) {
		return filepath.Dir(dir)
	}
	return dir
}

// Existing art detection reuses ply's own readers, so "already has a poster"
// means exactly what ply would serve. Each returns true when art is present.

func hasMoviePoster(path string) bool {
	return metadata.MovieLocal(path, dirNames(filepath.Dir(path))).Poster != ""
}
func hasMovieBackdrop(path string) bool {
	return metadata.MovieLocal(path, dirNames(filepath.Dir(path))).Backdrop != ""
}
func hasEpisodeStill(path string) bool {
	return metadata.EpisodeLocal(path, dirNames(filepath.Dir(path))).Thumb != ""
}
func hasShowPoster(showFolder string) bool {
	return metadata.ShowLocal(showFolder, dirNames(showFolder)).Poster != ""
}
func hasSeasonPoster(showFolder string, season int) bool {
	if metadata.SeasonPoster(showFolder, dirNames(showFolder), season) != "" {
		return true
	}
	// A poster in the season's own folder also counts.
	seasonDir := filepath.Join(showFolder, "Season "+pad2(season))
	return metadata.FolderPoster(seasonDir, dirNames(seasonDir)) != ""
}
