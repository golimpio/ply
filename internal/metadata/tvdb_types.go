package metadata

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
)

// The shapes of TheTVDB v4 responses, with just the fields ply reads. Absent
// and null fields decode to zero values. TVDB wraps every answer as
// {"status", "data", "links"}; the client unwraps it, so these are the "data".
//
// Ids and years are read from a JSON number or string alike, and the maps of
// translations from an object or an empty array: TVDB's own documentation and
// its answers have not always agreed on which, and one field of the wrong kind
// must not fail a whole list.

// flexString is a string that TVDB may send as a number (ids, years).
type flexString string

func (s *flexString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0 || string(b) == "null":
		*s = ""
	case b[0] == '"':
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = flexString(v)
	case b[0] == '-' || (b[0] >= '0' && b[0] <= '9'):
		var n json.Number
		if err := json.Unmarshal(b, &n); err != nil {
			return err
		}
		*s = flexString(n)
	default:
		var v string
		return json.Unmarshal(b, &v) // a boolean, an object or an array: the type error
	}
	return nil
}

// flexInt is an integer that TVDB may send as a string.
type flexInt int64

func (n *flexInt) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0 || string(b) == "null":
		*n = 0
	case b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s = strings.TrimSpace(s); s == "" {
			*n = 0
			return nil
		}
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return &json.UnmarshalTypeError{Value: "string", Type: reflect.TypeFor[int64]()}
		}
		*n = flexInt(v)
	default:
		var v int64
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*n = flexInt(v)
	}
	return nil
}

// flexMap is a map of language code to text that TVDB sends as [] when empty.
type flexMap map[string]string

func (m *flexMap) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*m = nil
		return nil
	}
	if b[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		if len(arr) == 0 {
			*m = nil
			return nil
		}
	}
	var v map[string]string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*m = v
	return nil
}

// Remote id sources, as TVDB names them in a remote id's sourceName.
const (
	TVDBSourceIMDb = "IMDB"
	TVDBSourceTMDB = "TheMovieDB.com"
)

// TVDBRemoteID is an id of the same title at another site.
type TVDBRemoteID struct {
	ID         string `json:"id"`
	SourceName string `json:"sourceName"`
	Type       int64  `json:"type"`
}

