package api

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
)

// text is n bytes of JSON-looking text that gzip makes much smaller.
func text(n int) []byte {
	return bytes.Repeat([]byte(`{"a":1}`), n/7+1)[:n]
}

func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("not gzip: %v (%.40q)", err, b)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// answer is a handler that answers 200 with the content type and the body.
func answer(contentType string, body []byte, header ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		for i := 0; i+1 < len(header); i += 2 {
			w.Header().Set(header[i], header[i+1])
		}
		w.Write(body)
	})
}

func serve(h http.Handler, method, target string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCompressRules(t *testing.T) {
	body := text(2048)
	gz := []string{"Accept-Encoding", "gzip"}
	for _, c := range []struct {
		name string
		h    http.Handler
		hdr  []string
		gzip bool // is the answer compressed
	}{
		{"JSON, asked", answer("application/json", body), gz, true},
		{"JSON, not asked", answer("application/json", body), nil, false},
		{"JSON, gzip refused with q=0", answer("application/json", body), []string{"Accept-Encoding", "gzip;q=0"}, false},
		{"JSON, only identity", answer("application/json", body), []string{"Accept-Encoding", "identity"}, false},
		{"JSON, gzip among others", answer("application/json", body), []string{"Accept-Encoding", "br, GZIP;q=0.5, deflate"}, true},
		{"JSON, with a charset", answer("application/json; charset=utf-8", body), gz, true},
		{"just under the size", answer("application/json", text(gzipMin-1)), gz, false},
		{"just at the size", answer("application/json", text(gzipMin)), gz, true},
		{"HTML", answer("text/html; charset=utf-8", body), gz, true},
		{"CSS", answer("text/css", body), gz, true},
		{"JavaScript", answer("text/javascript; charset=utf-8", body), gz, true},
		{"old JavaScript", answer("application/javascript", body), gz, true},
		{"SVG", answer("image/svg+xml", body), gz, true},
		{"WebVTT", answer("text/vtt; charset=utf-8", body), gz, true},
		{"+json", answer("application/problem+json", body), gz, true},
		{"a JPEG", answer("image/jpeg", body), gz, false},
		{"a video", answer("video/mp4", body), gz, false},
		{"a segment", answer("video/iso.segment", body), gz, false},
		{"a playlist", answer("application/vnd.apple.mpegurl", body), gz, false},
		{"a font", answer("font/woff2", body), gz, false},
		{"bytes", answer("application/octet-stream", body), gz, false},
		{"an event stream", answer("text/event-stream", body), gz, false},
		{"no type at all", answer("", body), gz, false},
		{"already encoded", answer("application/json", body, "Content-Encoding", "br"), gz, false},
		{"a range", answer("application/json", body), []string{"Accept-Encoding", "gzip", "Range", "bytes=0-9"}, false},
		{"a content range", answer("application/json", body, "Content-Range", "bytes 0-9/100"), gz, false},
		{"a length that is small", answer("application/json", text(100), "Content-Length", "100"), gz, false},
		{"a length that is big", answer("application/json", body, "Content-Length", "2048", "Accept-Ranges", "bytes"), gz, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := serve(compress(c.h), "GET", "/api/anything", c.hdr...)
			if rec.Code != 200 {
				t.Fatalf("status %d", rec.Code)
			}
			want := body
			switch c.name {
			case "just under the size":
				want = text(gzipMin - 1)
			case "just at the size":
				want = text(gzipMin)
			case "a length that is small":
				want = text(100)
			}
			got := rec.Body.Bytes()
			if c.gzip {
				if ce := rec.Header().Get("Content-Encoding"); ce != "gzip" {
					t.Fatalf("Content-Encoding = %q, want gzip", ce)
				}
				if !bytes.Equal(gunzip(t, got), want) {
					t.Error("the body does not decompress to the original")
				}
				if len(got) >= len(want)/2 {
					t.Errorf("%d bytes for %d: not compressed", len(got), len(want))
				}
				for _, h := range []string{"Content-Length", "Accept-Ranges"} {
					if v := rec.Header().Get(h); v != "" {
						t.Errorf("a compressed answer has %s: %q", h, v)
					}
				}
			} else {
				if ce := rec.Header().Get("Content-Encoding"); ce != "" && c.name != "already encoded" {
					t.Errorf("Content-Encoding = %q, want none", ce)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("the body was changed (%d bytes, want %d)", len(got), len(want))
				}
			}
			if v := rec.Header().Values("Vary"); !strings.Contains(strings.Join(v, ","), "Accept-Encoding") {
				t.Errorf("Vary = %q, want Accept-Encoding", v)
			}
		})
	}
}

