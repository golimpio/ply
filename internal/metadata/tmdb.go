package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"ply/internal/store"
)

// DefaultKey is the TMDB key built into the binary, set with
// -ldflags "-X ply/internal/metadata.DefaultKey=...". It is empty in the repo.
var DefaultKey string

// TMDB's endpoints.
const (
	DefaultBaseURL      = "https://api.themoviedb.org/3"
	DefaultImageBaseURL = "https://image.tmdb.org/t/p"
)

// Defaults of Options.
const (
	DefaultTimeout     = 15 * time.Second
	DefaultRate        = 20 // requests per second
	DefaultMaxInFlight = 4
	DefaultMaxWait     = 30 * time.Second // the longest Retry-After the client sits out
	maxRateRetries     = 2
	maxResponseBytes   = 8 << 20
	maxChangePages     = 200
)

// Options configures NewClient.
type Options struct {
	// Key is a v3 API key (32 hex digits, sent as the api_key parameter) or a
	// v4 read access token (a JWT, which contains dots; sent as a Bearer
	// header).
	Key          string
	BaseURL      string // default DefaultBaseURL
	ImageBaseURL string // default DefaultImageBaseURL
	Language     string // "en-US"; default "en-US"

	HTTPClient  *http.Client  // default: a client with no overall timeout (see Timeout)
	Timeout     time.Duration // per request attempt; default DefaultTimeout
	Rate        float64       // requests per second; default DefaultRate
	MaxInFlight int           // default DefaultMaxInFlight
	MaxWait     time.Duration // longest Retry-After waited out in place; default DefaultMaxWait

	Logger *slog.Logger // default slog.Default(). Never receives the key.
}

