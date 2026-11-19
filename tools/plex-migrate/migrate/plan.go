package migrate

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"

	"ply/tools/plex-migrate/plex"
)

// PathMap rewrites Plex file paths to the paths ply sees (a different mount
// prefix, e.g. Plex "/volume1/Movies" → ply "/Volumes/NAS/Movies"). Rules are
// "old=new"; the longest matching prefix wins.
type PathMap struct{ rules []pathRule }

type pathRule struct{ from, to string }

func ParsePathMap(specs []string) (PathMap, error) {
	var m PathMap
	for _, s := range specs {
		from, to, ok := strings.Cut(s, "=")
		if !ok || from == "" {
			return m, fmt.Errorf("path map %q: want old=new", s)
		}
		m.rules = append(m.rules, pathRule{filepath.Clean(from), filepath.Clean(to)})
	}
	sort.Slice(m.rules, func(i, j int) bool { return len(m.rules[i].from) > len(m.rules[j].from) })
	return m, nil
}

func (m PathMap) Map(p string) string {
	clean := filepath.Clean(p)
	for _, r := range m.rules {
		if clean == r.from || strings.HasPrefix(clean, r.from+string(filepath.Separator)) {
			return filepath.Join(r.to, strings.TrimPrefix(clean, r.from))
		}
	}
	return clean
}

// normPath is the join key: NFC-normalised and cleaned, so a macOS NFD path on
// one side matches the NFC path on the other. The original path is kept for
// writing sidecars to the real on-disk location.
func normPath(p string) string { return norm.NFC.String(filepath.Clean(p)) }

type OpType string

const (
	OpMatchMovie   OpType = "match-movie"
	OpMatchShow    OpType = "match-show"
	OpPoster       OpType = "poster"
	OpBackdrop     OpType = "backdrop"
	OpSeasonPoster OpType = "season-poster"
	OpEpisodeStill OpType = "episode-still"
	OpProgress     OpType = "progress"
	OpWatched      OpType = "watched"
)

// Op is one planned migration step. Skip marks it already satisfied (an existing
// sidecar, no id to match, no watch state) so apply leaves it alone.
type Op struct {
	Type        OpType
	Plex        plex.Item
	Slot        string // "poster" | "art" for art ops
	PlyID       int64
	PlyShow     string
	Target      string // sidecar path for art ops
	PositionSec float64
	DurationSec float64
	Watched     bool
	TMDB        int64
	IMDb        string
	Skip        bool
	Reason      string
}

type Stats struct {
	PlexMovies, PlexShows, PlexSeasons, PlexEpisodes int
	JoinedMovies, JoinedEpisodes                     int
}

type Plan struct {
	Ops       []Op
	Stats     Stats
	Unmatched []string // Plex files with no ply item at the (mapped) path
}

// BuildPlan joins the Plex library to ply by path and lists what would move.
// force makes art ops overwrite existing sidecars; otherwise they are skipped.
func BuildPlan(ctx context.Context, src plex.Source, plyItems []PlyItem, pm PathMap, force bool) (*Plan, error) {
	plexItems, err := src.Items(ctx)
	if err != nil {
		return nil, err
	}
	byPath := make(map[string]PlyItem, len(plyItems))
	for _, it := range plyItems {
		byPath[normPath(it.Path)] = it
	}
	join := func(files []string) []PlyItem {
		var out []PlyItem
		for _, f := range files {
			if it, ok := byPath[normPath(pm.Map(f))]; ok {
				out = append(out, it)
			}
		}
		return out
	}

	p := &Plan{}
	var episodes, seasons []plex.Item
	for _, it := range plexItems {
		switch it.Kind {
		case plex.Movie:
			p.buildMovie(it, join, force)
		case plex.Show:
			// handled after episodes/seasons are collected
		case plex.Season:
			p.Stats.PlexSeasons++
			seasons = append(seasons, it)
		case plex.Episode:
			p.Stats.PlexEpisodes++
			episodes = append(episodes, it)
		}
	}
	for _, show := range plexItems {
		if show.Kind == plex.Show {
			p.Stats.PlexShows++
			p.buildShow(show, episodes, seasons, join, force)
		}
	}
	return p, nil
}

func (p *Plan) artOp(t OpType, it plex.Item, slot, target string, exists bool, force bool) {
	op := Op{Type: t, Plex: it, Slot: slot, Target: target}
	if it.PosterURL == "" && slot == "poster" {
		op.Skip, op.Reason = true, "no poster in Plex"
	} else if it.ArtURL == "" && slot == "art" {
		op.Skip, op.Reason = true, "no backdrop in Plex"
	} else if exists && !force {
		op.Skip, op.Reason = true, "sidecar already present"
	}
	p.Ops = append(p.Ops, op)
}

