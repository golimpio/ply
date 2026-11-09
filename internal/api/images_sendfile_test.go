package api

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// readerFromWriter stands for the connection: it takes a file through ReadFrom, which
// is what lets net/http use sendfile, and says what it was handed.
type readerFromWriter struct {
	h           http.Header
	code        int
	n           int64
	viaReadFrom bool
	fromFile    bool // the reader was a file (cut to length), not a buffer
}

func (w *readerFromWriter) Header() http.Header {
	if w.h == nil {
		w.h = http.Header{}
	}
	return w.h
}
func (w *readerFromWriter) WriteHeader(c int) { w.code = c }
func (w *readerFromWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}
func (w *readerFromWriter) ReadFrom(r io.Reader) (int64, error) {
	w.viaReadFrom = true
	if lr, ok := r.(*io.LimitedReader); ok {
		_, w.fromFile = lr.R.(*os.File)
	}
	return io.Copy(struct{ io.Writer }{w}, r)
}

func serveToConn(h http.Handler, target, token string) *readerFromWriter {
	w := &readerFromWriter{}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	h.ServeHTTP(w, req)
	return w
}

// Images served from the cache go to the connection as the file itself: through every
// layer of the server the writer is the connection's (nothing wraps it), and the body
// is the open file, never read into memory first.
func TestCachedImagesAreHandedToTheConnectionAsFiles(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	wantStatus(t, m.get("/api/images/tmdb/w342/p603.jpg"), 200) // downloads it: now it is cached
	w := serveToConn(m.srv, "/api/images/tmdb/w342/p603.jpg", apiToken)
	if w.code != 200 || w.n != int64(len(jpegBytes)) || !w.viaReadFrom || !w.fromFile {
		t.Errorf("TMDB image: status %d, %d bytes, via ReadFrom %v, from a file %v", w.code, w.n, w.viaReadFrom, w.fromFile)
	}

	e := newEnv(t, "")
	g := e.withArt(fakeArtScript)
	t.Cleanup(g.Close)
	target := fmt.Sprintf("/api/items/%d/art/thumb", e.mp4)
	wantStatus(t, e.get(target), 200)
	w = serveToConn(e.srv, target, "")
	if w.code != 200 || w.n == 0 || !w.viaReadFrom || !w.fromFile {
		t.Errorf("artwork: status %d, %d bytes, via ReadFrom %v, from a file %v", w.code, w.n, w.viaReadFrom, w.fromFile)
	}
}
