package metadata

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"ply/internal/store"
)

// Errors of the lookups that need a provider that is not set up.
var (
	// ErrNoTVDB means a call needs TheTVDB and there is no working TVDB key.
	ErrNoTVDB = errors.New("TheTVDB is not set up: add a TVDB key in the settings")
	// ErrNoTMDB means a call needs TMDB and there is no TMDB key.
	ErrNoTMDB = errors.New("TMDB is not set up: add a TMDB key in the settings")
)

// Providers is what the worker can ask right now. Movies are TMDB's. Shows are
// TheTVDB's when it has a key, with TMDB for the shows TVDB does not place; with no
// TVDB key they are TMDB's, as in M6. A nil *Providers, or one with neither
// provider, is "online lookups are off".
type Providers struct {
	TMDB Provider // nil: no TMDB key
	TVDB Provider // nil: no TVDB key
}

// NewProviders returns the providers, or nil when there is none.
func NewProviders(tmdb, tvdb Provider) *Providers {
	if tmdb == nil && tvdb == nil {
		return nil
	}
	return &Providers{TMDB: tmdb, TVDB: tvdb}
}

// Movies is the provider of movies (TMDB), or nil.
func (p *Providers) Movies() Provider {
	if p == nil {
		return nil
	}
	return p.TMDB
}

// HasTVDB reports whether shows come from TVDB first.
func (p *Providers) HasTVDB() bool { return p != nil && p.TVDB != nil }

// ShowsOn reports whether shows can be looked up at all.
func (p *Providers) ShowsOn() bool { return p != nil && (p.TVDB != nil || p.TMDB != nil) }

// For is the provider of a source (SourceTMDB, SourceTVDB), or nil.
func (p *Providers) For(source string) Provider {
	switch {
	case p == nil:
		return nil
	case source == SourceTVDB:
		return p.TVDB
	}
	return p.TMDB
}

// MatchShow finds a show: at TVDB when it is on, else at TMDB. A show TVDB does not
// match with confidence (below MatchThreshold) is looked for at TMDB too, and the
// better of the two wins, TVDB's on a tie. A show TVDB cannot be asked about (out
// of reach, key refused) is an error, not a reason to go to TMDB: the show would
// move between providers with the weather. Nor is TMDB's being out of reach a
// reason to settle for TVDB's doubtful match.
//
// An id hint decides it: a TVDB id or an IMDb id at TVDB; a TMDB id alone (nothing
// that TVDB can place) goes straight to TMDB.
func (p *Providers) MatchShow(ctx context.Context, q Query) (Result, error) {
	switch {
	case !p.ShowsOn():
		return Result{}, ErrLookupsOff
	case p.TVDB == nil:
		return Match(ctx, p.TMDB, q)
	case p.TMDB == nil:
		return Match(ctx, p.TVDB, q)
	}
	if h := q.Hints; h.TMDB != 0 && h.TVDB == 0 && h.IMDb == "" {
		return Match(ctx, p.TMDB, q)
	}
	res, err := Match(ctx, p.TVDB, q)
	if err != nil {
		return Result{}, err
	}
	if res.ByID || (res.State == store.MatchMatched && !res.NeedsReview) {
		return res, nil
	}
	fb, err := Match(ctx, p.TMDB, q)
	if err != nil {
		return Result{}, err
	}
	if fb.State == store.MatchMatched && (fb.ByID || fb.Score > res.Score) {
		return fb, nil
	}
	return res, nil
}

// SearchShows lists the shows a query could mean for the "fix match" dialog: TVDB's,
// then TMDB's, each best first. One of the two being out of reach is no reason to
// show nothing: the error is returned only when neither answered.
func (p *Providers) SearchShows(ctx context.Context, q Query) ([]Scored, error) {
	if !p.ShowsOn() {
		return nil, ErrLookupsOff
	}
	var (
		out      []Scored
		firstErr error
		answered bool
	)
	for _, prov := range []Provider{p.TVDB, p.TMDB} {
		if prov == nil {
			continue
		}
		res, err := Search(ctx, prov, q)
		if err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			firstErr = cmpErr(firstErr, err)
			continue
		}
		answered = true
		out = append(out, res...)
	}
	if !answered {
		return nil, firstErr
	}
	return out, nil
}

