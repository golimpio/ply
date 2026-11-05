package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"ply/internal/store"
)

// MDBList (https://mdblist.com) gives the ratings of Rotten Tomatoes, Metacritic, Letterboxd and
// Trakt by TMDB id or IMDb id (docs/M10-RATINGS.md). The calls ply makes are
//
//	GET {base}/{tmdb|imdb}/{movie|show}/{id}?apikey=KEY   a title and its "ratings"
//	GET {base}/user?apikey=KEY                            the key's daily allowance and use
//
// Both shapes are as MDBList's clients use them (Kometa reads exactly these fields); the
// documentation pages could not be read when this was written.
const (
	DefaultMDBListBaseURL = "https://api.mdblist.com"

	// DefaultMDBListRate is requests per second: MDBList allows 1,000 reads in 5 minutes.
	DefaultMDBListRate = 2

	maxMDBListMessage = 200

	// mdblistRateWait is how long every request waits after a 200 answer that says "API Rate Limit
	// Reached!": MDBList's limit per period, which a minute clears.
	mdblistRateWait = time.Minute
)

// MDBListOptions configures NewMDBListClient.
type MDBListOptions struct {
	// Key is the account's API key. It goes in the apikey query parameter, so it is cut out of
	// every error and log line.
	Key     string
	BaseURL string // default DefaultMDBListBaseURL; must be https, except on this machine

	// HTTPClient is copied, not changed. Unless it has a CheckRedirect of its own, redirects are
	// not followed: a 3xx is an error, so that the key never goes where the base URL does not say.
	HTTPClient  *http.Client
	Timeout     time.Duration // per request attempt; default DefaultTimeout
	Rate        float64       // requests per second; default DefaultMDBListRate
	MaxInFlight int           // default 2
	MaxWait     time.Duration // longest Retry-After waited out in place; default DefaultMaxWait

	Logger *slog.Logger // default slog.Default(). Never receives the key.
}

// String, GoString and LogValue leave the key out of anything that prints or logs the options.
func (o MDBListOptions) String() string {
	return fmt.Sprintf("MDBListOptions{Key: %s, BaseURL: %q}", tvdbMask(o.Key), o.BaseURL)
}

func (o MDBListOptions) GoString() string { return o.String() }

func (o MDBListOptions) LogValue() slog.Value {
	return slog.GroupValue(slog.String("key", strings.Trim(tvdbMask(o.Key), `"`)), slog.String("base_url", o.BaseURL))
}

// MDBListClient talks to MDBList. It is safe for concurrent use. Requests are paced by a token
// bucket and capped in flight, and a 429 pauses every request for the time MDBList asks (for the
// rest of the UTC day when it is the daily allowance that ran out).
type MDBListClient struct {
	key     string
	base    string
	baseErr string // why base cannot be used, or ""
	http    *http.Client
	timeout time.Duration
	maxWait time.Duration
	log     *slog.Logger
	slots   chan struct{}
	bucket  *bucket

	mu         sync.Mutex
	pauseUntil time.Time

	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

func (c *MDBListClient) String() string { return fmt.Sprintf("MDBListClient{BaseURL: %q}", c.base) }

func (c *MDBListClient) GoString() string { return c.String() }

func (c *MDBListClient) LogValue() slog.Value {
	return slog.GroupValue(slog.String("base_url", c.base))
}

// checkMDBListBase says why base cannot be talked to: the key is in the query, so it must be
// https. A server on this machine is let off, for tests.
func checkMDBListBase(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "MDBList base URL is not valid"
	}
	switch u.Scheme {
	case "https":
		return ""
	case "http":
		if host := u.Hostname(); strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback() {
			return ""
		}
	}
	return "MDBList base URL must use https"
}

// NewMDBListClient returns a client. It does not touch the network. An empty key is allowed (every
// call then fails as unauthorized, without a request), and so is a base URL that cannot be used
// (every call then fails without a request, saying why).
func NewMDBListClient(o MDBListOptions) *MDBListClient {
	c := &MDBListClient{
		key:     strings.TrimSpace(o.Key),
		base:    strings.TrimRight(or(o.BaseURL, DefaultMDBListBaseURL), "/"),
		timeout: o.Timeout,
		maxWait: o.MaxWait,
		log:     o.Logger,
		now:     time.Now,
		sleep:   sleepCtx,
	}
	c.baseErr = checkMDBListBase(c.base)
	if o.HTTPClient == nil {
		c.http = &http.Client{CheckRedirect: noRedirect}
	} else {
		cp := *o.HTTPClient
		if cp.CheckRedirect == nil {
			cp.CheckRedirect = noRedirect
		}
		c.http = &cp
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
		rate = DefaultMDBListRate
	}
	inFlight := o.MaxInFlight
	if inFlight <= 0 {
		inFlight = 2
	}
	c.slots = make(chan struct{}, inFlight)
	c.bucket = newBucket(rate, int(max(rate, 1)), c.now)
	return c
}

