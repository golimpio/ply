package library

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"ply/internal/store"
)

func TestSidecars(t *testing.T) {
	const video = "/media/Movie (2020).mkv"
	type sub = store.ExternalSubtitle
	dir := "/media/"
	for _, tc := range []struct {
		name  string
		video string
		files []string
		want  []sub
	}{
		{
			name:  "bare name",
			files: []string{"Movie (2020).srt"},
			want:  []sub{{Path: dir + "Movie (2020).srt", Format: "srt"}},
		},
		{
			name:  "all three formats",
			files: []string{"Movie (2020).srt", "Movie (2020).vtt", "Movie (2020).ass"},
			want: []sub{
				{Path: dir + "Movie (2020).ass", Format: "ass"},
				{Path: dir + "Movie (2020).srt", Format: "srt"},
				{Path: dir + "Movie (2020).vtt", Format: "vtt"},
			},
		},
		{
			name:  "language",
			files: []string{"Movie (2020).en.srt", "Movie (2020).por.vtt", "Movie (2020).pt-BR.ass"},
			want: []sub{
				{Path: dir + "Movie (2020).en.srt", Language: "en", Format: "srt"},
				{Path: dir + "Movie (2020).por.vtt", Language: "por", Format: "vtt"},
				{Path: dir + "Movie (2020).pt-BR.ass", Language: "pt-br", Format: "ass"},
			},
		},
		{
			name:  "forced, before and after the language",
			files: []string{"Movie (2020).en.forced.srt", "Movie (2020).forced.srt"},
			want: []sub{
				{Path: dir + "Movie (2020).en.forced.srt", Language: "en", Format: "srt", Forced: true},
				{Path: dir + "Movie (2020).forced.srt", Format: "srt", Forced: true},
			},
		},
		{
			name:  "descriptive flags are accepted, hi is Hindi",
			files: []string{"Movie (2020).en.sdh.srt", "Movie (2020).hi.srt"},
			want: []sub{
				{Path: dir + "Movie (2020).en.sdh.srt", Language: "en", Format: "srt"},
				{Path: dir + "Movie (2020).hi.srt", Language: "hi", Format: "srt"},
			},
		},
		{
			name:  "case-insensitive base and extension",
			files: []string{"MOVIE (2020).EN.SRT"},
			want:  []sub{{Path: dir + "MOVIE (2020).EN.SRT", Language: "en", Format: "srt"}},
		},
		{
			name: "not sidecars",
			files: []string{
				"Movie (2020).mkv",             // the video itself
				"Movie (2020).txt",             // other extension
				"Movie (2020).sub",             // VobSub is not text
				"Movie (2020).idx",             //
				"Movie (2020) extras.srt",      // another title
				"Movie (2020)2.srt",            // another title
				"Movie (2020).Extras.Part.srt", // not a language
				"Movie (2020).en.fr.srt",       // two languages
				"Movie (2020).a.b.c.d.srt",     // too many parts
				"Other (2020).srt",             // another video
				"Movie.srt",                    // shorter name
				"._Movie (2020).srt",           // AppleDouble
				"Movie (2020).en.srt.bak",      // ends elsewhere
				".srt",
			},
		},
		{
			// Found by FuzzSidecars: the base runs into the extension of a file
			// name that starts with it. This used to panic (slice bounds), which
			// would have taken the whole scanner down. a.sr.srt is the control.
			name:  "base overlapping the extension",
			video: "/media/a.sr.mkv",
			files: []string{"a.srt", "a.sr.srt", "a.sr.en.srt"},
			want: []sub{
				{Path: dir + "a.sr.en.srt", Language: "en", Format: "srt"},
				{Path: dir + "a.sr.srt", Format: "srt"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.video
			if v == "" {
				v = video
			}
			got := sidecars(v, tc.files)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("sidecars = %+v\nwant       %+v", got, tc.want)
			}
		})
	}
}

func TestSidecarsOnlyBesideTheirVideo(t *testing.T) {
	// Another directory's file with the same name does not match: the caller
	// passes the names of the video's own directory.
	got := sidecars("/media/a/Movie.mkv", []string{"Movie.en.srt"})
	if len(got) != 1 || got[0].Path != "/media/a/Movie.en.srt" {
		t.Errorf("got %+v", got)
	}
}

func TestScanFindsSidecarSubtitles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "My Media")
	video := filepath.Join(root, "Movie (2020)", "Movie (2020).mkv")
	write(t, video, "v")
	en := filepath.Join(root, "Movie (2020)", "Movie (2020).en.srt")
	write(t, en, "1\n00:00:01,000 --> 00:00:02,000\nhi\n")
	write(t, filepath.Join(root, "Movie (2020)", "Movie (2020).pt.forced.vtt"), "WEBVTT\n")
	write(t, filepath.Join(root, "Movie (2020)", "notes.srt"), "x")

	s, st, fp := newScanner(t, root)
	mustScan(t, s)

	subs := func() []store.ExternalSubtitle {
		t.Helper()
		e, ok := paths(t, st)[video]
		if !ok {
			t.Fatal("video not indexed")
		}
		full, err := st.GetEntry(t.Context(), e.ID)
		if err != nil {
			t.Fatal(err)
		}
		return full.External
	}
	got := subs()
	if len(got) != 2 {
		t.Fatalf("subtitles = %+v, want 2", got)
	}
	if got[0].Path != en || got[0].Language != "en" || got[0].Forced {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Language != "pt" || !got[1].Forced || got[1].Format != "vtt" {
		t.Errorf("second = %+v", got[1])
	}

	// A sidecar appearing and one disappearing are picked up on a rescan even
	// though the video file did not change (positive control: it is skipped).
	if err := os.Remove(en); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "Movie (2020)", "Movie (2020).fr.ass"), "[Script Info]\n")
	probesBefore := fp.calls.Load()
	mustScan(t, s)
	if n := fp.calls.Load(); n != probesBefore {
		t.Fatalf("setup: the unchanged video was probed again (%d -> %d)", probesBefore, n)
	}
	got = subs()
	if len(got) != 2 || got[0].Language != "fr" || got[1].Language != "pt" {
		t.Errorf("after rescan: %+v, want fr and pt", got)
	}

	// Removing the last ones leaves none.
	for _, g := range got {
		os.Remove(g.Path)
	}
	mustScan(t, s)
	if got = subs(); len(got) != 0 {
		t.Errorf("after removing all: %+v", got)
	}
}
