package api

import "net/http"

// handleCollections lists the collections that have a title in the library, by
// name, each with how many titles it has. It is GET /api/collections.
func (s *Server) handleCollections(w http.ResponseWriter, r *http.Request) {
	collections, err := s.Store.Collections(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]CollectionCard, len(collections))
	for i, c := range collections {
		out[i] = CollectionCard{ID: c.ID, Name: c.Name, Poster: s.imageURL(c.Poster, posterSize),
			Backdrop: s.imageURL(c.Backdrop, backdropSize), Count: c.Count}
	}
	writeJSON(w, http.StatusOK, out)
}
