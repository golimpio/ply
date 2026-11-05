package metadata

import (
	"context"
	"errors"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"ply/internal/store"
)

// ErrNoSuchOrder means TVDB has no episodes for the show in the order asked for
// (a DVD or absolute order is something only some shows have).
var ErrNoSuchOrder = errors.New("TVDB has no episodes for this show in that order")

// TVDBProvider is the Provider backed by thetvdb.com. It has shows only: its
// movie methods find nothing.
type TVDBProvider struct{ c *TVDBClient }

var _ Provider = (*TVDBProvider)(nil)

// NewTVDBProvider wraps a client.
func NewTVDBProvider(c *TVDBClient) *TVDBProvider { return &TVDBProvider{c: c} }

// Name implements Provider.
func (p *TVDBProvider) Name() string { return SourceTVDB }

// TVDB's ids of the kinds of artwork ply uses (/artwork/types).
const (
	tvdbArtPoster     = 2
	tvdbArtBackground = 3
	tvdbArtClearLogo  = 23
)

// TVDBImageRef is the stored reference of a TVDB image: "tvdb:/banners/posters/1-1.jpg",
// from the path or the full artworks.thetvdb.com address. A reference that is not a
// plain TVDB image gives "": nothing that comes from TVDB can steer a download elsewhere.
func TVDBImageRef(ref string) string {
	p, ok := TVDBImagePath(ref)
	if !ok {
		return ""
	}
	return "tvdb:" + p
}

// tvdbImageExts are the file types of TVDB images that are served.
var tvdbImageExts = map[string]string{".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png"}

// TVDBContentType is the content type of a TVDB image path from its extension, or
// "" for a path that is not an image.
func TVDBContentType(p string) string { return tvdbImageExts[strings.ToLower(path.Ext(p))] }

// TVDBFile returns the path ("/banners/posters/1-1.jpg") of a "tvdb:/banners/..." ref;
// ok is false for any other ref, or a path that may not be served.
func TVDBFile(ref string) (string, bool) {
	rest, found := strings.CutPrefix(ref, "tvdb:")
	if !found || !strings.HasPrefix(rest, "/") {
		return "", false
	}
	return validTVDBFile(rest)
}

// validTVDBFile checks a path that came from a request or a ref: a plain TVDB image
// path, as TVDBImagePath defines it, that is a picture.
func validTVDBFile(p string) (string, bool) {
	clean, ok := TVDBImagePath(p)
	if !ok || clean != p || TVDBContentType(p) == "" {
		return "", false
	}
	return p, true
}

// ValidTVDBImage reports whether size and file (a path with or without the leading
// slash) may be served: size is on the allowlist and file is a plain TVDB image path.
func ValidTVDBImage(size, file string) bool {
	if !ValidSize(size) {
		return false
	}
	if !strings.HasPrefix(file, "/") {
		file = "/" + file
	}
	_, ok := validTVDBFile(file)
	return ok
}

// tvdbSmall reports whether a size is served from TVDB's thumbnail: w92 to w342.
func tvdbSmall(size string) bool { return size == "w92" || size == "w185" || size == "w342" }

func (p *TVDBProvider) SearchMovies(context.Context, string, int) ([]Candidate, error) {
	return nil, nil
}

func (p *TVDBProvider) SearchSeries(ctx context.Context, query string, year int) ([]Candidate, error) {
	res, err := p.c.Search(ctx, query, year)
	if err != nil {
		return nil, err
	}
	code := p.c.LanguageCode()
	out := make([]Candidate, 0, len(res))
	for i, r := range res {
		id := r.SeriesID()
		if id == 0 {
			continue
		}
		y, _ := strconv.Atoi(strings.TrimSpace(r.Year))
		if y == 0 {
			y = yearOf(r.FirstAirTime)
		}
		out = append(out, Candidate{ID: id, Source: SourceTVDB,
			Title: firstNonEmpty(r.Translations[code], r.Name), OriginalTitle: r.Name, Year: y,
			Overview: firstNonEmpty(r.Overviews[code], r.Overview), Poster: TVDBImageRef(firstNonEmpty(r.ImageURL, r.Thumbnail)),
			// TVDB gives no popularity: its relevance order stands in for it.
			Popularity: float64(len(res) - i)})
	}
	return out, nil
}

