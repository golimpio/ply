//go:build integration

package api

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ply/internal/hwaccel"
	"ply/internal/library"
	"ply/internal/probe"
	"ply/internal/store"
	"ply/internal/stream"
)

// transcodeClip is one generated source file and how it is requested.
type transcodeClip struct {
	file    string   // name inside the library
	args    []string // ffmpeg output arguments after the two lavfi inputs
	size    string   // testsrc2 size
	profile string   // POST body
	height  int      // expected output height
	audio   string   // source audio codec, expected in the output when it is copied
}

var transcodeClips = []transcodeClip{
	{
		file: "H264 1080p (2020).mp4", size: "1920x1080",
		args: []string{"-c:v", "libx264", "-preset", "ultrafast", "-g", "48", "-keyint_min", "48", "-sc_threshold", "0",
			"-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "128k"},
		// A player limited to 720p: the 1080p h264 must be scaled down.
		profile: `{"custom": {"name": "small", "containers": ["mp4"], "videoCodecs": ["h264"], "audioCodecs": ["aac"], "maxHeight": 720}}`,
		height:  720,
		audio:   "aac",
	},
	{
		file: "VP9 Opus (2021).webm", size: "640x360",
		args: []string{"-c:v", "libvpx-vp9", "-deadline", "realtime", "-cpu-used", "8", "-b:v", "500k", "-g", "48",
			"-pix_fmt", "yuv420p", "-c:a", "libopus", "-b:a", "96k"},
		profile: `{"profile": "avplayer"}`, // AVPlayer plays neither webm, vp9 nor opus
		height:  360,
		audio:   "opus",
	},
	{
		file: "HEVC 10bit (2022).mkv", size: "1920x1080",
		args: []string{"-c:v", "libx265", "-preset", "ultrafast", "-x265-params",
			"keyint=48:min-keyint=48:scenecut=0:open-gop=0:log-level=error", "-pix_fmt", "yuv420p10le",
			"-c:a", "eac3", "-b:a", "128k", "-ac", "2"},
		// Forced transcode: the profile takes hevc but only up to 720p.
		profile: `{"custom": {"name": "hd-ready", "containers": ["mp4"], "videoCodecs": ["h264", "hevc"], "audioCodecs": ["aac", "eac3"], "maxHeight": 720}}`,
		height:  720,
		audio:   "eac3",
	},
}

func makeTranscodeClips(t *testing.T, root string) {
	t.Helper()
	for _, c := range transcodeClips {
		out := filepath.Join(root, "Movies", c.file)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			t.Fatal(err)
		}
		args := []string{"-nostdin", "-v", "error", "-y",
			"-f", "lavfi", "-i", "testsrc2=size=" + c.size + ":rate=24",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "30"}
		args = append(append(args, c.args...), out)
		if b, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
			t.Fatalf("generate %s: %v\n%s", c.file, err, b)
		}
	}
}

func newTranscodeE2E(t *testing.T, hw *hwaccel.Detector, root string) (*e2e, map[string]int64) {
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
	srv := New(Options{Store: st, Scanner: sc, ID: "it", Name: "it", Version: "test", Streams: mgr, HW: hw})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	entries, err := st.ListItems(t.Context(), store.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, en := range entries {
		ids[filepath.Base(en.Path)] = en.ID
	}
	return &e2e{t: t, ts: ts, store: st, mgr: mgr, root: root, client: ts.Client()}, ids
}

type streamInfo struct {
	CodecType string `json:"codec_type"`
	CodecName string `json:"codec_name"`
	Profile   string `json:"profile"`
	PixFmt    string `json:"pix_fmt"`
	Height    int    `json:"height"`
	Channels  int    `json:"channels"`
}

// probeSegment writes init+seg to a file and returns its streams plus the
// video packets: presentation span and the number of keyframes.
func probeSegment(t *testing.T, init, seg []byte) (streams []streamInfo, first, end float64, keyframes, frames int) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.mp4")
	if err := os.WriteFile(p, append(append([]byte{}, init...), seg...), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-show_streams", "-of", "json", p).Output()
	if err != nil {
		t.Fatal(err)
	}
	var js struct {
		Streams []streamInfo `json:"streams"`
	}
	if err := json.Unmarshal(out, &js); err != nil {
		t.Fatal(err)
	}
	out, err = exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "packet=pts_time,duration_time,flags", "-of", "csv=p=0", p).Output()
	if err != nil {
		t.Fatal(err)
	}
	first, end = math.Inf(1), math.Inf(-1)
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(strings.TrimRight(l, ","), ",")
		if len(f) < 3 {
			continue
		}
		pts, e1 := strconv.ParseFloat(f[0], 64)
		dur, e2 := strconv.ParseFloat(f[1], 64)
		if e1 != nil || e2 != nil {
			continue
		}
		first, end = math.Min(first, pts), math.Max(end, pts+dur)
		frames++
		if strings.Contains(f[2], "K") {
			keyframes++
		}
	}
	return js.Streams, first, end, keyframes, frames
}

