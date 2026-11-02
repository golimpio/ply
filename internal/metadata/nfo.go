package metadata

import (
	"bytes"
	"encoding/xml"
	"io"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
)

// NFO is what a Kodi/Jellyfin-style .nfo file says about a movie, a show or an
// episode. Fields the file lacks are zero.
type NFO struct {
	Root          string // "movie", "tvshow", "episodedetails"; "" for a file that is not XML
	Title         string
	OriginalTitle string
	SortTitle     string
	Year          int
	Premiered     string // YYYY-MM-DD (the aired date of an episode)
	Plot          string
	Outline       string
	Tagline       string
	RuntimeMin    int
	MPAA          string
	Rating        float64 // out of 10
	Votes         int
	Genres        []string
	Studios       []string
	Directors     []string
	Writers       []string
	Actors        []NFOActor
	Set           string // the collection's name
	Season        int    // episodes
	Episode       int
	IDs           Hints
}

// NFOActor is an <actor> element.
type NFOActor struct {
	Name  string
	Role  string
	Order int
	Thumb string // a URL or a path, as written
}

// HasData reports whether the file told us anything.
func (n NFO) HasData() bool {
	return n.Title != "" || n.Plot != "" || n.Year != 0 || !n.IDs.IsZero() || len(n.Genres) > 0 || len(n.Actors) > 0
}

// Limits that keep a hostile or broken file from costing much.
const (
	MaxNFOBytes = 1 << 20
	maxText     = 64 << 10
	maxList     = 500
	maxDepth    = 32
)

// ReadNFO reads and parses an NFO file (at most MaxNFOBytes of it). Only a
// regular file is read: a FIFO or a device with an .nfo name is an error.
func ReadNFO(path string) (NFO, error) {
	f, err := OpenRegular(path)
	if err != nil {
		return NFO{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxNFOBytes))
	if err != nil {
		return NFO{}, err
	}
	return ParseNFO(data), nil
}

var (
	reTMDBURL = regexp.MustCompile(`(?i)themoviedb\.org/(?:movie|tv)/(\d{1,10})`)
	reIMDbURL = regexp.MustCompile(`(?i)imdb\.com/(?:[a-z]+/)?title/(tt\d{7,8})`)
	reTVDBURL = regexp.MustCompile(`(?i)thetvdb\.com/[^\s"'<>]*?[?&](?:series)?id=(\d{1,10})`)
	reYear    = regexp.MustCompile(`\d{4}`)
	reDate    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)
	reInt     = regexp.MustCompile(`^\d+`)
)

// ParseNFO reads an NFO leniently. It never fails: unknown elements are
// ignored, a broken or truncated XML file gives what came before the break, and
// a file that is not XML at all is scanned for TMDB/IMDb/TVDB URLs and IMDb ids
// (Kodi's "URL-only" NFO). It also looks for URLs after the XML.
func ParseNFO(data []byte) NFO {
	if len(data) > MaxNFOBytes {
		data = data[:MaxNFOBytes]
	}
	data = declaredToUTF8(toUTF8(data))
	var n NFO
	end := parseXML(data, &n)
	if n.IDs.IsZero() {
		// Ids in the text around the XML (Kodi appends the URL after it), never in
		// the XML itself: a plot that mentions a movie is not that movie's id.
		switch {
		case n.Root == "":
			n.IDs = scanURLs(data, true)
		default:
			n.IDs = scanURLs(outsideRoot(data, end), false)
		}
	}
	n.sanitize()
	return n
}

var reStartTag = regexp.MustCompile(`<[A-Za-z_]`)

// outsideRoot returns what lies before the root element's start tag and, after
// its end tag, the text that is not inside another element (end is the offset
// parseXML reported, -1 when the root never closed). data is the very text
// parseXML read, already UTF-8, so the offset is an offset into it. Comments,
// processing instructions and attributes after the root are not scanned.
func outsideRoot(data []byte, end int) []byte {
	var out []byte
	if loc := reStartTag.FindIndex(data); loc != nil {
		out = append(out, data[:loc[0]]...)
	}
	if end >= 0 && end < len(data) {
		out = append(out, '\n')
		out = append(out, textOutsideElements(data[end:])...)
	}
	return out
}

