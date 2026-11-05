package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ply/internal/metadata"
	"ply/internal/store"
)

// Limits on what a person can edit and how long a request may take.
const (
	maxEditBody    = 1 << 16
	peopleLimit    = 20
	personFetch    = 8 * time.Second
	matchTimeout   = 45 * time.Second
	searchTimeout  = 20 * time.Second
	maxTitleLen    = 500
	maxTextLen     = 20000
	maxTaglineLen  = 1000
	maxSearchQuery = 200
	upcomingLimit  = 50
)

var reIMDb = regexp.MustCompile(`^tt\d{7,8}$`)

func parseID(w http.ResponseWriter, r *http.Request, what string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, what+" not found")
		return 0, false
	}
	return id, true
}

// metadataOff answers a request that needs TMDB or the worker when there is
// none; it reports whether it did.
func (s *Server) metadataOff(w http.ResponseWriter) bool {
	if s.Metadata == nil {
		writeError(w, http.StatusServiceUnavailable, "metadata is not available")
		return true
	}
	return false
}

// providerName is whose error err is: TVDB's (a *metadata.TVDBError) or TMDB's.
func providerName(err error) string {
	var te *metadata.TVDBError
	if errors.As(err, &te) {
		return "TVDB"
	}
	return "TMDB"
}

// metadataError turns an error of the metadata package into a response.
func (s *Server) metadataError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, metadata.ErrLookupsOff):
		writeError(w, http.StatusConflict, "online metadata lookups are off: set a TMDB or TVDB key and turn them on in the settings")
	case errors.Is(err, metadata.ErrNoTVDB), errors.Is(err, metadata.ErrNoTMDB):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, metadata.ErrNoSuchTitle):
		writeError(w, http.StatusBadRequest, "no title with that id")
	case errors.Is(err, metadata.ErrNoSuchOrder):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, metadata.ErrNotTVDB):
		writeError(w, http.StatusBadRequest, err.Error())
	case r.Context().Err() != nil:
		// the client went away
	case metadata.IsUnauthorized(err):
		writeError(w, http.StatusBadGateway, providerName(err)+" refused the key")
	case metadata.IsTransient(err), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusBadGateway, "could not reach "+providerName(err)+": "+err.Error())
	default:
		s.internalError(w, r, err)
	}
}

// ---- people, genres, titles, collections ----

func (s *Server) handlePerson(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "person")
	if !ok {
		return
	}
	if s.Metadata != nil {
		// The details are fetched the first time anyone opens the page. A
		// failure only means the page has less on it.
		ctx, cancel := context.WithTimeout(r.Context(), personFetch)
		// A person of TVDB that TMDB knows too is merged into the TMDB person: the page is that one's.
		now, err := s.Metadata.EnsurePersonID(ctx, id)
		if err != nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, metadata.ErrLookupsOff) {
			s.Logger.Debug("metadata: person details", "id", id, "err", err)
		}
		id = now
		cancel()
	}
	d, err := s.Store.GetPerson(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "person not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := Person{
		PersonCard: PersonCard{ID: d.ID, Name: d.Name, Profile: s.imageURL(d.Profile, profileSize), KnownFor: d.KnownFor},
		Biography:  d.Biography, Birthday: d.Birthday, Deathday: d.Deathday, PlaceOfBirth: d.PlaceOfBirth, IMDbID: d.IMDbID,
		TMDBID: d.TMDBID, TVDBID: d.TVDBID, Credits: []PersonCredit{},
	}
	for _, c := range d.Credits {
		out.Credits = append(out.Credits, PersonCredit{TitleID: c.Title.ID, Kind: c.Kind, Job: c.Job, Character: c.Character,
			Title: s.titleCard(c.Title)})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePeople(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > maxSearchQuery {
		writeError(w, http.StatusBadRequest, "q is too long")
		return
	}
	people, err := s.Store.SearchPeople(r.Context(), q, peopleLimit)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]PersonCard, len(people))
	for i, p := range people {
		out[i] = PersonCard{ID: p.ID, Name: p.Name, Profile: s.imageURL(p.Profile, profileSize), KnownFor: p.KnownFor}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGenres(w http.ResponseWriter, r *http.Request) {
	genres, err := s.Store.Genres(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]GenreCount, len(genres))
	for i, g := range genres {
		out[i] = GenreCount{ID: g.ID, Name: g.Name, Count: g.Count}
	}
	writeJSON(w, http.StatusOK, out)
}