// Client talks to the TMDB API. It is safe for concurrent use. Requests are
// paced by a token bucket and capped in flight; a 429 pauses every request for
// the time TMDB asks.
type Client struct {
	key       string
	bearer    bool
	base      string
	imageBase string
	lang      string
	http      *http.Client
	timeout   time.Duration
	maxWait   time.Duration
	log       *slog.Logger
	slots     chan struct{}
	bucket    *bucket

	mu         sync.Mutex
	pauseUntil time.Time

	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

// NewClient returns a client. An empty key is allowed (every call then fails
// with an unauthorized error, as TMDB would answer it).
func NewClient(o Options) *Client {
	c := &Client{
		key:       strings.TrimSpace(o.Key),
		base:      strings.TrimRight(or(o.BaseURL, DefaultBaseURL), "/"),
		imageBase: strings.TrimRight(or(o.ImageBaseURL, DefaultImageBaseURL), "/"),
		lang:      or(o.Language, "en-US"),
		http:      o.HTTPClient,
		timeout:   o.Timeout,
		maxWait:   o.MaxWait,
		log:       o.Logger,
		now:       time.Now,
		sleep:     sleepCtx,
	}
	c.bearer = strings.Contains(c.key, ".")
	if c.http == nil {
		c.http = &http.Client{}
	}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	if c.maxWait <= 0 {
		c.maxWait = DefaultMaxWait
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	rate := o.Rate
	if rate <= 0 {
		rate = DefaultRate
	}
	inFlight := o.MaxInFlight
	if inFlight <= 0 {
		inFlight = DefaultMaxInFlight
	}
	c.slots = make(chan struct{}, inFlight)
	c.bucket = newBucket(rate, int(rate), c.now)
	return c
}

func or(v, def string) string {
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	return def
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Language is the TMDB language the client asks for ("en-US").
func (c *Client) Language() string { return c.lang }

// Region is the country of the language ("US" for "en-US"), for certifications.
func (c *Client) Region() string {
	if _, region, ok := strings.Cut(c.lang, "-"); ok {
		return strings.ToUpper(region)
	}
	return "US"
}

func (c *Client) langCode() string {
	code, _, _ := strings.Cut(c.lang, "-")
	return strings.ToLower(code)
}

// ImageURL is the address of an image: size is "w342", "original", ...; file
// is TMDB's file name with or without the leading slash. A size not on the
// allowlist or a file that is not a plain TMDB image name gives "": nothing
// that comes from TMDB can steer the address elsewhere.
func (c *Client) ImageURL(size, file string) string {
	if !ValidSize(size) || !ValidImagePath(file) {
		return ""
	}
	return c.imageBase + "/" + size + "/" + strings.TrimPrefix(file, "/")
}

// Error is a failed TMDB request. It never contains the API key.
type Error struct {
	Op         string // "GET /movie/603"
	Status     int    // HTTP status; 0 when there was no response
	Message    string // TMDB's status_message, or the cause
	Transient  bool   // worth retrying later: network, timeout, 429, 5xx, a broken response
	RetryAfter time.Duration
	Err        error // the cause, when there was no response
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("tmdb ")
	b.WriteString(e.Op)
	if e.Status != 0 {
		b.WriteString(": " + strconv.Itoa(e.Status) + " " + http.StatusText(e.Status))
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// IsTransient reports whether err is a TMDB failure that may pass: a network
// error or timeout, 429, a 5xx, or a response that could not be read. A
// cancelled context and permanent failures (404, 401) are not transient.
func IsTransient(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Transient
}

// IsNotFound reports whether TMDB answered 404.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// IsUnauthorized reports whether TMDB refused the key (401 or 403).
func IsUnauthorized(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden)
}

// RetryDelay is how long to wait before trying again after the failures-th
// consecutive failure (1 for the first): Backoff, or longer when TMDB said how
// long to stay away, but never longer than the longest Backoff.
func RetryDelay(err error, failures int) time.Duration {
	d := Backoff(failures)
	var e *Error
	if errors.As(err, &e) && e.RetryAfter > d {
		d = min(e.RetryAfter, backoffSteps[len(backoffSteps)-1])
	}
	return d
}

// backoffSteps are the waits after the first, second, ... failure.
var backoffSteps = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}

// Backoff is the wait before retrying after the failures-th consecutive
// failure: 1m, 5m, 30m, 2h, then 6h.
func Backoff(failures int) time.Duration {
	i := min(max(failures, 1), len(backoffSteps)) - 1
	return backoffSteps[i]
}

// bucket is a token bucket: it holds up to burst tokens and gains rate per
// second; a request takes one and waits when there is none.
type bucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
	now    func() time.Time
}

func newBucket(rate float64, burst int, now func() time.Time) *bucket {
	return &bucket{rate: rate, burst: float64(max(burst, 1)), tokens: float64(max(burst, 1)), last: now(), now: now}
}

// take reserves a token and returns how long to wait before using it.
func (b *bucket) take() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	b.last = now
	b.tokens--
	if b.tokens >= 0 {
		return 0
	}
	return time.Duration(-b.tokens / b.rate * float64(time.Second))
}

// acquire waits for a free slot, for any pause TMDB asked for and for a token,
// and returns the function that gives the slot back.
func (c *Client) acquire(ctx context.Context) (release func(), err error) {
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release = func() { <-c.slots }
	for {
		c.mu.Lock()
		wait := c.pauseUntil.Sub(c.now())
		c.mu.Unlock()
		if wait <= 0 {
			break
		}
		if wait > c.maxWait {
			// A pause that began while this call queued for a slot, longer than we
			// are willing to sit out: give the caller the 429 at once.
			release()
			return nil, rateLimited("", wait)
		}
		if err := c.sleep(ctx, wait); err != nil {
			release()
			return nil, err
		}
	}
	if wait := c.bucket.take(); wait > 0 {
		if err := c.sleep(ctx, wait); err != nil {
			release()
			return nil, err
		}
	}
	return release, nil
}

// rateLimited is the error of a call refused because TMDB asked us to stay away.
func rateLimited(op string, wait time.Duration) *Error {
	return &Error{Op: op, Status: http.StatusTooManyRequests, Transient: true, RetryAfter: wait, Message: "rate limited"}
}

func (c *Client) pauseLeft() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pauseUntil.Sub(c.now())
}

func (c *Client) pause(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if until := c.now().Add(d); until.After(c.pauseUntil) {
		c.pauseUntil = until
	}
}

// redact removes the key from text that came from elsewhere (a cause, a body).
func (c *Client) redact(s string) string {
	if c.key == "" {
		return s
	}
	for _, k := range []string{c.key, url.QueryEscape(c.key), url.PathEscape(c.key)} {
		s = strings.ReplaceAll(s, k, "***")
	}
	return s
}