// textOutsideElements returns the character data (CDATA included) of data that
// is at the top level, in pieces on lines of their own: what follows a first
// root, such as the URL Kodi appends, and not the plot or the attributes of
// a second root. It stops at the first syntax error.
func textOutsideElements(data []byte) []byte {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	d.Entity = xml.HTMLEntity
	d.CharsetReader = charsetReader
	var (
		out   []byte
		depth int
	)
	for {
		tok, err := d.Token()
		if err != nil {
			return out
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 {
				out = append(out, t...)
				out = append(out, '\n')
			}
		}
	}
}

// toUTF8 strips a UTF-8 byte order mark and converts UTF-16 that has one.
func toUTF8(data []byte) []byte {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return data[3:]
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}), bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		out, err := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder().Bytes(data)
		if err == nil {
			return out
		}
	}
	return data
}

// reXMLDecl finds the encoding in an XML declaration at the start of the text.
var reXMLDecl = regexp.MustCompile(`(?i)\A\s*<\?xml\s[^>]*?\bencoding\s*=\s*["']([^"']+)["']`)

// declaredToUTF8 converts a file whose XML declaration names a Latin-1 family
// encoding. It is done before parsing, not by the decoder's CharsetReader, so that
// the offsets the decoder reports count the same bytes as the text they are
// used to cut. Anything else is returned as it is.
func declaredToUTF8(data []byte) []byte {
	m := reXMLDecl.FindSubmatch(data)
	if m == nil {
		return data
	}
	switch strings.ToLower(string(m[1])) {
	case "iso-8859-1", "iso8859-1", "latin1", "latin-1", "windows-1252", "cp1252", "iso-8859-15":
		if out, err := charmap.Windows1252.NewDecoder().Bytes(data); err == nil {
			return out
		}
	}
	return data
}

// charsetReader lets the decoder go on whatever the declaration says: Latin-1 and
// UTF-16 were converted before parsing, and the text of anything else is read as
// it is. Bytes that are not UTF-8 stop the parse, and what came before is kept.
func charsetReader(_ string, input io.Reader) (io.Reader, error) {
	return input, nil
}

// frame is an open element.
type frame struct {
	name  string
	attrs []xml.Attr
	text  strings.Builder
	kids  map[string]string // text of the direct children, first one wins
}

func (f *frame) attr(name string) string {
	for _, a := range f.attrs {
		if strings.EqualFold(a.Name.Local, name) {
			return strings.TrimSpace(a.Value)
		}
	}
	return ""
}

// parseXML walks the first root element of data, filling n. It stops at the
// first error, keeping what it has. It returns the offset after the root's end
// tag, or -1 when there was no root or it did not close.
func parseXML(data []byte, n *NFO) (end int) {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	d.Entity = xml.HTMLEntity
	d.CharsetReader = charsetReader
	var (
		stack      []*frame
		ratingBest bool // a <rating default="true"> has been taken
	)
	for {
		tok, err := d.Token()
		if err != nil {
			return -1
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) == 0 && n.Root != "" {
				return -1 // a second root: the first one is the file's subject
			}
			if len(stack) >= maxDepth {
				return -1
			}
			f := &frame{name: strings.ToLower(t.Name.Local), attrs: t.Attr, kids: map[string]string{}}
			if len(stack) == 0 {
				n.Root = f.name
			}
			stack = append(stack, f)
		case xml.CharData:
			if len(stack) > 0 {
				// Keep at most maxText bytes per element, however the decoder
				// chunked the text (a closed 300 KiB plot arrives in one piece).
				if top := stack[len(stack)-1]; top.text.Len() < maxText {
					top.text.Write(t[:min(len(t), maxText-top.text.Len())])
				}
			}
		case xml.EndElement:
			if len(stack) == 0 {
				continue
			}
			f := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return int(d.InputOffset())
			}
			text := strings.TrimSpace(f.text.String())
			parent := stack[len(stack)-1]
			if _, seen := parent.kids[f.name]; !seen {
				parent.kids[f.name] = text
			}
			switch {
			case len(stack) == 1:
				n.element(f, text)
			case len(stack) == 2 && stack[1].name == "ratings" && f.name == "rating":
				n.ratingElement(f, &ratingBest)
			}
		}
	}
}

