package metadata

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ply/internal/store"
)

// Two of IMDb's non-commercial datasets (docs/M10-RATINGS.md): the ratings of titles and episodes,
// and which episode is which of a show. Each install downloads its own copy, keeps it in the cache
// folder and never serves it.
const (
	DefaultIMDbRatingsURL  = "https://datasets.imdbws.com/title.ratings.tsv.gz"
	DefaultIMDbEpisodesURL = "https://datasets.imdbws.com/title.episode.tsv.gz"
	// IMDbRatingsFile and IMDbEpisodesFile are the names the datasets are kept under in the cache folder.
	IMDbRatingsFile  = "title.ratings.tsv.gz"
	IMDbEpisodesFile = "title.episode.tsv.gz"
	// IMDbTitleURL is where a title is on imdb.com, with the id after it.
	IMDbTitleURL = "https://www.imdb.com/title/"

	// The first columns of each file. IMDb may add columns at the end; a file whose first columns are
	// others is not the dataset.
	imdbRatingsHeader  = "tconst\taverageRating\tnumVotes"
	imdbEpisodesHeader = "tconst\tparentTconst\tseasonNumber\tepisodeNumber"

	imdbYieldEvery = 1 << 16 // rows between two looks at what playback needs
	maxIMDbLine    = 4096    // a longer line is not a row of these files

	// A download that was cut off leaves its part file; this old, it is removed.
	imdbPartMaxAge  = time.Hour
	imdbPartPattern = ".ratings-*.part"
)

// The most a dataset may weigh as downloaded (they are about 7 and 45 MB), and the most it may unzip
// to (about 25 and 250 MB): a file that is more is not one. Variables for the tests.
var (
	maxIMDbDownload int64 = 256 << 20
	maxIMDbExpanded int64 = 3072 << 20
)

// IMDbURL is the page of a title on imdb.com.
func IMDbURL(tconst string) string { return IMDbTitleURL + tconst + "/" }

// IMDbRating is one row of the ratings dataset.
type IMDbRating struct {
	Value float64 // averageRating, out of 10
	Votes int     // numVotes
}

// IMDbParseStats says what a parse saw.
type IMDbParseStats struct {
	Rows    int // rows of data
	Kept    int // rows that were wanted
	Skipped int // rows that could not be read: a bad line, \N, season 0
}

// ErrIMDbFormat means the file is not the dataset: it is not a gzip, its header is not the one IMDb
// publishes, or it unzips to more than there can be. Nothing is stored from it.
var ErrIMDbFormat = errors.New("metadata: not IMDb's dataset")

// ErrIMDbShort means the file is a dataset with far fewer rows than the last one had (or none): a
// mirror that is cut off, not IMDb's data. Nothing is stored from it, and the copy we have stays.
var ErrIMDbShort = errors.New("metadata: IMDb's dataset has too few rows")

// imdbRow is what a row came to.
type imdbRow int

const (
	rowDropped imdbRow = iota // not wanted
	rowKept
	rowSkipped // could not be read
)

// scanIMDb reads a dataset from r as a stream, in constant memory: it unzips it, checks the first
// line to be header (or header and more columns), and calls row for each line after it. A line
// longer than maxIMDbLine is a skipped row, not given to row; a blank line is none. yield, when
// not nil, is called every imdbYieldEvery rows, and a failure of it stops the scan. A file that is
// cut short is an error (a half-read file must not pass for the whole of it).
func scanIMDb(ctx context.Context, r io.Reader, header string, yield func(context.Context) error, row func(line []byte) imdbRow) (IMDbParseStats, error) {
	var stats IMDbParseStats
	gz, err := gzip.NewReader(r)
	if err != nil {
		return stats, fmt.Errorf("%w: %v", ErrIMDbFormat, err)
	}
	defer gz.Close()
	br := bufio.NewReaderSize(&expandCap{r: gz, left: maxIMDbExpanded + 1}, 256<<10)

	first := true
	for {
		line, err := br.ReadSlice('\n')
		long := errors.Is(err, bufio.ErrBufferFull)
		if long {
			// Not a row: skip to the end of the line without keeping any of it.
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = br.ReadSlice('\n')
			}
			line = nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return stats, err
		}
		atEOF := errors.Is(err, io.EOF)
		line = bytes.TrimRight(line, "\r\n")

		switch {
		case first:
			first = false
			if !hasHeader(line, header) {
				return stats, fmt.Errorf("%w: unexpected header", ErrIMDbFormat)
			}
		case len(line) == 0 && !long:
			// a blank line, or the end of a file that ends with a newline
		default:
			stats.Rows++
			if stats.Rows%imdbYieldEvery == 0 {
				if err := ctx.Err(); err != nil {
					return stats, err
				}
				if yield != nil {
					if err := yield(ctx); err != nil {
						return stats, err
					}
				}
			}
			res := rowSkipped
			if !long {
				res = row(line)
			}
			switch res {
			case rowKept:
				stats.Kept++
			case rowSkipped:
				stats.Skipped++
			}
		}
		if atEOF {
			break
		}
	}
	return stats, nil
}