// MDBListError is a failed MDBList request. It never contains the key. errors.As finds it as an
// *Error too (see As), so IsTransient, IsNotFound, IsUnauthorized and RetryDelay read it.
type MDBListError struct {
	Op         string // "GET /tmdb/movie/603"
	Status     int    // HTTP status; 0 when there was no response
	Message    string // MDBList's message, or the cause
	Transient  bool   // worth retrying later: network, timeout, 429, 5xx, a broken response
	RetryAfter time.Duration
	// Daily is set when the key's allowance for the day is used up: nothing will work until
	// RetryAfter has passed (the next UTC midnight).
	Daily bool
	Err   error // the cause, when there was no response
}

func (e *MDBListError) Error() string {
	if e == nil {
		return "mdblist: <nil>"
	}
	var b strings.Builder
	b.WriteString("mdblist ")
	b.WriteString(e.Op)
	if e.Status != 0 {
		b.WriteString(": " + strconv.Itoa(e.Status) + " " + http.StatusText(e.Status))
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	return b.String()
}

func (e *MDBListError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// As lets errors.As(err, &*Error) see an MDBListError as the *Error the TMDB client's helpers
// read, so a failure of any provider is classified by the same code.
func (e *MDBListError) As(target any) bool {
	t, ok := target.(**Error)
	if !ok || e == nil {
		return false
	}
	*t = &Error{Op: e.Op, Status: e.Status, Message: e.Message, Transient: e.Transient, RetryAfter: e.RetryAfter, Err: e.Err}
	return true
}

// IsMDBListDaily reports whether err says the key's allowance for the day is used up, and until when.
func IsMDBListDaily(err error) (until time.Duration, ok bool) {
	var e *MDBListError
	if errors.As(err, &e) && e.Daily {
		return e.RetryAfter, true
	}
	return 0, false
}

// Gate returns, without sending anything, the error a request would fail with while MDBList has
// the client paused for longer than it waits in place; nil otherwise. A caller that counts its
// requests checks it first, so that a request that is not sent is not counted.
func (c *MDBListClient) Gate() error { return c.gate("") }

func (c *MDBListClient) gate(op string) error {
	if wait := c.pauseLeft(); wait > c.maxWait {
		return &MDBListError{Op: op, Status: http.StatusTooManyRequests, Transient: true, RetryAfter: wait, Message: "rate limited",
			Daily: wait > time.Hour}
	}
	return nil
}

// untilMidnightUTC is how long until the day (UTC) ends, when MDBList's daily count starts again
// (one minute more, for the clocks).
func untilMidnightUTC(now time.Time) time.Duration {
	n := now.UTC()
	next := time.Date(n.Year(), n.Month(), n.Day()+1, 0, 1, 0, 0, time.UTC)
	return next.Sub(n)
}

func (c *MDBListClient) pauseLeft() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pauseUntil.Sub(c.now())
}

func (c *MDBListClient) pause(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if until := c.now().Add(d); until.After(c.pauseUntil) {
		c.pauseUntil = until
	}
}

// acquire waits for a free slot, for any pause MDBList asked for and for a token of the bucket,
// and returns the function that gives the slot back.
func (c *MDBListClient) acquire(ctx context.Context) (release func(), err error) {
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release = func() { <-c.slots }
	for {
		wait := c.pauseLeft()
		if wait <= 0 {
			break
		}
		if wait > c.maxWait {
			release()
			return nil, &MDBListError{Status: http.StatusTooManyRequests, Transient: true, RetryAfter: wait, Message: "rate limited"}
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

// redact removes the key from text that came from elsewhere (a cause, a body).
func (c *MDBListClient) redact(s string) string {
	if c.key == "" {
		return s
	}
	for _, k := range []string{c.key, url.QueryEscape(c.key), url.PathEscape(c.key)} {
		s = strings.ReplaceAll(s, k, "***")
	}
	return s
}

// get fetches path (below the base URL) and decodes the JSON answer into out.
func (c *MDBListClient) get(ctx context.Context, path string, out any) error {
	op := "GET " + path
	if c.baseErr != "" {
		return &MDBListError{Op: op, Message: c.baseErr}
	}
	if c.key == "" {
		return &MDBListError{Op: op, Status: http.StatusUnauthorized, Message: "no MDBList key is set"}
	}
	target := c.base + path + "?" + url.Values{"apikey": {c.key}}.Encode()

	for attempt := 0; ; attempt++ {
		// Fail at once, rather than sit out a long pause, when MDBList asked us to stay away
		// for longer than we are willing to wait.
		if err := c.gate(op); err != nil {
			return err
		}
		release, err := c.acquire(ctx)
		if err != nil {
			var me *MDBListError
			if errors.As(err, &me) {
				me.Op = op
				return me
			}
			return fmt.Errorf("mdblist %s: %w", op, err)
		}
		start := c.now()
		status, header, body, err := c.do(ctx, target)
		var wait time.Duration
		var daily bool
		if err == nil && status == http.StatusTooManyRequests {
			// Record the pause before the slot is given back: a queued call that takes the slot
			// in between must see it, or it sends a second request.
			if daily = isDailyLimit(c.message(body)); daily {
				wait = untilMidnightUTC(c.now())
			} else {
				wait = retryAfter(header.Get("Retry-After"), c.now())
			}
			c.pause(wait)
		}
		release()
		if err != nil {
			return c.failure(ctx, op, err)
		}
		c.log.LogAttrs(ctx, slog.LevelDebug, "mdblist request", slog.String("path", path),
			slog.Int("status", status), slog.Duration("took", c.now().Sub(start)))

		switch {
		case status == http.StatusOK:
			// MDBList answers some failures with 200 and {"response": false, "error": "..."}.
			var fail struct {
				Response *bool  `json:"response"`
				Error    string `json:"error"`
			}
			if json.Unmarshal(body, &fail) == nil && fail.Response != nil && !*fail.Response {
				return c.failedAnswer(op, fail.Error)
			}
			if err := json.Unmarshal(body, out); err != nil {
				return &MDBListError{Op: op, Status: status, Transient: true, Message: "malformed response",
					Err: causeOf(err, c.redact(err.Error()))}
			}
			return nil
		case status == http.StatusTooManyRequests:
			c.log.LogAttrs(ctx, slog.LevelWarn, "mdblist rate limited", slog.String("path", path),
				slog.Duration("retry_after", wait), slog.Bool("daily", daily), slog.Int("attempt", attempt+1))
			if !daily && attempt < maxRateRetries && wait <= c.maxWait {
				continue
			}
			return &MDBListError{Op: op, Status: status, Transient: true, RetryAfter: wait, Daily: daily, Message: c.message(body)}
		case status >= 300 && status < 400:
			return &MDBListError{Op: op, Status: status, Message: "redirect not followed"}
		default:
			e := &MDBListError{Op: op, Status: status, Transient: status >= 500 || status == http.StatusRequestTimeout || status == http.StatusTooEarly,
				Message: c.message(body)}
			if status >= 500 {
				c.log.LogAttrs(ctx, slog.LevelWarn, "mdblist server error", slog.String("path", path), slog.Int("status", status))
			}
			return e
		}
	}
}

// failedAnswer is the error of a 200 answer that says it failed.
func (c *MDBListClient) failedAnswer(op, msg string) error {
	msg = tidyMessage(c.redact(msg), maxMDBListMessage)
	if isDailyLimit(msg) {
		wait := untilMidnightUTC(c.now())
		c.pause(wait)
		return &MDBListError{Op: op, Status: http.StatusTooManyRequests, Transient: true, RetryAfter: wait, Daily: true, Message: msg}
	}
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "rate limit"):
		c.pause(mdblistRateWait)
		return &MDBListError{Op: op, Status: http.StatusTooManyRequests, Transient: true, RetryAfter: mdblistRateWait, Message: msg}
	case strings.Contains(low, "invalid api") || strings.Contains(low, "apikey") || strings.Contains(low, "api key"):
		return &MDBListError{Op: op, Status: http.StatusUnauthorized, Message: msg}
	case strings.Contains(low, "not found") || strings.Contains(low, "no result"):
		return &MDBListError{Op: op, Status: http.StatusNotFound, Message: msg}
	}
	return &MDBListError{Op: op, Message: msg, Status: http.StatusBadGateway}
}

// isDailyLimit reports whether an error message says the day's allowance is used up.
func isDailyLimit(msg string) bool {
	low := strings.ToLower(msg)
	return strings.Contains(low, "daily") || strings.Contains(low, "api limit reached")
}

// do performs one attempt under the per-attempt timeout.
func (c *MDBListClient) do(ctx context.Context, target string) (status int, header http.Header, body []byte, err error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ply")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return 0, nil, nil, err
	}
	if len(body) > maxResponseBytes {
		return resp.StatusCode, resp.Header, nil, errors.New("response too large")
	}
	return resp.StatusCode, resp.Header, body, nil
}

// failure turns a transport error into an MDBListError without the URL net/http puts in its
// errors, which has the key in it. A cancelled caller is not a failure of MDBList.
func (c *MDBListClient) failure(ctx context.Context, op string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("mdblist %s: %w", op, ctx.Err())
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	msg := tidyMessage(c.redact(err.Error()), maxMDBListMessage)
	if errors.Is(err, context.DeadlineExceeded) {
		msg = "timed out"
	}
	return &MDBListError{Op: op, Transient: true, Message: msg, Err: causeOf(err, msg)}
}

// message extracts MDBList's error text from a body: {"error": "..."} or {"message": "..."}.
func (c *MDBListClient) message(body []byte) string {
	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	return tidyMessage(c.redact(firstNonEmpty(e.Error, e.Message)), maxMDBListMessage)
}

// ---- answers ----

// flexNum is a number that may be sent as a string, or null.
type flexNum struct {
	V     float64
	Valid bool
}

func (n *flexNum) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	*n = flexNum{}
	switch {
	case len(b) == 0 || string(b) == "null":
		return nil
	case b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return nil // "N/A": no number
		}
		*n = flexNum{V: v, Valid: true}
	default:
		var v float64
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*n = flexNum{V: v, Valid: true}
	}
	return nil
}