// element handles a direct child of the root.
func (n *NFO) element(f *frame, text string) {
	switch f.name {
	case "title":
		n.Title = first(n.Title, text)
	case "originaltitle":
		n.OriginalTitle = first(n.OriginalTitle, text)
	case "sorttitle":
		n.SortTitle = first(n.SortTitle, text)
	case "year":
		if n.Year == 0 {
			n.Year = year(text)
		}
	case "premiered", "releasedate", "aired":
		if d := reDate.FindString(text); d != "" && n.Premiered == "" {
			n.Premiered = d
		}
	case "plot":
		n.Plot = first(n.Plot, text)
	case "outline":
		n.Outline = first(n.Outline, text)
	case "tagline":
		n.Tagline = first(n.Tagline, text)
	case "runtime":
		if v, err := strconv.Atoi(reInt.FindString(text)); err == nil && v < 10000 && n.RuntimeMin == 0 {
			n.RuntimeMin = v
		}
	case "mpaa":
		n.MPAA = first(n.MPAA, text)
	case "genre":
		for _, g := range strings.Split(text, " / ") {
			n.Genres = addUnique(n.Genres, g)
		}
	case "studio":
		n.Studios = addUnique(n.Studios, text)
	case "director":
		n.Directors = addUnique(n.Directors, text)
	case "credits", "writer":
		n.Writers = addUnique(n.Writers, text)
	case "rating":
		if v, err := strconv.ParseFloat(text, 64); err == nil && n.Rating == 0 {
			n.setRating(v, 0)
		}
	case "votes":
		if v, err := strconv.Atoi(strings.ReplaceAll(text, ",", "")); err == nil && n.Votes == 0 {
			n.Votes = max(v, 0)
		}
	case "season":
		if v, err := strconv.Atoi(text); err == nil {
			n.Season = max(v, 0)
		}
	case "episode":
		if v, err := strconv.Atoi(text); err == nil {
			n.Episode = max(v, 0)
		}
	case "set":
		n.Set = first(n.Set, first(f.kids["name"], text))
	case "actor":
		if len(n.Actors) < maxList {
			if name := f.kids["name"]; name != "" {
				order, _ := strconv.Atoi(f.kids["order"])
				n.Actors = append(n.Actors, NFOActor{Name: name, Role: f.kids["role"], Order: max(order, 0), Thumb: f.kids["thumb"]})
			}
		}
	case "uniqueid":
		n.addID(f.attr("type"), text)
	case "tmdbid":
		n.addID("tmdb", text)
	case "imdbid":
		n.addID("imdb", text)
	case "tvdbid":
		n.addID("tvdb", text)
	case "id":
		n.addID("", text)
	}
}

// ratingElement reads one <ratings><rating max=".." default="..">: the one
// marked default wins, else the first.
func (n *NFO) ratingElement(f *frame, haveDefault *bool) {
	v, err := strconv.ParseFloat(f.kids["value"], 64)
	if err != nil {
		return
	}
	isDefault := strings.EqualFold(f.attr("default"), "true")
	if *haveDefault || (n.Rating != 0 && !isDefault) {
		return
	}
	scale, _ := strconv.ParseFloat(f.attr("max"), 64)
	n.setRating(v, scale)
	if votes, err := strconv.Atoi(strings.ReplaceAll(f.kids["votes"], ",", "")); err == nil {
		n.Votes = max(votes, 0)
	}
	*haveDefault = isDefault
}

