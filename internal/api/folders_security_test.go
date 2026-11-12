package api

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// noCORS fails when the response carries any Access-Control-* header.
func noCORS(t *testing.T, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	for k := range rec.Header() {
		if strings.HasPrefix(strings.ToLower(k), "access-control-") {
			t.Errorf("%s: sent %s: %q", what, k, rec.Header().Get(k))
		}
	}
}

// privateRequest is one of the requests that show or change the folders.
type privateRequest struct {
	name, method string
	target       func(f *folderEnv, id int64) string
	body         func(f *folderEnv) string
}

var privateRequests = []privateRequest{
	{"list folders", "GET", func(*folderEnv, int64) string { return "/api/folders" }, nil},
	{"browse", "GET", func(f *folderEnv, _ int64) string { return "/api/fs?path=" + f.base }, nil},
	{"places", "GET", func(*folderEnv, int64) string { return "/api/fs" }, nil},
	{"add", "POST", func(*folderEnv, int64) string { return "/api/folders" },
		func(f *folderEnv) string { return fmt.Sprintf(`{"path":%q,"kind":"movies"}`, f.path("movies")) }},
	{"change the type", "PATCH", func(_ *folderEnv, id int64) string { return fmt.Sprintf("/api/folders/%d", id) },
		func(*folderEnv) string { return `{"kind":"movies"}` }},
	{"scan", "POST", func(_ *folderEnv, id int64) string { return fmt.Sprintf("/api/folders/%d/scan", id) }, nil},
	{"remove", "DELETE", func(_ *folderEnv, id int64) string { return fmt.Sprintf("/api/folders/%d", id) }, nil},
	{"remove, forgetting", "DELETE", func(_ *folderEnv, id int64) string { return fmt.Sprintf("/api/folders/%d?forget=1", id) }, nil},
}

// mutates says whether the request, when it goes through, changes something that
// can be seen in the store or in what the scanner was told.
func (r privateRequest) mutates() bool { return r.method != "GET" }

func (r privateRequest) send(f *folderEnv, id int64, hdr map[string]string) *httptest.ResponseRecorder {
	body := ""
	if r.body != nil {
		body = r.body(f)
	}
	return f.do(r.method, r.target(f, id), hdr, body)
}

// What the browser sends when another site's script calls the API, or when a
// form on another site posts to it.
var foreignRequests = map[string]map[string]string{
	"another site":                {"Origin": "http://evil.example"},
	"a sandboxed page":            {"Origin": "null"},
	"another port of our host":    {"Origin": "http://example.com:8421"},
	"another scheme":              {"Origin": "https://example.com"},
	"a subdomain":                 {"Origin": "http://evil.example.com"},
	"Sec-Fetch-Site cross-site":   {"Sec-Fetch-Site": "cross-site"},
	"Sec-Fetch-Site, other case":  {"Sec-Fetch-Site": "Cross-Site"},
	"cross-site, with our Origin": {"Sec-Fetch-Site": "cross-site", "Origin": "http://example.com"},
	"both":                        {"Sec-Fetch-Site": "cross-site", "Origin": "http://evil.example"},
}

// What the server's own pages and native clients send.
var ownRequests = map[string]map[string]string{
	"a native client (no headers)":      nil,
	"our own page (Origin)":             {"Origin": "http://example.com"},
	"our own page (Origin, other case)": {"Origin": "http://EXAMPLE.com"},
	"Sec-Fetch-Site same-origin":        {"Sec-Fetch-Site": "same-origin"},
	"Sec-Fetch-Site none":               {"Sec-Fetch-Site": "none"},
	"our own page behind a TLS proxy":   {"Origin": "https://example.com", "X-Forwarded-Proto": "https"},
}

