package metadata

import (
	"cmp"
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"ply/internal/store"
)

// What is kept of a title's videos, and for how long.
const (
	maxVideos     = 10
	maxVideoName  = 200 // runes
	maxVideoLang  = 8   // "en" (TMDB), "eng" (TVDB): each provider's own code
	trailersTTL   = 30 * 24 * time.Hour
	trailersFetch = 8 * time.Second
)

// trailerLister is a provider that can list the trailers of a title (kind store.KindMovie or
// store.KindSeries, id its own), best first.
type trailerLister interface {
	Trailers(ctx context.Context, kind string, id int64) ([]store.Video, error)
}

// Trailers implements trailerLister: the trailers and teasers TMDB has in our language, in
// English and with no language.
func (p *TMDB) Trailers(ctx context.Context, kind string, id int64) ([]store.Video, error) {
	res, err := p.c.Videos(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	var out []store.Video
	for _, v := range res {
		typ := strings.ToLower(v.Type)
		if typ != store.VideoTrailer && typ != store.VideoTeaser {
			continue
		}
		out = append(out, store.Video{Site: strings.ToLower(v.Site), Key: v.Key, Name: v.Name, Type: typ,
			Language: strings.ToLower(v.Language), Official: v.Official, PublishedAt: dateOf(v.PublishedAt)})
	}
	return rankVideos(out, p.c.langCode(), "en"), nil
}

// Trailers implements trailerLister: the trailers of a show's extended record (TVDB has no
// movies), those at a site that plays them.
func (p *TVDBProvider) Trailers(ctx context.Context, kind string, id int64) ([]store.Video, error) {
	if kind != store.KindSeries {
		return nil, nil
	}
	s, err := p.c.SeriesExtended(ctx, id)
	if err != nil {
		return nil, err
	}
	var out []store.Video
	for _, t := range s.Trailers {
		if site, key, ok := ParseVideoURL(t.URL); ok {
			out = append(out, store.Video{Site: site, Key: key, Name: t.Name, Type: store.VideoTrailer,
				Language: strings.ToLower(t.Language)})
		}
	}
	return rankVideos(out, p.c.LanguageCode(), "eng"), nil
}

// rankVideos drops the videos that cannot be played (an unknown site, a key that is not one)
// and the second of the same video, and orders the rest best first: in the first of langs, then
// the next, then with no language, then any other; a trailer before a teaser; an official one
// first; the newest first. At most maxVideos are kept.
func rankVideos(vs []store.Video, langs ...string) []store.Video {
	var out []store.Video
	seen := map[string]bool{}
	for _, v := range vs {
		id := v.Site + ":" + v.Key
		if !store.ValidVideo(v.Site, v.Key) || seen[id] {
			continue
		}
		seen[id] = true
		v.Name = clipRunes(strings.TrimSpace(v.Name), maxVideoName)
		v.Language = clipRunes(v.Language, maxVideoLang)
		out = append(out, v)
	}
	langRank := func(l string) int {
		if i := slices.IndexFunc(langs, func(x string) bool { return strings.EqualFold(x, l) }); i >= 0 && l != "" {
			return i
		}
		if l == "" {
			return len(langs)
		}
		return len(langs) + 1
	}
	typeRank := func(t string) int {
		if t == store.VideoTeaser {
			return 1
		}
		return 0
	}
	official := func(v store.Video) int {
		if v.Official {
			return 0
		}
		return 1
	}
	slices.SortStableFunc(out, func(a, b store.Video) int {
		return cmp.Or(
			cmp.Compare(langRank(a.Language), langRank(b.Language)),
			cmp.Compare(typeRank(a.Type), typeRank(b.Type)),
			cmp.Compare(official(a), official(b)),
			strings.Compare(b.PublishedAt, a.PublishedAt),
		)
	})
	return out[:min(len(out), maxVideos)]
}

// dateOf is the YYYY-MM-DD at the start of a timestamp, or "" (reDate is the NFO's).
func dateOf(s string) string { return reDate.FindString(s) }

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ParseVideoURL reads the address of a video page at YouTube or Vimeo ("https://www.youtube.com/watch?v=…",
// "youtu.be/…", "/embed/…", "/shorts/…", "https://vimeo.com/123", "player.vimeo.com/video/123") and gives the
// site (store.SiteYouTube, store.SiteVimeo) and the video's key. ok is false for anything else.
func ParseVideoURL(raw string) (site, key string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return "", "", false
	}
	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(strings.TrimPrefix(host, "www."), "m.")
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch host {
	case "youtube.com", "youtube-nocookie.com":
		site = store.SiteYouTube
		switch {
		case len(segs) == 1 && segs[0] == "watch":
			key = u.Query().Get("v")
		case len(segs) == 2 && (segs[0] == "embed" || segs[0] == "shorts" || segs[0] == "v"):
			key = segs[1]
		}
	case "youtu.be":
		site = store.SiteYouTube
		if len(segs) == 1 {
			key = segs[0]
		}
	case "vimeo.com":
		site = store.SiteVimeo
		if len(segs) == 1 {
			key = segs[0]
		}
	case "player.vimeo.com":
		site = store.SiteVimeo
		if len(segs) == 2 && segs[0] == "video" {
			key = segs[1]
		}
	}
	if !store.ValidVideo(site, key) {
		return "", "", false
	}
	return site, key, true
}

// trailerSource is where a title's trailers are asked for, and the ref they are stored under: TMDB
// for a title it knows when it is on, else TVDB for a show it knows. nil when neither can be asked.
// The ref names the language too ("tmdb:603@en-US"): the videos asked for are in it, so another
// language is another list.
func trailerSource(p *Providers, t store.Title, lang string) (from trailerLister, id int64, ref string) {
	if l, ok := p.TMDB.(trailerLister); ok && t.TMDBID != 0 {
		return l, t.TMDBID, SourceTMDB + ":" + strconv.FormatInt(t.TMDBID, 10) + "@" + lang
	}
	if l, ok := p.TVDB.(trailerLister); ok && t.TVDBID != 0 && t.Kind == store.KindSeries {
		return l, t.TVDBID, SourceTVDB + ":" + strconv.FormatInt(t.TVDBID, 10) + "@" + lang
	}
	return nil, 0, ""
}

// Trailers returns a title's trailers, best first. They are fetched the first time anyone wants
// them (trailerSource says where from), stored, and fetched again once they are trailersTTL old or
// the title's id at the provider or the language changed. ErrLookupsOff when nothing may be asked:
// then none is returned, not even stored ones, since playing one reaches the site the privacy
// switch keeps away. store.ErrNotFound for no such title. A failed fetch is remembered as a
// person's is (see fetchFailure), for that ref, and returned with the trailers stored for it,
// which are better than none.
func (w *Worker) Trailers(ctx context.Context, titleID int64) ([]store.Video, error) {
	t, err := w.o.Store.GetTitle(ctx, titleID)
	if err != nil {
		return nil, err
	}
	p, r := w.o.Source.Provider(ctx)
	if p == nil {
		return nil, ErrLookupsOff
	}
	from, id, ref := trailerSource(p, t, r.Language)
	if from == nil {
		return nil, nil
	}
	stored, err := w.o.Store.GetTitleVideos(ctx, titleID)
	if err != nil {
		return nil, err
	}
	var have []store.Video
	if stored.Ref == ref {
		have = stored.Videos
	}
	now := w.now()
	if stored.Ref == ref && now.Sub(time.Unix(stored.CheckedAt, 0)) < trailersTTL {
		return have, nil
	}
	w.mu.Lock()
	f, failed := w.trailerFailed[titleID]
	w.mu.Unlock()
	if failed && f.ref != ref {
		failed, f = false, fetchFailure{} // what failed was another id's, or another language's
	}
	if failed && now.Before(f.until) {
		return have, f.err
	}
	fctx, cancel := context.WithTimeout(ctx, trailersFetch)
	videos, err := from.Trailers(fctx, t.Kind, id)
	cancel()
	if err != nil {
		w.noteFailure(ctx, w.trailerFailed, titleID, ref, f.attempts, err, now)
		return have, err
	}
	w.mu.Lock()
	delete(w.trailerFailed, titleID)
	w.mu.Unlock()
	// What was fetched is kept even if the caller has gone meanwhile.
	return videos, w.o.Store.SetTitleVideos(context.WithoutCancel(ctx), titleID, ref, videos, now.Unix())
}
