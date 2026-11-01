// Package api implements the HTTP API and serves the embedded web UI.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"ply/internal/art"
	"ply/internal/hwaccel"
	"ply/internal/library"
	"ply/internal/store"
	"ply/internal/stream"
	"ply/internal/subs"
)

// ScanController is the scanner as the API sees it.
type ScanController interface {
	// Trigger starts a background scan; false means one is already running.
	Trigger() bool
	Status(ctx context.Context) library.Status
}

// Options configures a Server.
type Options struct {
	Store   *store.Store
	Scanner ScanController
	UI      fs.FS // built SPA (index.html at the root); may be nil
	ID      string
	Name    string
	Version string
	Token   string // empty disables authentication
	Logger  *slog.Logger
	// Streams runs HLS sessions for remux and transcode playback; nil
	// disables them.
	Streams *stream.Manager
	// HW says which encoder transcodes use; nil means libx264 only.
	HW *hwaccel.Detector
	// Art generates artwork on demand; nil disables it.
	Art *art.Generator
	// Subs converts text subtitles to WebVTT; nil disables the subtitle
	// endpoint (tracks are then listed without a url).
	Subs *subs.Generator
}

// Server is the HTTP handler for the whole application.
type Server struct {
	Options
	handler http.Handler
}

// New builds the server. The returned value is an http.Handler.
func New(o Options) *Server {
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	s := &Server{Options: o}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/info", s.handleInfo)
	mux.HandleFunc("GET /api/items", s.handleItems)
	mux.HandleFunc("GET /api/shows", s.handleShows)
	mux.HandleFunc("GET /api/items/{id}", s.handleItem)
	mux.HandleFunc("POST /api/items/{id}/playback", s.handlePlayback)
	mux.HandleFunc("GET /api/items/{id}/file", s.handleFile)
	mux.HandleFunc("GET /api/items/{id}/art/{kind}", s.handleArt)
	mux.HandleFunc("GET /api/items/{id}/subtitles/{file}", s.handleSubtitle)
	mux.HandleFunc("PUT /api/items/{id}/progress", s.handleProgress)
	mux.HandleFunc("GET /api/continue", s.handleContinue)
	mux.HandleFunc("GET /api/sessions/{sid}/index.m3u8", s.handleSessionPlaylist)
	mux.HandleFunc("GET /api/sessions/{sid}/{file}", s.handleSessionFile)
	mux.HandleFunc("DELETE /api/sessions/{sid}", s.handleSessionDelete)
	mux.HandleFunc("POST /api/scan", s.handleScanStart)
	mux.HandleFunc("GET /api/scan", s.handleScanStatus)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	mux.Handle("/", spaHandler(o.UI))

	s.handler = middleware(mux, o.Token)
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// middleware applies CORS to everything and, when token is set, requires it
// for /api/* except /api/info.
//
// Neither layer wraps the ResponseWriter, so http.ServeContent still sees the
// server's own writer (io.ReaderFrom for sendfile, http.Flusher).
func middleware(next http.Handler, token string) http.Handler {
	return cors(requireToken(next, token))
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Headers", "Range, Authorization, Content-Type")
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		h.Set("Access-Control-Expose-Headers", "Content-Range, Accept-Ranges, Content-Length, Content-Type")
		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requireToken(next http.Handler, token string) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if needsAuth(r.URL.Path) && !validToken(r, token) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func needsAuth(path string) bool {
	return (path == "/api" || strings.HasPrefix(path, "/api/")) && path != "/api/info"
}

// validToken accepts "Authorization: Bearer <t>" or "?token=<t>".
func validToken(r *http.Request, want string) bool {
	candidates := []string{r.URL.Query().Get("token")}
	if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		candidates = append(candidates, strings.TrimSpace(h[7:]))
	}
	// Hash first so the comparison time does not depend on the length.
	w := sha256.Sum256([]byte(want))
	ok := 0
	for _, c := range candidates {
		g := sha256.Sum256([]byte(c))
		ok |= subtle.ConstantTimeCompare(g[:], w[:])
	}
	return ok == 1
}

// withToken adds the token to a relative URL when one is configured: as the
// first query parameter, or after the existing ones ("...?v=1&token=...").
func (s *Server) withToken(u string) string {
	if s.Token == "" {
		return u
	}
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + "token=" + url.QueryEscape(s.Token)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.Logger.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}