// hasHeader: the line is header, or header and further columns.
func hasHeader(line []byte, header string) bool {
	line = bytes.TrimPrefix(line, []byte("\xef\xbb\xbf"))
	h := []byte(header)
	return bytes.Equal(line, h) || (bytes.HasPrefix(line, h) && line[len(h)] == '\t')
}

// expandCap fails a read once more than its budget has been read: a gzip that unzips to gigabytes
// is not one of these files.
type expandCap struct {
	r    io.Reader
	left int64
}

func (c *expandCap) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, fmt.Errorf("%w: more than %d bytes unzipped", ErrIMDbFormat, maxIMDbExpanded)
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

// ParseIMDbRatings reads title.ratings.tsv.gz from r: only the rows for which want(tconst) is true
// are kept. A line that does not have the columns, a value that is not a number, \N, and a tconst
// that does not start with "tt" are skipped, not errors. See scanIMDb for the rest.
func ParseIMDbRatings(ctx context.Context, r io.Reader, want func(tconst []byte) bool, yield func(context.Context) error) (map[string]IMDbRating, IMDbParseStats, error) {
	kept := map[string]IMDbRating{}
	stats, err := scanIMDb(ctx, r, imdbRatingsHeader, yield, func(line []byte) imdbRow {
		tconst, rest, ok := splitTconst(line)
		if !ok {
			return rowSkipped
		}
		if !want(tconst) {
			return rowDropped
		}
		v, votes, ok := parseRating(rest)
		if !ok {
			return rowSkipped
		}
		kept[string(tconst)] = IMDbRating{Value: v, Votes: votes}
		return rowKept
	})
	if err != nil {
		return nil, stats, err
	}
	return kept, stats, nil
}

// ParseIMDbEpisodes reads title.episode.tsv.gz from r: the episodes of the shows for which
// want(parentTconst) is true, by the show's tconst. An episode with \N for its season or number,
// or in season 0 (the specials, which no provider numbers the way IMDb does), is skipped.
func ParseIMDbEpisodes(ctx context.Context, r io.Reader, want func(parent []byte) bool, yield func(context.Context) error) (map[string][]store.DatasetEpisode, IMDbParseStats, error) {
	kept := map[string][]store.DatasetEpisode{}
	stats, err := scanIMDb(ctx, r, imdbEpisodesHeader, yield, func(line []byte) imdbRow {
		tconst, rest, ok := splitTconst(line)
		if !ok {
			return rowSkipped
		}
		parent, rest, ok := cutField(rest)
		if !ok || len(parent) < 3 || parent[0] != 't' || parent[1] != 't' {
			return rowSkipped
		}
		if !want(parent) {
			return rowDropped
		}
		seasonField, rest, ok := cutField(rest)
		if !ok {
			return rowSkipped
		}
		numberField, _, _ := cutField(rest)
		season, err1 := strconv.Atoi(string(seasonField))
		number, err2 := strconv.Atoi(string(numberField))
		if err1 != nil || err2 != nil || season < 1 || number < 0 {
			return rowSkipped
		}
		kept[string(parent)] = append(kept[string(parent)], store.DatasetEpisode{IMDbID: string(tconst), Season: season, Number: number})
		return rowKept
	})
	if err != nil {
		return nil, stats, err
	}
	return kept, stats, nil
}

// splitTconst cuts "tt0111161\t9.3\t3135040" into the tconst and the rest ("9.3\t3135040"), without
// copying. ok is false for a line of another shape.
func splitTconst(line []byte) (tconst, rest []byte, ok bool) {
	if len(line) > maxIMDbLine || len(line) < 4 || line[0] != 't' || line[1] != 't' {
		return nil, nil, false
	}
	tconst, rest, ok = cutField(line)
	if !ok || len(tconst) < 3 {
		return nil, nil, false
	}
	return tconst, rest, true
}

// cutField cuts the text before the first tab from the text after it; ok is false without a tab.
func cutField(b []byte) (field, rest []byte, ok bool) {
	tab := bytes.IndexByte(b, '\t')
	if tab < 0 {
		return b, nil, false
	}
	return b[:tab], b[tab+1:], true
}

// parseRating reads "9.3\t3135040" (and any columns after). \N, a rating outside (0, 10] and a
// negative vote count are not rows.
func parseRating(rest []byte) (value float64, votes int, ok bool) {
	rating, rest, ok := cutField(rest)
	if !ok {
		return 0, 0, false
	}
	votesField, _, _ := cutField(rest)
	v, err := strconv.ParseFloat(string(rating), 64)
	if err != nil || !(v > 0 && v <= 10) {
		return 0, 0, false
	}
	n, err := strconv.Atoi(string(votesField))
	if err != nil || n < 0 {
		return 0, 0, false
	}
	return v, n, true
}