func cmpErr(first, err error) error {
	if first != nil {
		return first
	}
	return err
}

// slugResolver is a provider that can turn the slug of a thetvdb.com address into an id.
type slugResolver interface {
	SeriesBySlug(ctx context.Context, slug string) (int64, error)
}

// SeriesBySlug finds the show whose thetvdb.com address ends in slug
// ("breaking-bad"): the TVDB search for its words, and the result with exactly that
// slug. ErrNoSuchTitle when there is none.
func (p *TVDBProvider) SeriesBySlug(ctx context.Context, slug string) (int64, error) {
	res, err := p.c.Search(ctx, strings.ReplaceAll(slug, "-", " "), 0)
	if err != nil {
		return 0, err
	}
	for _, r := range res {
		if r.Slug == slug && r.SeriesID() != 0 {
			return r.SeriesID(), nil
		}
	}
	return 0, ErrNoSuchTitle
}

// ShowRef is the show a person named in a manual match: by an id at one provider, by
// the slug of a thetvdb.com address, or by IMDb id.
type ShowRef struct {
	Source   string // SourceTMDB | SourceTVDB, for ID
	ID       int64
	TVDBSlug string
	IMDbID   string
}

// ResolveShow turns a Ref into the show it names. An IMDb id is looked up at TVDB
// first when it is on, then at TMDB. ErrNoTVDB or ErrNoTMDB when the provider the
// ref needs is not set up, ErrNoSuchTitle when it does not know the title.
func (p *Providers) ResolveShow(ctx context.Context, ref Ref) (ShowRef, error) {
	if !p.ShowsOn() {
		return ShowRef{}, ErrLookupsOff
	}
	switch {
	case ref.TMDBID > 0:
		if p.TMDB == nil {
			return ShowRef{}, ErrNoTMDB
		}
		return ShowRef{Source: SourceTMDB, ID: ref.TMDBID}, nil
	case ref.TVDBID > 0:
		if p.TVDB == nil {
			return ShowRef{}, ErrNoTVDB
		}
		return ShowRef{Source: SourceTVDB, ID: ref.TVDBID}, nil
	case ref.TVDBSlug != "":
		if p.TVDB == nil {
			return ShowRef{}, ErrNoTVDB
		}
		sr, ok := p.TVDB.(slugResolver)
		if !ok {
			return ShowRef{}, ErrNoSuchTitle
		}
		id, err := sr.SeriesBySlug(ctx, ref.TVDBSlug)
		if err != nil {
			return ShowRef{}, err
		}
		return ShowRef{Source: SourceTVDB, ID: id}, nil
	}
	for _, prov := range []Provider{p.TVDB, p.TMDB} {
		if prov == nil {
			continue
		}
		_, series, err := prov.Find(ctx, ExternalIMDb, ref.IMDbID)
		if err != nil {
			return ShowRef{}, err
		}
		if len(series) > 0 {
			return ShowRef{Source: prov.Name(), ID: series[0].ID}, nil
		}
	}
	return ShowRef{}, ErrNoSuchTitle
}