func TestCompressHeadersAndStatuses(t *testing.T) {
	body := text(4096)
	gz := []string{"Accept-Encoding", "gzip"}

	// The ETag of what is compressed is weak, and is left alone when it is not.
	if got := serve(compress(answer("application/json", body, "ETag", `"abc"`)), "GET", "/api/x", gz...).Header().Get("ETag"); got != `W/"abc"` {
		t.Errorf("ETag of a compressed answer = %q, want W/\"abc\"", got)
	}
	if got := serve(compress(answer("application/json", body, "ETag", `"abc"`)), "GET", "/api/x").Header().Get("ETag"); got != `"abc"` {
		t.Errorf("ETag of an answer that was not compressed = %q, want it as it was", got)
	}

	// Anything but a 200 is as the handler made it.
	for _, code := range []int{http.StatusPartialContent, http.StatusNotModified, http.StatusNotFound, http.StatusInternalServerError, http.StatusMovedPermanently} {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			if code != http.StatusNotModified {
				w.Write(body)
			}
		})
		rec := serve(compress(h), "GET", "/api/x", gz...)
		if rec.Code != code || rec.Header().Get("Content-Encoding") != "" {
			t.Errorf("status %d: got %d with Content-Encoding %q", code, rec.Code, rec.Header().Get("Content-Encoding"))
		}
		if code != http.StatusNotModified && !bytes.Equal(rec.Body.Bytes(), body) {
			t.Errorf("status %d: the body was changed", code)
		}
	}

	// Only GET is compressed: HEAD has no body to compress, and a write's answer is small.
	for _, method := range []string{"HEAD", "POST", "PUT", "PATCH", "DELETE"} {
		rec := serve(compress(answer("application/json", body)), method, "/api/x", gz...)
		if rec.Header().Get("Content-Encoding") != "" || !bytes.Equal(rec.Body.Bytes(), body) {
			t.Errorf("%s: Content-Encoding %q, %d bytes", method, rec.Header().Get("Content-Encoding"), rec.Body.Len())
		}
	}

	// An answer without a body, and one written a few bytes at a time, come out whole.
	empty := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	})
	if rec := serve(compress(empty), "GET", "/api/x", gz...); rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("Content-Encoding") != "" {
		t.Errorf("empty answer: %d, %d bytes, Content-Encoding %q", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Encoding"))
	}
	var want bytes.Buffer
	pieces := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for i := range 200 {
			s := fmt.Sprintf("piece %03d;", i)
			want.WriteString(s)
			io.WriteString(w, s)
		}
	})
	rec := serve(compress(pieces), "GET", "/api/x", gz...)
	if rec.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(gunzip(t, rec.Body.Bytes()), want.Bytes()) {
		t.Errorf("an answer in 200 small writes: Content-Encoding %q, decompresses to %d bytes, want %d",
			rec.Header().Get("Content-Encoding"), len(gunzip(t, rec.Body.Bytes())), want.Len())
	}
	small := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "a")
		io.WriteString(w, "b")
	})
	if rec := serve(compress(small), "GET", "/api/x", gz...); rec.Body.String() != "ab" || rec.Header().Get("Content-Encoding") != "" {
		t.Errorf("two small writes: %q, Content-Encoding %q", rec.Body.String(), rec.Header().Get("Content-Encoding"))
	}
}