func (n *NFO) setRating(v float64, scale float64) {
	if scale > 0 && scale != 10 {
		v = v * 10 / scale
	}
	if v >= 0 && v <= 10 {
		n.Rating = v
	}
}

// addID records an id by its type; "" is a bare <id> or untyped <uniqueid>.
func (n *NFO) addID(typ, value string) {
	value = strings.TrimSpace(value)
	typ = strings.ToLower(typ)
	if imdb := imdbID(value); imdb != "" && (typ == "" || typ == "imdb") {
		n.IDs.IMDb = first(n.IDs.IMDb, imdb)
		return
	}
	v, err := strconv.ParseInt(value, 10, 64)
	if err != nil || v <= 0 {
		return
	}
	switch typ {
	case "tmdb", "themoviedb":
		if n.IDs.TMDB == 0 {
			n.IDs.TMDB = v
		}
	case "tvdb", "thetvdb":
		if n.IDs.TVDB == 0 {
			n.IDs.TVDB = v
		}
	case "":
		// A bare numeric <id> is a TheTVDB id in a show's NFO. In a movie's it
		// could be anything, and a wrong id would match with full confidence.
		if n.Root == "tvshow" && n.IDs.TVDB == 0 {
			n.IDs.TVDB = v
		}
	}
}

func imdbID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) >= 9 && len(s) <= 10 && s[:2] == "tt" {
		if _, err := strconv.Atoi(s[2:]); err == nil {
			return s
		}
	}
	return ""
}

// scanURLs finds ids in free text: TMDB, IMDb and TheTVDB URLs and, when
// bare is set (the file is not XML), IMDb ids on their own.
func scanURLs(data []byte, bare bool) Hints {
	var h Hints
	if m := reTMDBURL.FindSubmatch(data); m != nil {
		h.TMDB, _ = strconv.ParseInt(string(m[1]), 10, 64)
	}
	if m := reIMDbURL.FindSubmatch(data); m != nil {
		h.IMDb = strings.ToLower(string(m[1]))
	} else if bare {
		h.IMDb = ParseIDs(string(data)).IMDb
	}
	if m := reTVDBURL.FindSubmatch(data); m != nil {
		h.TVDB, _ = strconv.ParseInt(string(m[1]), 10, 64)
	}
	return h
}

func year(s string) int {
	if y := reYear.FindString(s); y != "" {
		v, _ := strconv.Atoi(y)
		if v >= 1800 && v <= 2200 {
			return v
		}
	}
	return 0
}

func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func addUnique(list []string, v string) []string {
	v = strings.TrimSpace(v)
	if v == "" || len(list) >= maxList {
		return list
	}
	for _, have := range list {
		if strings.EqualFold(have, v) {
			return list
		}
	}
	return append(list, v)
}

// sanitize makes every string valid UTF-8 and fills what follows from other
// fields.
func (n *NFO) sanitize() {
	clean := func(s *string) { *s = strings.TrimSpace(strings.ToValidUTF8(*s, "")) }
	for _, s := range []*string{&n.Root, &n.Title, &n.OriginalTitle, &n.SortTitle, &n.Plot, &n.Outline,
		&n.Tagline, &n.MPAA, &n.Set, &n.Premiered, &n.IDs.IMDb} {
		clean(s)
	}
	for _, list := range [][]string{n.Genres, n.Studios, n.Directors, n.Writers} {
		for i := range list {
			clean(&list[i])
		}
	}
	for i := range n.Actors {
		clean(&n.Actors[i].Name)
		clean(&n.Actors[i].Role)
		clean(&n.Actors[i].Thumb)
	}
	if n.Year == 0 {
		n.Year = year(n.Premiered)
	}
}
