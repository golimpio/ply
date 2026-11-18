package main

import (
	"testing"

	"ply/internal/probe"
	"ply/internal/store"
	"ply/internal/subs"
)

// A trailer's subtitles are not extracted ahead of time: it is played, not watched.
func TestSubsFeederSkipsTrailers(t *testing.T) {
	gen, err := subs.New(subs.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	f := &subsFeeder{gen: gen, cancelled: map[int64]bool{}}
	text := []probe.Stream{{Index: 0, Type: "video", Codec: "h264"}, {Index: 1, Type: "subtitle", Codec: "subrip", Language: "eng"}}
	film := store.Entry{ID: 1, Kind: store.KindMovie, Path: "/m/a.mkv", Streams: text}
	if !f.wants(film) {
		t.Fatal("control: a film with a text track is wanted")
	}
	tr := film
	tr.ID, tr.Kind, tr.Path = 2, store.KindTrailer, "/m/a-trailer.mkv"
	if f.wants(tr) {
		t.Error("a trailer is wanted for subtitle extraction")
	}
}