// MDBListRating is an entry of a title's "ratings".
type MDBListRating struct {
	Source string  `json:"source"` // imdb, tmdb, metacritic, tomatoes, tomatoesaudience, letterboxd, trakt, ...
	Value  flexNum `json:"value"`  // on the source's own scale
	Score  flexNum `json:"score"`  // MDBList's own 0-100 reading of it
	Votes  flexNum `json:"votes"`
	URL    string  `json:"url"`
	// State is Rotten Tomatoes' own word for the score, when MDBList gives one ("fresh",
	// "certified fresh", "upright", "verified hot", ...).
	State  string `json:"state"`
	Status string `json:"status"`
}

// MDBListTitle is the answer for one title.
type MDBListTitle struct {
	Title   string          `json:"title"`
	Year    flexInt         `json:"year"`
	Type    string          `json:"type"`
	Ratings []MDBListRating `json:"ratings"`
}

// MDBList media types and id providers.
const (
	MDBListMovie = "movie"
	MDBListShow  = "show"
)

var reIMDbID = regexp.MustCompile(`^tt\d{1,12}$`)

// ByTMDB asks for a title by its TMDB id; media is MDBListMovie or MDBListShow.
func (c *MDBListClient) ByTMDB(ctx context.Context, media string, id int64) (*MDBListTitle, error) {
	if (media != MDBListMovie && media != MDBListShow) || id <= 0 {
		return nil, fmt.Errorf("mdblist: no title to look up (%s %d)", media, id)
	}
	var out MDBListTitle
	if err := c.get(ctx, "/tmdb/"+media+"/"+strconv.FormatInt(id, 10), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ByIMDb asks for a title by its IMDb id ("tt0133093").
func (c *MDBListClient) ByIMDb(ctx context.Context, media, imdbID string) (*MDBListTitle, error) {
	if (media != MDBListMovie && media != MDBListShow) || !reIMDbID.MatchString(imdbID) {
		return nil, fmt.Errorf("mdblist: no title to look up (%s %q)", media, imdbID)
	}
	var out MDBListTitle
	if err := c.get(ctx, "/imdb/"+media+"/"+imdbID, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MDBListLimits is the key's allowance: Limit requests a day, Used so far today. Limit is 0 when
// the answer does not say.
type MDBListLimits struct {
	Limit int
	Used  int
}

// Limits asks GET /user for the key's daily allowance and use. It is also the cheapest call that
// shows whether the key works.
func (c *MDBListClient) Limits(ctx context.Context) (MDBListLimits, error) {
	var out struct {
		Requests flexInt `json:"api_requests"`
		Count    flexInt `json:"api_requests_count"`
	}
	if err := c.get(ctx, "/user", &out); err != nil {
		return MDBListLimits{}, err
	}
	return MDBListLimits{Limit: int(max(out.Requests, 0)), Used: int(max(out.Count, 0))}, nil
}

// Check shows whether the key works.
func (c *MDBListClient) Check(ctx context.Context) error {
	_, err := c.Limits(ctx)
	return err
}

// ---- mapping ----

// mdblistKinds maps MDBList's source names to ours and their scales.
var mdblistKinds = map[string]struct {
	source string
	scale  float64
}{
	"tomatoes":         {store.RatingRTCritics, 100},
	"tomatoesaudience": {store.RatingRTAudience, 100},
	"popcorn":          {store.RatingRTAudience, 100},
	"metacritic":       {store.RatingMetacritic, 100},
	"letterboxd":       {store.RatingLetterboxd, 5},
	"trakt":            {store.RatingTrakt, 100},
}

// ratingHosts are the sites a rating's link may go to: the link comes from outside and is shown to
// people as one.
var ratingHosts = map[string]string{
	store.RatingRTCritics:  "rottentomatoes.com",
	store.RatingRTAudience: "rottentomatoes.com",
	store.RatingMetacritic: "metacritic.com",
	store.RatingLetterboxd: "letterboxd.com",
	store.RatingTrakt:      "trakt.tv",
}

// cleanRatingURL returns raw when it is an https link to the source's own site, else "".
func cleanRatingURL(source, raw string) string {
	host := ratingHosts[source]
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || host == "" || u.Scheme != "https" || u.User != nil || len(raw) > 512 {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	if u.Port() != "" || (h != host && !strings.HasSuffix(h, "."+host)) {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

// rtState is Rotten Tomatoes' word for a score: the one MDBList gave when it is one we know,
// else fresh/rotten (critics) or upright/spilled (audience) by the 60% line that Rotten
// Tomatoes uses. Certified and verified hot are only given when MDBList says so: they need review
// counts that are not in the answer.
func rtState(source string, value float64, given string) string {
	g := strings.ToLower(strings.TrimSpace(given))
	g = strings.NewReplacer(" ", "_", "-", "_").Replace(g)
	switch source {
	case store.RatingRTCritics:
		switch g {
		case store.StateFresh, store.StateRotten, store.StateCertified:
			return g
		case "certified_fresh":
			return store.StateCertified
		}
		if value >= 60 {
			return store.StateFresh
		}
		return store.StateRotten
	case store.RatingRTAudience:
		switch g {
		case store.StateUpright, store.StateSpilled, store.StateVerifiedHot:
			return g
		}
		if value >= 60 {
			return store.StateUpright
		}
		return store.StateSpilled
	}
	return ""
}

// StoreRatings maps the answer to the ratings ply keeps (Rotten Tomatoes critics and audience,
// Metacritic, Letterboxd, Trakt), stamped fetchedAt. IMDb's and TMDB's are not taken: they have
// sources of their own. An entry with no value, one below its scale, or a source seen before is
// left out. Letterboxd's value is /5; a value above 5 (some answers give /10) is halved. A value
// above the scale of its source is on some other scale than ours: MDBList's own reading of it, from
// 0 to 100 ("score"), is used instead when it has one.
func (t *MDBListTitle) StoreRatings(fetchedAt int64) []store.Rating {
	var out []store.Rating
	seen := map[string]bool{}
	for _, r := range t.Ratings {
		k, ok := mdblistKinds[strings.ToLower(r.Source)]
		if !ok || seen[k.source] || !r.Value.Valid {
			continue
		}
		v := r.Value.V
		if k.source == store.RatingLetterboxd && v > 5 && v <= 10 {
			v /= 2
		}
		if v > k.scale && r.Score.Valid && r.Score.V > 0 {
			v = r.Score.V * k.scale / 100
		}
		if !(v > 0 && v <= k.scale) {
			continue
		}
		seen[k.source] = true
		rating := store.Rating{Source: k.source, Value: v, Scale: k.scale, URL: cleanRatingURL(k.source, r.URL), FetchedAt: fetchedAt}
		if r.Votes.Valid && r.Votes.V > 0 {
			rating.Votes = int(min(r.Votes.V, 1<<31))
		}
		if k.source == store.RatingRTCritics || k.source == store.RatingRTAudience {
			rating.State = rtState(k.source, v, firstNonEmpty(r.State, r.Status))
		}
		out = append(out, rating)
	}
	return out
}
