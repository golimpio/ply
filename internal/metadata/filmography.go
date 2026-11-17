package metadata

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"ply/internal/store"
)

// What is kept of a person's external filmography, and for how long.
const (
	maxFilmography   = 50
	filmographyTTL   = 30 * 24 * time.Hour
	filmographyFetch = 8 * time.Second
)

// filmographyLister is a provider that can list combined credits for a person.
type filmographyLister interface {
	CombinedCredits(ctx context.Context, id int64) (*PersonCombinedCredits, error)
}

// normalizeDepartment maps any provider department string to one of the standard
// categories: Acting, Directing, Writing, Production, Crew, or Other.
func normalizeDepartment(d string, creditKind string) string {
	d = strings.TrimSpace(d)
	if creditKind == store.CreditCast || d == "" {
		return "Acting"
	}
	switch strings.ToLower(d) {
	case "directing":
		return "Directing"
	case "writing":
		return "Writing"
	case "production", "producer":
		return "Production"
	case "camera", "sound", "editing", "art", "costume & make-up", "visual effects", "lighting", "crew":
		return "Crew"
	default:
		return "Other"
	}
}

// isSelfRole reports whether character represents a talk show or documentary cameo (e.g. "Self", "Himself").
func isSelfRole(char string) bool {
	c := strings.ToLower(strings.TrimSpace(char))
	if c == "" {
		return false
	}
	return c == "self" || c == "himself" || c == "herself" || strings.HasPrefix(c, "self -") || strings.HasPrefix(c, "self (")
}

// rankFilmography filters, normalizes and sorts combined credits for display.
func rankFilmography(comb *PersonCombinedCredits) []store.PersonFilmographyEntry {
	if comb == nil {
		return nil
	}

	// Filter "Self" roles only if the person has at least one real non-self acting role.
	hasRealActing := false
	for _, c := range comb.Cast {
		if !isSelfRole(c.Character) {
			hasRealActing = true
			break
		}
	}

	type candidate struct {
		entry store.PersonFilmographyEntry
	}

	var candidates []candidate

	// Helper to add or merge credit entries so each title appears once per medium.
	seen := make(map[string]int) // "kind:tmdbID" -> index in candidates

	add := func(c PersonCreditEntry, creditKind string) {
		kind := store.KindMovie
		if c.MediaType == "tv" {
			kind = store.KindSeries
		} else if c.MediaType != "movie" && c.MediaType != "" {
			return // ignore unknown media types
		}

		char := strings.TrimSpace(c.Character)
		if creditKind == store.CreditCast && hasRealActing && isSelfRole(char) {
			return // skip talk show cameo
		}

		title := strings.TrimSpace(c.TitleOrName())
		if title == "" {
			return
		}

		year := yearOf(c.Date())
		key := fmt.Sprintf("%s:%d", kind, c.ID)
		dept := normalizeDepartment(c.Department, creditKind)

		job := strings.TrimSpace(c.Job)
		if creditKind == store.CreditCast {
			job = ""
		}

		e := store.PersonFilmographyEntry{
			Kind:          kind,
			TMDBID:        c.ID,
			Title:         title,
			OriginalTitle: strings.TrimSpace(c.OriginalTitleOrName()),
			Year:          year,
			ReleaseDate:   c.Date(),
			Character:     char,
			Job:           job,
			Department:    dept,
			CreditKind:    creditKind,
			Poster:        ImageRef(c.PosterPath),
			Backdrop:      ImageRef(c.BackdropPath),
			Rating:        c.VoteAverage,
			VoteCount:     c.VoteCount,
			Popularity:    c.Popularity,
			EpisodeCount:  c.EpisodeCount,
		}

		if idx, ok := seen[key]; ok {
			// Merge role detail if this role is more prominent or has a character
			existing := &candidates[idx].entry
			if existing.Character == "" && e.Character != "" {
				existing.Character = e.Character
				existing.Department = e.Department
				existing.CreditKind = e.CreditKind
			} else if existing.Job == "" && e.Job != "" {
				existing.Job = e.Job
			}
			return
		}

		seen[key] = len(candidates)
		candidates = append(candidates, candidate{entry: e})
	}

	for _, c := range comb.Cast {
		add(c, store.CreditCast)
	}
	for _, c := range comb.Crew {
		add(c, store.CreditCrew)
	}

	// Sort best first: popularity DESC, voteCount DESC, year DESC
	slices.SortFunc(candidates, func(a, b candidate) int {
		if c := cmp.Compare(b.entry.Popularity, a.entry.Popularity); c != 0 {
			return c
		}
		if c := cmp.Compare(b.entry.VoteCount, a.entry.VoteCount); c != 0 {
			return c
		}
		return cmp.Compare(b.entry.Year, a.entry.Year)
	})

	if len(candidates) > maxFilmography {
		candidates = candidates[:maxFilmography]
	}

	out := make([]store.PersonFilmographyEntry, len(candidates))
	for i, c := range candidates {
		out[i] = c.entry
	}
	return out
}

// Filmography returns the external filmography for a person, best first.
// When lookups are off, it returns ErrLookupsOff.
func (w *Worker) Filmography(ctx context.Context, personID int64) ([]store.PersonFilmographyEntry, error) {
	person, err := w.o.Store.GetPerson(ctx, personID)
	if err != nil {
		return nil, err
	}

	p, r := w.o.Source.Provider(ctx)
	if p == nil {
		return nil, ErrLookupsOff
	}

	var from filmographyLister
	if l, ok := p.TMDB.(filmographyLister); ok && person.TMDBID > 0 {
		from = l
	}
	if from == nil {
		return nil, nil // TVDB-only or non-TMDB person has no TMDB filmography
	}

	ref := fmt.Sprintf("tmdb:%d@%s", person.TMDBID, r.Language)
	stored, err := w.o.Store.GetPersonFilmography(ctx, personID)
	if err != nil {
		return nil, err
	}

	var have []store.PersonFilmographyEntry
	if stored.Ref == ref {
		have = stored.Entries
	}

	now := w.now()
	if stored.Ref == ref && now.Sub(time.Unix(stored.CheckedAt, 0)) < filmographyTTL {
		return have, nil
	}

	w.mu.Lock()
	f, failed := w.filmographyFailed[personID]
	w.mu.Unlock()
	if failed && f.ref != ref {
		failed, f = false, fetchFailure{}
	}
	if failed && now.Before(f.until) {
		return have, f.err
	}

	fctx, cancel := context.WithTimeout(ctx, filmographyFetch)
	comb, err := from.CombinedCredits(fctx, person.TMDBID)
	cancel()
	if err != nil {
		w.noteFailure(ctx, w.filmographyFailed, personID, ref, f.attempts, err, now)
		return have, err
	}

	w.mu.Lock()
	delete(w.filmographyFailed, personID)
	w.mu.Unlock()

	entries := rankFilmography(comb)

	// Save to store even if caller disconnected
	return entries, w.o.Store.SetPersonFilmography(context.WithoutCancel(ctx), personID, ref, entries, now.Unix())
}
