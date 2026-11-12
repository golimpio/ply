package api

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ply/internal/probe"
	"ply/internal/store"
	"ply/internal/subs"
)

// A stand-in for ffmpeg as the subtitle converter drives it: it logs its
// arguments and writes a WebVTT file to the last one.
const fakeSubsScript = `
echo "$@" >> "$0.log"
for a in "$@"; do last=$a; done
printf 'WEBVTT\n\n00:01.000 --> 00:02.000\nhello from the fake\n' > "$last"
`

// withSubs gives the env a subtitle generator driven by script and adds two
// sidecar files to the mp4 item. It returns the fake's argument log.
func (e *env) withSubs(script string) (log string) {
	e.t.Helper()
	ff := fakeFFmpeg(e.t, script)
	g, err := subs.New(subs.Options{Dir: filepath.Join(e.t.TempDir(), "subs"), FFmpeg: ff})
	if err != nil {
		e.t.Fatal(err)
	}
	e.srv.Subs = g

	en := filepath.Join(e.dir, "Movie (2020).en.srt")
	pt := filepath.Join(e.dir, "Movie (2020).pt.forced.vtt")
	for _, p := range []string{en, pt} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			e.t.Fatal(err)
		}
	}
	err = e.store.SetExternalSubtitles(context.Background(), e.mp4, []store.ExternalSubtitle{
		{Path: pt, Language: "pt", Format: "vtt", Forced: true},
		{Path: en, Language: "en", Format: "srt"},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return ff + ".log"
}

func probeSub(index int, codec, lang string, forced, def bool) probe.Stream {
	return probe.Stream{Index: index, Type: "subtitle", Codec: codec, Language: lang, Forced: forced, Default: def}
}

func (e *env) detailOf(id int64, hdr map[string]string) ItemDetail {
	e.t.Helper()
	return decode[ItemDetail](e.t, e.do("GET", fmt.Sprintf("/api/items/%d", id), hdr, ""))
}

func TestSubtitleListing(t *testing.T) {
	e := newEnv(t, "")
	e.withSubs(fakeSubsScript)

	// Embedded: a PGS (image, burn-in only: no url) and a subrip (text: url).
	d := e.detailOf(e.mkv, nil)
	if len(d.Subtitles) != 2 {
		t.Fatalf("subtitles = %+v", d.Subtitles)
	}
	pgs, srt := d.Subtitles[0], d.Subtitles[1]
	if pgs.Key != "2" || !pgs.Image || pgs.URL != "" || pgs.Format != "hdmv_pgs_subtitle" {
		t.Errorf("pgs = %+v", pgs)
	}
	if want := fmt.Sprintf("/api/items/%d/subtitles/3.vtt", e.mkv); srt.Key != "3" || srt.Image || srt.URL != want {
		t.Errorf("srt = %+v, want url %s", srt, want)
	}

	// Sidecars come after the embedded streams, ordered by path: ext<N>.
	d = e.detailOf(e.mp4, nil)
	if len(d.Subtitles) != 2 {
		t.Fatalf("mp4 subtitles = %+v", d.Subtitles)
	}
	en, pt := d.Subtitles[0], d.Subtitles[1]
	if en.Key != "ext0" || en.Language != "en" || en.Format != "srt" || en.Forced || en.Image ||
		en.URL != fmt.Sprintf("/api/items/%d/subtitles/ext0.vtt", e.mp4) {
		t.Errorf("en = %+v", en)
	}
	if pt.Key != "ext1" || pt.Language != "pt" || pt.Format != "vtt" || !pt.Forced {
		t.Errorf("pt = %+v", pt)
	}
}

func TestSubtitleFlagsFromStreams(t *testing.T) {
	e := newEnv(t, "")
	id, err := e.store.UpsertItem(context.Background(), &store.Item{Kind: store.KindMovie, Title: "Flags",
		Path: filepath.Join(e.dir, "Flags (2020).mkv"), Root: e.dir, Size: 1, MTime: 1, Container: "mkv", DurationSec: 100},
		append(streamsH264(),
			probeSub(2, "subrip", "eng", false, true),
			probeSub(3, "ass", "eng", true, false)))
	if err != nil {
		t.Fatal(err)
	}
	d := e.detailOf(id, nil)
	if len(d.Subtitles) != 2 || !d.Subtitles[0].Default || d.Subtitles[0].Forced || d.Subtitles[1].Default || !d.Subtitles[1].Forced {
		t.Errorf("subtitles = %+v", d.Subtitles)
	}
	if d.Subtitles[0].URL != "" {
		t.Errorf("url without a subtitle generator: %q", d.Subtitles[0].URL)
	}
}

func TestSubtitleURLsCarryTheToken(t *testing.T) {
	e := newEnv(t, "s3cret")
	e.withSubs(fakeSubsScript)
	auth := map[string]string{"Authorization": "Bearer s3cret"}

	d := e.detailOf(e.mkv, auth)
	want := fmt.Sprintf("/api/items/%d/subtitles/3.vtt?token=s3cret", e.mkv)
	if got := d.Subtitles[1].URL; got != want {
		t.Errorf("embedded url = %q, want %q", got, want)
	}
	d = e.detailOf(e.mp4, auth)
	for _, s := range d.Subtitles {
		if !strings.HasSuffix(s.URL, "?token=s3cret") {
			t.Errorf("sidecar %s url = %q has no token", s.Key, s.URL)
		}
	}

	// The url works as it is (a <track> cannot send headers), and not without the token.
	rec := e.get(want)
	wantStatus(t, rec, 200)
	if !strings.HasPrefix(rec.Body.String(), "WEBVTT") {
		t.Errorf("body = %q", rec.Body.String())
	}
	wantJSONError(t, e.get(strings.TrimSuffix(want, "?token=s3cret")), 401)
}

func TestSubtitleServing(t *testing.T) {
	e := newEnv(t, "")
	log := e.withSubs(fakeSubsScript)

	rec := e.get(fmt.Sprintf("/api/items/%d/subtitles/3.vtt", e.mkv))
	wantStatus(t, rec, 200)
	if ct := rec.Header().Get("Content-Type"); ct != "text/vtt; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if !strings.HasPrefix(rec.Body.String(), "WEBVTT") || !strings.Contains(rec.Body.String(), "-->") {
		t.Errorf("body = %q", rec.Body.String())
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("no CORS header on the subtitle response")
	}
	// ffmpeg was asked for stream 3 of the item's own file.
	args, _ := os.ReadFile(log)
	if !strings.Contains(string(args), "-map 0:3") || !strings.Contains(string(args), "Cool Show S01E02 Pilot.mkv") {
		t.Errorf("ffmpeg args = %q", args)
	}

	// Cached: a second request does not run ffmpeg again.
	wantStatus(t, e.get(fmt.Sprintf("/api/items/%d/subtitles/3.vtt", e.mkv)), 200)
	if lines := strings.Count(string(mustRead(t, log)), "\n"); lines != 1 {
		t.Errorf("ffmpeg ran %d times, want 1", lines)
	}

	// A sidecar is converted from its own file, without -map.
	rec = e.get(fmt.Sprintf("/api/items/%d/subtitles/ext0.vtt", e.mp4))
	wantStatus(t, rec, 200)
	args = mustRead(t, log)
	last := strings.Split(strings.TrimSpace(string(args)), "\n")[1]
	if !strings.Contains(last, "Movie (2020).en.srt") || strings.Contains(last, "-map") {
		t.Errorf("sidecar ffmpeg args = %q", last)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestImageSubtitlesAreNotServed(t *testing.T) {
	e := newEnv(t, "")
	log := e.withSubs(fakeSubsScript)
	// Positive control: the text track of the same item is served.
	wantStatus(t, e.get(fmt.Sprintf("/api/items/%d/subtitles/3.vtt", e.mkv)), 200)

	wantJSONError(t, e.get(fmt.Sprintf("/api/items/%d/subtitles/2.vtt", e.mkv)), 404) // hdmv_pgs_subtitle
	wantJSONError(t, e.get(fmt.Sprintf("/api/items/%d/subtitles/3.vtt", e.multi)), 404)
	// Only the one run for the text track: the image request never reached ffmpeg.
	if n := strings.Count(string(mustRead(t, log)), "\n"); n != 1 {
		t.Errorf("ffmpeg ran %d times, want 1", n)
	}
}

func TestSubtitleNotFound(t *testing.T) {
	e := newEnv(t, "")
	e.withSubs(fakeSubsScript)
	wantStatus(t, e.get(fmt.Sprintf("/api/items/%d/subtitles/3.vtt", e.mkv)), 200) // positive control

	for _, target := range []string{
		fmt.Sprintf("/api/items/%d/subtitles/99.vtt", e.mkv),      // no such stream
		fmt.Sprintf("/api/items/%d/subtitles/0.vtt", e.mkv),       // a video stream
		fmt.Sprintf("/api/items/%d/subtitles/1.vtt", e.mkv),       // an audio stream
		fmt.Sprintf("/api/items/%d/subtitles/03.vtt", e.mkv),      // not canonical
		fmt.Sprintf("/api/items/%d/subtitles/-3.vtt", e.mkv),      // negative
		fmt.Sprintf("/api/items/%d/subtitles/abc.vtt", e.mkv),     // junk
		fmt.Sprintf("/api/items/%d/subtitles/3", e.mkv),           // no extension
		fmt.Sprintf("/api/items/%d/subtitles/3.srt", e.mkv),       // wrong extension
		fmt.Sprintf("/api/items/%d/subtitles/ext0.vtt", e.mkv),    // this item has no sidecars
		fmt.Sprintf("/api/items/%d/subtitles/ext2.vtt", e.mp4),    // only ext0 and ext1 exist
		fmt.Sprintf("/api/items/%d/subtitles/ext00.vtt", e.mp4),   // not canonical
		fmt.Sprintf("/api/items/%d/subtitles/ext-1.vtt", e.mp4),   // negative
		fmt.Sprintf("/api/items/%d/subtitles/ext.vtt", e.mp4),     // no number
		"/api/items/99999/subtitles/3.vtt",                        // no such item
		"/api/items/nope/subtitles/3.vtt",                         // bad id
		fmt.Sprintf("/api/items/%d/subtitles/..%%2f3.vtt", e.mkv), // traversal attempt
	} {
		wantJSONError(t, e.get(target), 404)
	}

	// A sidecar that vanished from disk is gone, not an error.
	if err := os.Remove(filepath.Join(e.dir, "Movie (2020).en.srt")); err != nil {
		t.Fatal(err)
	}
	wantJSONError(t, e.get(fmt.Sprintf("/api/items/%d/subtitles/ext0.vtt", e.mp4)), 404)
}

func TestSubtitlesDisabled(t *testing.T) {
	e := newEnv(t, "")
	wantJSONError(t, e.get(fmt.Sprintf("/api/items/%d/subtitles/3.vtt", e.mkv)), 404)
	for _, s := range e.detailOf(e.mkv, nil).Subtitles {
		if s.URL != "" {
			t.Errorf("subtitle %s has url %q with no generator", s.Key, s.URL)
		}
	}
}

func TestSubtitleConversionFailure(t *testing.T) {
	e := newEnv(t, "")
	e.withSubs("echo 'Invalid data found' >&2; exit 1")
	wantJSONError(t, e.get(fmt.Sprintf("/api/items/%d/subtitles/3.vtt", e.mkv)), 422)
}

func TestSubtitleDelete(t *testing.T) {
	e := newEnv(t, "")
	e.withSubs(fakeSubsScript)

	// Positive control: verify ext0 exists initially
	d := e.detailOf(e.mp4, nil)
	if len(d.Subtitles) != 2 || d.Subtitles[0].Key != "ext0" {
		t.Fatalf("positive control failed: expected ext0 to exist, got: %+v", d.Subtitles)
	}

	// Embedded subtitle stream cannot be deleted -> 400
	resp := e.do("DELETE", fmt.Sprintf("/api/items/%d/subtitles/3", e.mkv), nil, "")
	if resp.Code != 400 {
		t.Fatalf("delete embedded subtitle = %d, want 400", resp.Code)
	}

	// Delete sidecar subtitle ext0 -> 204
	resp = e.do("DELETE", fmt.Sprintf("/api/items/%d/subtitles/ext0", e.mp4), nil, "")
	if resp.Code != 204 {
		t.Fatalf("delete ext0 = %d, want 204", resp.Code)
	}

	// Verify ext0 is gone and only 1 remains
	dAfter := e.detailOf(e.mp4, nil)
	if len(dAfter.Subtitles) != 1 {
		t.Fatalf("after delete, subtitles count = %d, want 1; %+v", len(dAfter.Subtitles), dAfter.Subtitles)
	}
}

func TestSubtitleUpload(t *testing.T) {
	e := newEnv(t, "")

	// Create multipart upload request
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "test.es.srt")
	if err != nil {
		t.Fatal(err)
	}
	part.Write([]byte("1\n00:00:01,000 --> 00:00:04,000\nHola mundo\n"))
	_ = writer.WriteField("language", "es")
	_ = writer.WriteField("forced", "false")
	writer.Close()

	req := httptest.NewRequest("POST", fmt.Sprintf("/api/items/%d/subtitles", e.mp4), body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	e.srv.ServeHTTP(w, req)

	if w.Code != 201 {
		t.Fatalf("upload status = %d, want 201: %s", w.Code, w.Body.String())
	}

	// Verify subtitle exists on item
	d := e.detailOf(e.mp4, nil)
	found := false
	for _, s := range d.Subtitles {
		if s.Language == "es" && s.Format == "srt" && s.External {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("uploaded subtitle not found on item detail: %+v", d.Subtitles)
	}
}

func TestSubtitleSearchUnconfigured(t *testing.T) {
	e := newEnv(t, "")
	resp := e.get(fmt.Sprintf("/api/items/%d/subtitles/search", e.mp4))
	if resp.Code != 200 {
		t.Fatalf("search status = %d, want 200", resp.Code)
	}
	var res SubtitleSearchResponse
	res = decode[SubtitleSearchResponse](t, resp)
	if res.Configured {
		t.Fatalf("expected configured = false, got true")
	}
}
