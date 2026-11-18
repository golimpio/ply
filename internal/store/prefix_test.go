package store

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestPrefixSuccessor(t *testing.T) {
	for _, c := range []struct{ prefix, want string }{
		{"", ""},
		{"abc", "abd"},
		{"dir/", "dir0"},
		{"m7.reread.", "m7.reread/"},
		{"a\xff", "b"},
		{"\xff", ""},
		{"\xff\xff", ""},
		{"a\xff\xff", "b"},
	} {
		if got := prefixSuccessor(c.prefix); got != c.want {
			t.Errorf("prefixSuccessor(%q) = %q, want %q", c.prefix, got, c.want)
		}
	}
}

// explainPlan is the query plan of q, one line per step: what the planner says
// it will do, which is what an index is for. It asks the writer: a connection
// keeps the statistics it loaded, and the writer has the ones the test's ANALYZE
// just wrote. In production the migration's ANALYZE runs before the readers
// open, so they load the same statistics.
func explainPlan(t *testing.T, s *Store, q string, args ...any) string {
	t.Helper()
	rows, err := s.rw.QueryContext(context.Background(), `EXPLAIN QUERY PLAN `+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}

// A directory name with bytes that are not valid UTF-8 (a Linux filesystem
// allows them): the byte range finds the file inside. The character-counting
// substr form did not: Go counts an overlong sequence's bytes, SQLite decodes
// it as one character, so the two lengths disagree.
func TestItemsBelowRawBytes(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	dir := "/m/\xc0\xaf" // an overlong UTF-8 sequence, as raw bytes
	inside := movie(dir+"/f.mkv", "F")
	inside.Root = "/m"
	id, err := s.UpsertItem(ctx, inside, nil)
	if err != nil {
		t.Fatal(err)
	}
	beside := movie(dir+"x/g.mkv", "G") // the same start, another folder
	beside.Root = "/m"
	bid, err := s.UpsertItem(ctx, beside, nil)
	if err != nil {
		t.Fatal(err)
	}
	if e, err := s.GetEntry(ctx, bid); err != nil || e.ID != bid {
		t.Fatalf("control: the sibling item is there: %v", err)
	}
	got, err := s.ItemsBelow(ctx, dir)
	if err != nil || len(got) != 1 || got[0].ID != id {
		t.Errorf("ItemsBelow(%q) = %+v, %v; want the one file inside", dir, got, err)
	}
}

// The same byte semantics for settings keys: a prefix with an overlong sequence
// matches the keys that start with those bytes, and only those.
func TestSettingsWithPrefixRawBytes(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	prefix := "x.\xc0\xaf"
	for k, v := range map[string]string{prefix + ".y": "1", prefix + "z.y": "2", "x.\xc0": "3"} {
		if err := s.SetSetting(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.SettingsWithPrefix(ctx, prefix)
	want := map[string]string{prefix + ".y": "1", prefix + "z.y": "2"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("SettingsWithPrefix(raw bytes) = %v, %v; want %v", got, err, want)
	}
}

// The trailer folder probe seeks the path index (a byte range) instead of
// scanning every movie per trailer candidate. The statistics need rows to be
// about: the migration ANALYZEs a populated database; here the rows come after
// Open, so ANALYZE again.
func TestTrailerFolderProbeSeeksPathIndex(t *testing.T) {
	s, _ := openTemp(t)
	var b strings.Builder
	b.WriteString(`INSERT INTO items (kind, path, root, size, mtime, added_at, updated_at) VALUES `)
	for f := 0; f < 12; f++ {
		for m := 0; m < 10; m++ {
			if f > 0 || m > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `('movie', '/media/Film %d/Movie %d.mkv', '/media', 1, 1, 1, 1)`, f, m)
		}
	}
	for e := 0; e < 300; e++ {
		fmt.Fprintf(&b, `,('episode', '/tv/Show %d/S01E%02d.mkv', '/tv', 1, 1, 1, 1)`, e/30, e%30+1)
	}
	if _, err := s.rw.Exec(b.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rw.Exec(`ANALYZE`); err != nil {
		t.Fatal(err)
	}
	plan := explainPlan(t, s, filmsBelowSQL, KindMovie, "/media/Film 3", "/media/Film 3")
	if !strings.Contains(plan, "path>? AND path<?") {
		t.Errorf("the trailer folder probe does not seek the path index:\n%s", plan)
	}
}

// A trailer whose owner folder's name carries raw bytes still belongs to the
// film below it (the correlated byte range, like the bound one).
func TestTitleTrailersRawBytes(t *testing.T) {
	s, _ := openTemp(t)
	dir := "/media/\xc0\xaf (2020)"
	_, title := linkedMovie(t, s, dir+"/Film.mkv", "Film", 2020, 1)
	tr := addTrailer(t, s, trailerAt(dir+"/Film-trailer.mp4", dir, "Film", 2020, ""))
	if got := trailerIDs(t, s, title); len(got) != 1 || got[0] != tr {
		t.Errorf("trailers of a folder with raw bytes = %v, want [%d]", got, tr)
	}
}