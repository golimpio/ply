// Package api implements the HTTP API and serves the embedded web UI.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"ply/internal/art"
	"ply/internal/hwaccel"
	"ply/internal/library"
	"ply/internal/metadata"
	"ply/internal/store"
	"ply/internal/stream"
	"ply/internal/subs"
	"ply/internal/watch"
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
	// Watcher reports which media roots are being watched (GET /api/scan); nil
	// when watching is off.
	Watcher interface{ Status() []watch.RootStatus }
	// Metadata (M6). Each may be nil: the endpoints that need it then answer
	// 503, and the lists carry no metadata.
	Metadata *metadata.Worker   // matching, refreshing, fixing matches
	Settings *metadata.Settings // the TMDB key, the privacy switch, the language
	Images   *metadata.ImageCache
	// Now is the clock; nil means time.Now. (GET /api/upcoming asks which day it is.)
	Now func() time.Time
	// AllowedHosts are extra Host names to answer, beyond the ones knownHost
	// always accepts (a public name behind a reverse proxy, say). An entry that
	// starts with "." accepts every name under it.
	AllowedHosts []string

	// Library folders (M7, docs/M7-FOLDERS.md). FlagFolders are the ones given on
	// the command line (Kind is "" for -media): they are listed as set at startup
	// and cannot be removed here.
	FlagFolders []library.Root
	// SetRoots is called, after every change, with the active roots (the flag
	// folders and the saved ones) so that the scanner and the watcher follow; nil
	// means nobody needs telling. The API calls it with its folders lock held,
	// because the order of the calls is the order of the changes (a removal calls
	// it before DropRoot): it must not block. It sets the roots and schedules the
	// scan of the new ones; it does not wait for it.
	SetRoots func(roots []library.Root)
	// DropRoot removes a folder's items, archiving what a person made of them
	// unless forget is set, and returns how many it removed. It may wait for a
	// running scan. nil means Store.ArchiveAndDeleteRoot, which is for tests: the
	// scanner's own DropRoot is the one that is safe against a running scan.
	DropRoot func(ctx context.Context, path string, forget bool) (int, error)
	// LastScan says when a folder was last scanned whole, without trouble (the
	// scanner's LastScan); the zero time means not since the server started. nil
	// reports 0 (unknown) for every folder.
	LastScan func(root string) time.Time

	// The limits on plays given with -max-streams and -max-transcodes (or the
	// environment variables that stand in for them); 0 means not given. A limit
	// that was given cannot be changed through the settings. ApplyLimits makes the
	// session manager follow them, the settings and the defaults.
	MaxStreamsFlag, MaxTranscodesFlag int
}

