package metadata

import (
	"context"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"ply/internal/store"
)

// Scores at or above MatchThreshold are accepted; at or above ReviewThreshold
// they are accepted but flagged for a person to look at.
const (
	MatchThreshold  = 0.80
	ReviewThreshold = 0.55
)

// topCandidates is how many search results are scored.
const topCandidates = 10

// Query is what to look for: the title and year as parsed from a file name,
// and any ids the names or an NFO gave.
type Query struct {
	Kind  string // store.KindMovie | store.KindSeries
	Title string
	Year  int // 0 = unknown
	Hints Hints
}

// Scored is a candidate with its score in [0, 1].
type Scored struct {
	Candidate
	Score float64
}

// Result is the outcome of Match.
type Result struct {
	TMDBID      int64 // 0 when nothing matched
	Score       float64
	State       string // store.MatchMatched | store.MatchUnmatched
	NeedsReview bool
	ByID        bool     // an id hint decided it
	Candidates  []Scored // what was scored, best first (none for an id hint)
}

// Match finds the TMDB title for a query. An id hint (TMDB id, IMDb id, TVDB
// id for shows) decides it with score 1; an IMDb or TVDB id that TMDB does not
// know falls through to the search. A TMDB id hint is trusted as it is: the
// fetch that follows tells whether it exists.
//
// A transient provider error is returned as it is: a file is never
// unmatched because TMDB was out of reach.
func Match(ctx context.Context, p Provider, q Query) (Result, error) {
	if id, ok, err := matchByID(ctx, p, q); err != nil {
		return Result{}, err
	} else if ok {
		return Result{TMDBID: id, Score: 1, State: store.MatchMatched, ByID: true}, nil
	}
	scored, err := Search(ctx, p, q)
	if err != nil {
		return Result{}, err
	}
	if len(scored) == 0 {
		return Result{State: store.MatchUnmatched}, nil
	}
	best := scored[0]
	state, review := Classify(best.Score)
	res := Result{Score: best.Score, State: state, NeedsReview: review, Candidates: scored}
	if state == store.MatchMatched {
		res.TMDBID = best.TMDBID
	}
	return res, nil
}

// Classify turns a score into a match state and whether a person should look.
func Classify(score float64) (state string, needsReview bool) {
	switch {
	case score >= MatchThreshold:
		return store.MatchMatched, false
	case score >= ReviewThreshold:
		return store.MatchMatched, true
	}
	return store.MatchUnmatched, false
}

func matchByID(ctx context.Context, p Provider, q Query) (id int64, ok bool, err error) {
	h := q.Hints
	if h.TMDB != 0 {
		return h.TMDB, true, nil
	}
	pick := func(movies, series []Candidate) []Candidate {
		if q.Kind == store.KindSeries {
			return series
		}
		return movies
	}
	if h.IMDb != "" {
		movies, series, err := p.Find(ctx, ExternalIMDb, h.IMDb)
		if err != nil {
			return 0, false, err
		}
		if c := pick(movies, series); len(c) > 0 {
			return c[0].TMDBID, true, nil
		}
	}
	if h.TVDB != 0 && q.Kind == store.KindSeries {
		_, series, err := p.Find(ctx, ExternalTVDB, strconv.FormatInt(h.TVDB, 10))
		if err != nil {
			return 0, false, err
		}
		if len(series) > 0 {
			return series[0].TMDBID, true, nil
		}
	}
	return 0, false, nil
}

// Search runs the searches for a query and returns the top candidates, best
// first. It tries the title with the year, then without it, then a simplified
// title (with the year, then without), and scores the results of the first
// search that finds anything. It is also what the "fix match" dialog lists.
func Search(ctx context.Context, p Provider, q Query) ([]Scored, error) {
	for _, try := range searchPlan(q) {
		var (
			cands []Candidate
			err   error
		)
		if q.Kind == store.KindSeries {
			cands, err = p.SearchSeries(ctx, try.query, try.year)
		} else {
			cands, err = p.SearchMovies(ctx, try.query, try.year)
		}
		if err != nil {
			return nil, err
		}
		if len(cands) > 0 {
			return ScoreCandidates(q, cands), nil
		}
	}
	return nil, nil
}

type search struct {
	query string
	year  int
}

// searchPlan lists the searches to try, in order, without repeats.
func searchPlan(q Query) []search {
	title := strings.TrimSpace(q.Title)
	if title == "" {
		return nil
	}
	var plan []search
	add := func(query string, year int) {
		s := search{query, year}
		if query != "" && !slices.Contains(plan, s) {
			plan = append(plan, s)
		}
	}
	add(title, q.Year)
	add(title, 0)
	simple := SimplifyQuery(title)
	if simple != strings.ToLower(title) {
		add(simple, q.Year)
		add(simple, 0)
	}
	return plan
}