func (p *TVDBProvider) Find(ctx context.Context, source, id string) (movies, series []Candidate, err error) {
	switch source {
	case ExternalTVDB:
		// The id is TVDB's own: nothing to ask. The fetch that follows says whether it exists.
		if n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64); err == nil && n > 0 {
			return nil, []Candidate{{ID: n, Source: SourceTVDB}}, nil
		}
	case ExternalIMDb:
		matches, err := p.c.SearchRemoteID(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		for _, m := range matches {
			if s := m.Series; s != nil && s.ID != 0 {
				series = append(series, Candidate{ID: s.ID, Source: SourceTVDB, Title: s.Name, OriginalTitle: s.Name,
					Year: firstInt(yearOf(s.FirstAired), atoi(s.Year)), Overview: s.Overview, Poster: TVDBImageRef(s.Image)})
			}
		}
	}
	return nil, series, nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func (p *TVDBProvider) Movie(context.Context, int64) (*store.TitleInput, error) {
	return nil, &TVDBError{Op: "movie", Status: http.StatusNotFound, Message: "TVDB has no movies"}
}

// seasonType is TVDB's name for an order of episodes.
func seasonType(order string) (string, error) {
	switch order {
	case "", store.OrderAired:
		return "official", nil
	case store.OrderDVD:
		return "dvd", nil
	case store.OrderAbsolute:
		return "absolute", nil
	}
	return "", errors.New("metadata: unknown episode order " + order)
}

func (p *TVDBProvider) Series(ctx context.Context, id int64, order string, _ []int) (*store.TitleInput, error) {
	st, err := seasonType(order)
	if err != nil {
		return nil, err
	}
	if order == "" {
		order = store.OrderAired
	}
	ser, err := p.c.SeriesExtended(ctx, id)
	if err != nil {
		return nil, err
	}
	eps, err := p.c.Episodes(ctx, id, st, "")
	if err != nil {
		return nil, err
	}
	if order != store.OrderAired && len(eps.Episodes) == 0 {
		return nil, ErrNoSuchOrder
	}
	return tvdbInput(ser, eps.Episodes, order, st, p.c.Language()), nil
}

func (p *TVDBProvider) Person(ctx context.Context, id int64) (*store.Person, error) {
	d, err := p.c.Person(ctx, id)
	if err != nil {
		return nil, err
	}
	bio := d.Biography(p.c.Language())
	if bio == "" {
		bio = d.Biography("eng")
	}
	if bio == "" {
		for _, b := range d.Biographies {
			if b.Biography != "" {
				bio = b.Biography
				break
			}
		}
	}
	return &store.Person{TVDBID: d.ID, TMDBID: d.RemoteIDs.TMDBID(), Name: d.Name, Profile: TVDBImageRef(d.Image),
		Biography: bio, Birthday: d.Birth, Deathday: d.Death, PlaceOfBirth: d.BirthPlace, IMDbID: d.RemoteIDs.IMDbID()}, nil
}

// Changes is TVDB's updates feed for shows, from start on; end is not used (the
// feed has no end), and movies have none.
func (p *TVDBProvider) Changes(ctx context.Context, kind string, start, _ time.Time) ([]int64, error) {
	if kind != store.KindSeries {
		return nil, nil
	}
	return p.c.Updates(ctx, start)
}

func (p *TVDBProvider) Check(ctx context.Context) error { return p.c.Check(ctx) }

func (p *TVDBProvider) ImageURL(size, file string) string {
	if !ValidSize(size) {
		return ""
	}
	return TVDBImageURL(file, tvdbSmall(size))
}

// tvdbCountries maps a region (the part of the language after "-") to the
// three-letter country code TVDB's content ratings use.
var tvdbCountries = map[string]string{
	"US": "usa", "GB": "gbr", "CA": "can", "AU": "aus", "NZ": "nzl", "IE": "irl", "DE": "deu", "AT": "aut", "CH": "che",
	"FR": "fra", "ES": "esp", "PT": "prt", "BR": "bra", "IT": "ita", "NL": "nld", "BE": "bel", "SE": "swe", "NO": "nor",
	"DK": "dnk", "FI": "fin", "PL": "pol", "RU": "rus", "JP": "jpn", "KR": "kor", "CN": "chn", "TW": "twn", "MX": "mex",
	"AR": "arg", "IN": "ind",
}

// tvdbCertification is the content rating of the show in the region's country, else
// in the US; "" when there is neither.
func tvdbCertification(ratings []TVDBContentRating, lang string) string {
	region := "US"
	if _, r, ok := strings.Cut(strings.ReplaceAll(lang, "_", "-"), "-"); ok && len(r) == 2 {
		region = strings.ToUpper(r)
	}
	for _, country := range []string{tvdbCountries[region], "usa"} {
		if country == "" {
			continue
		}
		for _, r := range ratings {
			if strings.EqualFold(r.Country, country) && r.Name != "" {
				return r.Name
			}
		}
	}
	return ""
}

// bestTVDBArtwork picks the artwork of a kind: the one in the language, else one with
// no text on it (no language), else English, else any; the highest score within that.
func bestTVDBArtwork(arts []TVDBArtwork, kind int64, code string) string {
	best, bestRank := "", -1
	var bestScore float64
	for _, a := range arts {
		if a.Type != kind {
			continue
		}
		ref := TVDBImageRef(a.Image)
		if ref == "" {
			continue
		}
		rank := 0
		switch {
		case a.Language == code:
			rank = 3
		case a.Language == "":
			rank = 2
		case a.Language == "eng":
			rank = 1
		}
		if rank > bestRank || (rank == bestRank && a.Score > bestScore) {
			best, bestRank, bestScore = ref, rank, a.Score
		}
	}
	return best
}

// tvdbInput maps a TVDB show and its episodes in one order to a title. order is the
// store's name for it (aired, dvd, absolute), seasonKind TVDB's (official, dvd,
// absolute), lang the language asked for ("en-US").
func tvdbInput(s *TVDBSeries, eps []TVDBEpisode, order, seasonKind, lang string) *store.TitleInput {
	code := tvdbLanguage(lang)
	if code == "" {
		code = "eng"
	}
	in := &store.TitleInput{Title: store.Title{
		Kind: store.KindSeries, TVDBID: s.ID, TMDBID: s.RemoteIDs.TMDBID(), IMDbID: s.RemoteIDs.IMDbID(),
		Title:         firstNonEmpty(s.Translations.Name(lang), s.Name),
		OriginalTitle: s.Name, Year: firstInt(yearOf(s.FirstAired), atoi(s.Year)),
		Overview:    firstNonEmpty(s.Translations.Overview(lang), s.Overview),
		ReleaseDate: s.FirstAired, EndDate: s.LastAired, Status: s.Status.Name, RuntimeMin: s.AverageRuntime,
		Certification: tvdbCertification(s.ContentRatings, lang),
		Poster:        firstNonEmpty(bestTVDBArtwork(s.Artworks, tvdbArtPoster, code), TVDBImageRef(s.Image)),
		Backdrop:      bestTVDBArtwork(s.Artworks, tvdbArtBackground, code),
		Logo:          bestTVDBArtwork(s.Artworks, tvdbArtClearLogo, code),
		Source:        store.SourceTVDB, EpisodeOrder: order,
	}}
	in.SortTitle = SortTitle(in.Title.Title)
	for _, g := range s.Genres {
		// TVDB's genre ids are not TMDB's: the store matches by name and numbers new ones itself.
		in.Genres = append(in.Genres, store.Genre{Name: g.Name})
	}
	in.Credits = tvdbCredits(s.Characters)

	// The episodes, in the numbers of the order asked for. In the absolute order a show
	// is one season, numbered through; the specials (season 0) are not part of it.
	seen := map[[2]int]bool{}
	bySeason := map[int]*store.Season{}
	for _, e := range eps {
		season, number := e.SeasonNumber, e.Number
		if order == store.OrderAbsolute && season != 0 {
			season = 1
			if e.AbsoluteNumber > 0 {
				number = e.AbsoluteNumber
			}
		}
		if season < 0 || number <= 0 || seen[[2]int{season, number}] {
			continue
		}
		seen[[2]int{season, number}] = true
		se := bySeason[season]
		if se == nil {
			se = &store.Season{Number: season}
			bySeason[season] = se
		}
		se.Episodes = append(se.Episodes, store.Episode{Season: season, Number: number, Name: e.Name, Overview: e.Overview,
			AirDate: e.Aired, RuntimeMin: e.Runtime, Still: TVDBImageRef(e.Image)})
		if e.Aired != "" && (se.AirDate == "" || e.Aired < se.AirDate) {
			se.AirDate = e.Aired
		}
	}
	for _, sn := range s.Seasons {
		if se := bySeason[sn.Number]; se != nil && sn.Type.Type == seasonKind && se.Poster == "" {
			se.Poster = TVDBImageRef(sn.Image)
		}
	}
	for _, se := range bySeason {
		slices.SortFunc(se.Episodes, func(a, b store.Episode) int { return a.Number - b.Number })
		in.Seasons = append(in.Seasons, *se)
	}
	slices.SortFunc(in.Seasons, func(a, b store.Season) int { return a.Number - b.Number })
	// Every season comes at once: what is stored for the show is what TVDB says now.
	in.ResetSeasons = len(in.Seasons) > 0

	if s.NextAired != "" {
		in.NextAirDate = s.NextAired
		for _, se := range in.Seasons {
			if se.Number == 0 {
				continue
			}
			if i := slices.IndexFunc(se.Episodes, func(e store.Episode) bool { return e.AirDate == s.NextAired }); i >= 0 {
				e := se.Episodes[i]
				in.NextEpisodeSeason, in.NextEpisodeNumber, in.NextEpisodeName = e.Season, e.Number, e.Name
				break
			}
		}
	}
	return in
}

// tvdbCredits maps a show's characters to credits: the actors, in TVDB's order, and
// the creators, directors, writers and producers.
func tvdbCredits(chars []TVDBCharacter) []store.Credit {
	var cast []TVDBCharacter
	var crew []store.Credit
	for _, c := range chars {
		if c.PeopleID == 0 || strings.TrimSpace(c.PersonName) == "" {
			continue
		}
		switch c.PeopleType {
		case "Actor":
			cast = append(cast, c)
		case "Creator":
			crew = append(crew, tvdbCrew(c, "Creator"))
		case "Director":
			crew = append(crew, tvdbCrew(c, "Director"))
		case "Writer":
			crew = append(crew, tvdbCrew(c, "Writer"))
		case "Producer", "Executive Producer":
			crew = append(crew, tvdbCrew(c, "Producer"))
		}
	}
	slices.SortStableFunc(cast, func(a, b TVDBCharacter) int { return a.Sort - b.Sort })
	var out []store.Credit
	for i, c := range cast[:min(len(cast), maxCast)] {
		out = append(out, store.Credit{Person: store.Person{TVDBID: c.PeopleID, Name: c.PersonName, Profile: TVDBImageRef(c.PersonImgURL)},
			Kind: store.CreditCast, Character: c.Name, Order: i})
	}
	return append(out, orderCrew(crew)...)
}

func tvdbCrew(c TVDBCharacter, job string) store.Credit {
	return store.Credit{Person: store.Person{TVDBID: c.PeopleID, Name: c.PersonName, Profile: TVDBImageRef(c.PersonImgURL)},
		Kind: store.CreditCrew, Job: job}
}
