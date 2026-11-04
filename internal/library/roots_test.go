package library

import (
	"slices"
	"testing"
)

func TestMergeRoots(t *testing.T) {
	tests := []struct {
		name         string
		flags, saved []Root
		want         []Root
	}{
		{"nothing", nil, nil, []Root{}},
		{"flags only", []Root{{"/a", KindMovies}, {"/b", ""}}, nil, []Root{{"/a", KindMovies}, {"/b", ""}}},
		{"saved only", nil, []Root{{"/a", KindShows}}, []Root{{"/a", KindShows}}},
		{"flags first, then the saved ones",
			[]Root{{"/f", KindMovies}}, []Root{{"/s1", KindShows}, {"/s2", KindMovies}},
			[]Root{{"/f", KindMovies}, {"/s1", KindShows}, {"/s2", KindMovies}}},
		{"a saved folder that is also a flag folder is one",
			[]Root{{"/a", KindMovies}}, []Root{{"/a", KindMovies}, {"/b", KindShows}},
			[]Root{{"/a", KindMovies}, {"/b", KindShows}}},
		{"-media takes the saved type", []Root{{"/a", ""}}, []Root{{"/a", KindShows}}, []Root{{"/a", KindShows}}},
		{"-media with no saved type stays untyped", []Root{{"/a", ""}}, []Root{{"/b", KindShows}},
			[]Root{{"/a", ""}, {"/b", KindShows}}},
		{"-movies wins over a stale saved type", []Root{{"/a", KindMovies}}, []Root{{"/a", KindShows}},
			[]Root{{"/a", KindMovies}}},
		{"repeats are dropped", []Root{{"/a", ""}, {"/a", ""}}, []Root{{"/b", KindShows}, {"/b", KindShows}},
			[]Root{{"/a", ""}, {"/b", KindShows}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MergeRoots(tt.flags, tt.saved); !slices.Equal(got, tt.want) {
				t.Errorf("MergeRoots = %v, want %v", got, tt.want)
			}
		})
	}
}