// The media is not wrapped at all: the handler gets the server's own writer, so that
// http.ServeContent can sendfile and flush, whatever the type or the size.
func TestCompressDoesNotWrapMedia(t *testing.T) {
	var seen http.ResponseWriter
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = w
		w.Header().Set("Content-Type", "application/json") // even if it said it was text
		w.Write(text(8192))
	})
	for _, path := range []string{
		"/api/items/12/file", "/api/items/12/art/thumb", "/api/items/12/art/backdrop",
		"/api/images/tmdb/w342/abc.jpg", "/api/images/local/item/1/poster", "/api/images/tvdb/w342/banners/a.jpg",
		"/api/sessions/abc/index.m3u8", "/api/sessions/abc/init.mp4", "/api/sessions/abc/seg00001.m4s", "/api/events",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		compress(probe).ServeHTTP(rec, req)
		if seen != http.ResponseWriter(rec) {
			t.Errorf("GET %s: the handler got %T, not the server's writer", path, seen)
		}
		if rec.Header().Get("Content-Encoding") != "" || rec.Body.Len() != 8192 || rec.Header().Get("Vary") != "" {
			t.Errorf("GET %s: Content-Encoding %q, %d bytes, Vary %q", path, rec.Header().Get("Content-Encoding"), rec.Body.Len(), rec.Header().Get("Vary"))
		}
	}
	// The controls: the lists, the item's page, the subtitles and the web UI are wrapped, and compressed.
	for _, path := range []string{"/api/items", "/api/items/12", "/api/items/12/subtitles/3.vtt", "/api/sessions", "/api/shows/Some%20Show", "/index.html", "/assets/app.js"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		seen = nil
		compress(probe).ServeHTTP(rec, req)
		if _, ok := seen.(*compressWriter); !ok || rec.Header().Get("Content-Encoding") != "gzip" {
			t.Errorf("control, GET %s: the handler got %T, Content-Encoding %q", path, seen, rec.Header().Get("Content-Encoding"))
		}
	}
}

// The wrapper keeps the optional interfaces of the writer under it (docs/DESIGN.md, "Efficiency rules"):
// io.ReaderFrom, which an answer that is sent as it is passes on, and http.Flusher; and copying a reader
// through it, as http.ServeContent does, gives the same bytes compressed or not.
func TestCompressWriterKeepsTheOptionalInterfaces(t *testing.T) {
	var rf, fl atomic.Bool
	big := text(10000)
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, ok := w.(io.ReaderFrom)
		rf.Store(ok)
		_, ok = w.(http.Flusher)
		fl.Store(ok)
		if strings.HasSuffix(r.URL.Path, ".png") {
			w.Header().Set("Content-Type", "image/png")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		io.Copy(w, io.LimitReader(bytes.NewReader(big), int64(len(big)))) // not a WriterTo: it uses ReadFrom
	})
	ts := httptest.NewServer(compress(probe))
	t.Cleanup(ts.Close)

	resp := getRaw(t, ts.URL+"/api/x")
	body, _ := io.ReadAll(resp.Body)
	if !rf.Load() || !fl.Load() {
		t.Errorf("the handler sees ReaderFrom %v and Flusher %v", rf.Load(), fl.Load())
	}
	if resp.Header.Get("Content-Encoding") != "gzip" || !bytes.Equal(gunzip(t, body), big) {
		t.Errorf("copied JSON: Content-Encoding %q, %d bytes", resp.Header.Get("Content-Encoding"), len(body))
	}
	resp = getRaw(t, ts.URL+"/x.png")
	body, _ = io.ReadAll(resp.Body)
	if resp.Header.Get("Content-Encoding") != "" || !bytes.Equal(body, big) {
		t.Errorf("copied PNG: Content-Encoding %q, %d bytes", resp.Header.Get("Content-Encoding"), len(body))
	}
}

// recordingWriter is a writer that has the zero-copy path and counts the uses of it.
type recordingWriter struct {
	*httptest.ResponseRecorder
	readFrom int
}

