package api

import (
	"context"
	"strings"

	"ply/internal/store"
)

// metaView holds, for one request, the metadata of the titles its items belong
// to: loaded in a few queries for the whole list instead of two per item.
type metaView struct {
	s        *Server
	series   map[string]store.SeriesMatch
	briefs   map[int64]store.TitleBrief
	episodes map[int64]map[[2]int]store.Episode
}

// newMetaView loads what the entries need. A failure to read metadata is
// logged and gives a view that adds nothing: metadata must never break a list.
func (s *Server) newMetaView(ctx context.Context, entries []store.Entry) *metaView {
	v := &metaView{s: s, series: map[string]store.SeriesMatch{}, briefs: map[int64]store.TitleBrief{},
		episodes: map[int64]map[[2]int]store.Episode{}}
	hasEpisodes := false
	var ids []int64
	seen := map[int64]bool{}
	add := func(id int64) {
		if id != 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, e := range entries {
		if e.Kind == store.KindEpisode {
			hasEpisodes = true
		} else {
			add(e.TitleID)
		}
	}
	if hasEpisodes {
		sm, err := s.Store.SeriesMatches(ctx)
		if err != nil {
			s.Logger.Warn("metadata: reading shows", "err", err)
		}
		v.series = sm
		if v.series == nil {
			v.series = map[string]store.SeriesMatch{}
		}
		for _, e := range entries {
			if e.Kind == store.KindEpisode {
				add(v.series[e.Show].TitleID)
			}
		}
	}
	if briefs, err := s.Store.TitleBriefs(ctx, ids); err != nil {
		s.Logger.Warn("metadata: reading titles", "err", err)
	} else {
		v.briefs = briefs
	}
	for _, e := range entries {
		if e.Kind != store.KindEpisode {
			continue
		}
		id := v.series[e.Show].TitleID
		if _, done := v.episodes[id]; done || id == 0 {
			continue
		}
		eps, err := s.Store.Episodes(ctx, id)
		if err != nil {
			s.Logger.Warn("metadata: reading episodes", "err", err)
		}
		m := make(map[[2]int]store.Episode, len(eps))
		for _, ep := range eps {
			m[[2]int{ep.Season, ep.Number}] = ep
		}
		v.episodes[id] = m
	}
	return v
}

// titleIDOf is the title an entry's metadata comes from: a movie's own, a
// show's for an episode.
func (v *metaView) titleIDOf(e store.Entry) int64 {
	if e.Kind == store.KindEpisode {
		return v.series[e.Show].TitleID
	}
	return e.TitleID
}

// pickArt chooses the image to show: the item's own sidecar art, then the
// title's (TMDB's, or another file's sidecar), then a cover embedded in the
// file.
func pickArt(own, ownRef, titleRef string) string {
	embedded := strings.HasPrefix(own, store.EmbeddedPrefix)
	switch {
	case own != "" && !embedded:
		return ownRef
	case titleRef != "":
		return titleRef
	case own != "":
		return ownRef
	}
	return ""
}

// apply adds the metadata to an item summary.
func (v *metaView) apply(sum *ItemSummary, e store.Entry) {
	s := v.s
	titleID := v.titleIDOf(e)
	sum.MatchState, sum.NeedsReview = e.MatchState, e.NeedsReview
	var show store.SeriesMatch // the episode's show; zero for a movie or a show nobody looked at
	if e.Kind == store.KindEpisode {
		var ok bool
		show, ok = v.series[e.Show]
		sum.MatchState, sum.NeedsReview = store.MatchPending, false
		if ok {
			sum.MatchState, sum.NeedsReview = show.State, show.NeedsReview
		}
	}
	b, ok := v.briefs[titleID]
	if titleID != 0 && ok {
		sum.TitleID = ptr(titleID)
		sum.Logo = s.imageURL(b.Logo, logoSize)
		sum.Rating = optFloat(b.Rating)
		sum.Certification = b.Certification
		sum.Genres = b.Genres
	}

	switch {
	case e.Kind == store.KindEpisode:
		sum.Poster = s.imageURL(pickArt(show.LocalPoster, store.LocalRef(store.ArtSeries, show.ID, "poster"), b.Poster), posterSize)
		sum.Backdrop = s.imageURL(pickArt(show.LocalBackdrop, store.LocalRef(store.ArtSeries, show.ID, "backdrop"), b.Backdrop), backdropSize)
		if ep, found := v.episodes[titleID][[2]int{e.Season, e.Episode}]; found {
			if ep.Name != "" {
				sum.EpisodeTitle = ep.Name
				if e.Title == "" {
					sum.Title = ep.Name
				}
			}
			sum.Overview = ep.Overview
			sum.Still = s.imageURL(pickArt(e.LocalThumb, store.LocalRef(store.ArtItem, e.ID, "thumb"), ep.Still), stillSize)
		} else if e.LocalThumb != "" {
			sum.Still = s.imageURL(store.LocalRef(store.ArtItem, e.ID, "thumb"), stillSize)
		}
	default:
		sum.Poster = s.imageURL(pickArt(e.LocalPoster, store.LocalRef(store.ArtItem, e.ID, "poster"), b.Poster), posterSize)
		sum.Backdrop = s.imageURL(pickArt(e.LocalBackdrop, store.LocalRef(store.ArtItem, e.ID, "backdrop"), b.Backdrop), backdropSize)
	}
}

// episodeInfo is the metadata of an episode, if any.
func (v *metaView) episodeInfo(e store.Entry) *EpisodeInfo {
	if e.Kind != store.KindEpisode {
		return nil
	}
	ep, ok := v.episodes[v.titleIDOf(e)][[2]int{e.Season, e.Episode}]
	if !ok {
		return nil
	}
	return &EpisodeInfo{Name: ep.Name, Overview: ep.Overview, AirDate: ep.AirDate, Rating: optFloat(ep.Rating),
		Still: v.s.imageURL(pickArt(e.LocalThumb, store.LocalRef(store.ArtItem, e.ID, "thumb"), ep.Still), stillSize)}
}

// summaries builds the summaries of a list of entries with art and metadata.
func (s *Server) summaries(ctx context.Context, entries []store.Entry) []ItemSummary {
	v := s.newMetaView(ctx, entries)
	out := make([]ItemSummary, len(entries))
	for i, e := range entries {
		out[i] = summarize(e)
		out[i].Art = s.artOf(e)
		v.apply(&out[i], e)
	}
	return out
}