// Lookup finds the one show a person pasted in the search box, as a candidate that
// scores 1: "tvdb:81189" or the address of a show at thetvdb.com. ok is false when
// the text is neither (then it is a title to search for). The candidate has what the
// fetch of the show gives.
func (p *Providers) Lookup(ctx context.Context, text string) (c Scored, ok bool, err error) {
	id, slug, found := ParseTVDBRef(text)
	if !found {
		return Scored{}, false, nil
	}
	if p == nil || p.TVDB == nil {
		return Scored{}, true, ErrNoTVDB
	}
	if id == 0 {
		sr, can := p.TVDB.(slugResolver)
		if !can {
			return Scored{}, true, ErrNoSuchTitle
		}
		if id, err = sr.SeriesBySlug(ctx, slug); err != nil {
			return Scored{}, true, err
		}
	}
	var cand Candidate
	if b, can := p.TVDB.(briefer); can {
		cand, err = b.Brief(ctx, id)
	} else {
		var in *store.TitleInput
		if in, err = p.TVDB.Series(ctx, id, store.OrderAired, nil); err == nil {
			cand = Candidate{ID: id, Source: SourceTVDB, Title: in.Title.Title, OriginalTitle: in.OriginalTitle,
				Year: in.Year, Overview: in.Overview, Poster: in.Poster}
		}
	}
	if IsNotFound(err) {
		return Scored{}, true, ErrNoSuchTitle
	}
	if err != nil {
		return Scored{}, true, err
	}
	cand.ID, cand.Source = id, SourceTVDB
	return Scored{Candidate: cand, Score: 1}, true, nil
}

// briefer is a provider that can describe a show without fetching its episodes.
type briefer interface {
	Brief(ctx context.Context, id int64) (Candidate, error)
}

// Brief describes a show in a search result's terms: one request, no episodes.
func (p *TVDBProvider) Brief(ctx context.Context, id int64) (Candidate, error) {
	s, err := p.c.SeriesExtended(ctx, id)
	if err != nil {
		return Candidate{}, err
	}
	lang := p.c.Language()
	return Candidate{ID: s.ID, Source: SourceTVDB, Title: firstNonEmpty(s.Translations.Name(lang), s.Name), OriginalTitle: s.Name,
		Year: firstInt(yearOf(s.FirstAired), atoi(s.Year)), Overview: firstNonEmpty(s.Translations.Overview(lang), s.Overview),
		Poster: TVDBImageRef(s.Image)}, nil
}

var (
	reTVDBPrefix = regexp.MustCompile(`(?i)^tvdb\s*:\s*(\d{1,10})$`)
	reTVDBSlug   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,199}$`)
)

// ParseTVDBRef reads what a person types to name a show at TVDB: "tvdb:81189", or the
// address of its page at thetvdb.com ("https://thetvdb.com/series/breaking-bad",
// with or without the scheme and "www", with a trailing "/allseasons/official" or a
// query; the old "?tab=series&id=81189" form). It gives the id, or the slug when the
// address has no id; ok is false for anything else, including an address at any other
// host.
func ParseTVDBRef(text string) (id int64, slug string, ok bool) {
	text = strings.TrimSpace(text)
	if m := reTVDBPrefix.FindStringSubmatch(text); m != nil {
		n, _ := strconv.ParseInt(m[1], 10, 64)
		return n, "", n > 0
	}
	if !strings.Contains(text, "://") {
		text = "https://" + text
	}
	u, err := url.Parse(text)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Port() != "" {
		return 0, "", false
	}
	if host := strings.ToLower(u.Hostname()); host != "thetvdb.com" && host != "www.thetvdb.com" {
		return 0, "", false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) >= 2 && segs[0] == "dereferrer" { // /dereferrer/series/81189
		segs = segs[1:]
	}
	if len(segs) >= 2 && segs[0] == "series" {
		name := segs[1]
		if n, err := strconv.ParseInt(name, 10, 64); err == nil && n > 0 {
			return n, "", true
		}
		if reTVDBSlug.MatchString(name) {
			return 0, strings.ToLower(name), true
		}
		return 0, "", false
	}
	// The old address: thetvdb.com/?tab=series&id=81189
	if q := u.Query(); q.Get("tab") == "series" {
		if n, err := strconv.ParseInt(q.Get("id"), 10, 64); err == nil && n > 0 {
			return n, "", true
		}
	}
	return 0, "", false
}
