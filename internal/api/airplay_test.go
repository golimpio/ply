package api

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ply/internal/stream"
)

func TestAirPlayUsesOneReceiverIndependentSession(t *testing.T) {
	e := newEnv(t, "")
	m := e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)
	local := decode[Playback](t, e.start(e.mp4, "{}", safari))
	if local.Method != "direct" || local.SessionID != "" {
		t.Fatalf("local direct-play control: %+v", local)
	}
	rec := e.start(e.mp4, `{"profile":"airplay","startSec":37,"audioIndex":1}`, safari)
	wantStatus(t, rec, 200)
	pb := decode[Playback](t, rec)
	if pb.Method != "remux" || pb.SessionID == "" || pb.AudioIndex != 1 || pb.StartSec != 37 {
		t.Fatalf("AirPlay playback: %+v", pb)
	}
	// The receiver has its own address and user agent. It fetches the same HLS
	// session, never an additional direct play keyed to the receiver's identity.
	req := httptest.NewRequest(http.MethodGet, pb.URL, nil)
	req.RemoteAddr = "192.0.2.77:1234"
	req.Header.Set("User-Agent", "AppleTV")
	got := httptest.NewRecorder()
	e.srv.ServeHTTP(got, req)
	wantStatus(t, got, 200)
	if m.Count() != 1 || len(m.Playing()) != 1 || m.Playing()[0].ID != pb.SessionID {
		t.Fatalf("receiver duplicated the play: %+v", m.Playing())
	}
	if !strings.Contains(got.Body.String(), "#EXT-X-START:TIME-OFFSET=37.000") {
		t.Fatalf("receiver lost resume: %.300s", got.Body.String())
	}
}

func TestAirPlayTextSubtitleRendition(t *testing.T) {
	token := "s3 cret&x"
	e := newEnv(t, token)
	e.withSubs(fakeSubsScript)
	e.withStreams(fakeAirPlayFFmpeg(t), func(o *stream.Options) { o.WaitTimeout = 5 * time.Second })
	auth := map[string]string{"Authorization": "Bearer " + token}
	rec := e.post(fmt.Sprintf("/api/items/%d/playback", e.mkv), `{"profile":"airplay","subtitleKey":"3","startSec":123}`, auth)
	wantStatus(t, rec, 200)
	pb := decode[Playback](t, rec)
	root := "/api/sessions/" + pb.SessionID + "/"
	if pb.Method != "transcode" || !strings.Contains(pb.URL, "master.m3u8") {
		t.Fatalf("playback: %+v", pb)
	}
	master := e.get(pb.URL)
	wantStatus(t, master, 200)
	for _, want := range []string{
		`TYPE=SUBTITLES,GROUP-ID="subs",NAME="Portuguese"`,
		`DEFAULT=YES,AUTOSELECT=YES,LANGUAGE="pt"`,
		`CODECS="avc1.640028,mp4a.40.2",RESOLUTION=640x360`,
		`SUBTITLES="subs"`, "subtitles.m3u8?token=s3+cret%26x", "index.m3u8?token=s3+cret%26x",
	} {
		if !strings.Contains(master.Body.String(), want) {
			t.Errorf("master lacks %q: %s", want, master.Body.String())
		}
	}
	suffix := "?token=" + url.QueryEscape(token)
	playlist := e.get(root + "subtitles.m3u8" + suffix)
	wantStatus(t, playlist, 200)
	for _, want := range []string{"#EXT-X-TARGETDURATION:2400", "#EXTINF:2400.000,", "#EXT-X-ENDLIST", "subtitles.vtt" + suffix} {
		if !strings.Contains(playlist.Body.String(), want) {
			t.Errorf("subtitle playlist lacks %q: %s", want, playlist.Body.String())
		}
	}
	vtt := e.get(root + "subtitles.vtt" + suffix)
	wantStatus(t, vtt, 200)
	if vtt.Header().Get("Content-Type") != "text/vtt; charset=utf-8" {
		t.Errorf("VTT content type: %s", vtt.Header().Get("Content-Type"))
	}
	for _, want := range []string{"WEBVTT\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:180000\n\n", "00:01.000 --> 00:02.000", "hello from the fake"} {
		if !strings.Contains(vtt.Body.String(), want) {
			t.Errorf("VTT lacks %q: %s", want, vtt.Body.String())
		}
	}
	ranged := e.do("GET", root+"subtitles.vtt"+suffix, map[string]string{"Range": "bytes=3-10"}, "")
	wantStatus(t, ranged, 200)
	head := e.do("HEAD", root+"subtitles.vtt"+suffix, nil, "")
	wantStatus(t, head, 200)
	if ranged.Body.String() != vtt.Body.String() || ranged.Header().Get("Content-Length") != strconv.Itoa(vtt.Body.Len()) || head.Header().Get("Content-Length") != ranged.Header().Get("Content-Length") {
		t.Fatalf("transformed range/HEAD mismatch: range=%v HEAD=%v", ranged.Header(), head.Header())
	}
	// Positive controls above establish every endpoint before testing auth and stop.
	for _, file := range []string{"master.m3u8", "subtitles.m3u8", "subtitles.vtt"} {
		wantStatus(t, e.get(root+file), 401)
	}
	wantStatus(t, e.do("DELETE", strings.TrimSuffix(root, "/"), auth, ""), 204)
	for _, file := range []string{"master.m3u8", "subtitles.m3u8", "subtitles.vtt", "index.m3u8"} {
		wantStatus(t, e.get(root+file+suffix), 410)
	}
}

