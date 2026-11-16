package trailerdl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// fakeYouTube answers the watch page and the player with fixtures: by video id, the file of
// testdata/youtube the player answers with.
type fakeYouTube struct {
	t       *testing.T
	players map[string]string
	mu      sync.Mutex
	asked   []string // "watch <id>", "player <id>"
	header  http.Header
	body    map[string]any
}

func (f *fakeYouTube) start() YouTube {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/watch":
			f.asked = append(f.asked, "watch "+r.URL.Query().Get("v"))
			http.ServeFile(w, r, "testdata/youtube/watch.html")
		case "/youtubei/v1/player":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				f.t.Errorf("player body: %v", err)
			}
			id, _ := body["videoId"].(string)
			f.asked = append(f.asked, "player "+id)
			f.header, f.body = r.Header.Clone(), body
			data, err := os.ReadFile("testdata/youtube/" + f.players[id])
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	f.t.Cleanup(srv.Close)
	return YouTube{HTTP: srv.Client(), Base: srv.URL}
}

func TestYouTubeResolve(t *testing.T) {
	f := &fakeYouTube{t: t, players: map[string]string{"vKQi3bBA1y8": "player_ok.json"}}
	src, err := f.start().Resolve(context.Background(), "vKQi3bBA1y8")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.asked, ", ") != "watch vKQi3bBA1y8, player vKQi3bBA1y8" {
		t.Errorf("asked %v", f.asked)
	}
	h := f.header
	if h.Get("X-YouTube-Client-Name") != "101" || h.Get("X-YouTube-Client-Version") != "1.02" ||
		h.Get("X-Goog-Visitor-Id") != "CgtWSVNJVE9SLUlEKKqfxMgG" || h.Get("Origin") != "https://www.youtube.com" {
		t.Errorf("player headers: %v", h)
	}
	client := f.body["context"].(map[string]any)["client"].(map[string]any)
	if client["clientName"] != "VISIONOS" || client["visitorData"] != "CgtWSVNJVE9SLUlEKKqfxMgG" ||
		f.body["contentCheckOk"] != true || f.body["racyCheckOk"] != true {
		t.Errorf("player body: %v", f.body)
	}
	if src.Video.URL != "MEDIA/avc1-1080" || src.Video.Height != 1080 || src.Video.Size != 31906091 {
		t.Errorf("video: %+v, want the 1080p H.264 stream", src.Video)
	}
	if src.Audio.URL != "MEDIA/aac" {
		t.Errorf("audio: %+v, want AAC", src.Audio)
	}
	if src.Wait.Seconds() != 5 { // the start ad, skippable after 5 s; the end ad does not count
		t.Errorf("wait: %v, want 5s", src.Wait)
	}
}

func TestYouTubeLoginRequired(t *testing.T) {
	f := &fakeYouTube{t: t, players: map[string]string{"vKQi3bBA1y8": "player_login.json"}}
	_, err := f.start().Resolve(context.Background(), "vKQi3bBA1y8")
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "Sign in to confirm your age") {
		t.Errorf("err = %v, want ErrUnavailable with the reason", err)
	}
}

func TestYouTubeSkipsCipheredFormats(t *testing.T) {
	f := &fakeYouTube{t: t, players: map[string]string{"vKQi3bBA1y8": "player_cipher_only.json"}}
	_, err := f.start().Resolve(context.Background(), "vKQi3bBA1y8")
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "YouTube gave no stream this client can read") {
		t.Errorf("err = %v", err)
	}
}

func TestYouTubeWrongVideoID(t *testing.T) {
	f := &fakeYouTube{t: t, players: map[string]string{"vKQi3bBA1y8": "player_other_id.json"}}
	if _, err := f.start().Resolve(context.Background(), "vKQi3bBA1y8"); err == nil {
		t.Error("another video's streams were taken")
	}
}

// A paid film's page names its trailer: it is followed, once.
func TestYouTubeFollowsTrailerVideoID(t *testing.T) {
	f := &fakeYouTube{t: t, players: map[string]string{"vKQi3bBA1y8": "player_ypc_trailer.json", "HhesaQXLuRY": "player_ypc_trailer.json"}}
	_, err := f.start().Resolve(context.Background(), "vKQi3bBA1y8")
	if err == nil || strings.Count(strings.Join(f.asked, ","), "player") != 2 || f.asked[len(f.asked)-1] != "player HhesaQXLuRY" {
		t.Errorf("asked %v, err %v; want the trailer asked once, then an error", f.asked, err)
	}
	f = &fakeYouTube{t: t, players: map[string]string{"vKQi3bBA1y8": "player_ypc_trailer.json", "HhesaQXLuRY": "player_ok.json"}}
	// player_ok.json names vKQi3bBA1y8: a trailer answered with another id is still refused.
	if _, err := f.start().Resolve(context.Background(), "vKQi3bBA1y8"); err == nil {
		t.Error("the followed trailer's answer was not checked")
	}
}

func TestPickVideo(t *testing.T) {
	s := func(mime string, h int) Stream { return Stream{URL: mime, MIME: mime, Height: h} }
	for _, c := range []struct {
		name string
		in   []Stream
		max  int
		want string
	}{
		{"H.264 wins a tie", []Stream{s(`video/mp4; codecs="av01.0.08M.08"`, 1080), s(`video/mp4; codecs="avc1.640028"`, 1080)}, 1080, `video/mp4; codecs="avc1.640028"`},
		{"the highest under the cap", []Stream{s(`video/webm; codecs="vp9"`, 1440), s(`video/mp4; codecs="avc1.4d401f"`, 720)}, 1080, `video/mp4; codecs="avc1.4d401f"`},
		{"higher beats H.264", []Stream{s(`video/mp4; codecs="avc1.4d401f"`, 720), s(`video/mp4; codecs="av01.0.08M.08"`, 1080)}, 1080, `video/mp4; codecs="av01.0.08M.08"`},
		{"audio is not video", []Stream{{URL: "a", MIME: `audio/mp4; codecs="mp4a.40.2"`}}, 1080, ""},
	} {
		got, ok := pickVideo(c.in, c.max)
		if got.URL != c.want || ok != (c.want != "") {
			t.Errorf("%s: %+v, %v", c.name, got, ok)
		}
	}
}