// ScoreCandidates scores the first topCandidates of cands against the query
// and returns them best first (ties keep TMDB's order).
func ScoreCandidates(q Query, cands []Candidate) []Scored {
	if len(cands) > topCandidates {
		cands = cands[:topCandidates]
	}
	out := make([]Scored, len(cands))
	for i, c := range cands {
		out[i] = Scored{Candidate: c, Score: score(q, c, popularityRank(cands, i))}
	}
	slices.SortStableFunc(out, func(a, b Scored) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		}
		return 0
	})
	return out
}

// score is 0.6*title similarity + 0.3*year score + 0.1*popularity rank,
// rounded so that a score that is 0.8 on paper is not 0.7999999 on a CPU.
func score(q Query, c Candidate, popRank float64) float64 {
	sim := math.Max(titleSimilarity(q.Title, c.Title), titleSimilarity(q.Title, c.OriginalTitle))
	s := 0.6*sim + 0.3*yearScore(q.Year, c.Year) + 0.1*popRank
	return math.Round(s*1e6) / 1e6
}

// yearScore: an exact year 1, a year off by one 0.8, an unknown query year 0.5,
// anything else 0.
func yearScore(queryYear, candidateYear int) float64 {
	switch {
	case queryYear == 0:
		return 0.5
	case candidateYear == queryYear:
		return 1
	case candidateYear != 0 && (candidateYear == queryYear-1 || candidateYear == queryYear+1):
		return 0.8
	}
	return 0
}

// popularityRank is 1 for the most popular of the candidates, falling linearly
// to 0 for the least popular; equally popular candidates share a rank.
func popularityRank(cands []Candidate, i int) float64 {
	if len(cands) < 2 {
		return 1
	}
	more := 0 // candidates strictly more popular
	for _, c := range cands {
		if c.Popularity > cands[i].Popularity {
			more++
		}
	}
	return 1 - float64(more)/float64(len(cands)-1)
}

// titleSimilarity compares two titles after folding case, accents and
// punctuation: 1 when they are equal (also when only the spaces differ:
// "Spiderman" and "Spider-Man"), else the Dice coefficient of their words.
func titleSimilarity(a, b string) float64 {
	fa, fb := foldTitle(a), foldTitle(b)
	if fa == "" || fb == "" {
		return 0
	}
	if fa == fb || strings.ReplaceAll(fa, " ", "") == strings.ReplaceAll(fb, " ", "") {
		return 1
	}
	wa, wb := strings.Fields(fa), strings.Fields(fb)
	common := 0
	left := slices.Clone(wb)
	for _, w := range wa {
		if i := slices.Index(left, w); i >= 0 {
			common++
			left = slices.Delete(left, i, i+1)
		}
	}
	return 2 * float64(common) / float64(len(wa)+len(wb))
}

// foldTitle lower-cases, strips accents, turns "&" into "and" and every other
// punctuation into a space, and collapses spaces.
func foldTitle(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		switch {
		case unicode.Is(unicode.Mn, r):
			// an accent
		case r == '&':
			b.WriteString(" and ")
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		case r == '\'' || r == '’':
			// "Don't" is one word, "Dont"
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// SimplifyQuery is the fallback search text: folded, with "&" as "and",
// punctuation dropped and a leading "the", "a" or "an" removed.
func SimplifyQuery(title string) string {
	f := foldTitle(title)
	for _, article := range []string{"the ", "a ", "an "} {
		if rest, ok := strings.CutPrefix(f, article); ok && rest != "" {
			return rest
		}
	}
	return f
}

var reFolderYear = regexp.MustCompile(`^(.*?)[\s._-]*[(\[]\s*((?:19|20)\d{2})\s*[)\]]`)

// MovieQuery builds the query for a movie file from the title and year parsed
// from its name. When the file name has no year but its folder name does
// ("Movies/Blade Runner (1982)/br.mkv"), the folder's title and year are used.
// Id tags are cut out of the title and read into the hints, from the file name
// and its folder.
func MovieQuery(title string, year int, path string) Query {
	q := Query{Kind: store.KindMovie, Title: StripIDs(title), Year: year, Hints: PathHints(path, 1)}
	if year == 0 {
		dir := filepath.Base(filepath.Dir(path))
		if t, y := folderTitleYear(StripIDs(dir)); y != 0 {
			q.Title, q.Year = t, y
		}
	}
	return q
}

// ShowQuery builds the query for a show from its name and one of its episode
// files; the show folder's id tags count (two folders up for a Season folder).
func ShowQuery(show string, episodePath string) Query {
	return Query{Kind: store.KindSeries, Title: StripIDs(show), Hints: PathHints(episodePath, 2)}
}

// folderTitleYear reads "Blade Runner (1982)" or "Blade Runner [1982]" as a
// title and a year. A year without brackets is not trusted ("Blade Runner 2049"),
// and a name that is only a bracketed year gives nothing.
func folderTitleYear(name string) (title string, year int) {
	m := reFolderYear.FindStringSubmatch(name)
	if m == nil || strings.TrimSpace(m[1]) == "" {
		return "", 0
	}
	year, _ = strconv.Atoi(m[2])
	return strings.TrimSpace(strings.NewReplacer(".", " ", "_", " ").Replace(m[1])), year
}