func TestAirPlaySubtitleValidationAndSidecar(t *testing.T) {
	e := newEnv(t, "")
	e.withSubs(fakeSubsScript)
	e.withStreams(fakeAirPlayFFmpeg(t), func(o *stream.Options) { o.WaitTimeout = 5 * time.Second })
	entry, err := e.store.GetEntry(t.Context(), e.mp4)
	if err != nil {
		t.Fatal(err)
	}
	for i := range entry.External {
		entry.External[i].Language = ""
	}
	if err := e.store.SetExternalSubtitles(t.Context(), e.mp4, entry.External); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/items/%d/playback", e.mp4)
	// A real sidecar is accepted before asserting that invalid choices are absent.
	rec := e.post(path, `{"profile":"airplay","subtitleKey":"ext0"}`, nil)
	wantStatus(t, rec, 200)
	pb := decode[Playback](t, rec)
	if !strings.Contains(pb.URL, "master.m3u8") {
		t.Fatalf("sidecar not represented: %+v", pb)
	}
	wantStatus(t, e.get("/api/sessions/"+pb.SessionID+"/subtitles.vtt"), 200)
	master := e.get(pb.URL)
	wantStatus(t, master, 200)
	if !strings.Contains(master.Body.String(), `NAME="Subtitles"`) || !strings.Contains(master.Body.String(), `LANGUAGE="und"`) {
		t.Fatalf("unknown subtitle language not represented: %s", master.Body.String())
	}
	for _, body := range []string{
		`{"profile":"airplay","subtitleKey":"ext999"}`,
		`{"profile":"airplay","subtitleKey":"../ext0"}`,
		`{"profile":"browser","subtitleKey":"ext0"}`,
		`{"custom":{"name":"airplay","containers":["mp4"],"videoCodecs":["h264"],"audioCodecs":["aac"],"hls":true},"subtitleKey":"ext0"}`,
		`{"custom":{"name":"AIRPLAY","containers":["mp4"],"videoCodecs":["h264"],"audioCodecs":["aac"],"hls":true}}`,
		`{"profile":"airplay","subtitleKey":"ext0","subtitleIndex":0}`,
	} {
		wantStatus(t, e.post(path, body, nil), 400)
	}
	wantStatus(t, e.post(fmt.Sprintf("/api/items/%d/playback", e.mkv), `{"profile":"airplay","subtitleKey":"2"}`, nil), 400)
	if got := hlsAttribute("Português\"\n#EXT-X-INJECT\r"); got != "Português#EXT-X-INJECT" {
		t.Errorf("unsafe HLS attribute %q", got)
	}
}

func TestAirPlayWirelessGraceIsBoundedAndReversible(t *testing.T) {
	e := newEnv(t, "")
	clk := newClock()
	m := e.withStreams(fakeFFmpeg(t, "exec sleep 60"), func(o *stream.Options) { o.Clock = clk.Now })
	rec := e.start(e.mp4, `{"profile":"airplay"}`, safari)
	wantStatus(t, rec, 200)
	pb := decode[Playback](t, rec)
	beat := "/api/sessions/" + pb.SessionID + "/heartbeat"
	wantStatus(t, e.post(beat, `{"wireless":true,"paused":false}`, nil), 204)
	clk.Advance(100 * time.Second)
	time.Sleep(60 * time.Millisecond)
	if _, ok := m.Get(pb.SessionID); !ok {
		t.Fatal("receiver was lost after browser heartbeat stopped")
	}
	wantStatus(t, e.get(pb.URL), 200) // receiver request refreshes grace without a heartbeat
	clk.Advance(100 * time.Second)
	time.Sleep(60 * time.Millisecond)
	if _, ok := m.Get(pb.SessionID); !ok {
		t.Fatal("media request collapsed the receiver's grace")
	}
	wantStatus(t, e.post(beat, `{"wireless":false,"paused":false}`, nil), 204)
	clk.Advance(31 * time.Second)
	waitFor(t, "return to local heartbeat policy", func() bool { return m.Count() == 0 })

	rec = e.start(e.mp4, `{"profile":"airplay"}`, safari)
	wantStatus(t, rec, 200)
	pb = decode[Playback](t, rec)
	if _, ok := m.Get(pb.SessionID); !ok {
		t.Fatal("new receiver session missing")
	}
	clk.Advance(181 * time.Second)
	waitFor(t, "silent receiver to expire", func() bool { return m.Count() == 0 })
}

