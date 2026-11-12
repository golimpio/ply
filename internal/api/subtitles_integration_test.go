//go:build integration

package api

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"maps"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ply/internal/hwaccel"
	"ply/internal/library"
	"ply/internal/probe"
	"ply/internal/store"
	"ply/internal/stream"
	"ply/internal/subs"
)

const (
	embeddedSRT = "1\n00:00:01,000 --> 00:00:04,000\nHello <i>world</i>\n\n2\n00:00:05,000 --> 00:00:08,000\nSecond cue\n"
	englishSRT  = "1\n00:00:02,000 --> 00:00:03,000\nEnglish sidecar\n"
)

// writeVobSub writes a VobSub pair (x.idx + x.sub) holding one real bitmap
// subtitle: a white 100x20 box at (110,140) of a 320x180 picture, shown from
// 2.0 s for about 2 s. ffmpeg cannot author bitmap subtitles from text, so the
// SPU packet (RLE data and control sequence) is built here by hand. It returns
// the .idx path.
func writeVobSub(t *testing.T, dir string) string {
	t.Helper()
	const w, h, x1, y1 = 100, 20, 110, 140
	x2, y2 := x1+w-1, y1+h-1
	row := []byte{0x00, 0x01} // "colour 1 to the end of the line"
	var field []byte
	for range h / 2 {
		field = append(field, row...)
	}
	rle := append(append([]byte{}, field...), field...)

	ctl := 4 + len(rle)
	next := ctl + 24
	dcsq0 := binary.BigEndian.AppendUint16(nil, 0)
	dcsq0 = binary.BigEndian.AppendUint16(dcsq0, uint16(next))
	dcsq0 = append(dcsq0,
		0x03, 0x00, 0x10, // colours: pattern = palette entry 1
		0x04, 0x00, 0xF0, // contrast: pattern opaque
		0x05, byte(x1>>4), byte(x1&0xF)<<4|byte(x2>>8), byte(x2), byte(y1>>4), byte(y1&0xF)<<4|byte(y2>>8), byte(y2),
		0x06)
	dcsq0 = binary.BigEndian.AppendUint16(dcsq0, 4)
	dcsq0 = binary.BigEndian.AppendUint16(dcsq0, uint16(4+len(field)))
	dcsq0 = append(dcsq0, 0x01, 0xFF) // start display, end of commands
	if len(dcsq0) != 24 {
		t.Fatalf("control sequence is %d bytes", len(dcsq0))
	}
	dcsq1 := binary.BigEndian.AppendUint16(nil, 176) // stop after 176 * 1024/90000 s = 2 s
	dcsq1 = binary.BigEndian.AppendUint16(dcsq1, uint16(next))
	dcsq1 = append(dcsq1, 0x02, 0xFF)

	body := append(append(append([]byte{}, rle...), dcsq0...), dcsq1...)
	spu := binary.BigEndian.AppendUint16(nil, uint16(4+len(body)))
	spu = binary.BigEndian.AppendUint16(spu, uint16(ctl))
	spu = append(spu, body...)

	pts := uint64(2 * 90000)
	ptsBytes := []byte{byte(0x21 | (pts>>29)&0x0E), byte(pts >> 22), byte(0x01 | (pts>>14)&0xFE), byte(pts >> 7), byte(0x01 | (pts<<1)&0xFE)}
	payload := append([]byte{0x20}, spu...) // sub-picture stream 0
	pack := []byte{0, 0, 1, 0xBA, 0x44, 0x00, 0x04, 0x00, 0x04, 0x01, 0x89, 0xC3, 0xF8}
	pes := append([]byte{0, 0, 1, 0xBD}, binary.BigEndian.AppendUint16(nil, uint16(3+5+len(payload)))...)
	pes = append(append(append(pes, 0x81, 0x80, 0x05), ptsBytes...), payload...)

	if err := os.WriteFile(filepath.Join(dir, "x.sub"), append(pack, pes...), 0o644); err != nil {
		t.Fatal(err)
	}
	palette := "000000, ffffff" + strings.Repeat(", 000000", 14)
	idx := "# VobSub index file, v7 (do not modify this line!)\nsize: 320x180\npalette: " + palette +
		"\nlangidx: 0\nid: en, index: 0\ntimestamp: 00:00:02:000, filepos: 000000000\n"
	p := filepath.Join(dir, "x.idx")
	if err := os.WriteFile(p, []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func ffmpegOK(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("ffmpeg", append([]string{"-nostdin", "-v", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

// makeSubtitleLibrary writes two 12 s clips: a movie with an embedded srt and
// two sidecars, and one with a bitmap (dvd_subtitle) track.
func makeSubtitleLibrary(t *testing.T) (root string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "media root")
	movies := filepath.Join(root, "Movies")
	if err := os.MkdirAll(movies, 0o755); err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	srt := filepath.Join(scratch, "embedded.srt")
	if err := os.WriteFile(srt, []byte(embeddedSRT), 0o644); err != nil {
		t.Fatal(err)
	}
	video := []string{"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000"}
	enc := []string{"-t", "12", "-c:v", "libx264", "-preset", "ultrafast", "-g", "48", "-keyint_min", "48", "-sc_threshold", "0",
		"-pix_fmt", "yuv420p", "-c:a", "aac"}

	ffmpegOK(t, append(append(append(video, "-i", srt, "-map", "0:v", "-map", "1:a", "-map", "2:s"), enc...),
		"-c:s", "srt", "-metadata:s:s:0", "language=eng", "-disposition:s:0", "default",
		filepath.Join(movies, "Sub Movie (2021).mkv"))...)
	if err := os.WriteFile(filepath.Join(movies, "Sub Movie (2021).en.srt"), []byte(englishSRT), 0o644); err != nil {
		t.Fatal(err)
	}
	// Windows-1252 text: "caf\xe9" must come out as UTF-8.
	latin := "1\n00:00:01,000 --> 00:00:02,000\ncaf\xe9\n"
	if err := os.WriteFile(filepath.Join(movies, "Sub Movie (2021).pt.forced.srt"), []byte(latin), 0o644); err != nil {
		t.Fatal(err)
	}

	idx := writeVobSub(t, scratch)
	ffmpegOK(t, append(append(append(video, "-i", idx, "-map", "0:v", "-map", "1:a", "-map", "2:s"), enc...),
		"-c:s", "copy", "-metadata:s:s:0", "language=eng",
		filepath.Join(movies, "Image Sub (2022).mkv"))...)
	return root
}

// newSubsE2E scans root and serves it with a subtitle generator and the given encoder.
func newSubsE2E(t *testing.T, hw *hwaccel.Detector, root string) (e *e2e, subsDir string, ids map[string]int64) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "data dir", "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sc := library.NewScanner(t.Context(), st, probe.FFprobe{}, []string{root}, nil)
	if err := sc.Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	mgr, err := stream.NewManager(stream.Options{TempRoot: t.TempDir(), Cache: st})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	subsDir = filepath.Join(t.TempDir(), "data dir", "subs")
	gen, err := subs.New(subs.Options{Dir: subsDir})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Options{Store: st, Scanner: sc, ID: "it", Name: "it", Version: "test", Streams: mgr, HW: hw, Subs: gen})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	entries, err := st.ListItems(t.Context(), store.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	ids = map[string]int64{}
	for _, en := range entries {
		ids[filepath.Base(en.Path)] = en.ID
	}
	return &e2e{t: t, ts: ts, store: st, mgr: mgr, root: root, client: ts.Client()}, subsDir, ids
}

func TestSubtitlesEndToEnd(t *testing.T) {
	root := makeSubtitleLibrary(t)
	e, subsDir, ids := newSubsE2E(t, nil, root)
	movie, image := ids["Sub Movie (2021).mkv"], ids["Image Sub (2022).mkv"]
	if movie == 0 || image == 0 {
		t.Fatalf("not scanned: %v", ids)
	}

	getDetail := func(id int64) ItemDetail {
		resp, b := e.do("GET", fmt.Sprintf("/api/items/%d", id), "", nil)
		if resp.StatusCode != 200 {
			t.Fatalf("detail = %d: %s", resp.StatusCode, b)
		}
		var d ItemDetail
		if err := json.Unmarshal(b, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}

	// Listing: the embedded srt, then the sidecars ordered by path ("...en.srt" before "...pt.forced.srt").
	d := getDetail(movie)
	if len(d.Subtitles) != 3 {
		t.Fatalf("subtitles = %+v", d.Subtitles)
	}
	emb, en, pt := d.Subtitles[0], d.Subtitles[1], d.Subtitles[2]
	if emb.Key != "2" || emb.Format != "subrip" || emb.Language != "eng" || !emb.Default || emb.Image || emb.URL == "" {
		t.Errorf("embedded = %+v", emb)
	}
	if en.Key != "ext0" || en.Language != "en" || en.Format != "srt" || en.Forced || en.URL == "" {
		t.Errorf("en = %+v", en)
	}
	if pt.Key != "ext1" || pt.Language != "pt" || !pt.Forced || pt.URL == "" {
		t.Errorf("pt = %+v", pt)
	}

	fetch := func(url string) string {
		t.Helper()
		resp, b := e.do("GET", url, "", nil)
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s = %d: %s", url, resp.StatusCode, b)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "text/vtt; charset=utf-8" {
			t.Errorf("GET %s Content-Type = %q", url, ct)
		}
		return string(b)
	}

	// Embedded extraction: a WebVTT with both cues, tags kept, times on the file's timeline.
	vtt := fetch(emb.URL)
	for _, want := range []string{"WEBVTT", "00:01.000 --> 00:04.000", "Hello <i>world</i>", "00:05.000 --> 00:08.000", "Second cue"} {
		if !strings.Contains(vtt, want) {
			t.Errorf("embedded vtt lacks %q:\n%s", want, vtt)
		}
	}
	if got := fetch(en.URL); !strings.Contains(got, "English sidecar") || !strings.HasPrefix(got, "WEBVTT") {
		t.Errorf("sidecar vtt:\n%s", got)
	}
	if got := fetch(pt.URL); !strings.Contains(got, "café") {
		t.Errorf("a Windows-1252 sidecar did not come out as UTF-8:\n%q", got)
	}

	// Cached under <id>-<key>-<mtime>.vtt.
	var cached []string
	entries, _ := os.ReadDir(subsDir)
	for _, ent := range entries {
		cached = append(cached, ent.Name())
	}
	item, err := e.store.GetEntry(t.Context(), movie)
	if err != nil {
		t.Fatal(err)
	}
	wantName := fmt.Sprintf("%d-2-%d.vtt", movie, item.MTime)
	found := false
	for _, n := range cached {
		found = found || n == wantName
	}
	if !found || len(cached) != 3 {
		t.Errorf("cache = %v, want %s plus two sidecar files", cached, wantName)
	}
	// A second request is served from the cache: the file is not rewritten.
	before, _ := os.Stat(filepath.Join(subsDir, wantName))
	fetch(emb.URL)
	if after, _ := os.Stat(filepath.Join(subsDir, wantName)); !after.ModTime().Equal(before.ModTime()) {
		t.Error("the cached extraction was redone")
	}

	// The bitmap track is listed (selectable for burn-in) but has no url and is a 404 here.
	di := getDetail(image)
	if len(di.Subtitles) != 1 || !di.Subtitles[0].Image || di.Subtitles[0].Format != "dvd_subtitle" || di.Subtitles[0].URL != "" {
		t.Fatalf("image item subtitles = %+v", di.Subtitles)
	}
	resp, b := e.do("GET", fmt.Sprintf("/api/items/%d/subtitles/%s.vtt", image, di.Subtitles[0].Key), "", nil)
	if resp.StatusCode != 404 {
		t.Errorf("image subtitle = %d: %s", resp.StatusCode, b)
	}
}

// brightFrames decodes the first segment of a session and counts the frames
// whose crop of the box region (x 120-199, y 145-154) is white.
func brightFrames(t *testing.T, e *e2e, pb Playback) (white, total int) {
	t.Helper()
	return brightFramesIn(t, e, pb, 120, 145, 80, 10)
}

// brightFramesIn counts the frames of the first segment whose w x h crop at
// (x, y) is white.
func brightFramesIn(t *testing.T, e *e2e, pb Playback, x, y, w, h int) (white, total int) {
	t.Helper()
	_, pl := e.do("GET", pb.URL, "", nil)
	initURI, segs := parsePlaylist(t, string(pl))
	base := "/api/sessions/" + pb.SessionID + "/"
	resp, initBytes := e.do("GET", base+initURI, "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("init = %d", resp.StatusCode)
	}
	resp, seg := e.do("GET", base+segs[0].uri, "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("segment 0 = %d: %s", resp.StatusCode, seg)
	}
	f := filepath.Join(t.TempDir(), "s.mp4")
	if err := os.WriteFile(f, append(append([]byte{}, initBytes...), seg...), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-i", f, "-vf",
		fmt.Sprintf("crop=%d:%d:%d:%d,scale=1:1:flags=area,format=rgb24", w, h, x, y), "-f", "rawvideo", "-").Output()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	for i := 0; i+3 <= len(out); i += 3 {
		total++
		if out[i] > 235 && out[i+1] > 235 && out[i+2] > 235 {
			white++
		}
	}
	return white, total
}

// Selecting a bitmap subtitle burns it into a transcode; without selecting it
// the picture stays clean. The subtitle is a real dvd_subtitle stream.
func TestImageSubtitleIsBurnedIn(t *testing.T) {
	root := makeSubtitleLibrary(t)
	detected := hwaccel.Detect(t.Context(), hwaccel.Options{})
	encoders := []struct {
		name string
		info hwaccel.Info
	}{{"libx264", detected.SoftwareOnly()}}
	if detected.Encoder.Hardware() {
		encoders = append(encoders, struct {
			name string
			info hwaccel.Info
		}{detected.Encoder.Name, detected})
	}
	const profile = `"custom": {"name": "mp4", "containers": ["mp4"], "videoCodecs": ["h264"], "audioCodecs": ["aac"]}`

	for _, enc := range encoders {
		t.Run(enc.name, func(t *testing.T) {
			e, _, ids := newSubsE2E(t, hwaccel.Static(enc.info), root)
			id := ids["Image Sub (2022).mkv"]
			post := func(body string) Playback {
				t.Helper()
				resp, b := e.do("POST", fmt.Sprintf("/api/items/%d/playback", id), body, nil)
				if resp.StatusCode != 200 {
					t.Fatalf("POST playback = %d: %s", resp.StatusCode, b)
				}
				var pb Playback
				if err := json.Unmarshal(b, &pb); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { e.do("DELETE", "/api/sessions/"+pb.SessionID, "", nil) })
				return pb
			}

			// Control: the mkv only needs a remux for this profile, and the picture is clean.
			plain := post(`{` + profile + `}`)
			if plain.Method != "remux" {
				t.Fatalf("without a subtitle: %+v", plain)
			}
			if white, total := brightFrames(t, e, plain); total == 0 || white != 0 {
				t.Fatalf("clean picture has %d/%d white frames", white, total)
			}

			burn := post(`{` + profile + `, "subtitleIndex": 2}`)
			if burn.Method != "transcode" || burn.VideoAction != "transcode" {
				t.Fatalf("with the bitmap subtitle: %+v", burn)
			}
			t.Logf("reasons: %v", burn.Reasons)
			white, total := brightFrames(t, e, burn)
			// 2 s of 24 fps video carries the box.
			if white < 36 || white > 60 {
				t.Errorf("%d of %d frames show the burned-in box, want about 48", white, total)
			}
		})
	}
	if p := ffmpegRunning(t, root); len(p) != 0 {
		t.Errorf("ffmpeg children left: %v", p)
	}
}

// A bitmap subtitle is drawn for its own canvas (the .idx says 320x180 here),
// which is often not the size of the video. The burn-in has to scale it to the
// video; with the canvas left as it is, the box lands at the canvas's
// coordinates, in the top-left part of the picture. (The other burn-in tests
// use a canvas the size of the video and cannot tell the two apart.)
func TestImageSubtitleIsScaledToTheVideo(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media root")
	movies := filepath.Join(root, "Movies")
	if err := os.MkdirAll(movies, 0o755); err != nil {
		t.Fatal(err)
	}
	idx := writeVobSub(t, t.TempDir())
	ffmpegOK(t, "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-i", idx, "-map", "0:v", "-map", "1:a", "-map", "2:s", "-t", "12", "-c:v", "libx264", "-preset", "ultrafast", "-g", "48",
		"-keyint_min", "48", "-sc_threshold", "0", "-pix_fmt", "yuv420p", "-c:a", "aac", "-c:s", "copy", "-metadata:s:s:0", "language=eng",
		filepath.Join(movies, "Big Video (2023).mkv"))

	detected := hwaccel.Detect(t.Context(), hwaccel.Options{})
	soft := detected.SoftwareOnly()
	older := soft // the same ffmpeg, as if its scale filter took no reference input
	older.Filters = map[string]bool{}
	maps.Copy(older.Filters, soft.Filters)
	delete(older.Filters, hwaccel.RefScale)
	variants := []struct {
		name string
		info hwaccel.Info
	}{{"scale2ref", older}}
	if soft.Filters[hwaccel.RefScale] {
		variants = append(variants, struct {
			name string
			info hwaccel.Info
		}{"scale=rw:rh", soft})
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			e, _, ids := newSubsE2E(t, hwaccel.Static(v.info), root)
			id := ids["Big Video (2023).mkv"]
			post := func(body string) Playback {
				t.Helper()
				resp, b := e.do("POST", fmt.Sprintf("/api/items/%d/playback", id), body, nil)
				if resp.StatusCode != 200 {
					t.Fatalf("POST playback = %d: %s", resp.StatusCode, b)
				}
				var pb Playback
				if err := json.Unmarshal(b, &pb); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { e.do("DELETE", "/api/sessions/"+pb.SessionID, "", nil) })
				return pb
			}
			const profile = `"custom": {"name": "mp4", "containers": ["mp4"], "videoCodecs": ["h264"], "audioCodecs": ["aac"]}`
			// The box is 100x20 at (110,140) on the 320x180 canvas: 200x40 at
			// (220,280) on the 640x360 video. "Right" is inside that; "wrong" is
			// where an unscaled canvas would put it.
			right := func(pb Playback) (int, int) { return brightFramesIn(t, e, pb, 240, 290, 160, 20) }
			wrong := func(pb Playback) (int, int) { return brightFramesIn(t, e, pb, 120, 145, 80, 10) }

			plain := post(`{` + profile + `}`) // control: both places are clean without the subtitle
			if w1, n := right(plain); n == 0 || w1 != 0 {
				t.Fatalf("clean picture has %d/%d white frames in the right place", w1, n)
			}
			if w2, n := wrong(plain); n == 0 || w2 != 0 {
				t.Fatalf("clean picture has %d/%d white frames in the wrong place", w2, n)
			}

			burn := post(`{` + profile + `, "subtitleIndex": 2}`)
			if burn.Method != "transcode" {
				t.Fatalf("with the bitmap subtitle: %+v", burn)
			}
			if white, total := right(burn); white < 36 || white > 60 {
				t.Errorf("%d of %d frames show the box where the scaled canvas puts it, want about 48", white, total)
			}
			if white, total := wrong(burn); white != 0 {
				t.Errorf("%d of %d frames show white where an unscaled canvas would put the box", white, total)
			}
		})
	}
}