// imdbValidators are what the last download said about the file, so that the next request can
// ask whether it changed.
type imdbValidators struct {
	ETag         string
	LastModified string
}

// imdbFetch is the outcome of a conditional download.
type imdbFetch struct {
	NotModified bool // 304: the file in the cache is the current one, and was not touched
	imdbValidators
	Bytes int64
	Rows  int // rows of data in the new file
}

// fetchIMDb downloads a dataset to dest if it changed since v (a request with If-None-Match and
// If-Modified-Since; v is empty when there is no file to compare with). It is written to a
// temporary file beside dest and read through once, to its end: it must be a gzip, whole, with
// the header of the dataset and at least minRows rows. Only then is it flushed to disk and
// renamed over dest, so that dest is always a complete dataset, and a download that is not one
// leaves the copy we have as it was (ErrIMDbFormat, ErrIMDbShort). A 304 leaves dest alone.
// Redirects are followed only over https (or to this machine).
func fetchIMDb(ctx context.Context, client *http.Client, src, dest, header string, minRows int, v imdbValidators) (imdbFetch, error) {
	if err := checkIMDbURL(src); err != nil {
		return imdbFetch{}, err
	}
	c := *client
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		return checkIMDbURL(req.URL.String())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return imdbFetch{}, err
	}
	req.Header.Set("User-Agent", "ply")
	// The file is a gzip; a server that adds Content-Encoding to it must not have it undone.
	req.Header.Set("Accept-Encoding", "identity")
	if v.ETag != "" {
		req.Header.Set("If-None-Match", v.ETag)
	}
	if v.LastModified != "" {
		req.Header.Set("If-Modified-Since", v.LastModified)
	}
	resp, err := c.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return imdbFetch{}, fmt.Errorf("imdb dataset: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return imdbFetch{NotModified: true, imdbValidators: v}, nil
	case http.StatusOK:
	default:
		return imdbFetch{}, fmt.Errorf("imdb dataset: %s", resp.Status)
	}
	if resp.ContentLength > maxIMDbDownload {
		return imdbFetch{}, fmt.Errorf("imdb dataset: %d bytes is too much", resp.ContentLength)
	}

	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return imdbFetch{}, err
	}
	sweepIMDbParts(dir)
	tmp, err := os.CreateTemp(dir, imdbPartPattern)
	if err != nil {
		return imdbFetch{}, err
	}
	defer os.Remove(tmp.Name()) // a no-op once it is renamed
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, maxIMDbDownload+1))
	if err == nil && n > maxIMDbDownload {
		err = fmt.Errorf("more than %d bytes", maxIMDbDownload)
	}
	var stats IMDbParseStats
	if err == nil {
		stats, err = checkIMDbFile(ctx, tmp, header)
	}
	if err == nil && stats.Rows < max(minRows, 1) {
		err = fmt.Errorf("%w: %d rows, and at least %d are expected", ErrIMDbShort, stats.Rows, max(minRows, 1))
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return imdbFetch{}, fmt.Errorf("imdb dataset: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return imdbFetch{}, err
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return imdbFetch{}, err
	}
	return imdbFetch{imdbValidators: imdbValidators{ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified")},
		Bytes: n, Rows: stats.Rows}, nil
}

// checkIMDbFile reads the file from the start to its end through a gzip reader and counts its rows:
// it is a gzip, whole, and a dataset with this header.
func checkIMDbFile(ctx context.Context, f *os.File, header string) (IMDbParseStats, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return IMDbParseStats{}, err
	}
	return scanIMDb(ctx, f, header, nil, func([]byte) imdbRow { return rowDropped })
}

// sweepIMDbParts removes the part files that a download left behind when it was cut off (the
// process ended before the file was renamed or removed). A young one may be a download in progress.
func sweepIMDbParts(dir string) {
	parts, _ := filepath.Glob(filepath.Join(dir, imdbPartPattern))
	for _, p := range parts {
		if fi, err := os.Stat(p); err == nil && time.Since(fi.ModTime()) > imdbPartMaxAge {
			os.Remove(p)
		}
	}
}

// checkIMDbURL accepts https, and http only to this machine (tests, a local mirror).
func checkIMDbURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("imdb dataset: the address is not valid")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if host := u.Hostname(); strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback() {
			return nil
		}
	}
	return errors.New("imdb dataset: the address must use https")
}

// parseHTTPTime reads a Last-Modified header; the zero time when it is not one.
func parseHTTPTime(v string) time.Time {
	t, err := http.ParseTime(v)
	if err != nil {
		return time.Time{}
	}
	return t
}