func TestTranscodeEndToEnd(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media root")
	makeTranscodeClips(t, root)

	detected := hwaccel.Detect(t.Context(), hwaccel.Options{})
	t.Logf("detected encoder: %s (%s)", detected.Encoder.Name, strings.Join(detected.Probe, "; "))
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

	for _, enc := range encoders {
		for _, clip := range transcodeClips {
			t.Run(enc.name+"/"+clip.file, func(t *testing.T) {
				e, ids := newTranscodeE2E(t, hwaccel.Static(enc.info), root)
				id, ok := ids[clip.file]
				if !ok {
					t.Fatalf("%s not scanned: %v", clip.file, ids)
				}
				resp, b := e.do("POST", fmt.Sprintf("/api/items/%d/playback", id), clip.profile, nil)
				if resp.StatusCode != 200 {
					t.Fatalf("POST playback = %d: %s", resp.StatusCode, b)
				}
				var pb Playback
				if err := json.Unmarshal(b, &pb); err != nil {
					t.Fatal(err)
				}
				if pb.Method != "transcode" || pb.VideoAction != "transcode" || pb.SessionID == "" {
					t.Fatalf("playback = %+v", pb)
				}
				t.Logf("reasons: %v", pb.Reasons)
				if !strings.Contains(strings.Join(pb.Reasons, "|"), enc.name) {
					t.Errorf("reasons do not name the encoder %s", enc.name)
				}

				_, pl := e.do("GET", pb.URL, "", nil)
				initURI, segs := parsePlaylist(t, string(pl))
				if len(segs) != 5 {
					t.Fatalf("%d segments, want 5:\n%s", len(segs), pl)
				}
				for i, s := range segs[:len(segs)-1] {
					if math.Abs(s.dur-6) > 0.001 {
						t.Errorf("segment %d is %.3f s in the playlist, want 6", i, s.dur)
					}
				}
				base := "/api/sessions/" + pb.SessionID + "/"
				resp, initBytes := e.do("GET", base+initURI, "", nil)
				if resp.StatusCode != 200 {
					t.Fatalf("init = %d", resp.StatusCode)
				}

				// Segment 0, a far segment (a seek: ffmpeg restarts there), the
				// last one, and back to a middle one.
				for _, i := range []int{0, 3, 4, 1} {
					resp, seg := e.do("GET", base+segs[i].uri, "", nil)
					if resp.StatusCode != 200 {
						t.Fatalf("segment %d = %d: %s", i, resp.StatusCode, seg)
					}
					streams, first, end, keyframes, frames := probeSegment(t, initBytes, seg)
					want := segs[i].start + stream.TimelineOffset
					t.Logf("seg %d: %.3f..%.3f (playlist %.3f+%.3f), %d frames, %d keyframes, %d bytes",
						i, first, end, want, segs[i].dur, frames, keyframes, len(seg))

					var v, a *streamInfo
					for j := range streams {
						switch streams[j].CodecType {
						case "video":
							v = &streams[j]
						case "audio":
							a = &streams[j]
						}
					}
					if v == nil || a == nil {
						t.Fatalf("segment %d streams: %+v", i, streams)
					}
					if v.CodecName != "h264" || v.Profile != "High" || v.PixFmt != "yuv420p" || v.Height != clip.height {
						t.Errorf("segment %d video = %+v, want h264 High yuv420p %dp", i, *v, clip.height)
					}
					if pb.AudioAction == "transcode" && (a.CodecName != "aac" || a.Channels != 2) {
						t.Errorf("segment %d audio = %+v, want aac stereo", i, *a)
					}
					if pb.AudioAction == "copy" && a.CodecName != clip.audio {
						t.Errorf("segment %d audio = %+v, want the source's %s copied", i, *a, clip.audio)
					}
					if math.Abs(first-want) > 0.15 || math.Abs(end-(want+segs[i].dur)) > 0.15 {
						t.Errorf("segment %d media %.3f..%.3f, playlist says %.3f..%.3f", i, first, end, want, want+segs[i].dur)
					}
					if keyframes != 1 {
						t.Errorf("segment %d has %d keyframes, want exactly the forced one", i, keyframes)
					}
				}

				sess, _ := e.mgr.Get(pb.SessionID)
				dir := sess.Dir()
				if resp, _ := e.do("DELETE", "/api/sessions/"+pb.SessionID, "", nil); resp.StatusCode != 204 {
					t.Fatalf("DELETE = %d", resp.StatusCode)
				}
				if p := ffmpegRunning(t, filepath.Join(root, "Movies", clip.file)); len(p) != 0 {
					t.Errorf("ffmpeg still running after DELETE: %v", p)
				}
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Errorf("session dir still there: %v", err)
				}
			})
		}
	}
	if p := ffmpegRunning(t, root); len(p) != 0 {
		t.Errorf("ffmpeg children left: %v", p)
	}
}