func TestAirPlayVTTWriterPreservesStreamingAndLengths(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/vtt")
	original := "WEBVTT\n\n00:01.000 --> 00:02.000\nHello\n"
	rec.Header().Set("Content-Length", strconv.Itoa(len(original)))
	w := &hlsVTTWriter{ResponseWriter: rec}
	for _, chunk := range []string{"WE", "BVTT\n\n00:01.000 --> ", "00:02.000\nHello\n"} {
		if _, err := io.WriteString(w, chunk); err != nil {
			t.Fatal(err)
		}
		w.Flush()
	}
	want := "WEBVTT\n" + hlsTimestampMap + strings.TrimPrefix(original, "WEBVTT\n")
	if rec.Body.String() != want || !rec.Flushed || rec.Header().Get("Content-Length") != strconv.Itoa(len(want)) {
		t.Fatalf("transformed response: %q headers=%v flushed=%v", rec.Body.String(), rec.Header(), rec.Flushed)
	}
	// An HTTP error is not a WebVTT response and must be passed through.
	rec = httptest.NewRecorder()
	rec.Header().Set("Content-Type", "application/json")
	w = &hlsVTTWriter{ResponseWriter: rec}
	w.WriteHeader(422)
	io.WriteString(w, `{"error":"bad"}`)
	if rec.Code != 422 || rec.Body.String() != `{"error":"bad"}` {
		t.Fatalf("changed error response: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAirPlayStreamsSubtitlesBeforeExtractionFinishes(t *testing.T) {
	testAirPlayProgressive(t, false)
}

func TestAirPlayStopEndsSubtitleRequest(t *testing.T) {
	testAirPlayProgressive(t, true)
}

func testAirPlayProgressive(t *testing.T, stop bool) {
	t.Helper()
	e := newEnv(t, "")
	clk := newClock()
	m := e.withStreams(fakeFFmpeg(t, "exec sleep 60"), func(o *stream.Options) { o.Clock = clk.Now })
	log := e.withSubs(`
for a in "$@"; do last=$a; done
printf 'WEBVTT\n\n00:01.000 --> 00:02.000\nfirst cue\n\n' > "$last"
while [ ! -f "$0.release" ]; do sleep 0.01; done
printf '00:03.000 --> 00:04.000\nlast cue\n' >> "$last"
`)
	e.srv.subsGrace = time.Millisecond
	releaseFile := strings.TrimSuffix(log, ".log") + ".release"
	t.Cleanup(e.srv.Subs.Close)
	t.Cleanup(func() { os.WriteFile(releaseFile, nil, 0o600) })
	rec := e.post(fmt.Sprintf("/api/items/%d/playback", e.mkv), `{"profile":"airplay","subtitleKey":"3"}`, nil)
	wantStatus(t, rec, 200)
	pb := decode[Playback](t, rec)
	srv := httptest.NewServer(e.srv)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/sessions/"+pb.SessionID+"/subtitles.vtt", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	reader := bufio.NewReader(res.Body)
	var prefix strings.Builder
	for !strings.Contains(prefix.String(), "first cue") {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("first cues blocked on complete extraction: %v, prefix=%q", err, prefix.String())
		}
		prefix.WriteString(line)
	}
	if res.StatusCode != 200 || !strings.Contains(prefix.String(), "MPEGTS:180000") {
		t.Fatalf("bad progressive prefix: %d %q", res.StatusCode, prefix.String())
	}
	// The request is now known to be live: keep the video beyond the receiver
	// grace while its subtitle response is still being produced.
	clk.Advance(4 * time.Minute)
	time.Sleep(60 * time.Millisecond)
	if _, ok := m.Get(pb.SessionID); !ok {
		t.Fatal("video session idled during active subtitle response")
	}
	if stop {
		wantStatus(t, e.do("DELETE", "/api/sessions/"+pb.SessionID, nil, ""), 204)
		if _, err := io.ReadAll(reader); err != nil {
			t.Fatalf("subtitle response did not end when session stopped: %v", err)
		}
		wantStatus(t, e.get("/api/sessions/"+pb.SessionID+"/subtitles.vtt"), 410)
		return
	}
	if err := os.WriteFile(releaseFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tail, err := io.ReadAll(reader)
	if err != nil || !strings.Contains(string(tail), "last cue") {
		t.Fatalf("completion: %v %q", err, tail)
	}
}

// A small generated AVC/AAC init with dimensions unlike the source item, so
// the master test detects declarations guessed from source HEVC metadata.
func fakeAirPlayFFmpeg(t *testing.T) string {
	t.Helper()
	data, err := hex.DecodeString("000000106674797069736f6d00000000000001126d6f6f76000000927472616b0000008a6d646961000000826d696e660000007a7374626c0000007273747364000000000000000100000062617663310000000000000000000000000000000000000000000000000280016800000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000c6176634301640028000000787472616b000000706d646961000000686d696e66000000607374626c00000058737473640000000000000001000000486d70346100000000000000000000000000000000000000000000000000000000000000246573647300000000031600010004114000000000000000000000000005021210")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "init-fixture.mp4")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return fakeFFmpeg(t, fmt.Sprintf(`
last=""
for a in "$@"; do
  if [ "$last" = "-hls_fmp4_init_filename" ]; then init="$a"; fi
  last="$a"
done
cp %q "$init"
exec sleep 60
`, path))
}
