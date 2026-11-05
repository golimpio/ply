package api

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ply/internal/metadata"
	"ply/internal/store"
)

// imageHeaders are set on every image response. The images come from outside
// (TMDB) or from the user's folders: they are only ever shown, never run.
func imageHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
}

var tmdbContentTypes = map[string]string{".jpg": "image/jpeg", ".png": "image/png", ".svg": "image/svg+xml"}

// handleTMDBImage serves GET /api/images/tmdb/{size}/{file} from the cache,
// downloading the image from TMDB the first time. Anything the size allowlist
// or the file name rule does not allow is a 400 before it touches the disk or
// the network; an image that cannot be had is a 404 (the page falls back to the
// frame-grab artwork).
func (s *Server) handleTMDBImage(w http.ResponseWriter, r *http.Request) {
	size, file := r.PathValue("size"), r.PathValue("file")
	if !metadata.ValidTMDBImage(size, file) {
		writeError(w, http.StatusBadRequest, "invalid image")
		return
	}
	if s.Images == nil {
		writeError(w, http.StatusNotFound, "no such image")
		return
	}
	path, err := s.Images.TMDB(r.Context(), size, file)
	switch {
	case err == nil:
	case r.Context().Err() != nil:
		return // the client went away
	case errors.Is(err, metadata.ErrBadImage):
		writeError(w, http.StatusBadRequest, "invalid image")
		return
	default:
		writeError(w, http.StatusNotFound, "no such image")
		return
	}
	f, err := metadata.OpenRegular(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "no such image")
		return
	}
	defer f.Close()
	fi, _ := f.Stat()
	h := w.Header()
	imageHeaders(h)
	h.Set("Content-Type", tmdbContentTypes[strings.ToLower(filepath.Ext(file))])
	// A TMDB file name names its content: it never changes.
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// handleTVDBImage serves GET /api/images/tvdb/{size}/{path...} (path is TVDB's, like
// banners/posters/81189-10.jpg) from the cache, downloading the image from
// artworks.thetvdb.com the first time: nothing else is ever fetched, whatever the
// path says. A size off the allowlist or a path that is not plain (letters, digits and
// / _ . - only, no "..", no empty or dot segment, a picture's extension) is a 400
// before it touches the disk or the network; an image that cannot be had is a 404.
func (s *Server) handleTVDBImage(w http.ResponseWriter, r *http.Request) {
	size, file := r.PathValue("size"), "/"+r.PathValue("path")
	if !metadata.ValidTVDBImage(size, file) {
		writeError(w, http.StatusBadRequest, "invalid image")
		return
	}
	if s.Images == nil {
		writeError(w, http.StatusNotFound, "no such image")
		return
	}
	path, err := s.Images.TVDB(r.Context(), size, file)
	switch {
	case err == nil:
	case r.Context().Err() != nil:
		return // the client went away
	case errors.Is(err, metadata.ErrBadImage):
		writeError(w, http.StatusBadRequest, "invalid image")
		return
	default:
		writeError(w, http.StatusNotFound, "no such image")
		return
	}
	f, err := metadata.OpenRegular(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "no such image")
		return
	}
	defer f.Close()
	fi, _ := f.Stat()
	h := w.Header()
	imageHeaders(h)
	h.Set("Content-Type", metadata.TVDBContentType(file))
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// handleLocalImage serves GET /api/images/local/{kind}/{id}/{slot}: artwork
// found next to a file, by the path the database holds (never one from the
// request), or the cover embedded in the media file.
func (s *Server) handleLocalImage(w http.ResponseWriter, r *http.Request) {
	kind, slot := r.PathValue("kind"), r.PathValue("slot")
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 || (kind != store.ArtItem && kind != store.ArtSeries) {
		writeError(w, http.StatusNotFound, "no such image")
		return
	}
	path, err := s.Store.LocalArt(r.Context(), kind, id, slot)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such image")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if stream, ok := store.ParseEmbeddedRef(path); ok {
		if path, ok = s.embeddedCover(w, r, kind, id, stream); !ok {
			return
		}
	}
	// Whatever the database says, only a regular file that really is an image
	// is sent (the scanner follows symlinks: "poster.jpg" may lead anywhere).
	f, fi, contentType, err := metadata.OpenLocalImage(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.Logger.Debug("local image", "kind", kind, "id", id, "err", err)
		}
		writeError(w, http.StatusNotFound, "no such image")
		return
	}
	defer f.Close()
	serveImage(w, r, f, fi.ModTime(), contentType)
}

// embeddedCover extracts (once) the cover inside an item's media file. It
// writes the error response itself and reports false when there is no cover.
func (s *Server) embeddedCover(w http.ResponseWriter, r *http.Request, kind string, id int64, stream int) (string, bool) {
	if kind != store.ArtItem || s.Images == nil {
		writeError(w, http.StatusNotFound, "no such image")
		return "", false
	}
	e, err := s.Store.GetEntry(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "no such image")
		return "", false
	}
	path, err := s.Images.Embedded(r.Context(), id, e.Path, stream)
	if err != nil {
		if r.Context().Err() == nil {
			writeError(w, http.StatusNotFound, "no such image")
		}
		return "", false
	}
	return path, true
}

// serveImage sends sidecar artwork. It may change on disk, so it is revalidated
// (Last-Modified) rather than cached for good.
func serveImage(w http.ResponseWriter, r *http.Request, f *os.File, modTime time.Time, contentType string) {
	h := w.Header()
	imageHeaders(h)
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "", modTime, f)
}
