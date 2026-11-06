package api

import (
	"compress/gzip"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// gzipMin is the smallest answer worth compressing: below it the gzip header and trailer, the
// time to set a compressor up and the client's to open one cost more than the bytes they save.
const gzipMin = 1024

// gzipPool keeps the compressors: one holds a few hundred kilobytes of state, and a page of
// the library is asked for many times. BestSpeed because a person is waiting for the answer,
// and the server may be playing a film from the same machine: JSON of the library shrinks ten
// times at this level too, and the difference to the default is under a tenth of the size.
var gzipPool = sync.Pool{New: func() any {
	zw, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
	return zw
}}

// compress gzips the answers of the API that are text and over gzipMin bytes, for a client that
// accepts it (docs/DESIGN.md, "Compression and list caching"). It is meant for JSON, and for the pages of the web
// UI and the subtitles, which are text too. It never touches:
//
//   - the media: the original file, the artwork, the images, a session's playlist and
//     segments (neverCompressed). Those requests are not wrapped at all, so that http.ServeContent
//     still sees the server's own writer and can sendfile;
//   - the event stream of the queue (/api/events), and any answer that flushes before it has
//     gzipMin bytes to compress: it is a stream, and it is let out as it comes (the subtitles
//     that are served while ffmpeg writes them);
//   - a request for a range, and every answer that is not a plain 200;
//   - what is not text: the content type of the answer decides (compressible), so that a poster
//     or a segment that came through by mistake would not be compressed either.
//
// An answer that is compressed has no Content-Length and no Accept-Ranges, and its ETag, if it has
// one, is weak (it is not the bytes of the original any more). Every answer that could have been
// compressed says Vary: Accept-Encoding, whether it was or not.
func compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) || neverCompressed(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		if r.Method != http.MethodGet || r.Header.Get("Range") != "" || !acceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		cw := &compressWriter{ResponseWriter: w}
		defer cw.finish()
		next.ServeHTTP(cw, r)
	})
}

// neverCompressed is true for the paths whose answers are media or a stream.
func neverCompressed(p string) bool {
	switch {
	case p == "/api/events", strings.HasPrefix(p, "/api/sessions/"), strings.HasPrefix(p, "/api/images/"):
		return true
	}
	if rest, ok := strings.CutPrefix(p, "/api/items/"); ok {
		_, sub, _ := strings.Cut(rest, "/")
		return sub == "file" || strings.HasPrefix(sub, "art/")
	}
	return false
}

// acceptsGzip reads Accept-Encoding: gzip is there, and its q is not 0.
func acceptsGzip(r *http.Request) bool {
	for _, v := range r.Header.Values("Accept-Encoding") {
		for _, part := range strings.Split(v, ",") {
			name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
			if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
				continue
			}
			if q, ok := strings.CutPrefix(strings.ReplaceAll(strings.ToLower(params), " ", ""), "q="); ok {
				if f, err := strconv.ParseFloat(q, 64); err == nil && f == 0 {
					return false
				}
			}
			return true
		}
	}
	return false
}

// compressible says whether a Content-Type is text that gzip pays for: JSON, HTML, JavaScript, CSS,
// SVG and WebVTT.
func compressible(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	switch mt {
	case "application/json", "text/html", "text/css", "text/javascript", "application/javascript", "image/svg+xml", "text/vtt":
		return true
	}
	return strings.HasSuffix(mt, "+json")
}

// compressWriter holds an answer back until it knows whether to compress it: from the headers
// when it can (the status, the type, a Content-Length), otherwise when gzipMin bytes have been
// written. An answer that ends or flushes before that is sent as it is.
type compressWriter struct {
	http.ResponseWriter
	status int          // what the handler asked for; 0 until it has asked
	buf    []byte       // written before the answer was decided
	zw     *gzip.Writer // set once the answer is being compressed
	plain  bool         // set once it is being sent as it is
}

func (w *compressWriter) decided() bool { return w.zw != nil || w.plain }

func (w *compressWriter) WriteHeader(code int) {
	if code >= 100 && code < 200 { // interim: not the answer
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.status != 0 {
		return // the first status stands, as it does for net/http
	}
	w.status = code
	h := w.Header()
	if code != http.StatusOK || h.Get("Content-Encoding") != "" || h.Get("Content-Range") != "" || !compressible(h.Get("Content-Type")) {
		w.startPlain()
		return
	}
	if cl := h.Get("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err != nil || n < gzipMin {
			w.startPlain()
			return
		}
		w.startGzip()
	}
	// No length: it is decided by what is written.
}

func (w *compressWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	switch {
	case w.zw != nil:
		return w.zw.Write(p)
	case w.plain:
		return w.ResponseWriter.Write(p)
	case len(w.buf)+len(p) < gzipMin:
		w.buf = append(w.buf, p...)
		return len(p), nil
	}
	w.startGzip()
	return w.zw.Write(p)
}

// Flush lets out what has been written: a stream is not held back to be compressed, so an
// answer that is not decided yet goes out as it is; one that is being compressed is flushed
// through the compressor.
func (w *compressWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if !w.decided() {
		w.startPlain()
	}
	if w.zw != nil {
		w.zw.Flush()
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// ReadFrom is what io.Copy and http.ServeContent use: an answer that is sent as it is goes to the
// writer underneath, which may sendfile it (the media never get here, see compress, but a wrapper
// must not hide io.ReaderFrom); one that is compressed, or not yet decided, is copied through Write.
func (w *compressWriter) ReadFrom(r io.Reader) (int64, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok && w.plain {
		return rf.ReadFrom(r)
	}
	return io.Copy(struct{ io.Writer }{w}, r)
}

// Unwrap lets http.ResponseController reach the writer underneath (deadlines, hijacking).
func (w *compressWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *compressWriter) startPlain() {
	w.plain = true
	w.ResponseWriter.WriteHeader(w.status)
	if len(w.buf) > 0 {
		w.ResponseWriter.Write(w.buf)
		w.buf = nil
	}
}

func (w *compressWriter) startGzip() {
	h := w.Header()
	h.Del("Content-Length")
	h.Del("Accept-Ranges")
	h.Set("Content-Encoding", "gzip")
	if et := h.Get("ETag"); et != "" && !strings.HasPrefix(et, "W/") {
		h.Set("ETag", "W/"+et)
	}
	w.ResponseWriter.WriteHeader(w.status)
	w.zw = gzipPool.Get().(*gzip.Writer)
	w.zw.Reset(w.ResponseWriter)
	if len(w.buf) > 0 {
		w.zw.Write(w.buf)
		w.buf = nil
	}
}

// finish ends the answer: what is held back goes out as it is, a compressor is closed and given back.
func (w *compressWriter) finish() {
	switch {
	case w.zw != nil:
		w.zw.Close()
		w.zw.Reset(io.Discard)
		gzipPool.Put(w.zw)
		w.zw = nil
	case !w.plain && w.status != 0:
		w.startPlain()
	}
}
