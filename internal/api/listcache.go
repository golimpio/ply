package api

import (
	"net/http"
	"strconv"
	"strings"
)

// The lists of the library (GET /api/items, /api/shows, /api/titles) are what a browser asks for
// again and again, and what costs the server most: a big library takes a quarter of a second to
// read and several megabytes to send. Each carries a strong ETag made from the state of the
// library, so that a client that has the list gets a 304 without the library being read again.
//
//	"<generation>.<colours>"   (and ".<projection>" for a projection of the list: ".card")
//
// The generation is the store's (Store.Generation): it changes after every write a list can
// show, and starts at a new number on every run. The colours are the artwork generator's
// (art.Generator.ColourVersion), because the list carries the colour of each picture and that
// is not in the database; "n" when the server has no artwork. The ETag does not depend on the
// rest of the query: the browser keeps the validator of each URL apart.

// libraryETag is the validator of a list asked for now; projection names the shape of it when
// it is not the full one ("card"), so that the two never share a tag. It must be taken before the
// library is read: a write that lands in between then makes the list newer than its ETag says
// (the next request finds a newer ETag and gets the list again), never older. Taken after, a
// client could be told that a list from before the write is the current one.
func (s *Server) libraryETag(projection string) string {
	colours := "n"
	if s.Art != nil {
		colours = strconv.FormatUint(s.Art.ColourVersion(), 36)
	}
	tag := strconv.FormatUint(s.Store.Generation(), 36) + "." + colours
	if projection != "" {
		tag += "." + projection
	}
	return `"` + tag + `"`
}

// listCache begins the answer to a list (projection: see libraryETag). It says how it may be kept
// (the browser must ask again every time, which is cheap) and returns the ETag the answer will
// carry. When the client already has that list it answers 304 itself, and done is true: nothing
// is read.
func (s *Server) listCache(w http.ResponseWriter, r *http.Request, projection string) (etag string, done bool) {
	etag = s.libraryETag(projection)
	w.Header().Set("Cache-Control", "private, no-cache")
	if etagMatches(r.Header.Values("If-None-Match"), etag) {
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return etag, true
	}
	return etag, false
}

// writeList sends a list with its ETag. A degraded list (some of what the rows show could not be
// read, see metaView.degraded) is sent, but with no ETag and Cache-Control: no-store: the browser
// must not keep a list with holes in it as the current one, and ask again.
func writeList(w http.ResponseWriter, etag string, v any, degraded bool) {
	if degraded {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("ETag", etag)
	}
	writeJSON(w, http.StatusOK, v)
}

// etagMatches is the weak comparison of If-None-Match (RFC 9110, 13.1.2): "*", or a tag that is
// etag, whether either is marked weak. (compress marks the ETag of an answer it gzips as weak.)
func etagMatches(headers []string, etag string) bool {
	etag = strings.TrimPrefix(etag, "W/")
	for _, h := range headers {
		for _, tag := range strings.Split(h, ",") {
			tag = strings.TrimSpace(tag)
			if tag == "*" || strings.TrimPrefix(tag, "W/") == etag {
				return true
			}
		}
	}
	return false
}
