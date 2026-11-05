package metadata

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"ply/internal/probe"
	"ply/internal/store"
)

// dirNames lists the files (not directories) of dir; nil when it cannot be read.
func dirNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

var reSeasonFolder = regexp.MustCompile(`(?i)^(season[\s._-]*\d+|s\d{1,2}|specials?)$`)

// showDir is the folder of a show, given the path of one of its episodes: the
// episode's folder, or the one above it when that is a Season folder.
func showDir(episodePath string) string {
	dir := filepath.Dir(episodePath)
	if reSeasonFolder.MatchString(filepath.Base(dir)) {
		return filepath.Dir(dir)
	}
	return dir
}

var reMPAAPrefix = regexp.MustCompile(`(?i)^rated\s+`)

// cleanCertification turns an NFO's <mpaa> ("Rated R", "US:PG-13", "DE:16")
// into the bare rating.
func cleanCertification(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(reMPAAPrefix.ReplaceAllString(s, ""))
}

// firstNonEmpty returns the first argument that is not empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// localTitle builds a title from what the files themselves say: the NFO, and
// failing that the tags embedded in the media file. ok is false when there is no
// title text to build one from. The title has no TMDB id (source "nfo").
func localTitle(kind string, n NFO, tags probe.Tags) (in *store.TitleInput, ok bool) {
	title := n.Title
	if kind == store.KindSeries {
		title = firstNonEmpty(n.Title, tags.Show)
	} else {
		title = firstNonEmpty(n.Title, tags.Title)
	}
	if title == "" {
		return nil, false
	}
	in = &store.TitleInput{Title: store.Title{
		Kind: kind, Title: title, OriginalTitle: n.OriginalTitle,
		SortTitle:   firstNonEmpty(n.SortTitle, SortTitle(title)),
		Year:        firstInt(n.Year, tags.Year),
		Overview:    firstNonEmpty(n.Plot, n.Outline, tags.Description),
		Tagline:     n.Tagline,
		ReleaseDate: n.Premiered,
		RuntimeMin:  n.RuntimeMin, Rating: n.Rating, VoteCount: n.Votes,
		Certification: cleanCertification(n.MPAA),
		IMDbID:        n.IDs.IMDb,
		Source:        store.SourceNFO,
	}}
	for _, g := range n.Genres {
		in.Genres = append(in.Genres, store.Genre{Name: g})
	}
	if n.Set != "" {
		in.Collection = &store.Collection{Name: n.Set}
	}
	in.Credits = nfoCredits(n)
	return in, true
}

func firstInt(vals ...int) int {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}

// nfoCredits are the NFO's actors (in the order given) then its directors and
// writers.
func nfoCredits(n NFO) []store.Credit {
	var credits []store.Credit
	actors := append([]NFOActor(nil), n.Actors...)
	for i, a := range actors {
		credits = append(credits, store.Credit{Person: store.Person{Name: a.Name, Profile: ""}, Kind: store.CreditCast,
			Character: a.Role, Order: firstInt(a.Order, i)})
	}
	order := 0
	for _, d := range n.Directors {
		credits = append(credits, store.Credit{Person: store.Person{Name: d}, Kind: store.CreditCrew, Job: "Director", Order: order})
		order++
	}
	for _, w := range n.Writers {
		credits = append(credits, store.Credit{Person: store.Person{Name: w}, Kind: store.CreditCrew, Job: "Writer", Order: order})
		order++
	}
	return credits
}

// overlayNFO lays the NFO's text on top of a title fetched from TMDB or TVDB and
// reports whether it changed anything (then a TMDB title's source is "mixed"; a
// TVDB title keeps "tvdb", which is what tells a refresh where to ask).
// What is overlaid: the titles, overview, tagline, dates, runtime and
// certification. Genres, collection and credits come from the NFO only when
// TMDB had none.
func overlayNFO(in *store.TitleInput, n NFO) bool {
	changed := false
	set := func(dst *string, v string) {
		if v != "" && *dst != v {
			*dst = v
			changed = true
		}
	}
	if n.Title != "" && in.Title.Title != n.Title {
		in.Title.Title = n.Title
		in.SortTitle = firstNonEmpty(n.SortTitle, SortTitle(n.Title))
		changed = true
	} else if n.SortTitle != "" {
		set(&in.SortTitle, n.SortTitle)
	}
	set(&in.OriginalTitle, n.OriginalTitle)
	set(&in.Overview, firstNonEmpty(n.Plot, n.Outline))
	set(&in.Tagline, n.Tagline)
	set(&in.ReleaseDate, n.Premiered)
	set(&in.Certification, cleanCertification(n.MPAA))
	if n.Year != 0 && in.Year != n.Year {
		in.Year = n.Year
		changed = true
	}
	if n.RuntimeMin != 0 && in.RuntimeMin != n.RuntimeMin {
		in.RuntimeMin = n.RuntimeMin
		changed = true
	}
	if len(in.Genres) == 0 && len(n.Genres) > 0 {
		for _, g := range n.Genres {
			in.Genres = append(in.Genres, store.Genre{Name: g})
		}
		changed = true
	}
	if in.Collection == nil && n.Set != "" {
		in.Collection = &store.Collection{Name: n.Set}
		changed = true
	}
	if len(in.Credits) == 0 {
		if c := nfoCredits(n); len(c) > 0 {
			in.Credits = c
			changed = true
		}
	}
	if changed && in.Source != store.SourceTVDB {
		in.Source = store.SourceMixed
	}
	return changed
}