func (r *TVDBRemoteID) UnmarshalJSON(b []byte) error {
	type plain TVDBRemoteID
	aux := struct {
		*plain
		ID flexString `json:"id"`
	}{plain: (*plain)(r)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	r.ID = string(aux.ID)
	return nil
}

// TVDBRemoteIDs is a record's remoteIds.
type TVDBRemoteIDs []TVDBRemoteID

// Get returns the id at source (TVDBSourceIMDb, ...), or "".
func (r TVDBRemoteIDs) Get(source string) string {
	for _, id := range r {
		if strings.EqualFold(id.SourceName, source) {
			return id.ID
		}
	}
	return ""
}

// IMDbID is the record's IMDb id ("tt0903747"), or "".
func (r TVDBRemoteIDs) IMDbID() string { return r.Get(TVDBSourceIMDb) }

// TMDBID is the record's TMDB id, or 0. TVDB may write it as a bare number or
// with a kind in front ("tv/1396"); the number is what is returned.
func (r TVDBRemoteIDs) TMDBID() int64 {
	id := r.Get(TVDBSourceTMDB)
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// TVDBSearchResult is one hit of /search.
type TVDBSearchResult struct {
	TVDBID          string `json:"tvdb_id"` // "81189"
	Name            string `json:"name"`
	Slug            string `json:"slug"`
	Year            string `json:"year"`
	FirstAirTime    string `json:"first_air_time"`
	Overview        string `json:"overview"`
	ImageURL        string `json:"image_url"`
	Thumbnail       string `json:"thumbnail"`
	PrimaryLanguage string `json:"primary_language"`
	Status          string `json:"status"`
	Network         string `json:"network"`
	// Translations and Overviews map a TVDB language code ("por") to the name
	// or overview in it. The search itself is not restricted by language: TVDB's
	// language parameter filters by a show's primary language, not by the text.
	Translations map[string]string `json:"translations"`
	Overviews    map[string]string `json:"overviews"`
	RemoteIDs    TVDBRemoteIDs     `json:"remote_ids"`
}

func (r *TVDBSearchResult) UnmarshalJSON(b []byte) error {
	type plain TVDBSearchResult
	aux := struct {
		*plain
		TVDBID       flexString `json:"tvdb_id"`
		Year         flexString `json:"year"`
		Translations flexMap    `json:"translations"`
		Overviews    flexMap    `json:"overviews"`
	}{plain: (*plain)(r)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	r.TVDBID, r.Year = string(aux.TVDBID), string(aux.Year)
	r.Translations, r.Overviews = aux.Translations, aux.Overviews
	return nil
}

// SeriesID is TVDBID as a number, or 0 when it is not one.
func (r TVDBSearchResult) SeriesID() int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(r.TVDBID), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// TVDBGenre is a genre as TVDB names it.
type TVDBGenre struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// TVDBArtwork is an entry of a series' artworks. Type is TVDB's id of the kind
// of artwork (poster, background, ...), from /artwork/types. Image and
// Thumbnail are full artworks.thetvdb.com addresses; see TVDBImagePath.
type TVDBArtwork struct {
	ID           int64   `json:"id"`
	Type         int64   `json:"type"`
	Image        string  `json:"image"`
	Thumbnail    string  `json:"thumbnail"`
	Language     string  `json:"language"` // a TVDB code; empty: no text on it
	Score        float64 `json:"score"`
	Width        int     `json:"width"`
	Height       int     `json:"height"`
	IncludesText bool    `json:"includesText"`
}

// URL is the address of the artwork: with small, the thumbnail TVDB names when
// it names one, else the image with "_t" added as TVDBImageURL does. It is ""
// when the paths are not plain TVDB ones.
func (a TVDBArtwork) URL(small bool) string {
	if small && a.Thumbnail != "" {
		if u := TVDBImageURL(a.Thumbnail, false); u != "" {
			return u
		}
	}
	return TVDBImageURL(a.Image, small)
}

// TVDBCharacter is an entry of a series' characters: a person in a role.
// PeopleType says what the role is ("Actor", "Director", "Writer", ...);
// Type is TVDB's id of it.
type TVDBCharacter struct {
	PeopleID     int64  `json:"peopleId"`
	PersonName   string `json:"personName"`
	PeopleType   string `json:"peopleType"`
	Type         int64  `json:"type"`
	Name         string `json:"name"` // the character played
	Sort         int    `json:"sort"`
	Image        string `json:"image"`        // the character's image
	PersonImgURL string `json:"personImgURL"` // the person's
	IsFeatured   bool   `json:"isFeatured"`
}

func (c *TVDBCharacter) UnmarshalJSON(b []byte) error {
	type plain TVDBCharacter
	aux := struct {
		*plain
		PeopleID flexInt `json:"peopleId"`
	}{plain: (*plain)(c)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	c.PeopleID = int64(aux.PeopleID)
	return nil
}

// TVDBSeasonType says how a set of seasons is ordered: "official" (aired),
// "dvd", "absolute", "alternate" or "regional".
type TVDBSeasonType struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// TVDBSeason is a season in a series' extended record. A series has the seasons
// of every order, told apart by Type.Type.
type TVDBSeason struct {
	ID     int64          `json:"id"`
	Number int            `json:"number"`
	Image  string         `json:"image"`
	Type   TVDBSeasonType `json:"type"`
}

// TVDBContentRating is a rating of a series in one country.
type TVDBContentRating struct {
	Name        string `json:"name"`    // "TV-MA"
	Country     string `json:"country"` // "usa"
	ContentType string `json:"contentType"`
}

// TVDBCompany is a network or studio.
type TVDBCompany struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// TVDBTranslation is a name or an overview in one language.
type TVDBTranslation struct {
	Language  string `json:"language"` // a TVDB code
	Name      string `json:"name"`
	Overview  string `json:"overview"`
	IsPrimary bool   `json:"isPrimary"`
}

// TVDBTranslations is the translations of a record, every language it has.
type TVDBTranslations struct {
	NameTranslations     []TVDBTranslation `json:"nameTranslations"`
	OverviewTranslations []TVDBTranslation `json:"overviewTranslations"`
}

// Name is the name in lang (a BCP 47 tag or a TVDB code), or "" when TVDB has
// none: the caller then uses the original-language text.
func (t TVDBTranslations) Name(lang string) string {
	code := tvdbLanguage(lang)
	for _, tr := range t.NameTranslations {
		if code != "" && tr.Language == code && tr.Name != "" {
			return tr.Name
		}
	}
	return ""
}

// Overview is the overview in lang, or "" when TVDB has none.
func (t TVDBTranslations) Overview(lang string) string {
	code := tvdbLanguage(lang)
	for _, tr := range t.OverviewTranslations {
		if code != "" && tr.Language == code && tr.Overview != "" {
			return tr.Overview
		}
	}
	return ""
}

// TVDBSeries is a series: /series/{id}/extended (with meta=translations), the
// series record of /series/{id}/episodes, or the series of a remote id search.
// The shorter records leave out what only the extended one has.
type TVDBSeries struct {
	ID                int64   `json:"id"`
	Name              string  `json:"name"`
	Slug              string  `json:"slug"`
	Overview          string  `json:"overview"`
	Image             string  `json:"image"`
	Year              string  `json:"year"`
	FirstAired        string  `json:"firstAired"` // "2008-01-20"
	LastAired         string  `json:"lastAired"`
	NextAired         string  `json:"nextAired"` // empty when nothing is scheduled
	OriginalLanguage  string  `json:"originalLanguage"`
	OriginalCountry   string  `json:"originalCountry"`
	AverageRuntime    int     `json:"averageRuntime"`
	Score             float64 `json:"score"`
	DefaultSeasonType int64   `json:"defaultSeasonType"`
	Status            struct {
		Name string `json:"name"` // "Continuing", "Ended", ...
	} `json:"status"`
	OriginalNetwork TVDBCompany         `json:"originalNetwork"`
	Genres          []TVDBGenre         `json:"genres"`
	Artworks        []TVDBArtwork       `json:"artworks"`
	Characters      []TVDBCharacter     `json:"characters"`
	Seasons         []TVDBSeason        `json:"seasons"`
	RemoteIDs       TVDBRemoteIDs       `json:"remoteIds"`
	ContentRatings  []TVDBContentRating `json:"contentRatings"`
	Translations    TVDBTranslations    `json:"translations"`
}

func (s *TVDBSeries) UnmarshalJSON(b []byte) error {
	type plain TVDBSeries
	aux := struct {
		*plain
		ID   flexInt    `json:"id"`
		Year flexString `json:"year"`
	}{plain: (*plain)(s)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	s.ID, s.Year = int64(aux.ID), string(aux.Year)
	return nil
}

// TVDBEpisode is an episode of /series/{id}/episodes. Number and SeasonNumber
// are in the order that was asked for; AbsoluteNumber is 0 when there is none.
type TVDBEpisode struct {
	ID             int64  `json:"id"`
	SeriesID       int64  `json:"seriesId"`
	SeasonNumber   int    `json:"seasonNumber"`
	Number         int    `json:"number"`
	AbsoluteNumber int    `json:"absoluteNumber"`
	Name           string `json:"name"`
	Overview       string `json:"overview"`
	Aired          string `json:"aired"` // "2008-01-20"
	Runtime        int    `json:"runtime"`
	Image          string `json:"image"`
	FinaleType     string `json:"finaleType"`
}

func (e *TVDBEpisode) UnmarshalJSON(b []byte) error {
	type plain TVDBEpisode
	aux := struct {
		*plain
		ID       flexInt `json:"id"`
		SeriesID flexInt `json:"seriesId"`
	}{plain: (*plain)(e)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	e.ID, e.SeriesID = int64(aux.ID), int64(aux.SeriesID)
	return nil
}

// TVDBEpisodes is all the episodes of a series in one order and language, the
// pages put together and each episode once. Series is the series record of the
// first page.
type TVDBEpisodes struct {
	Series   TVDBSeries    `json:"series"`
	Episodes []TVDBEpisode `json:"episodes"`
}

// TVDBBiography is a person's biography in one language.
type TVDBBiography struct {
	Biography string `json:"biography"`
	Language  string `json:"language"` // a TVDB code
}

// TVDBPerson is /people/{id}/extended.
type TVDBPerson struct {
	ID          int64           `json:"id"`
	Name        string          `json:"name"`
	Image       string          `json:"image"`
	Birth       string          `json:"birth"`
	Death       string          `json:"death"`
	BirthPlace  string          `json:"birthPlace"`
	Biographies []TVDBBiography `json:"biographies"`
	RemoteIDs   TVDBRemoteIDs   `json:"remoteIds"`
}

func (p *TVDBPerson) UnmarshalJSON(b []byte) error {
	type plain TVDBPerson
	aux := struct {
		*plain
		ID flexInt `json:"id"`
	}{plain: (*plain)(p)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	p.ID = int64(aux.ID)
	return nil
}

// Biography is the biography in lang (a BCP 47 tag or a TVDB code), or "".
func (p *TVDBPerson) Biography(lang string) string {
	code := tvdbLanguage(lang)
	for _, b := range p.Biographies {
		if code != "" && b.Language == code && b.Biography != "" {
			return b.Biography
		}
	}
	return ""
}

// TVDBMovie is the movie of a remote id search.
type TVDBMovie struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Year  string `json:"year"`
	Image string `json:"image"`
}

func (m *TVDBMovie) UnmarshalJSON(b []byte) error {
	type plain TVDBMovie
	aux := struct {
		*plain
		ID   flexInt    `json:"id"`
		Year flexString `json:"year"`
	}{plain: (*plain)(m)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	m.ID, m.Year = int64(aux.ID), string(aux.Year)
	return nil
}

// TVDBRemoteMatch is one hit of /search/remoteid: a base record of whatever
// the id belongs to. Only the field of that kind is set.
type TVDBRemoteMatch struct {
	Series *TVDBSeries `json:"series"`
	Movie  *TVDBMovie  `json:"movie"`
	People *TVDBPerson `json:"people"`
}

// tvdbLinks is the paging of a list: next is empty on the last page, and
// total_items is how many entries the whole list has (nil: not said).
type tvdbLinks struct {
	Next       string   `json:"next"`
	TotalItems *flexInt `json:"total_items"`
}

// total is the number of entries in the whole list, when TVDB says.
func (l tvdbLinks) total() (int, bool) {
	if l.TotalItems == nil {
		return 0, false
	}
	return int(*l.TotalItems), true
}

// tvdbUpdate is an entry of /updates. The type of a record is in recordType or
// entityType (TVDB fills one of them, depending on the kind of update).
type tvdbUpdate struct {
	RecordID   int64  `json:"recordId"`
	RecordType string `json:"recordType"`
	EntityType string `json:"entityType"`
}

func (u *tvdbUpdate) UnmarshalJSON(b []byte) error {
	type plain tvdbUpdate
	aux := struct {
		*plain
		RecordID flexInt `json:"recordId"`
	}{plain: (*plain)(u)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	u.RecordID = int64(aux.RecordID)
	return nil
}

// isSeries is false only for a record that says it is something else.
func (u tvdbUpdate) isSeries() bool {
	return (u.RecordType == "" || u.RecordType == "series") && (u.EntityType == "" || u.EntityType == "series")
}

// tvdbEpisodesPage is a page of /series/{id}/episodes in the shape of the route
// with no language: data = {series, episodes}. The route with a language puts
// the episodes inside the series record, which is read separately.
type tvdbEpisodesPage struct {
	Series   *TVDBSeries   `json:"series"`
	Episodes []TVDBEpisode `json:"episodes"`
}