// get fetches path (below the base URL) with the query parameters q and decodes
// the JSON answer into out.
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	op := "GET " + path
	params := url.Values{}
	maps.Copy(params, q)
	if params.Get("language") == "" {
		params.Set("language", c.lang)
	}
	if !c.bearer && c.key != "" {
		params.Set("api_key", c.key)
	}
	target := c.base + path + "?" + params.Encode()

	for attempt := 0; ; attempt++ {
		// Fail at once, rather than sit out a long pause, when TMDB asked us to
		// stay away for longer than we are willing to wait.
		if wait := c.pauseLeft(); wait > c.maxWait {
			return rateLimited(op, wait)
		}
		release, err := c.acquire(ctx)
		if err != nil {
			if rl, ok := errors.AsType[*Error](err); ok {
				rl.Op = op
				return rl
			}
			return fmt.Errorf("tmdb %s: %w", op, err)
		}
		start := c.now()
		status, header, body, err := c.do(ctx, target)
		var wait time.Duration
		if err == nil && status == http.StatusTooManyRequests {
			// Record the pause before the slot is given back: a queued call that takes
			// the slot in between must see it, or it sends a second request.
			wait = retryAfter(header.Get("Retry-After"), c.now())
			c.pause(wait)
		}
		release()
		if err != nil {
			return c.failure(ctx, op, err)
		}
		c.log.LogAttrs(ctx, slog.LevelDebug, "tmdb request", slog.String("path", path),
			slog.Int("status", status), slog.Duration("took", c.now().Sub(start)))

		switch {
		case status == http.StatusOK:
			if err := json.Unmarshal(body, out); err != nil {
				return &Error{Op: op, Status: status, Transient: true, Message: "malformed response", Err: err}
			}
			return nil
		case status == http.StatusTooManyRequests:
			c.log.LogAttrs(ctx, slog.LevelWarn, "tmdb rate limited", slog.String("path", path),
				slog.Duration("retry_after", wait), slog.Int("attempt", attempt+1))
			if attempt < maxRateRetries && wait <= c.maxWait {
				continue
			}
			return &Error{Op: op, Status: status, Transient: true, RetryAfter: wait, Message: c.message(body)}
		default:
			e := &Error{Op: op, Status: status, Transient: status >= 500 || status == http.StatusRequestTimeout || status == http.StatusTooEarly, Message: c.message(body)}
			if status >= 500 {
				c.log.LogAttrs(ctx, slog.LevelWarn, "tmdb server error", slog.String("path", path), slog.Int("status", status))
			}
			return e
		}
	}
}

// do performs one attempt under the per-attempt timeout.
func (c *Client) do(ctx context.Context, target string) (status int, header http.Header, body []byte, err error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ply")
	if c.bearer {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return 0, nil, nil, err
	}
	return resp.StatusCode, resp.Header, body, nil
}

// failure turns a transport error into an Error without the URL net/http puts
// in its errors, which has the key in it. A cancelled caller is not a failure
// of TMDB: that error is returned as it is.
func (c *Client) failure(ctx context.Context, op string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("tmdb %s: %w", op, ctx.Err())
	}
	if ue, ok := errors.AsType[*url.Error](err); ok {
		err = ue.Err
	}
	msg := c.redact(err.Error())
	if errors.Is(err, context.DeadlineExceeded) {
		msg = "timed out"
	}
	// The cause is kept for errors.Is/As but only in a form that cannot print the key.
	return &Error{Op: op, Transient: true, Message: msg, Err: causeOf(err, msg)}
}

// causeOf is the error to keep as the cause of an Error: the error itself when
// its text is safe to show, else a plain error with the redacted text (the
// original must not stay reachable through errors.Unwrap, where anything that
// walks the chain would print the key). Timeouts and cancellations keep their
// identity.
func causeOf(err error, safe string) error {
	if err.Error() == safe {
		return err
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		return context.Canceled
	}
	return errors.New(safe)
}

