package plex

import "testing"

func TestParseIDs(t *testing.T) {
	cases := []struct {
		name  string
		guid  string
		array []string
		want  IDs
	}{
		{"legacy imdb", "com.plexapp.agents.imdb://tt0107302?lang=en", nil, IDs{IMDb: "tt0107302"}},
		{"legacy tmdb", "com.plexapp.agents.themoviedb://603?lang=en", nil, IDs{TMDB: 603}},
		{"legacy tvdb", "com.plexapp.agents.tvdb://81189?lang=en", nil, IDs{TVDB: 81189}},
		{"legacy thetvdb", "com.plexapp.agents.thetvdb://81189", nil, IDs{TVDB: 81189}},
		{"legacy none", "com.plexapp.agents.none://123?lang=en", nil, IDs{}},
		{"new plex guid with array", "plex://movie/5d7768e296b655001fe094c0",
			[]string{"imdb://tt0088763", "tmdb://105", "tvdb://299"}, IDs{IMDb: "tt0088763", TMDB: 105, TVDB: 299}},
		{"array only tmdb", "plex://show/abc", []string{"tmdb://1396"}, IDs{TMDB: 1396}},
		{"array imdb without tt", "", []string{"imdb://0107302"}, IDs{IMDb: "tt0107302"}},
		{"array wins over legacy", "com.plexapp.agents.imdb://tt9999999?lang=en",
			[]string{"imdb://tt0107302", "tmdb://603"}, IDs{IMDb: "tt0107302", TMDB: 603}},
		{"array with lang query", "", []string{"tmdb://603?lang=en"}, IDs{TMDB: 603}},
		{"empty", "", nil, IDs{}},
		{"garbage array entries", "", []string{"nonsense", "imdb://", "tmdb://abc"}, IDs{}},
		{"negative tmdb ignored", "", []string{"tmdb://-5"}, IDs{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseIDs(tc.guid, tc.array)
			if got != tc.want {
				t.Errorf("ParseIDs(%q, %v) = %+v, want %+v", tc.guid, tc.array, got, tc.want)
			}
		})
	}
}

func TestIDsEmpty(t *testing.T) {
	if !(IDs{}).Empty() {
		t.Error("zero IDs should be Empty")
	}
	if (IDs{TMDB: 1}).Empty() {
		t.Error("IDs with a TMDB id should not be Empty")
	}
}

func TestKindOfType(t *testing.T) {
	for typ, want := range map[int]Kind{1: Movie, 2: Show, 3: Season, 4: Episode} {
		got, ok := KindOfType(typ)
		if !ok || got != want {
			t.Errorf("KindOfType(%d) = %q,%v want %q", typ, got, ok, want)
		}
	}
	if _, ok := KindOfType(8); ok { // artist: out of scope
		t.Error("KindOfType(8) should be out of scope")
	}
}
