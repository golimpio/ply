package plex

import (
	"strconv"
	"strings"
)

// ParseIDs extracts external ids from a Plex agent guid (legacy agents) and the
// Guid array (the new tv.plex.agents.* / plex:// agents). Where both name a
// provider, the Guid array wins. A "?lang=…" query is stripped.
// "com.plexapp.agents.none" and a bare "plex://…" carry no id by themselves.
func ParseIDs(guid string, guidArray []string) IDs {
	var id IDs
	// Legacy: com.plexapp.agents.<provider>://<value>?lang=en
	if rest, ok := strings.CutPrefix(guid, "com.plexapp.agents."); ok {
		provider, value, _ := strings.Cut(rest, "://")
		value = stripQuery(value)
		switch provider {
		case "imdb":
			id.IMDb = normalizeIMDb(value)
		case "themoviedb", "tmdb":
			id.TMDB, _ = strconv.ParseInt(value, 10, 64)
		case "tvdb", "thetvdb":
			id.TVDB, _ = strconv.ParseInt(value, 10, 64)
		}
	}
	// New: Guid array entries "provider://value".
	for _, g := range guidArray {
		provider, value, ok := strings.Cut(g, "://")
		if !ok {
			continue
		}
		value = stripQuery(value)
		switch provider {
		case "imdb":
			if v := normalizeIMDb(value); v != "" {
				id.IMDb = v
			}
		case "tmdb":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil && n > 0 {
				id.TMDB = n
			}
		case "tvdb":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil && n > 0 {
				id.TVDB = n
			}
		}
	}
	return id
}

func stripQuery(s string) string {
	if i := strings.IndexByte(s, '?'); i >= 0 {
		return s[:i]
	}
	return s
}

// normalizeIMDb ensures the "tt" prefix and trims space; "" stays "".
func normalizeIMDb(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "tt") {
		s = "tt" + s
	}
	return s
}