func (p *Plan) buildMovie(it plex.Item, join func([]string) []PlyItem, force bool) {
	p.Stats.PlexMovies++
	joined := join(it.Files)
	if len(joined) == 0 {
		p.Unmatched = append(p.Unmatched, it.Files...)
		return
	}
	p.Stats.JoinedMovies++
	first := joined[0]
	if !it.IDs.Empty() {
		p.Ops = append(p.Ops, Op{Type: OpMatchMovie, Plex: it, PlyID: first.ID, TMDB: it.IDs.TMDB, IMDb: it.IDs.IMDb})
	}
	for _, ply := range joined {
		p.artOp(OpPoster, it, "poster", moviePosterTarget(ply.Path), hasMoviePoster(ply.Path), force)
		p.artOp(OpBackdrop, it, "art", movieBackdropTarget(ply.Path), hasMovieBackdrop(ply.Path), force)
	}
	p.watchOps(first, it)
}

func (p *Plan) buildShow(show plex.Item, episodes, seasons []plex.Item, join func([]string) []PlyItem, force bool) {
	// A representative joined episode gives the ply show name and folder.
	var rep *PlyItem
	var showFolder string
	for _, ep := range episodes {
		if ep.Show != show.Title {
			continue
		}
		if joined := join(ep.Files); len(joined) > 0 {
			rep = &joined[0]
			showFolder = showFolderOf(rep.Path)
			break
		}
	}
	if rep != nil && !show.IDs.Empty() {
		p.Ops = append(p.Ops, Op{Type: OpMatchShow, Plex: show, PlyShow: rep.Show, TMDB: show.IDs.TMDB, IMDb: show.IDs.IMDb})
	}
	if rep != nil {
		p.artOp(OpPoster, show, "poster", showPosterTarget(showFolder), hasShowPoster(showFolder), force)
	}
	for _, se := range seasons {
		if se.Show != show.Title || rep == nil {
			continue
		}
		p.artOp(OpSeasonPoster, se, "poster", seasonPosterTarget(showFolder, se.Season), hasSeasonPoster(showFolder, se.Season), force)
	}
	for _, ep := range episodes {
		if ep.Show != show.Title {
			continue
		}
		joined := join(ep.Files)
		if len(joined) == 0 {
			p.Unmatched = append(p.Unmatched, ep.Files...)
			continue
		}
		p.Stats.JoinedEpisodes++
		ply := joined[0]
		p.artOp(OpEpisodeStill, ep, "poster", episodeStillTarget(ply.Path), hasEpisodeStill(ply.Path), force)
		p.watchOps(ply, ep)
	}
}

func (p *Plan) watchOps(ply PlyItem, it plex.Item) {
	if it.Watch.OffsetMS > 0 {
		p.Ops = append(p.Ops, Op{Type: OpProgress, Plex: it, PlyID: ply.ID,
			PositionSec: float64(it.Watch.OffsetMS) / 1000, DurationSec: ply.DurationSec})
	}
	if it.Watch.ViewCount > 0 {
		p.Ops = append(p.Ops, Op{Type: OpWatched, Plex: it, PlyID: ply.ID, Watched: true})
	}
}

// Render is the dry-run report: coverage, planned vs skipped ops, unmatched files.
func (p *Plan) Render() string {
	var b strings.Builder
	s := p.Stats
	fmt.Fprintf(&b, "Plex library: %d movies, %d shows, %d seasons, %d episodes\n",
		s.PlexMovies, s.PlexShows, s.PlexSeasons, s.PlexEpisodes)
	fmt.Fprintf(&b, "Joined by path: %d/%d movies, %d/%d episodes\n",
		s.JoinedMovies, s.PlexMovies, s.JoinedEpisodes, s.PlexEpisodes)
	counts := map[OpType][2]int{} // [planned, skipped]
	for _, op := range p.Ops {
		c := counts[op.Type]
		if op.Skip {
			c[1]++
		} else {
			c[0]++
		}
		counts[op.Type] = c
	}
	order := []OpType{OpMatchMovie, OpMatchShow, OpPoster, OpBackdrop, OpSeasonPoster, OpEpisodeStill, OpProgress, OpWatched}
	b.WriteString("Operations (planned / skipped):\n")
	for _, t := range order {
		if c, ok := counts[t]; ok {
			fmt.Fprintf(&b, "  %-14s %d / %d\n", t, c[0], c[1])
		}
	}
	if len(p.Unmatched) > 0 {
		fmt.Fprintf(&b, "Unmatched files (%d), first 20:\n", len(p.Unmatched))
		for i, f := range p.Unmatched {
			if i >= 20 {
				break
			}
			fmt.Fprintf(&b, "  %s\n", f)
		}
	}
	return b.String()
}