// filterID reads an optional positive integer query parameter.
func filterID(q map[string][]string, name string) (int64, bool) {
	vals := q[name]
	if len(vals) == 0 || vals[0] == "" {
		return 0, true
	}
	id, err := strconv.ParseInt(vals[0], 10, 64)
	return id, err == nil && id > 0
}

func (s *Server) handleTitles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var f store.TitleFilter
	var ok1, ok2, ok3 bool
	f.GenreID, ok1 = filterID(q, "genre")
	f.CollectionID, ok2 = filterID(q, "collection")
	f.PersonID, ok3 = filterID(q, "person")
	if !ok1 || !ok2 || !ok3 {
		writeError(w, http.StatusBadRequest, "genre, collection and person must be ids")
		return
	}
	cards, err := s.Store.Titles(r.Context(), f)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.titleCards(cards))
}

// handleUpcoming lists the library's shows with an episode still to air, soonest
// first. Today is the server's own date: TMDB's air dates have no zone.
func (s *Server) handleUpcoming(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	shows, err := s.Store.Upcoming(r.Context(), now.Format(time.DateOnly), upcomingLimit)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]UpcomingShow, len(shows))
	for i, u := range shows {
		card := s.titleCard(u.TitleCard)
		out[i] = UpcomingShow{TitleID: u.ID, Show: u.Show, Poster: card.Poster, Backdrop: card.Backdrop, NextAirDate: u.NextAirDate}
		if e := u.NextEpisode; e.Number > 0 {
			out[i].NextEpisode = &NextEpisode{Season: e.Season, Episode: e.Number, Name: e.Name}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCollection(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "collection")
	if !ok {
		return
	}
	c, err := s.Store.GetCollection(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "collection not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, CollectionDetail{ID: c.ID, Name: c.Name, Overview: c.Overview,
		Poster: s.imageURL(c.Poster, posterSize), Backdrop: s.imageURL(c.Backdrop, backdropSize), Titles: s.titleCards(c.Titles)})
}

// ---- shows ----

// showNames resolves {name}: exact, else the way the show filter does, without
// regard to case.
func (s *Server) findShow(w http.ResponseWriter, r *http.Request) (store.Show, bool) {
	name := r.PathValue("name")
	shows, err := s.Store.Shows(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return store.Show{}, false
	}
	for _, exact := range []bool{true, false} {
		for _, sh := range shows {
			if sh.Name == name || (!exact && strings.EqualFold(sh.Name, name)) {
				return sh, true
			}
		}
	}
	writeError(w, http.StatusNotFound, "show not found")
	return store.Show{}, false
}

// showMeta fills the metadata fields of a show from its row and title.
func (s *Server) showMeta(sm store.SeriesMatch, hasRow bool, b store.TitleBrief, hasBrief bool) (MetaFields, *int, string) {
	m := MetaFields{MatchState: store.MatchPending}
	if hasRow {
		m.MatchState, m.NeedsReview = sm.State, sm.NeedsReview
	}
	var own, ownBack string
	if sm.LocalPoster != "" {
		own = store.LocalRef(store.ArtSeries, sm.ID, "poster")
	}
	if sm.LocalBackdrop != "" {
		ownBack = store.LocalRef(store.ArtSeries, sm.ID, "backdrop")
	}
	var year *int
	var overview string
	if hasBrief {
		m.TitleID = ptr(b.ID)
		m.Logo = s.imageURL(b.Logo, logoSize)
		m.Rating = optFloat(b.Rating)
		m.Certification = b.Certification
		m.Genres = b.Genres
		year, overview = optInt(b.Year), b.Overview
		m.Poster = s.imageURL(pickArt(sm.LocalPoster, own, b.Poster), posterSize)
		m.Backdrop = s.imageURL(pickArt(sm.LocalBackdrop, ownBack, b.Backdrop), backdropSize)
	} else {
		m.Poster, m.Backdrop = s.imageURL(own, posterSize), s.imageURL(ownBack, backdropSize)
	}
	return m, year, overview
}

