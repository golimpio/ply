package api

// Who a request is (docs/M11-CLIENTS.md, "Auth"): the owner (the shared token, or no token at
// all), a paired device (its own token, as Bearer), or whoever holds a URL the server built (a URL
// token, good for that URL's scope, for a day or two, while its device is paired).

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"ply/internal/store"
)

// principal is who a request is: a paired device, or the owner (device "").
type principal struct{ device string }

func (p principal) owner() bool { return p.device == "" }

type principalKey struct{}

// principalOf is who r is; the owner for a request that went through no authentication (a handler
// called directly, as in a test).
func principalOf(r *http.Request) principal {
	p, _ := r.Context().Value(principalKey{}).(principal)
	return p
}

// requireAuth answers 401 to an /api request (but /api/info and /api/pair) without a credential,
// and 403 to a device that asks for what only the owner may do. Without a token every request is
// the owner's. A path the checks could read apart from the router is a 400 (routedPath).
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !needsAuth(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		route, ok := routedPath(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "a path with an invalid escape or a dot segment")
			return
		}
		p, ok := s.authenticate(r, route)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		ctx := context.WithValue(r.Context(), principalKey{}, p)
		if !p.owner() {
			if ownerOnly(r.Method, route) {
				writeError(w, http.StatusForbidden, "only the owner's token may do this")
				return
			}
			// A revoke ends what the device has open (its feed) at once.
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			defer cancel()
			defer s.pairs.hold(p.device, cancel)()
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// routedPath is the path as the router matches it, which the checks here must read too: each
// segment unescaped ("%73ettings" is "settings"), but a slash inside a segment kept as "%2F" (as
// in a show named "AC/DC": one segment, not two), and "%" as "%25". ok is false for a path the
// checks cannot read as the router does: an invalid escape, or a "." or ".." segment (the router
// keeps an encoded one as a literal, where a cleaning check would follow it).
func routedPath(r *http.Request) (p string, ok bool) {
	segs := strings.Split(r.URL.EscapedPath(), "/")
	for i, s := range segs {
		u, err := url.PathUnescape(s)
		if err != nil || u == "." || u == ".." {
			return "", false
		}
		segs[i] = segmentEscaper.Replace(u)
	}
	return strings.Join(segs, "/"), true
}

// routedPathOf is routedPath for the checks that only sort requests (private); for a path that
// requireAuth refuses, its decoded form, as before.
func routedPathOf(r *http.Request) string {
	if p, ok := routedPath(r); ok {
		return p
	}
	return r.URL.Path
}

var segmentEscaper = strings.NewReplacer("%", "%25", "/", "%2F")

func needsAuth(p string) bool {
	return (p == "/api" || strings.HasPrefix(p, "/api/")) && p != "/api/info" && p != "/api/pair"
}

// authenticate finds who r is from its credential: the shared token as Bearer or ?token=, a
// device's token as Bearer, or a URL token as ?token= on a GET or HEAD, good for route (r's path
// as the router matches it).
func (s *Server) authenticate(r *http.Request, route string) (principal, bool) {
	if s.Token == "" {
		return principal{}, true
	}
	if validToken(r, s.Token) {
		return principal{}, true
	}
	if b := bearer(r); b != "" {
		id, ok := s.pairs.device(r.Context(), s.Store, b)
		return principal{device: id}, ok
	}
	q := r.URL.Query().Get("token")
	if !strings.HasPrefix(q, urlTokenPrefix) || r.Method != http.MethodGet && r.Method != http.MethodHead {
		return principal{}, false
	}
	dev, ok := s.urls.verify(q, route)
	if !ok || dev != "" && !s.pairs.paired(r.Context(), s.Store, dev) {
		return principal{}, false
	}
	return principal{device: dev}, true
}

// ownerOnly reports whether r is something only the owner may do: pair and revoke, look at the
// server's folders, change the server (its settings, folders, queue, jobs and schedules, a scan, a
// refresh of the metadata) and curate the library (a match, a refresh of a title's metadata, an
// edit of a title, a subtitle file added or removed). A viewer's own state is a device's to
// change: playing, progress, watched, its plays, a subtitle downloaded. A device registering
// another device is refused by handleDevicePut.
func ownerOnly(method, p string) bool {
	switch {
	case p == "/api/pairing", p == "/api/settings/test", p == "/api/fs", strings.HasPrefix(p, "/api/fs/"):
		return true
	case strings.HasPrefix(p, "/api/devices/") && strings.HasSuffix(p, "/token"):
		return true
	case method == http.MethodGet || method == http.MethodHead:
		return false
	}
	for _, root := range []string{"/api/settings", "/api/folders", "/api/queue", "/api/jobs", "/api/schedules", "/api/scan", "/api/metadata"} {
		if p == root || strings.HasPrefix(p, root+"/") {
			return true
		}
	}
	segs := strings.Split(strings.TrimPrefix(p, "/api/"), "/")
	if len(segs) < 2 {
		return false
	}
	rest := strings.Join(segs[2:], "/")
	switch segs[0] {
	case "items", "shows":
		return rest == "match" || rest == "metadata/refresh" ||
			segs[0] == "items" && strings.HasPrefix(rest+"/", "subtitles/") && rest != "subtitles/download"
	case "titles":
		return rest == "" // PATCH /api/titles/{id}; its watched state is a viewer's
	}
	return false
}

// bearer is the credential of an "Authorization: Bearer" header; "" for none.
func bearer(r *http.Request) string {
	if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// validToken reports whether r carries want, the shared token, as Bearer or ?token=.
func validToken(r *http.Request, want string) bool {
	// Hash first so the comparison time does not depend on the length.
	w := sha256.Sum256([]byte(want))
	ok := 0
	for _, c := range []string{r.URL.Query().Get("token"), bearer(r)} {
		g := sha256.Sum256([]byte(c))
		ok |= subtle.ConstantTimeCompare(g[:], w[:])
	}
	return ok == 1
}

// tokenHash is how a device's token is kept: its SHA-256, in hex.
func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// pairedTokens are the paired devices' tokens (as their hashes), kept in memory: read on every
// request a device makes, written on pairing and revoking, loaded from the store the first time.
type pairedTokens struct {
	// change is held across a pairing's or a revoke's write to the store and to the maps, so
	// that the two cannot be left apart.
	change sync.Mutex

	mu     sync.Mutex
	loaded bool
	byHash map[string]string // token hash → device id
	byID   map[string]string // device id → token hash
	live   map[string]map[*context.CancelFunc]struct{}
}

// hold records a request of device id under way, ended by cancel when the device is revoked;
// the caller releases it when it is done.
func (t *pairedTokens) hold(id string, cancel context.CancelFunc) (release func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.live == nil {
		t.live = map[string]map[*context.CancelFunc]struct{}{}
	}
	if t.live[id] == nil {
		t.live[id] = map[*context.CancelFunc]struct{}{}
	}
	key := &cancel
	t.live[id][key] = struct{}{}
	if _, paired := t.byID[id]; t.loaded && !paired {
		cancel() // revoked between the check of its credential and now
	}
	return func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		delete(t.live[id], key)
		if len(t.live[id]) == 0 {
			delete(t.live, id)
		}
	}
}

func (t *pairedTokens) load(ctx context.Context, st *store.Store) bool {
	if t.loaded {
		return true
	}
	m, err := st.DeviceTokens(ctx)
	if err != nil {
		return false // tried again next time
	}
	t.byHash, t.byID = m, make(map[string]string, len(m))
	for h, id := range m {
		t.byID[id] = h
	}
	t.loaded = true
	return true
}

// device is the paired device whose token this is.
func (t *pairedTokens) device(ctx context.Context, st *store.Store, token string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.load(ctx, st) {
		return "", false
	}
	id, ok := t.byHash[tokenHash(token)]
	return id, ok
}

// paired reports whether the device id is paired now.
func (t *pairedTokens) paired(ctx context.Context, st *store.Store, id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.load(ctx, st) {
		return false
	}
	_, ok := t.byID[id]
	return ok
}

// set records that device id now has the token of hash ("" for none: revoked, and what it has
// open is ended), in place of the one it had. It is called after the store has it, under change.
func (t *pairedTokens) set(id, hash string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if hash == "" {
		for cancel := range t.live[id] {
			(*cancel)()
		}
	}
	if !t.loaded {
		return // the first load reads it from the store
	}
	if old, ok := t.byID[id]; ok {
		delete(t.byHash, old)
		delete(t.byID, id)
	}
	if hash != "" {
		t.byHash[hash], t.byID[id] = id, hash
	}
}

// URL tokens: "u1.<device>.<expires>.<mac>" (docs/M11-CLIENTS.md, "URL tokens").
const urlTokenPrefix = "u1."

// urlTokenDays bounds how far ahead an expiry may be: one past it was not made by sign.
const urlTokenDays = 3

type urlSigner struct {
	key []byte
	now func() time.Time
}

// newURLSigner makes the key of the URL tokens from the server's secret and the shared token, so
// that a new -token voids the URLs made with the old one.
func newURLSigner(secret []byte, token string) *urlSigner {
	if len(secret) == 0 {
		secret = make([]byte, 32)
		rand.Read(secret)
	}
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("ply url tokens\x00" + token))
	return &urlSigner{key: m.Sum(nil), now: time.Now}
}