func (w *recordingWriter) ReadFrom(r io.Reader) (int64, error) {
	w.readFrom++
	return io.Copy(w.ResponseRecorder, r)
}

// What is sent as it is goes to the ReadFrom of the writer underneath (that is where sendfile is), and
// what is compressed does not: the reader is copied through the compressor.
func TestCompressReadFromGoesToTheWriterUnderneathOnlyForWhatIsNotCompressed(t *testing.T) {
	big := text(10000)
	copyOf := func(contentType string) (*recordingWriter, *http.Request) {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", contentType)
			io.Copy(w, io.LimitReader(bytes.NewReader(big), int64(len(big)))) // not a WriterTo: io.Copy uses ReadFrom
		})
		w := &recordingWriter{ResponseRecorder: httptest.NewRecorder()}
		req := httptest.NewRequest("GET", "/x/not-media", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		compress(h).ServeHTTP(w, req)
		return w, req
	}

	w, _ := copyOf("image/png")
	if w.readFrom != 1 || w.Header().Get("Content-Encoding") != "" || !bytes.Equal(w.Body.Bytes(), big) {
		t.Errorf("a PNG: %d uses of the writer's ReadFrom (want 1), Content-Encoding %q, %d bytes", w.readFrom, w.Header().Get("Content-Encoding"), w.Body.Len())
	}
	w, _ = copyOf("application/json")
	if w.readFrom != 0 || w.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(gunzip(t, w.Body.Bytes()), big) {
		t.Errorf("JSON: %d uses of the writer's ReadFrom (want 0), Content-Encoding %q", w.readFrom, w.Header().Get("Content-Encoding"))
	}
}

// getRaw asks for a URL with the Accept-Encoding it is given and does not undo the
// encoding, so that what came over the wire can be seen.
func getRaw(t *testing.T, url string, hdr ...string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// An answer that is let out in pieces, with a flush between them, arrives in pieces: the
// flush goes through the compressor to the connection. A stream (what flushes before it has
// anything to compress) is not compressed at all.
func TestCompressFlushes(t *testing.T) {
	// Each handler waits for its gate: the test opens it once it has seen what was flushed.
	gates := map[string]chan struct{}{}
	for _, p := range []string{"/api/big", "/api/items/1/subtitles/1.vtt", "/api/events"} {
		gates[p] = make(chan struct{})
	}
	open := func(p string) { close(gates[p]) }
	mux := http.NewServeMux()
	mux.HandleFunc("/api/big", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(text(4096))
		w.(http.Flusher).Flush()
		<-gates[r.URL.Path]
		io.WriteString(w, "TAIL")
	})
	mux.HandleFunc("/api/items/1/subtitles/1.vtt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		rc.Flush()
		io.WriteString(w, "WEBVTT\n\none\n\n")
		rc.Flush()
		<-gates[r.URL.Path]
		io.WriteString(w, "two\n\n")
	})
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "retry: 3000\n\n")
		w.(http.Flusher).Flush()
		<-gates[r.URL.Path]
		io.WriteString(w, "event: x\n\n")
	})
	ts := httptest.NewServer(compress(mux))
	t.Cleanup(ts.Close)
	t.Cleanup(func() { // first: a failed test must not leave a handler at its gate for ts.Close to wait for
		for _, g := range gates {
			select {
			case <-g:
			default:
				close(g)
			}
		}
	})

	t.Run("flush after the compressor has begun", func(t *testing.T) {
		resp := getRaw(t, ts.URL+"/api/big")
		if resp.Header.Get("Content-Encoding") != "gzip" {
			t.Fatalf("Content-Encoding = %q: the control needs a compressed answer", resp.Header.Get("Content-Encoding"))
		}
		zr, err := gzip.NewReader(resp.Body) // returns once the header is here: nothing waits for the end
		if err != nil {
			t.Fatal(err)
		}
		readExactly(t, zr, string(text(4096))) // the handler is still waiting at its gate
		open("/api/big")
		rest, err := io.ReadAll(zr)
		if err != nil || string(rest) != "TAIL" {
			t.Errorf("rest = %q, %v", rest, err)
		}
	})

	for _, c := range []struct{ path, first string }{
		{"/api/items/1/subtitles/1.vtt", "WEBVTT\n\none\n\n"},
		{"/api/events", "retry: 3000\n\n"},
	} {
		t.Run("stream "+c.path, func(t *testing.T) {
			resp := getRaw(t, ts.URL+c.path)
			if resp.StatusCode != 200 || resp.Header.Get("Content-Encoding") != "" {
				t.Fatalf("status %d, Content-Encoding %q: a stream is let out as it is", resp.StatusCode, resp.Header.Get("Content-Encoding"))
			}
			readExactly(t, resp.Body, c.first) // the handler is still waiting at its gate
			open(c.path)
			io.Copy(io.Discard, resp.Body)
		})
	}
}