func (s *Server) handleShows(w http.ResponseWriter, r *http.Request) {
	shows, err := s.Store.Shows(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	rows, err := s.Store.SeriesMatches(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var ids []int64
	for _, sh := range shows {
		if id := rows[sh.Name].TitleID; id != 0 {
			ids = append(ids, id)
		}
	}
	briefs, err := s.Store.TitleBriefs(r.Context(), ids)
	if err != nil {
		s.Logger.Warn("metadata: reading titles", "err", err)
	}
	out := make([]ShowSummary, len(shows))
	for i, sh := range shows {
		row, hasRow := rows[sh.Name]
		b, hasBrief := briefs[row.TitleID]
		m, year, overview := s.showMeta(row, hasRow, b, hasBrief && row.TitleID != 0)
		out[i] = ShowSummary{Show: sh.Name, Seasons: sh.Seasons, EpisodeCount: sh.EpisodeCount, Year: year, Overview: overview, MetaFields: m}
	}
	writeJSON(w, http.StatusOK, out)
}

func seasonName(n int) string {
	if n == 0 {
		return "Specials"
	}
	return fmt.Sprintf("Season %d", n)
}

// showDetail builds the response of GET /api/shows/{name}.
func (s *Server) showDetail(ctx context.Context, sh store.Show) (ShowDetail, error) {
	row, err := s.Store.GetSeriesMatch(ctx, sh.Name)
	hasRow := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return ShowDetail{}, err
	}
	var (
		brief    store.TitleBrief
		hasBrief bool
		detail   *TitleDetail
		stored   = map[int]store.Season{}
	)
	if hasRow && row.TitleID != 0 {
		if briefs, err := s.Store.TitleBriefs(ctx, []int64{row.TitleID}); err == nil {
			brief, hasBrief = briefs[row.TitleID]
		}
		if d, err := s.Store.GetTitleDetail(ctx, row.TitleID); err == nil {
			detail = s.titleDetail(d)
		}
		if seasons, err := s.Store.Seasons(ctx, row.TitleID); err == nil {
			for _, se := range seasons {
				stored[se.Number] = se
			}
		}
	}
	m, year, overview := s.showMeta(row, hasRow, brief, hasBrief)
	out := ShowDetail{Show: sh.Name, EpisodeCount: sh.EpisodeCount, Year: year, Overview: overview, MetaFields: m, Meta: detail,
		Seasons: []SeasonInfo{}}

	counts := map[int]int{}
	items, err := s.Store.ShowItems(ctx, sh.Name)
	if err != nil {
		return ShowDetail{}, err
	}
	for _, it := range items {
		counts[it.Season]++
	}
	for _, n := range sh.Seasons {
		si := SeasonInfo{Season: n, Name: seasonName(n), EpisodeCount: counts[n]}
		if se, ok := stored[n]; ok {
			si.Name = firstNonEmpty(se.Name, si.Name)
			si.Overview, si.AirDate, si.Poster = se.Overview, se.AirDate, s.imageURL(se.Poster, posterSize)
		}
		out.Seasons = append(out.Seasons, si)
	}
	return out, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func (s *Server) handleShow(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.findShow(w, r)
	if !ok {
		return
	}
	d, err := s.showDetail(r.Context(), sh)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// ---- fixing matches ----

type matchRequest struct {
	TMDBID *int64  `json:"tmdbId"`
	IMDbID *string `json:"imdbId"`
	// For a show: TVDB's id, or the address of its page at thetvdb.com ("tvdb:81189" and
	// "thetvdb.com/series/breaking-bad" are accepted too).
	TVDBID *int64  `json:"tvdbId"`
	TVDB   *string `json:"tvdb"`
}

// ref validates the body of a match request: exactly one of tmdbId, imdbId, tvdbId and
// tvdb.
func (m matchRequest) ref() (metadata.Ref, string) {
	given := 0
	for _, set := range []bool{m.TMDBID != nil, m.IMDbID != nil, m.TVDBID != nil, m.TVDB != nil} {
		if set {
			given++
		}
	}
	switch {
	case given > 1:
		return metadata.Ref{}, "give one of tmdbId, tvdbId, tvdb and imdbId, not several"
	case m.TMDBID != nil:
		if *m.TMDBID <= 0 {
			return metadata.Ref{}, "tmdbId must be a positive number"
		}
		return metadata.Ref{TMDBID: *m.TMDBID}, ""
	case m.TVDBID != nil:
		if *m.TVDBID <= 0 {
			return metadata.Ref{}, "tvdbId must be a positive number"
		}
		return metadata.Ref{TVDBID: *m.TVDBID}, ""
	case m.TVDB != nil:
		id, slug, ok := metadata.ParseTVDBRef(*m.TVDB)
		if !ok {
			return metadata.Ref{}, `tvdb must look like tvdb:81189 or https://thetvdb.com/series/breaking-bad`
		}
		return metadata.Ref{TVDBID: id, TVDBSlug: slug}, ""
	case m.IMDbID != nil:
		id := strings.ToLower(strings.TrimSpace(*m.IMDbID))
		if !reIMDb.MatchString(id) {
			return metadata.Ref{}, "imdbId must look like tt0133093"
		}
		return metadata.Ref{IMDbID: id}, ""
	}
	return metadata.Ref{}, "give tmdbId, tvdbId, tvdb or imdbId"
}

// decodeBody reads the JSON body of an edit (at most maxEditBody bytes) into v.
// It answers 413 for a body that is too big and 400 for one that is not JSON.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEditBody)).Decode(v)
	var tooBig *http.MaxBytesError
	switch {
	case err == nil:
		return true
	case errors.As(err, &tooBig):
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
	default:
		writeError(w, http.StatusBadRequest, "invalid JSON body")
	}
	return false
}

func readMatchRequest(w http.ResponseWriter, r *http.Request) (metadata.Ref, bool) {
	var req matchRequest
	if !decodeBody(w, r, &req) {
		return metadata.Ref{}, false
	}
	ref, problem := req.ref()
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return metadata.Ref{}, false
	}
	return ref, true
}

// itemForMatch loads {id} and checks that it is a movie.
func (s *Server) movieForMatch(w http.ResponseWriter, r *http.Request) (store.Entry, bool) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return store.Entry{}, false
	}
	if e.Kind != store.KindMovie {
		writeError(w, http.StatusBadRequest, "only movies are matched one by one: match the show instead")
		return store.Entry{}, false
	}
	return e, true
}

