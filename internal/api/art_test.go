package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ply/internal/art"
	"ply/internal/probe"
	"ply/internal/store"
)

// A stand-in for ffmpeg as the art generator drives it: it writes a JPEG-ish
// file to the *.tmp output and 16x9 RGB pixels to stdout.
const fakeArtScript = `
out=
for a in "$@"; do case "$a" in *.tmp) out=$a;; esac; done
[ -n "$out" ] || exit 0
printf '\377\330fakejpeg' > "$out"
i=0; while [ $i -lt 144 ]; do printf '\200\100\060'; i=$((i+1)); done
`

func (e *env) withArt(script string) *art.Generator {
	e.t.Helper()
	g, err := art.New(context.Background(), art.Options{Dir: filepath.Join(e.t.TempDir(), "art"), FFmpeg: fakeFFmpeg(e.t, script)})
	if err != nil {
		e.t.Fatal(err)
	}
	e.srv.Art = g
	return g
}

func TestArtDisabled(t *testing.T) {
	e := newEnv(t, "")
	wantJSONError(t, e.get(fmt.Sprintf("/api/items/%d/art/thumb", e.mp4)), 404)

	// "art" is present and null, not missing.
	raw := decode[[]map[string]any](t, e.get("/api/items"))
	for _, it := range raw {
		v, ok := it["art"]
		if !ok || v != nil {
			t.Errorf("item %v: art = %v (present %v), want null", it["id"], v, ok)
		}
	}
	d := decode[map[string]any](t, e.get(fmt.Sprintf("/api/items/%d", e.mp4)))
	if v, ok := d["art"]; !ok || v != nil {
		t.Errorf("detail art = %v (present %v), want null", v, ok)
	}
}

func TestArtLinks(t *testing.T) {
	for _, token := range []string{"", "tok"} {
		t.Run("token="+token, func(t *testing.T) {
			e := newEnv(t, token)
			e.withArt(fakeArtScript)
			hdr := map[string]string{}
			if token != "" {
				hdr["Authorization"] = "Bearer " + token
			}
			entry, err := e.store.GetEntry(context.Background(), e.mp4)
			if err != nil {
				t.Fatal(err)
			}

			list := decode[[]ItemSummary](t, e.do("GET", "/api/items", hdr, ""))
			var got *ArtURLs
			for _, it := range list {
				if it.ID == e.mp4 {
					got = it.Art
				}
			}
			if got == nil {
				t.Fatal("item has no art links")
			}
			base := fmt.Sprintf("/api/items/%d/art/", e.mp4)
			v := fmt.Sprintf("?v=%d", entry.MTime)
			wantBackdrop, wantThumb := base+"backdrop"+v, base+"thumb"+v
			if token != "" { // the token joins the existing query with "&"
				wantBackdrop += "&token=" + token
				wantThumb += "&token=" + token
			}
			if got.Backdrop != wantBackdrop || got.Thumb != wantThumb {
				t.Errorf("art = %+v, want backdrop %q thumb %q", got, wantBackdrop, wantThumb)
			}
			if strings.Count(got.Thumb, "?") != 1 {
				t.Errorf("malformed URL %q", got.Thumb)
			}
			if got.Color != "" {
				t.Errorf("colour %q before any image exists", got.Color)
			}

			// Following the link needs no Authorization header.
			wantStatus(t, e.get(got.Thumb), 200)
			d := decode[ItemDetail](t, e.do("GET", fmt.Sprintf("/api/items/%d", e.mp4), hdr, ""))
			if d.Art == nil || !regexp.MustCompile(`^#[0-9a-f]{6}$`).MatchString(d.Art.Color) {
				t.Errorf("detail art after generation = %+v, want a #rrggbb colour", d.Art)
			}
		})
	}
}

func TestArtNotForItemsWithoutVideo(t *testing.T) {
	e := newEnv(t, "")
	e.withArt(fakeArtScript)
	path := filepath.Join(e.dir, "song.mp3")
	os.WriteFile(path, []byte("x"), 0o644)
	id, err := e.store.UpsertItem(context.Background(), &store.Item{Kind: store.KindMovie, Title: "Song", Path: path,
		Root: e.dir, Size: 1, MTime: 5, Container: "mp3"}, []probe.Stream{{Index: 0, Type: "audio", Codec: "mp3"}})
	if err != nil {
		t.Fatal(err)
	}
	d := decode[map[string]any](t, e.get(fmt.Sprintf("/api/items/%d", id)))
	if v, ok := d["art"]; !ok || v != nil {
		t.Errorf("art = %v (present %v), want null", v, ok)
	}
}

func TestArtEndpoint(t *testing.T) {
	e := newEnv(t, "")
	gen := e.withArt(fakeArtScript)
	url := fmt.Sprintf("/api/items/%d/art/", e.mkv) // the HDR item

	t.Run("404 matrix", func(t *testing.T) {
		for _, p := range []string{
			fmt.Sprintf("/api/items/%d/art/poster", e.mp4), // bad kind
			fmt.Sprintf("/api/items/%d/art/", e.mp4),       // no kind
			"/api/items/abc/art/thumb",                     // bad id
			"/api/items/99999/art/thumb",                   // unknown id
		} {
			if rec := e.get(p); rec.Code != 404 {
				t.Errorf("GET %s = %d, want 404", p, rec.Code)
			}
		}
		wantJSONError(t, e.get("/api/items/99999/art/thumb"), 404)
		if gen.Runs() != 0 {
			t.Errorf("ffmpeg ran %d times for requests that must 404", gen.Runs())
		}
	})

	t.Run("image, headers and cache", func(t *testing.T) {
		rec := e.get(url + "thumb?v=1")
		wantStatus(t, rec, 200)
		if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
			t.Errorf("Content-Type = %q", ct)
		}
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=31536000") {
			t.Errorf("Cache-Control = %q", cc)
		}
		if !strings.HasPrefix(rec.Body.String(), "\xff\xd8") {
			t.Errorf("body = %q", rec.Body.String())
		}
		runs := gen.Runs()
		if runs == 0 {
			t.Fatal("ffmpeg never ran: the fake was not used")
		}
		again := e.get(url + "thumb?v=1")
		wantStatus(t, again, 200)
		if gen.Runs() != runs {
			t.Errorf("second request ran ffmpeg again (%d -> %d)", runs, gen.Runs())
		}
		if again.Body.String() != rec.Body.String() {
			t.Error("cached image differs")
		}
		// The other kind is a separate image.
		wantStatus(t, e.get(url+"backdrop"), 200)
		if gen.Runs() == runs {
			t.Error("backdrop was not generated separately")
		}
	})

	t.Run("range", func(t *testing.T) {
		rec := e.do("GET", url+"thumb", map[string]string{"Range": "bytes=0-1"}, "")
		wantStatus(t, rec, 206)
	})
}

func TestArtFailureIsNotFound(t *testing.T) {
	e := newEnv(t, "")
	e.withArt(`
case "$*" in *-filters*) exit 0;; esac
echo "no video here" >&2
exit 1`)
	wantJSONError(t, e.get(fmt.Sprintf("/api/items/%d/art/thumb", e.mp4)), 404)
}

func TestArtNeedsTheToken(t *testing.T) {
	e := newEnv(t, "tok")
	e.withArt(fakeArtScript)
	p := fmt.Sprintf("/api/items/%d/art/thumb", e.mp4)
	wantJSONError(t, e.get(p), 401)
	wantStatus(t, e.get(p+"?token=tok"), 200)
	wantStatus(t, e.do("GET", p, map[string]string{"Authorization": "Bearer tok"}, ""), 200)
}