// Through the whole server: the lists, the web UI and the subtitles are compressed when they
// are asked for so; the original file, a range of anything and a 304 are not.
func TestCompressThroughTheServer(t *testing.T) {
	e := newEnv(t, "")
	seedBigLibrary(t, e.store, 30, 3, 1, 2) // a list of a few dozen rows is several KB
	ui := fstest.MapFS{
		"index.html":      {Data: []byte("<html>" + strings.Repeat("ply app ", 300) + "</html>")},
		"assets/app.js":   {Data: bytes.Repeat([]byte("console.log('app');\n"), 200)},
		"assets/logo.png": {Data: bytes.Repeat([]byte{0x89, 'P', 'N', 'G', 1, 2, 3, 4}, 400)},
	}
	e.srv = New(Options{Store: e.store, Scanner: e.scanner, UI: ui, ID: "abc123", Name: "Test Server", Version: "9.9.9"})
	ts := e.liveServer()

	for _, path := range []string{"/api/items", "/api/items?kind=movie", "/api/shows", "/api/titles", "/assets/app.js", "/"} {
		plain, err := httpClient.Do(func() *http.Request {
			r, _ := http.NewRequest("GET", ts.URL+path, nil)
			r.Header.Set("Accept-Encoding", "identity")
			return r
		}())
		if err != nil {
			t.Fatal(err)
		}
		plainBody, _ := io.ReadAll(plain.Body)
		plain.Body.Close()
		if plain.Header.Get("Content-Encoding") != "" || len(plainBody) < gzipMin {
			t.Fatalf("GET %s without gzip: Content-Encoding %q, %d bytes; the control needs a long plain answer", path, plain.Header.Get("Content-Encoding"), len(plainBody))
		}

		zipped := getRaw(t, ts.URL+path)
		zbody, _ := io.ReadAll(zipped.Body)
		if zipped.Header.Get("Content-Encoding") != "gzip" || !strings.Contains(zipped.Header.Get("Vary"), "Accept-Encoding") {
			t.Errorf("GET %s: Content-Encoding %q, Vary %q", path, zipped.Header.Get("Content-Encoding"), zipped.Header.Get("Vary"))
			continue
		}
		if !bytes.Equal(gunzip(t, zbody), plainBody) {
			t.Errorf("GET %s: the compressed body is not the same", path)
		}
		if len(zbody)*3 > len(plainBody) {
			t.Errorf("GET %s: %d bytes compressed from %d", path, len(zbody), len(plainBody))
		}
	}

	// A range of a text file, and a file that is not text: neither is compressed.
	resp := getRaw(t, ts.URL+"/assets/app.js", "Range", "bytes=0-18")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusPartialContent || resp.Header.Get("Content-Encoding") != "" || string(body) != "console.log('app');\n"[:19] {
		t.Errorf("a range of app.js: %d, Content-Encoding %q, %q", resp.StatusCode, resp.Header.Get("Content-Encoding"), body)
	}
	if logo := getRaw(t, ts.URL+"/assets/logo.png"); logo.Header.Get("Content-Encoding") != "" {
		t.Errorf("a PNG came with Content-Encoding %q", logo.Header.Get("Content-Encoding"))
	}

	// The original file: the bytes of the file, whatever the client says it accepts.
	e.srv = New(Options{Store: e.store, Scanner: e.scanner, UI: testUI, ID: "abc123", Name: "Test Server", Version: "9.9.9"})
	ts2 := e.liveServer()
	for _, hdr := range [][]string{nil, {"Range", "bytes=2-5"}} {
		resp := getRaw(t, fmt.Sprintf("%s/api/items/%d/file", ts2.URL, e.mp4), hdr...)
		body, _ := io.ReadAll(resp.Body)
		want, status := fileContent, 200
		if hdr != nil {
			want, status = "2345", 206
		}
		if resp.StatusCode != status || resp.Header.Get("Content-Encoding") != "" || string(body) != want {
			t.Errorf("file %v: %d, Content-Encoding %q, %q", hdr, resp.StatusCode, resp.Header.Get("Content-Encoding"), body)
		}
	}

	// A 304 has no body and no encoding.
	list := getRaw(t, ts2.URL+"/api/titles")
	etag := list.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on a list")
	}
	nm := getRaw(t, ts2.URL+"/api/titles", "If-None-Match", etag)
	if nm.StatusCode != http.StatusNotModified || nm.Header.Get("Content-Encoding") != "" {
		t.Errorf("If-None-Match: %d, Content-Encoding %q", nm.StatusCode, nm.Header.Get("Content-Encoding"))
	}
}

