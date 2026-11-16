package api

import (
	"net/http"
	"slices"
	"strconv"

	"ply/internal/metadata"
	"ply/internal/store"
)

// What a preview carries of a title's credits.
const previewCast = 12

// previewCrewJobs are the crew jobs a preview names: those of the credit lines of a title's page.
var previewCrewJobs = []string{"Creator", "Director", "Writer", "Screenplay", "Story", "Novel"}

// TitlePreview is a title outside the library as TMDB describes it (GET
// /api/metadata/preview/{kind}/{tmdbId}): what the dialog of a person's credit shows once it is
// in. Nothing of it is stored.
type TitlePreview struct {
	Kind          string          `json:"kind"`
	TMDBID        int64           `json:"tmdbId"`
	IMDbID        string          `json:"imdbId,omitempty"`
	Title         string          `json:"title"`
	OriginalTitle string          `json:"originalTitle,omitempty"`
	Year          int             `json:"year"`
	Overview      string          `json:"overview,omitempty"`
	Tagline       string          `json:"tagline,omitempty"`
	ReleaseDate   string          `json:"releaseDate,omitempty"`
	EndDate       string          `json:"endDate,omitempty"`
	Status        string          `json:"status,omitempty"`
	RuntimeMin    int             `json:"runtimeMin,omitempty"`
	Certification string          `json:"certification,omitempty"`
	Poster        string          `json:"poster,omitempty"`
	Backdrop      string          `json:"backdrop,omitempty"`
	Genres        []string        `json:"genres"`
	Cast          []PreviewCredit `json:"cast"`
	Crew          []PreviewCredit `json:"crew"`
}

// PreviewCredit is a person of a preview: not one of the library's people, so it has no page.
// Role is the character played, or the crew job.
type PreviewCredit struct {
	TMDBID  int64  `json:"tmdbId,omitempty"`
	Name    string `json:"name"`
	Role    string `json:"role,omitempty"`
	Profile string `json:"profile,omitempty"`
}

func (s *Server) handleTitlePreview(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind != store.KindMovie && kind != store.KindSeries {
		writeError(w, http.StatusBadRequest, `kind must be "movie" or "series"`)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "title not found")
		return
	}
	if s.metadataOff(w) {
		return
	}
	in, err := s.Metadata.Preview(r.Context(), kind, id)
	if metadata.IsNotFound(err) {
		writeError(w, http.StatusNotFound, "title not found")
		return
	}
	if err != nil {
		s.metadataError(w, r, err)
		return
	}
	out := TitlePreview{Kind: kind, TMDBID: id, IMDbID: in.IMDbID, Title: in.Title.Title, OriginalTitle: in.OriginalTitle,
		Year: in.Year, Overview: in.Overview, Tagline: in.Tagline, ReleaseDate: in.ReleaseDate, EndDate: in.EndDate,
		Status: in.Status, RuntimeMin: in.RuntimeMin, Certification: in.Certification,
		Poster: s.imageURL(in.Poster, posterSize), Backdrop: s.imageURL(in.Backdrop, backdropSize),
		Genres: []string{}, Cast: []PreviewCredit{}, Crew: []PreviewCredit{}}
	for _, g := range in.Genres {
		out.Genres = append(out.Genres, g.Name)
	}
	for _, c := range in.Credits {
		p := PreviewCredit{TMDBID: c.Person.TMDBID, Name: c.Person.Name, Profile: s.imageURL(c.Person.Profile, profileSize)}
		switch {
		case c.Kind == store.CreditCast && len(out.Cast) < previewCast:
			p.Role = c.Character
			out.Cast = append(out.Cast, p)
		case c.Kind == store.CreditCrew && slices.Contains(previewCrewJobs, c.Job):
			p.Role = c.Job
			out.Crew = append(out.Crew, p)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