// Server is the HTTP handler for the whole application.
type Server struct {
	Options
	handler http.Handler

	foldersMu sync.Mutex // one change to the folders at a time: the checks and the write are one step
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
	mux.HandleFunc("GET /api/shows/{name}", s.handleShow)
	mux.HandleFunc("PUT /api/shows/{name}/match", s.handleMatchShow)
	mux.HandleFunc("DELETE /api/shows/{name}/match", s.handleUnmatchShow)
	mux.HandleFunc("POST /api/shows/{name}/metadata/refresh", s.handleRefreshShow)
	mux.HandleFunc("GET /api/items/{id}", s.handleItem)
	mux.HandleFunc("POST /api/items/{id}/playback", s.handlePlayback)
	mux.HandleFunc("GET /api/items/{id}/file", s.handleFile)
	mux.HandleFunc("GET /api/items/{id}/art/{kind}", s.handleArt)
	mux.HandleFunc("PUT /api/items/{id}/match", s.handleMatchItem)
	mux.HandleFunc("DELETE /api/items/{id}/match", s.handleUnmatchItem)
	mux.HandleFunc("POST /api/items/{id}/metadata/refresh", s.handleRefreshItem)
	mux.HandleFunc("GET /api/items/{id}/subtitles/{file}", s.handleSubtitle)
	mux.HandleFunc("PUT /api/items/{id}/progress", s.handleProgress)
	mux.HandleFunc("GET /api/continue", s.handleContinue)
	mux.HandleFunc("GET /api/sessions/{sid}/index.m3u8", s.handleSessionPlaylist)
	mux.HandleFunc("GET /api/sessions/{sid}/{file}", s.handleSessionFile)
	mux.HandleFunc("DELETE /api/sessions/{sid}", s.handleSessionDelete)
	mux.HandleFunc("GET /api/sessions", s.handleSessions)
	mux.HandleFunc("POST /api/sessions/{sid}/heartbeat", s.handleSessionHeartbeat)
	mux.HandleFunc("GET /api/people", s.handlePeople)
	mux.HandleFunc("GET /api/people/{id}", s.handlePerson)
	mux.HandleFunc("GET /api/genres", s.handleGenres)
	mux.HandleFunc("GET /api/titles", s.handleTitles)
	mux.HandleFunc("PATCH /api/titles/{id}", s.handlePatchTitle)
	mux.HandleFunc("GET /api/collections/{id}", s.handleCollection)
	mux.HandleFunc("GET /api/upcoming", s.handleUpcoming)
	mux.HandleFunc("GET /api/images/tmdb/{size}/{file}", s.handleTMDBImage)
	mux.HandleFunc("GET /api/images/local/{kind}/{id}/{slot}", s.handleLocalImage)
	mux.HandleFunc("GET /api/metadata/search", s.handleMetadataSearch)
	mux.HandleFunc("GET /api/metadata/status", s.handleMetadataStatus)
	mux.HandleFunc("GET /api/metadata/review", s.handleReview)
	mux.HandleFunc("POST /api/metadata/refresh", s.handleRefreshAll)
	mux.HandleFunc("GET /api/settings", s.handleSettings)
	mux.HandleFunc("PATCH /api/settings", s.handlePatchSettings)
	mux.HandleFunc("PUT /api/settings", s.handlePatchSettings) // what the settings page sends
	mux.HandleFunc("POST /api/scan", s.handleScanStart)
	mux.HandleFunc("GET /api/scan", s.handleScanStatus)
	mux.HandleFunc("GET /api/folders", s.handleFolders)
	mux.HandleFunc("POST /api/folders", s.handleAddFolder)
	mux.HandleFunc("PATCH /api/folders/{id}", s.handlePatchFolder)
	mux.HandleFunc("DELETE /api/folders/{id}", s.handleDeleteFolder)
	mux.HandleFunc("GET /api/fs", s.handleFS)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	mux.Handle("/", spaHandler(o.UI))

	s.handler = middleware(mux, o.Token, o.AllowedHosts)
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// middleware applies CORS to everything but the private endpoints, answers only
// names this server is known by, keeps other sites from changing anything,
// keeps them from touching the private endpoints at all and, when token is set,
// requires it for /api/* except /api/info.
//
// No layer wraps the ResponseWriter, so http.ServeContent still sees the
// server's own writer (io.ReaderFrom for sendfile, http.Flusher).
func middleware(next http.Handler, token string, hosts []string) http.Handler {
	return cors(knownHosts(sameOriginWrites(sameSiteOnly(requireToken(next, token)), token), hosts))
}

// private reports whether the path is one of the endpoints that show or change
// the folders ply serves: /api/folders and /api/fs, which lists the server's
// directories; or what is being watched and by whom: /api/sessions (titles and
// the addresses of the clients), and the two calls that act on one session,
// DELETE /api/sessions/{sid} and POST /api/sessions/{sid}/heartbeat. They are for
// the server's own pages and for native clients, never for a page of another
// site, whatever the token. The playlist and the segments of a session
// (/api/sessions/{sid}/index.m3u8, /api/sessions/{sid}/seg00001.m4s) are not:
// Cast and AirPlay receivers fetch them from another origin.
func private(urlPath string) bool {
	p := path.Clean(urlPath)
	for _, root := range []string{"/api/folders", "/api/fs"} {
		if p == root || strings.HasPrefix(p, root+"/") {
			return true
		}
	}
	if p == "/api/sessions" {
		return true
	}
	if rest, ok := strings.CutPrefix(p, "/api/sessions/"); ok {
		sid, file, hasFile := strings.Cut(rest, "/")
		return sid != "" && (!hasFile || file == "heartbeat")
	}
	return false
}

// crossSite reports whether a browser marks the request as coming from a page of
// another site (see sameSiteOnly).
func crossSite(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return true
	}
	origin := r.Header.Get("Origin")
	return origin != "" && !sameOrigin(origin, r)
}