func (s *Server) handleMatchItem(w http.ResponseWriter, r *http.Request) {
	e, ok := s.movieForMatch(w, r)
	if !ok {
		return
	}
	ref, ok := readMatchRequest(w, r)
	if !ok || s.metadataOff(w) {
		return
	}
	if ref.TVDBID != 0 || ref.TVDBSlug != "" {
		writeError(w, http.StatusBadRequest, "TVDB has no movies: give tmdbId or imdbId")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), matchTimeout)
	defer cancel()
	if err := s.Metadata.MatchMovie(ctx, e.ID, ref); err != nil {
		s.metadataError(w, r, err)
		return
	}
	s.writeItemDetail(w, r, e.ID)
}

func (s *Server) handleUnmatchItem(w http.ResponseWriter, r *http.Request) {
	e, ok := s.movieForMatch(w, r)
	if !ok || s.metadataOff(w) {
		return
	}
	if err := s.Metadata.UnmatchItem(r.Context(), e.ID); err != nil {
		s.metadataError(w, r, err)
		return
	}
	s.writeItemDetail(w, r, e.ID)
}

// writeItemDetail answers with the item as GET /api/items/{id} would.
func (s *Server) writeItemDetail(w http.ResponseWriter, r *http.Request, id int64) {
	e, err := s.Store.GetEntry(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "item not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.writeDetail(w, r, e, "avplayer")
}

func (s *Server) handleMatchShow(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.findShow(w, r)
	if !ok {
		return
	}
	ref, ok := readMatchRequest(w, r)
	if !ok || s.metadataOff(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), matchTimeout)
	defer cancel()
	if err := s.Metadata.MatchShow(ctx, sh.Name, ref); err != nil {
		s.metadataError(w, r, err)
		return
	}
	s.writeShowDetail(w, r, sh)
}