// What a URL token covers: the URL's own path, or for a playlist the directory it is in (its
// segments and the playlists it names are relative to it). The two are told apart in the MAC.
func pathScope(p string) string { return "path:" + path.Clean("/"+p) }
func dirScope(p string) string  { return "dir:" + path.Dir(path.Clean("/"+p)) + "/" }

// expires is when a token made now expires: the start of the UTC day after next, so that every
// token of a day is the same.
func (u *urlSigner) expires() int64 {
	day := u.now().UTC().Truncate(24 * time.Hour)
	return day.Add(48 * time.Hour).Unix()
}

func (u *urlSigner) mac(scope, device string, exp int64) string {
	m := hmac.New(sha256.New, u.key)
	m.Write([]byte(scope + "\x00" + device + "\x00" + strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:16])
}

// sign is the token of scope (pathScope or dirScope), made for device ("" for no device).
func (u *urlSigner) sign(scope, device string) string {
	exp := u.expires()
	return urlTokenPrefix + device + "." + strconv.FormatInt(exp, 10) + "." + u.mac(scope, device, exp)
}

// verify reports the device a token was made for, if it is good for a request of path p now: one
// made for p itself, or for the directory p is in.
func (u *urlSigner) verify(tok, p string) (string, bool) {
	parts := strings.Split(strings.TrimPrefix(tok, urlTokenPrefix), ".")
	if len(parts) != 3 || parts[0] != "" && !store.ValidDeviceID(parts[0]) {
		return "", false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	now := u.now().Unix()
	if err != nil || exp <= now || exp > now+urlTokenDays*86400 {
		return "", false
	}
	for _, scope := range []string{pathScope(p), dirScope(p)} {
		if hmac.Equal([]byte(parts[2]), []byte(u.mac(scope, parts[0], exp))) {
			return parts[0], true
		}
	}
	return "", false
}