// The queue's event stream and the subtitles that are served while they are extracted are
// asked for with gzip, as a browser does, and still come as they are written, uncompressed.
func TestStreamsAreNotHeldBackWhenGzipIsAsked(t *testing.T) {
	t.Run("events", func(t *testing.T) {
		e, _ := newActivityEnv(t, nil)
		ts := httptest.NewServer(e.srv)
		t.Cleanup(ts.Close) // after the stream is closed: the cleanup of getRaw runs first
		resp := getRaw(t, ts.URL+"/api/events")
		if resp.StatusCode != 200 || resp.Header.Get("Content-Encoding") != "" || resp.Header.Get("Content-Type") != "text/event-stream" {
			t.Fatalf("%d, Content-Encoding %q, %q", resp.StatusCode, resp.Header.Get("Content-Encoding"), resp.Header.Get("Content-Type"))
		}
		readExactly(t, resp.Body, "retry: 3000\n\n")
	})
	t.Run("subtitles", func(t *testing.T) {
		e := newEnv(t, "")
		_, release := e.slowSubs(secondCue, 0)
		ts := e.liveServer()
		t.Cleanup(func() { os.WriteFile(release, nil, 0o644) })
		resp := getRaw(t, ts.URL+fmt.Sprintf("/api/items/%d/subtitles/3.vtt", e.mkv))
		if resp.StatusCode != 200 || resp.Header.Get("Content-Encoding") != "" {
			t.Fatalf("%d, Content-Encoding %q", resp.StatusCode, resp.Header.Get("Content-Encoding"))
		}
		const first = "WEBVTT\n\n00:01.000 --> 00:02.000\none\n\n"
		readExactly(t, resp.Body, first) // ffmpeg has not been let go on
		os.WriteFile(release, nil, 0o644)
		rest, err := io.ReadAll(resp.Body)
		if err != nil || string(rest) != secondCue {
			t.Errorf("rest = %q, %v", rest, err)
		}
		// Done: the same track is a file, and is compressed when it is long enough. (This one is not.)
		again := getRaw(t, ts.URL+fmt.Sprintf("/api/items/%d/subtitles/3.vtt", e.mkv))
		body, _ := io.ReadAll(again.Body)
		if again.StatusCode != 200 || string(body) != first+secondCue {
			t.Errorf("cached: %d %q", again.StatusCode, body)
		}
	})
}