// sameSiteOnly answers 403 to a request for a private endpoint that a browser
// marks as coming from another site: Sec-Fetch-Site: cross-site, or an Origin
// that is not this server's. Requests with neither header (curl, native
// clients) and the server's own pages pass. CORS never opens these endpoints
// (see cors), so a page of another site could not read the answer; this keeps
// it from causing it, and from trying a token it found.
func sameSiteOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if private(r.URL.Path) {
			if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
				writeError(w, http.StatusForbidden, "cross-site request refused (Sec-Fetch-Site: cross-site)")
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r) {
				writeError(w, http.StatusForbidden, fmt.Sprintf("cross-origin request refused (Origin %s does not match Host %s; "+
					"a reverse proxy must pass the original Host header)", origin, r.Host))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// knownHosts refuses a request whose Host is a name ply doesn't know. That
// stops DNS rebinding: a page on evil.com whose name is re-pointed at
// this server's LAN address would otherwise be same-origin with it, read the
// library and pass sameOriginWrites.
func knownHosts(next http.Handler, extra []string) http.Handler {
	self, _ := os.Hostname()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !knownHost(r.Host, self, extra) {
			h := hostOnly(r.Host)
			writeError(w, http.StatusForbidden, fmt.Sprintf("unknown host name %s; start ply with -allow-host %s to use it "+
				"(if ply is behind a proxy, also pass the original Host header through)", h, h))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// knownHost reports whether host (a Host header, port optional) names this
// server: an IP address; a name no one can point at us from the internet
// (localhost, a single label, .local mDNS, .home.arpa, .internal, and the
// RFC 2606/6761 reserved names); the machine's hostname; or an entry of extra.
func knownHost(host, self string, extra []string) bool {
	h := strings.TrimSuffix(strings.ToLower(hostOnly(host)), ".")
	if h == "" || net.ParseIP(h) != nil || !strings.Contains(h, ".") {
		return true
	}
	for _, suffix := range []string{".localhost", ".local", ".home.arpa", ".internal", ".test", ".example", ".invalid"} {
		if strings.HasSuffix(h, suffix) {
			return true
		}
	}
	switch h {
	case "example.com", "example.net", "example.org", strings.TrimSuffix(strings.ToLower(self), "."):
		return true
	}
	for _, e := range extra {
		e = strings.TrimSuffix(strings.ToLower(e), ".")
		if e != "" && (h == e || strings.HasPrefix(e, ".") && (strings.HasSuffix(h, e) || h == e[1:])) {
			return true
		}
	}
	return false
}

// hostOnly drops the port (and IPv6 brackets) from a Host header.
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return strings.Trim(host, "[]")
}

// sameOriginWrites refuses a request that is not a read (not GET, HEAD or
// OPTIONS) and comes from a page of another site, unless it carries the token.
// CORS is open for the media and for Cast, so without this any page the user
// opens could change the settings of a server that has no token. Browsers send
// Origin on every such request; native clients send none and are not affected.
func sameOriginWrites(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r) && (token == "" || !validToken(r, token)) {
				writeError(w, http.StatusForbidden, fmt.Sprintf("cross-origin request refused (Origin %s does not match Host %s; "+
					"a reverse proxy must pass the original Host header)", origin, r.Host))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin reports whether origin is the scheme and host the request was made
// to. A TLS-terminating proxy says so in X-Forwarded-Proto; a page of another
// site cannot set that header (the CORS preflight would not allow it).
func sameOrigin(origin string, r *http.Request) bool {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return strings.EqualFold(origin, scheme+"://"+r.Host)
}

// cors opens the API to every origin (the media, the artwork and Cast need it),
// except for the private endpoints: they get no Access-Control-* header on any
// response, preflight included, so a browser never lets another site's script
// use them.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if private(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Headers", "Range, Authorization, Content-Type")
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
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