func (s *Server) handleUnmatchShow(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.findShow(w, r)
	if !ok || s.metadataOff(w) {
		return
	}
	if err := s.Metadata.UnmatchShow(r.Context(), sh.Name); err != nil {
		s.metadataError(w, r, err)
		return
	}
	s.writeShowDetail(w, r, sh)
}

func (s *Server) writeShowDetail(w http.ResponseWriter, r *http.Request, sh store.Show) {
	d, err := s.showDetail(r.Context(), sh)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// ---- editing a title ----

type titlePatch struct {
	Title     *string `json:"title"`
	SortTitle *string `json:"sortTitle"`
	Year      *int    `json:"year"`
	Overview  *string `json:"overview"`
	Tagline   *string `json:"tagline"`
	// EpisodeOrder (aired, dvd or absolute) is for a show from TVDB: the files are mapped
	// to its episodes again in that order and the show is fetched again. It does not lock
	// the title; the text fields do.
	EpisodeOrder *string `json:"episodeOrder"`
}

// text reports whether the patch edits text, which locks the title.
func (p titlePatch) text() bool {
	p.EpisodeOrder = nil
	return p != (titlePatch{})
}

func (s *Server) handlePatchTitle(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "title")
	if !ok {
		return
	}
	var req titlePatch
	if !decodeBody(w, r, &req) {
		return
	}
	if req == (titlePatch{}) {
		writeError(w, http.StatusBadRequest, "give at least one of title, sortTitle, year, overview, tagline, episodeOrder")
		return
	}
	if req.EpisodeOrder != nil && !store.ValidEpisodeOrder(*req.EpisodeOrder) {
		writeError(w, http.StatusBadRequest, "episodeOrder must be aired, dvd or absolute")
		return
	}
	for _, f := range []struct {
		name string
		v    *string
		max  int
	}{{"title", req.Title, maxTitleLen}, {"sortTitle", req.SortTitle, maxTitleLen}, {"overview", req.Overview, maxTextLen}, {"tagline", req.Tagline, maxTaglineLen}} {
		if f.v == nil {
			continue
		}
		*f.v = strings.TrimSpace(*f.v)
		if len(*f.v) > f.max {
			writeError(w, http.StatusBadRequest, f.name+" is too long")
			return
		}
	}
	if req.Title != nil && *req.Title == "" {
		writeError(w, http.StatusBadRequest, "title must not be empty")
		return
	}
	if req.Year != nil && (*req.Year < 0 || *req.Year > 2200) {
		writeError(w, http.StatusBadRequest, "year must be between 0 and 2200")
		return
	}
	// Only titles that are in the library can be edited.
	if _, err := s.Store.GetTitleDetail(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "title not found")
			return
		}
		s.internalError(w, r, err)
		return
	}
	// The order first: if TVDB cannot give the show in it, nothing is changed.
	changedOrder := req.EpisodeOrder != nil
	if req.EpisodeOrder != nil {
		if s.metadataOff(w) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), matchTimeout)
		err := s.Metadata.SetEpisodeOrder(ctx, id, *req.EpisodeOrder)
		cancel()
		if err != nil {
			s.metadataError(w, r, err)
			return
		}
	}
	if req.text() {
		err := s.Store.PatchTitle(r.Context(), id, store.TitlePatch{Title: req.Title, SortTitle: req.SortTitle, Year: req.Year,
			Overview: req.Overview, Tagline: req.Tagline})
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "title not found")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	d, err := s.Store.GetTitleDetail(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := s.titleDetail(d)
	if changedOrder {
		// How many of the show's files have no episode in the new order: the page can say so.
		if n, err := s.Store.UnmappedEpisodeFiles(r.Context(), id); err == nil {
			out.Unmapped = &n
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- refresh, status, review, search ----

func (s *Server) accepted(w http.ResponseWriter) {
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

func (s *Server) handleRefreshItem(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok || s.metadataOff(w) {
		return
	}
	s.Metadata.RefreshItem(e.ID)
	s.accepted(w)
}

func (s *Server) handleRefreshShow(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.findShow(w, r)
	if !ok || s.metadataOff(w) {
		return
	}
	s.Metadata.RefreshShow(sh.Name)
	s.accepted(w)
}

func (s *Server) handleRefreshAll(w http.ResponseWriter, r *http.Request) {
	if s.metadataOff(w) {
		return
	}
	s.Metadata.RefreshAll()
	s.accepted(w)
}

func (s *Server) handleMetadataStatus(w http.ResponseWriter, r *http.Request) {
	if s.Metadata == nil {
		writeJSON(w, http.StatusOK, metadata.Status{})
		return
	}
	writeJSON(w, http.StatusOK, s.Metadata.Status(r.Context()))
}

func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ReviewList(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]ReviewEntry, len(list))
	for i, e := range list {
		out[i] = ReviewEntry{Kind: e.Kind, Show: e.Show, Title: e.Title, Year: e.Year, MatchState: e.MatchState,
			NeedsReview: e.NeedsReview, Score: e.Score}
		if e.ItemID != 0 {
			out[i].ItemID = ptr(e.ItemID)
		}
		for _, ep := range e.Episodes {
			out[i].Episodes = append(out[i].Episodes, ReviewEpisode{ID: ep.ID, Path: ep.Path})
		}
		if e.Candidate != nil {
			c := s.titleCard(*e.Candidate)
			out[i].Candidate = &c
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleMetadataSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := q.Get("kind")
	if kind != store.KindMovie && kind != store.KindSeries {
		writeError(w, http.StatusBadRequest, `kind must be "movie" or "series"`)
		return
	}
	title := strings.TrimSpace(q.Get("q"))
	if title == "" || len(title) > maxSearchQuery {
		writeError(w, http.StatusBadRequest, "q must be the title to look for")
		return
	}
	year := 0
	if v := q.Get("year"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 2200 {
			writeError(w, http.StatusBadRequest, "year must be a year")
			return
		}
		year = n
	}
	if s.metadataOff(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), searchTimeout)
	defer cancel()
	// The fix dialog searches for what was typed; ids in it do not apply.
	res, err := s.Metadata.Search(ctx, metadata.Query{Kind: kind, Title: title, Year: year})
	if err != nil {
		s.metadataError(w, r, err)
		return
	}
	out := make([]Candidate, len(res))
	for i, c := range res {
		out[i] = Candidate{Source: c.Source, ID: c.ID, Title: c.Title, OriginalTitle: c.OriginalTitle, Year: c.Year, Overview: c.Overview,
			Poster: s.imageURL(c.Poster, "w185"), Score: c.Score}
		if c.Source == metadata.SourceTVDB {
			out[i].TVDBID = c.ID
		} else {
			out[i].TMDBID = c.ID
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- settings ----

func (s *Server) settingsView(ctx context.Context) SettingsView {
	v := SettingsView{Language: metadata.DefaultLanguage}
	if s.Settings != nil {
		r := s.Settings.Resolve(ctx)
		v = SettingsView{TMDBKeySet: r.KeySet(), TMDBKeySource: string(r.KeySource), MetadataEnabled: r.Enabled,
			MetadataForcedOff: r.ForcedOff, Language: r.Language,
			TVDBKeySet: r.TVDBKeySet(), TVDBPINSet: r.TVDBPINSet(), TVDBKeySource: string(r.TVDBKeySource)}
	}
	v.TVDBAttribution, v.TVDBAttributionURL = tvdbAttribution, tvdbAttributionURL
	l := s.playLimits(ctx)
	v.MaxStreams, v.MaxStreamsSource = l.streams, l.streamsSource
	v.MaxTranscodes, v.MaxTranscodesSource = l.transcodes, l.transcodesSource
	return v
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.settingsView(r.Context()))
}

type settingsRequest struct {
	TMDBKey         *string `json:"tmdbKey"`
	TVDBKey         *string `json:"tvdbKey"` // "" removes the stored key
	TVDBPIN         *string `json:"tvdbPin"` // "" removes the stored PIN
	MetadataEnabled *bool   `json:"metadataEnabled"`
	Language        *string `json:"language"`
	MaxStreams      *int    `json:"maxStreams"`
	MaxTranscodes   *int    `json:"maxTranscodes"`
}

// handlePatchSettings is PATCH (and PUT) /api/settings: it changes the settings
// that are in the body and leaves the others. Everything that can be checked
// without side effects is, before anything is stored (the limits: range and
// whether a flag fixed them; the shape of the body), so that a request that is
// wrong in any of them changes nothing. What remains is the key, which only TMDB
// can refuse, and that is why the metadata settings are stored first and the
// limits second: a key TMDB refuses leaves the limits as they were, as a user
// would expect, and the only way to have half of a request stored is for the
// database to fail between the two writes (a 500; the metadata part stays). The
// two cannot be one transaction: Settings.Update checks the key over the network.
func (s *Server) handlePatchSettings(w http.ResponseWriter, r *http.Request) {
	var req settingsRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req == (settingsRequest{}) {
		writeError(w, http.StatusBadRequest, "give at least one of tmdbKey, tvdbKey, tvdbPin, metadataEnabled, language, maxStreams, maxTranscodes")
		return
	}
	metadataChange := req.TMDBKey != nil || req.TVDBKey != nil || req.TVDBPIN != nil || req.MetadataEnabled != nil || req.Language != nil
	if metadataChange && s.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings are not available")
		return
	}
	if !s.checkLimitChange(r.Context(), w, req) {
		return
	}
	if metadataChange {
		err := s.Settings.Update(r.Context(), metadata.Update{Key: req.TMDBKey, Enabled: req.MetadataEnabled, Language: req.Language,
			TVDBKey: req.TVDBKey, TVDBPIN: req.TVDBPIN})
		switch {
		case err == nil:
		case errors.Is(err, metadata.ErrInvalidSetting), errors.Is(err, metadata.ErrKeyRejected), errors.Is(err, metadata.ErrTVDBRejected):
			writeError(w, http.StatusBadRequest, err.Error())
			return
		case errors.Is(err, metadata.ErrUnreachable), errors.Is(err, metadata.ErrTVDBUnreachable):
			writeError(w, http.StatusBadGateway, err.Error())
			return
		default:
			s.internalError(w, r, err)
			return
		}
		if s.Metadata != nil {
			s.Metadata.Reload() // a new key or language takes effect at once
		}
	}
	if req.MaxStreams != nil || req.MaxTranscodes != nil {
		if err := s.storeLimits(r.Context(), req); err != nil {
			s.internalError(w, r, err)
			return
		}
		s.ApplyLimits(r.Context())
	}
	view := s.settingsView(r.Context())
	if req.TMDBKey != nil && strings.TrimSpace(*req.TMDBKey) != "" {
		view.TMDBKeyChecked = ptr(!view.MetadataForcedOff)
	}
	// The key, not the PIN: a PIN alone is checked against the key that is there, but it is
	// the answer about a key that tvdbKeyChecked gives.
	if req.TVDBKey != nil && strings.TrimSpace(*req.TVDBKey) != "" {
		view.TVDBKeyChecked = ptr(!view.MetadataForcedOff)
	}
	writeJSON(w, http.StatusOK, view)
}

// SettingsTest is the answer of POST /api/settings/test.
type SettingsTest struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"` // why not: never has a key or a PIN in it
}

// handleTestSettings is POST /api/settings/test {"provider": "tmdb" | "tvdb"}: it checks the key
// in use for that provider, from wherever it comes, with a request made now (a login for
// TVDB, with its PIN if there is one). A key that does not work is an answer, not a failure of
// the request: 200 with ok false and the reason. The key is not in the reply, and it is not
// something the page can ask for: the page only sees whether it works.
func (s *Server) handleTestSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider != metadata.SourceTMDB && provider != metadata.SourceTVDB {
		writeError(w, http.StatusBadRequest, `provider must be "tmdb" or "tvdb"`)
		return
	}
	if s.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings are not available")
		return
	}
	err := s.Settings.Test(r.Context(), provider)
	if r.Context().Err() != nil {
		return // the client went away
	}
	if err != nil {
		writeJSON(w, http.StatusOK, SettingsTest{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, SettingsTest{OK: true})
}
