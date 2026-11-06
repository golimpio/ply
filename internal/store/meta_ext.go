package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

// EmbeddedPrefix starts the local_poster of an item whose file carries its own
// cover art: "embedded:<ffprobe stream index>". The image is extracted from the
// file on first request. Such a poster ranks below TMDB's, and SetItemArt does
// not replace it with "" (the scanner knows sidecar files only).
const EmbeddedPrefix = "embedded:"

// EmbeddedRef is the local_poster value for a cover in the given stream.
func EmbeddedRef(streamIndex int) string { return EmbeddedPrefix + strconv.Itoa(streamIndex) }

// SetItemEmbeddedCover records that the item's file has cover art in the stream.
// It leaves a sidecar poster alone and returns ErrNotFound for an unknown item.
func (s *Store) SetItemEmbeddedCover(ctx context.Context, itemID int64, streamIndex int) error {
	return s.write(ctx, prioLow, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE items SET local_poster = ? WHERE id = ? AND (local_poster = '' OR local_poster LIKE 'embedded:%')`,
			EmbeddedRef(streamIndex), itemID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var one int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM items WHERE id = ?`, itemID).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		return nil
	})
}

// ParseEmbeddedRef reads "embedded:<n>"; ok is false for anything else.
func ParseEmbeddedRef(v string) (streamIndex int, ok bool) {
	rest, found := strings.CutPrefix(v, EmbeddedPrefix)
	if !found {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	return n, err == nil && n >= 0
}

// TitleBrief is a card with what a list row shows besides it.
type TitleBrief struct {
	TitleCard
	Overview      string
	Logo          string
	Certification string
	Genres        []string // names, by name
}

// briefChunk bounds the number of ids in one query. SQLite takes 32,766 variables; the card query
// reads the library's items and series to find each title's files, whatever it is asked for, so the
// fewer queries a list of thousands of titles takes the better (a list of 5,000 films was 13 queries
// of 400 ids and took 142 ms for its titles, one of 5,000 takes 62). A variable for the tests, which
// make it small to cross a boundary with a few titles.
var briefChunk = 5000

// TitleBriefs returns the library titles among ids, keyed by id.
func (s *Store) TitleBriefs(ctx context.Context, ids []int64) (m map[int64]TitleBrief, err error) {
	err = s.snapshot(ctx, func(q queryer) error {
		m = map[int64]TitleBrief{}
		for start := 0; start < len(ids); start += briefChunk {
			chunk := ids[start:min(start+briefChunk, len(ids))]
			cards, err := cardsByID(ctx, q, chunk)
			if err != nil {
				return err
			}
			if len(cards) == 0 {
				continue
			}
			args := make([]any, len(chunk))
			for i, id := range chunk {
				args[i] = id
			}
			in := "(?" + strings.Repeat(", ?", len(chunk)-1) + ")"
			rows, err := q.QueryContext(ctx, `SELECT id, overview, logo, certification FROM titles WHERE id IN `+in, args...)
			if err != nil {
				return err
			}
			for rows.Next() {
				var (
					id                       int64
					overview, logo, certific string
				)
				if err := rows.Scan(&id, &overview, &logo, &certific); err != nil {
					rows.Close()
					return err
				}
				if c, ok := cards[id]; ok {
					m[id] = TitleBrief{TitleCard: c, Overview: overview, Logo: logo, Certification: certific}
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			rows, err = q.QueryContext(ctx, `
				SELECT tg.title_id, g.name FROM title_genres tg JOIN genres g ON g.id = tg.genre_id
				WHERE tg.title_id IN `+in+` ORDER BY tg.title_id, LOWER(g.name), g.id`, args...)
			if err != nil {
				return err
			}
			for rows.Next() {
				var (
					id   int64
					name string
				)
				if err := rows.Scan(&id, &name); err != nil {
					rows.Close()
					return err
				}
				if b, ok := m[id]; ok {
					b.Genres = append(b.Genres, name)
					m[id] = b
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		return nil
	})
	return m, err
}

// ShowItems returns the episode files of a show, by season and episode.
func (s *Store) ShowItems(ctx context.Context, show string) ([]Item, error) {
	return queryItems(ctx, s.ro, `SELECT `+itemCols+` FROM items WHERE kind = 'episode' AND show = ?
		ORDER BY season, episode, id`, []any{show})
}

// ShowSeasons returns the season numbers that have an episode file of the show.
func (s *Store) ShowSeasons(ctx context.Context, show string) ([]int, error) {
	return s.ints(ctx, `SELECT DISTINCT season FROM items WHERE kind = 'episode' AND show = ? ORDER BY season`, show)
}

// SeasonsWithFiles returns the seasons that have an episode file of any show
// linked to the title.
func (s *Store) SeasonsWithFiles(ctx context.Context, titleID int64) ([]int, error) {
	return s.ints(ctx, `
		SELECT DISTINCT i.season FROM series_matches sm JOIN items i ON i.kind = 'episode' AND i.show = sm.show
		WHERE sm.title_id = ? ORDER BY i.season`, titleID)
}

func (s *Store) ints(ctx context.Context, query string, args ...any) ([]int, error) {
	rows, err := s.ro.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// SeasonsNeedingFetchAll is SeasonsNeedingFetch for every matched show: the
// seasons with episode files and no stored episodes, by title id. A show from TVDB
// is left out: its fetch brings every season at once, so a season it has no episodes
// for is one TVDB does not have (asking again would fetch the whole show for nothing).
func (s *Store) SeasonsNeedingFetchAll(ctx context.Context) (map[int64][]int, error) {
	rows, err := s.ro.QueryContext(ctx, `
		SELECT DISTINCT sm.title_id, i.season FROM series_matches sm
		JOIN items i ON i.kind = 'episode' AND i.show = sm.show
		JOIN titles t ON t.id = sm.title_id AND t.source <> 'tvdb' AND (t.tmdb_id IS NOT NULL OR t.tvdb_id IS NOT NULL)
		WHERE NOT EXISTS (SELECT 1 FROM episodes e WHERE e.title_id = sm.title_id AND e.season = i.season)
		ORDER BY sm.title_id, i.season`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[int64][]int{}
	for rows.Next() {
		var (
			id     int64
			season int
		)
		if err := rows.Scan(&id, &season); err != nil {
			return nil, err
		}
		m[id] = append(m[id], season)
	}
	return m, rows.Err()
}

// ReleasePending makes every pending movie and show due now. The worker calls it
// when it can reach TMDB again after a change of settings: files it put off
// because there was no key are looked at at once. It changes writeChunk rows per
// transaction.
func (s *Store) ReleasePending(ctx context.Context) error {
	_, err := s.updateAll(ctx, []bulkUpdate{
		{"items", `next_attempt_at = 0`, `match_state = 'pending' AND next_attempt_at > 0`},
		{"series_matches", `next_attempt_at = 0`, `match_state = 'pending' AND next_attempt_at > 0`},
	})
	return err
}

// RequeueUnmatched makes every unmatched movie and show pending and due again,
// with its attempts reset, and returns how many it requeued. It changes writeChunk
// rows per transaction.
func (s *Store) RequeueUnmatched(ctx context.Context) (int, error) {
	const set = `match_state = 'pending', next_attempt_at = 0, match_attempts = 0, needs_review = 0`
	return s.updateAll(ctx, []bulkUpdate{
		{"items", set, `match_state = 'unmatched' AND kind = 'movie'`},
		{"series_matches", set, `match_state = 'unmatched'`},
	})
}

// SetItemMatchIfPending is SetItemMatch for the worker's own results: it writes
// only while the item is still pending and still a movie, so that a fix a person
// made in the meantime is not overwritten, nor is an item that was read again as
// an episode (SetParsed puts it back to pending) given the title the worker found
// for the movie it was. ok says whether it wrote; ErrNotFound for an
// unknown item.
func (s *Store) SetItemMatchIfPending(ctx context.Context, itemID int64, m Match) (ok bool, err error) {
	if err := m.validate(); err != nil {
		return false, err
	}
	err = s.write(ctx, prioLow, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE items SET title_id = ?, match_state = ?, needs_review = ?, match_score = ?,
				match_attempts = ?, next_attempt_at = ?
			WHERE id = ? AND match_state = 'pending' AND kind = 'movie'`,
			nullInt(m.TitleID), m.State, m.NeedsReview, m.Score, m.Attempts, m.NextAttemptAt, itemID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			ok = true
			return nil
		}
		var one int
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM items WHERE id = ?`, itemID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return ok && err == nil, err // written only once committed
}

// SetSeriesMatchIfPending is SetItemMatchIfPending for a show; a show with no
// row yet counts as pending. ErrNotFound for a show without episodes.
func (s *Store) SetSeriesMatchIfPending(ctx context.Context, show string, m Match) (ok bool, err error) {
	if err := m.validate(); err != nil {
		return false, err
	}
	err = s.write(ctx, prioLow, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO series_matches (show, title_id, match_state, needs_review, match_score,
				match_attempts, next_attempt_at)
			SELECT ?, ?, ?, ?, ?, ?, ? WHERE `+hasEpisodes+`
			ON CONFLICT (show) DO UPDATE SET title_id = excluded.title_id, match_state = excluded.match_state,
				needs_review = excluded.needs_review, match_score = excluded.match_score,
				match_attempts = excluded.match_attempts, next_attempt_at = excluded.next_attempt_at
			WHERE series_matches.match_state = 'pending'`,
			show, nullInt(m.TitleID), m.State, m.NeedsReview, m.Score, m.Attempts, m.NextAttemptAt, show)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			ok = true
			return nil
		}
		var has int
		err = tx.QueryRowContext(ctx, `SELECT 1 WHERE `+hasEpisodes, show).Scan(&has)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return ok && err == nil, err // written only once committed
}

// TouchTitle records that the title was looked at, without changing it: a
// title TMDB no longer knows is then not asked for again at once. ErrNotFound
// for an unknown title. It counts in Generation (it is exec, not execQuiet): fetched_at is the
// time of TMDB's rating, which the lists show (rating.fetchedAt).
func (s *Store) TouchTitle(ctx context.Context, titleID, fetchedAt int64) error {
	res, err := s.exec(ctx, prioLow, `UPDATE titles SET fetched_at = ? WHERE id = ?`, fetchedAt, titleID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetItemMatch returns the matching state of one item, or ErrNotFound.
func (s *Store) GetItemMatch(ctx context.Context, itemID int64) (Match, error) {
	var (
		m       Match
		titleID sql.NullInt64
	)
	err := s.ro.QueryRowContext(ctx, `
		SELECT title_id, match_state, needs_review, match_score, match_attempts, next_attempt_at
		FROM items WHERE id = ?`, itemID).
		Scan(&titleID, &m.State, &m.NeedsReview, &m.Score, &m.Attempts, &m.NextAttemptAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Match{}, ErrNotFound
	}
	m.TitleID = titleID.Int64
	return m, err
}

// LinkItemTitle stores the title and links the movie to it in one transaction,
// so that nothing (a prune, say) can remove the title between the two. m's
// TitleID is replaced by the stored title's id.
//
// A non-empty expect makes it a compare-and-set for the worker: the item must
// be in that state (the worker's is MatchPending, the state it took the item
// in); when it is not, because a person decided meanwhile, nothing is written
// and ok is false. A person's own actions pass "" and always win.
// ErrNotFound for an unknown item.
func (s *Store) LinkItemTitle(ctx context.Context, itemID int64, in *TitleInput, m Match, expect string) (titleID int64, ok bool, err error) {
	if err := m.validate(); err != nil {
		return 0, false, err
	}
	err = s.write(ctx, linkPrio(expect), func(tx *sql.Tx) error {
		var state, kind string
		err := tx.QueryRowContext(ctx, `SELECT match_state, kind FROM items WHERE id = ?`, itemID).Scan(&state, &kind)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		// The worker's result (expect is set) is for a movie that is still pending: an
		// item read again as an episode meanwhile is pending too, and is not that movie.
		if expect != "" && (state != expect || kind != KindMovie) {
			return nil
		}
		if titleID, err = upsertTitleTx(ctx, tx, in); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE items SET title_id = ?, match_state = ?, needs_review = ?, match_score = ?,
				match_attempts = ?, next_attempt_at = ?
			WHERE id = ?`,
			titleID, m.State, m.NeedsReview, m.Score, m.Attempts, m.NextAttemptAt, itemID); err != nil {
			return err
		}
		ok = true
		return nil
	})
	if err != nil || !ok {
		return 0, false, err
	}
	return titleID, true, nil
}

// linkPrio is the priority of LinkItemTitle and LinkSeriesTitle: the worker's
// results (it passes the state it expects) are background work; a person's own
// action (expect "") is waited for.
func linkPrio(expect string) prio {
	if expect == "" {
		return prioHigh
	}
	return prioLow
}

// LinkSeriesTitle is LinkItemTitle for a show. A show with no row yet counts as
// pending. ErrNotFound for a show without episodes. The worker's result (expect is set)
// that is not from TVDB (a TMDB fallback match) does not change a title that is stored from
// TVDB: the show is linked to it as it is. A person's pick does.
func (s *Store) LinkSeriesTitle(ctx context.Context, show string, in *TitleInput, m Match, expect string) (titleID int64, ok bool, err error) {
	if err := m.validate(); err != nil {
		return 0, false, err
	}
	err = s.write(ctx, linkPrio(expect), func(tx *sql.Tx) error {
		var hasEps int
		if err := tx.QueryRowContext(ctx, `SELECT 1 WHERE `+hasEpisodes, show).Scan(&hasEps); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		state := MatchPending
		err := tx.QueryRowContext(ctx, `SELECT match_state FROM series_matches WHERE show = ?`, show).Scan(&state)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if expect != "" && state != expect {
			return nil
		}
		if expect != "" { // the worker's own result: it does not turn a show from TVDB into TMDB's
			own := *in
			own.keepTVDB = true
			in = &own
		}
		if titleID, err = upsertTitleTx(ctx, tx, in); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO series_matches (show, title_id, match_state, needs_review, match_score, match_attempts, next_attempt_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (show) DO UPDATE SET title_id = excluded.title_id, match_state = excluded.match_state,
				needs_review = excluded.needs_review, match_score = excluded.match_score,
				match_attempts = excluded.match_attempts, next_attempt_at = excluded.next_attempt_at`,
			show, titleID, m.State, m.NeedsReview, m.Score, m.Attempts, m.NextAttemptAt); err != nil {
			return err
		}
		ok = true
		return nil
	})
	if err != nil || !ok {
		return 0, false, err
	}
	return titleID, true, nil
}