// message extracts TMDB's status_message from an error body.
func (c *Client) message(body []byte) string {
	var e struct {
		Message string `json:"status_message"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	msg := c.redact(strings.TrimSpace(e.Message))
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

// maxRetryAfter is the longest Retry-After that is believed. A server that asks
// for more (or for so much that the number overflows a Duration) is asked again
// in an hour; a pause of days would silence the client until it is restarted.
const maxRetryAfter = time.Hour

// retryAfter reads a Retry-After header: seconds, or an HTTP date. Missing or
// unreadable means two seconds, and nothing is longer than maxRetryAfter.
func retryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	secs, err := strconv.ParseInt(v, 10, 64)
	switch {
	case err == nil && secs >= 0:
		// Cut the number before it is multiplied: 9223372037 seconds overflows.
		return time.Duration(min(secs, int64(maxRetryAfter/time.Second))) * time.Second
	case errors.Is(err, strconv.ErrRange) && !strings.HasPrefix(v, "-"):
		return maxRetryAfter // more seconds than there are int64s
	}
	if t, err := http.ParseTime(v); err == nil {
		return min(max(t.Sub(now), 0), maxRetryAfter)
	}
	return 2 * time.Second
}

// Configuration fetches /configuration; it is also the cheapest call that
// shows whether the key works.
func (c *Client) Configuration(ctx context.Context) (*Configuration, error) {
	var out Configuration
	if err := c.get(ctx, "/configuration", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SearchMovies searches /search/movie. year 0 means any.
func (c *Client) SearchMovies(ctx context.Context, query string, year int) ([]MovieResult, error) {
	q := url.Values{"query": {query}, "include_adult": {"false"}}
	if year > 0 {
		q.Set("year", strconv.Itoa(year))
	}
	var out searchMovies
	if err := c.get(ctx, "/search/movie", q, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// SearchTV searches /search/tv. year 0 means any.
func (c *Client) SearchTV(ctx context.Context, query string, year int) ([]TVResult, error) {
	q := url.Values{"query": {query}, "include_adult": {"false"}}
	if year > 0 {
		q.Set("first_air_date_year", strconv.Itoa(year))
	}
	var out searchTV
	if err := c.get(ctx, "/search/tv", q, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// imageLanguages is the include_image_language parameter: artwork with text in
// our language, and textless artwork.
func (c *Client) imageLanguages() string { return c.langCode() + ",null" }

// MovieDetails fetches /movie/{id} with credits, release dates, external ids
// and images.
func (c *Client) MovieDetails(ctx context.Context, id int64) (*MovieDetails, error) {
	q := url.Values{
		"append_to_response":     {"credits,release_dates,external_ids,images"},
		"include_image_language": {c.imageLanguages()},
	}
	var out MovieDetails
	if err := c.get(ctx, "/movie/"+strconv.FormatInt(id, 10), q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TVDetails fetches /tv/{id} with aggregate credits, content ratings, external
// ids and images.
func (c *Client) TVDetails(ctx context.Context, id int64) (*TVDetails, error) {
	q := url.Values{
		"append_to_response":     {"aggregate_credits,content_ratings,external_ids,images"},
		"include_image_language": {c.imageLanguages()},
	}
	var out TVDetails
	if err := c.get(ctx, "/tv/"+strconv.FormatInt(id, 10), q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SeasonDetails fetches /tv/{id}/season/{n}.
func (c *Client) SeasonDetails(ctx context.Context, tvID int64, season int) (*SeasonDetails, error) {
	var out SeasonDetails
	if err := c.get(ctx, "/tv/"+strconv.FormatInt(tvID, 10)+"/season/"+strconv.Itoa(season), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Videos fetches /movie/{id}/videos (kind store.KindMovie) or /tv/{id}/videos: the videos in our
// language, in English and with no language.
func (c *Client) Videos(ctx context.Context, kind string, id int64) ([]Video, error) {
	path := "/movie/"
	if kind == store.KindSeries {
		path = "/tv/"
	}
	q := url.Values{"include_video_language": {c.langCode() + ",en,null"}}
	var out videoList
	if err := c.get(ctx, path+strconv.FormatInt(id, 10)+"/videos", q, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// PersonDetails fetches /person/{id}.
func (c *Client) PersonDetails(ctx context.Context, id int64) (*PersonDetails, error) {
	var out PersonDetails
	if err := c.get(ctx, "/person/"+strconv.FormatInt(id, 10), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CollectionDetails fetches /collection/{id}.
func (c *Client) CollectionDetails(ctx context.Context, id int64) (*CollectionDetails, error) {
	var out CollectionDetails
	if err := c.get(ctx, "/collection/"+strconv.FormatInt(id, 10), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// External id sources of Find.
const (
	ExternalIMDb = "imdb_id"
	ExternalTVDB = "tvdb_id"
)

// Find looks a title up by an external id (an IMDb id like "tt0133093", or a
// TheTVDB id) with /find/{id}.
func (c *Client) Find(ctx context.Context, source, id string) (*FindResult, error) {
	var out FindResult
	if err := c.get(ctx, "/find/"+url.PathEscape(id), url.Values{"external_source": {source}}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MovieChanges returns the ids of the movies that changed between start and
// end (at most 14 days apart), following every page.
func (c *Client) MovieChanges(ctx context.Context, start, end time.Time) ([]int64, error) {
	return c.changes(ctx, "/movie/changes", start, end)
}

// TVChanges is MovieChanges for shows.
func (c *Client) TVChanges(ctx context.Context, start, end time.Time) ([]int64, error) {
	return c.changes(ctx, "/tv/changes", start, end)
}

// ErrChangesTruncated means the changes feed has more pages than are followed.
// The list is not returned: one that stops short would pass for the whole of
// what changed, and the changes after it would never be looked at.
var ErrChangesTruncated = errors.New("metadata: TMDB's changes feed is longer than the pages that are followed")

func (c *Client) changes(ctx context.Context, path string, start, end time.Time) ([]int64, error) {
	var ids []int64
	for page := 1; ; page++ {
		q := url.Values{
			"start_date": {start.UTC().Format("2006-01-02")},
			"end_date":   {end.UTC().Format("2006-01-02")},
			"page":       {strconv.Itoa(page)},
		}
		var out changesPage
		if err := c.get(ctx, path, q, &out); err != nil {
			return nil, err
		}
		for _, r := range out.Results {
			ids = append(ids, r.ID)
		}
		if page >= out.TotalPages {
			return ids, nil
		}
		if page >= maxChangePages {
			return nil, ErrChangesTruncated
		}
	}
}