func merge(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// A request from another site is refused, for every private endpoint, with and
// without a token, even with the right one; nothing it asks for happens. The
// same request from the server's own page does happen.
func TestPrivateEndpointsRefuseCrossSiteRequests(t *testing.T) {
	for _, token := range []string{"", folderToken} {
		for _, req := range privateRequests {
			t.Run(fmt.Sprintf("token=%v/%s", token != "", req.name), func(t *testing.T) {
				f := newFolderEnv(t, token)
				ui := f.add(f.path("other"), "shows")
				auth := f.auth() // the token, when there is one: it must not be enough

				for name, hdr := range foreignRequests {
					for _, withAuth := range []bool{false, true} {
						h := hdr
						if withAuth {
							h = merge(hdr, auth)
						}
						rec := req.send(f, ui.ID, h)
						wantJSONError(t, rec, 403)
						noCORS(t, rec, name)
					}
				}
				// Nothing happened.
				if saved := f.folders(); len(saved) != 1 || saved[0].Kind != "shows" {
					t.Errorf("a refused request changed the folders: %+v", saved)
				}
				if calls := f.callLog(); len(calls) != 0 {
					t.Errorf("a refused request reached the scanner: %v", calls)
				}

				// Positive controls: the same request from the server's own page, or from a
				// client that sends no browser headers, is handled.
				for name, hdr := range ownRequests {
					fresh := newFolderEnv(t, token)
					id := fresh.add(fresh.path("other"), "shows").ID
					rec := req.send(fresh, id, merge(hdr, fresh.auth()))
					if rec.Code >= 400 {
						t.Errorf("%s: %s = %d: %s", name, req.name, rec.Code, rec.Body)
					}
					noCORS(t, rec, name)
					if req.mutates() && len(fresh.callLog()) == 0 {
						t.Errorf("%s: %s went through but the scanner heard nothing", name, req.name)
					}
				}
			})
		}
	}
}

// The token is needed, when there is one, and a wrong one is no better than none.
func TestPrivateEndpointsNeedTheToken(t *testing.T) {
	for _, req := range privateRequests {
		t.Run(req.name, func(t *testing.T) {
			f := newFolderEnv(t, folderToken)
			ui := f.add(f.path("other"), "shows")
			for name, hdr := range map[string]map[string]string{
				"no token":             nil,
				"wrong bearer":         {"Authorization": "Bearer nope"},
				"empty bearer":         {"Authorization": "Bearer "},
				"basic":                {"Authorization": "Basic " + folderToken},
				"bare":                 {"Authorization": folderToken},
				"our Origin, no token": {"Origin": "http://example.com"},
			} {
				rec := req.send(f, ui.ID, hdr)
				wantJSONError(t, rec, 401)
				noCORS(t, rec, name)
			}
			if len(f.callLog()) != 0 || len(f.folders()) != 1 {
				t.Errorf("an unauthorised request had an effect: %v %v", f.callLog(), f.folders())
			}
			// Positive controls: the Bearer header and ?token= both work.
			rec := req.send(f, ui.ID, map[string]string{"Authorization": "Bearer " + folderToken})
			if rec.Code >= 400 {
				t.Errorf("bearer: %d %s", rec.Code, rec.Body)
			}
			g := newFolderEnv(t, folderToken)
			id := g.add(g.path("other"), "shows").ID
			target := req.target(g, id)
			sep := "?"
			if strings.Contains(target, "?") {
				sep = "&"
			}
			body := ""
			if req.body != nil {
				body = req.body(g)
			}
			if rec := g.do(req.method, target+sep+"token="+folderToken, nil, body); rec.Code >= 400 {
				t.Errorf("?token=: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

// Whatever the answer, a private endpoint sends no Access-Control header: a
// script on another site cannot read it, and a preflight for it fails.
func TestPrivateEndpointsNeverSendCORSHeaders(t *testing.T) {
	for _, token := range []string{"", folderToken} {
		f := newFolderEnv(t, token)
		ui := f.add(f.path("other"), "shows")
		auth := f.auth()
		bad := merge(auth, nil)
		for _, tc := range []struct {
			what, method, target, body string
			hdr                        map[string]string
		}{
			{"list", "GET", "/api/folders", "", auth},
			{"list, 401", "GET", "/api/folders", "", nil},
			{"browse", "GET", "/api/fs?path=" + f.base, "", auth},
			{"browse, 404", "GET", "/api/fs?path=" + f.path("nope"), "", auth},
			{"browse, 400", "GET", "/api/fs?path=rel", "", auth},
			{"browse, from our page", "GET", "/api/fs?path=" + f.base, "", merge(auth, map[string]string{"Origin": "http://example.com"})},
			{"unknown subpath", "GET", "/api/fs/x", "", auth},
			{"unknown subpath of folders", "GET", "/api/folders/1/x", "", auth},
			{"trailing slash", "GET", "/api/folders/", "", auth},
			{"add, 400", "POST", "/api/folders", `{}`, bad},
			{"add, 409", "POST", "/api/folders", fmt.Sprintf(`{"path":%q,"kind":"shows"}`, f.path("other")), auth},
			{"patch, 404", "PATCH", "/api/folders/999", `{"kind":"shows"}`, auth},
			{"delete, 404", "DELETE", "/api/folders/999", "", auth},
			{"patch", "PATCH", fmt.Sprintf("/api/folders/%d", ui.ID), `{"kind":"movies"}`, auth},
			{"delete", "DELETE", fmt.Sprintf("/api/folders/%d", ui.ID), "", auth},
			{"preflight of a PATCH", "OPTIONS", fmt.Sprintf("/api/folders/%d", ui.ID), "", map[string]string{
				"Origin": "http://evil.example", "Access-Control-Request-Method": "PATCH", "Access-Control-Request-Headers": "authorization, content-type"}},
			{"preflight of a POST", "OPTIONS", "/api/folders", "", map[string]string{
				"Origin": "http://evil.example", "Access-Control-Request-Method": "POST"}},
			{"preflight of a listing", "OPTIONS", "/api/fs", "", map[string]string{
				"Origin": "http://evil.example", "Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "authorization"}},
			{"preflight with the token", "OPTIONS", "/api/fs", "", merge(auth, map[string]string{
				"Origin": "http://evil.example", "Access-Control-Request-Method": "GET"})},
			{"unknown host", "GET", "http://rebind.evil.com/api/fs", "", auth},
			{"unknown host, folders", "GET", "http://rebind.evil.com/api/folders", "", auth},
			{"unclean path", "GET", "/api//fs", "", auth},
			{"unclean path with dots", "GET", "/api/x/../folders", "", auth},
		} {
			rec := f.do(tc.method, tc.target, tc.hdr, tc.body)
			noCORS(t, rec, fmt.Sprintf("token=%v %s (%d)", token != "", tc.what, rec.Code))
			if tc.method == "OPTIONS" && rec.Code < 400 && rec.Code != 204 {
				t.Errorf("%s: preflight = %d", tc.what, rec.Code)
			}
		}

		// Positive control: everything else in the API still sends them.
		rec := f.do("GET", "/api/items", auth, "")
		wantStatus(t, rec, 200)
		checkCORSHeaders(t, rec, "GET /api/items")
		rec = f.do("OPTIONS", "/api/items", map[string]string{"Origin": "http://evil.example", "Access-Control-Request-Method": "GET"}, "")
		wantStatus(t, rec, 204)
		checkCORSHeaders(t, rec, "preflight of /api/items")
		// Even a path that begins like a private one but is not.
		rec = f.do("GET", "/api/foldersx", auth, "")
		checkCORSHeaders(t, rec, "GET /api/foldersx")
		rec = f.do("GET", "/api/fsx", auth, "")
		checkCORSHeaders(t, rec, "GET /api/fsx")
	}
}

// The preflight of a private endpoint is not answered with the permission a
// browser looks for (a 2xx with Access-Control-Allow-*): checked above for the
// headers; here, that the old open answer (204) is not given to it either.
func TestPreflightOfAPrivateEndpointIsNotAnswered(t *testing.T) {
	f := newFolderEnv(t, "")
	hdr := map[string]string{"Origin": "http://evil.example", "Access-Control-Request-Method": "DELETE"}
	for _, target := range []string{"/api/folders", "/api/folders/1", "/api/fs"} {
		if rec := f.do("OPTIONS", target, hdr, ""); rec.Code == 204 {
			t.Errorf("OPTIONS %s = 204", target)
		}
	}
	// Control: for an ordinary endpoint it is.
	if rec := f.do("OPTIONS", "/api/items", hdr, ""); rec.Code != 204 {
		t.Errorf("control: OPTIONS /api/items = %d", rec.Code)
	}
}

// A page of another site cannot learn the server's directories by guessing at
// the token either: the cross-site check comes before it.
func TestCrossSiteRequestDoesNotTestTheToken(t *testing.T) {
	f := newFolderEnv(t, folderToken)
	rec := f.do("GET", "/api/fs?path="+f.base, map[string]string{"Origin": "http://evil.example", "Authorization": "Bearer wrong"}, "")
	wantJSONError(t, rec, 403)
	rec = f.do("GET", "/api/fs?path="+f.base, map[string]string{"Origin": "http://evil.example"}, "")
	wantJSONError(t, rec, 403)
	// Control: from our own page, a wrong token is a 401 and the right one a listing.
	own := map[string]string{"Origin": "http://example.com"}
	wantJSONError(t, f.do("GET", "/api/fs?path="+f.base, merge(own, map[string]string{"Authorization": "Bearer wrong"}), ""), 401)
	wantStatus(t, f.do("GET", "/api/fs?path="+f.base, merge(own, f.auth()), ""), 200)
}

// The cross-site check is for the private endpoints: the rest of the API keeps
// answering other origins as before (TestCrossSiteReadsAndPreflightsStayOpen).
func TestCrossSiteStillReadsTheRestOfTheAPI(t *testing.T) {
	f := newFolderEnv(t, "")
	for _, hdr := range foreignRequests {
		rec := f.do("GET", "/api/items", hdr, "")
		wantStatus(t, rec, 200)
		checkCORSHeaders(t, rec, "GET /api/items")
	}
	if n, _ := f.store.CountItems(context.Background()); n != 3 {
		t.Fatalf("control: %d items", n)
	}
}
