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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// TheTVDB's API and the host of its images.
const (
	DefaultTVDBBaseURL = "https://api4.thetvdb.com/v4"
	TVDBImageHost      = "artworks.thetvdb.com"
)

// Defaults and limits of TVDBOptions. Timeout, MaxWait and MaxInFlight default
// as the TMDB client's do.
const (
	DefaultTVDBRate = 10                  // requests per second; TVDB publishes no limit
	tvdbTokenLife   = 25 * 24 * time.Hour // a token is good for a month; it is renewed after this
	tvdbRejectTTL   = time.Minute         // how long a refused key or PIN is remembered
	maxTVDBPages    = 200
	maxTVDBTokens   = 4   // tokens kept to cut out of error text
	maxTVDBMessage  = 200 // bytes of TVDB's message kept in an error
)

// tvdbRejected is what a refused key or PIN is called, to the user.
const tvdbRejected = "TVDB rejected the key or PIN"

// tvdbSeasonTypes are the orders /series/{id}/episodes/{season-type} knows.
var tvdbSeasonTypes = []string{"default", "official", "dvd", "absolute", "alternate", "regional"}

// errTVDBTooLarge is a body that is longer than maxResponseBytes.
var errTVDBTooLarge = errors.New("response too large")

// TVDBOptions configures NewTVDBClient.
type TVDBOptions struct {
	// Key is the project's API key. PIN is the user's subscriber PIN: a key under
	// the user-supported model may work without one, and a licensed key needs
	// none. An empty PIN is left out of the login.
	Key string
	PIN string

	BaseURL  string // default DefaultTVDBBaseURL; must be https, except on this machine
	Language string // "en-US" or a TVDB code ("eng"); default "en-US"

	// HTTPClient is copied, not changed. Unless it has a CheckRedirect of its
	// own, redirects are not followed: a 3xx is an error, so that the key and
	// the token never go anywhere the base URL does not say.
	HTTPClient  *http.Client  // default: a client with no overall timeout (see Timeout)
	Timeout     time.Duration // per request attempt; default DefaultTimeout
	Rate        float64       // requests per second; default DefaultTVDBRate
	MaxInFlight int           // default DefaultMaxInFlight
	MaxWait     time.Duration // longest Retry-After waited out in place; default DefaultMaxWait

	Logger *slog.Logger // default slog.Default(). Never receives the key, the PIN or the token.
}

// tvdbMask shows whether a secret is set, and nothing else of it.
func tvdbMask(s string) string {
	if s == "" {
		return `""`
	}
	return `"***"`
}

// String, GoString and LogValue leave the key and the PIN out of anything that
// prints or logs the options.
func (o TVDBOptions) String() string {
	return fmt.Sprintf("TVDBOptions{Key: %s, PIN: %s, BaseURL: %q, Language: %q}", tvdbMask(o.Key), tvdbMask(o.PIN), o.BaseURL, o.Language)
}

func (o TVDBOptions) GoString() string { return o.String() }

func (o TVDBOptions) LogValue() slog.Value {
	return slog.GroupValue(slog.String("key", strings.Trim(tvdbMask(o.Key), `"`)), slog.String("pin", strings.Trim(tvdbMask(o.PIN), `"`)),
		slog.String("base_url", o.BaseURL), slog.String("language", o.Language))
}

// TVDBClient talks to the TVDB v4 API. It is safe for concurrent use. It logs in
// when it first needs to and keeps the token in memory only; requests are paced
// by a token bucket and capped in flight, and a 429 pauses every request for the
// time TVDB asks.
type TVDBClient struct {
	key     string
	pin     string
	base    string
	baseErr string // why base cannot be used, or ""
	lang    string
	http    *http.Client
	timeout time.Duration
	maxWait time.Duration
	log     *slog.Logger
	slots   chan struct{}
	bucket  *bucket

	mu         sync.Mutex // guards pauseUntil
	pauseUntil time.Time

	loginSlot chan struct{} // one slot, held while logging in, so concurrent calls log in once

	authMu      sync.Mutex // guards the fields below
	token       string
	tokenAt     time.Time
	seen        []string // the last few tokens, to cut out of error text
	rejectErr   *TVDBError
	rejectUntil time.Time

	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

// String, GoString and LogValue leave the key, the PIN and the token out of
// anything that prints or logs the client.
func (c *TVDBClient) String() string {
	return fmt.Sprintf("TVDBClient{BaseURL: %q, Language: %q}", c.base, c.lang)
}

func (c *TVDBClient) GoString() string { return c.String() }

func (c *TVDBClient) LogValue() slog.Value {
	return slog.GroupValue(slog.String("base_url", c.base), slog.String("language", c.lang))
}

// noRedirect makes net/http hand back a 3xx as it is.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// checkTVDBBase says why base cannot be talked to: it carries the key, the PIN
// and the token, so it must be https. A server on this machine is let off, for
// tests and for a local mirror.
func checkTVDBBase(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "TVDB base URL is not valid"
	}
	switch u.Scheme {
	case "https":
		return ""
	case "http":
		if host := u.Hostname(); strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback() {
			return ""
		}
	}
	return "TVDB base URL must use https"
}

// NewTVDBClient returns a client. It does not touch the network: it logs in
// with the first call. An empty key is allowed (every call then fails with an
// unauthorized error, without a request), and so is a base URL that cannot be
// used (every call then fails without a request, saying why).
func NewTVDBClient(o TVDBOptions) *TVDBClient {
	c := &TVDBClient{
		key:       strings.TrimSpace(o.Key),
		pin:       strings.TrimSpace(o.PIN),
		base:      strings.TrimRight(or(o.BaseURL, DefaultTVDBBaseURL), "/"),
		lang:      or(o.Language, "en-US"),
		timeout:   o.Timeout,
		maxWait:   o.MaxWait,
		log:       o.Logger,
		loginSlot: make(chan struct{}, 1),
		now:       time.Now,
		sleep:     sleepCtx,
	}
	c.baseErr = checkTVDBBase(c.base)
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
		rate = DefaultTVDBRate
	}
	inFlight := o.MaxInFlight
	if inFlight <= 0 {
		inFlight = DefaultMaxInFlight
	}
	c.slots = make(chan struct{}, inFlight)
	c.bucket = newBucket(rate, int(rate), c.now)
	return c
}

// Language is the language the client asks for, as it was given ("en-US").
func (c *TVDBClient) Language() string { return c.lang }

// LanguageCode is Language as TVDB's code ("eng").
func (c *TVDBClient) LanguageCode() string { return c.langCode("") }

// langCode is the TVDB code for lang; the client's own language when lang is
// empty, and English for one TVDB has no code for. The code is always letters,
// so it is safe in a path.
func (c *TVDBClient) langCode(lang string) string {
	if strings.TrimSpace(lang) == "" {
		lang = c.lang
	}
	if code := tvdbLanguage(lang); code != "" {
		return code
	}
	return "eng"
}

// tvdbLanguages maps the two-letter code of a language (ISO 639-1) to the code
// TVDB uses (ISO 639-2/T: "deu", not "ger"; TVDB's /languages lists them). The
// ISO 639-2/B spellings of the languages that have two are mapped to TVDB's too.
// Portuguese is the odd one: TVDB's "pt" is the Portuguese of Brazil, and its
// "por" the Portuguese of Portugal.
var tvdbLanguages = map[string]string{
	"af": "afr", "ar": "ara", "az": "aze", "be": "bel", "bg": "bul", "bn": "ben", "bs": "bos",
	"ca": "cat", "cs": "ces", "cy": "cym", "da": "dan", "de": "deu", "el": "ell",
	"en": "eng", "eo": "epo", "es": "spa", "et": "est", "eu": "eus", "fa": "fas",
	"fi": "fin", "fr": "fra", "ga": "gle", "gl": "glg", "gu": "guj", "he": "heb",
	"hi": "hin", "hr": "hrv", "hu": "hun", "hy": "hye", "id": "ind", "is": "isl",
	"it": "ita", "ja": "jpn", "ka": "kat", "kk": "kaz", "km": "khm", "kn": "kan",
	"ko": "kor", "la": "lat", "lb": "ltz", "lt": "lit", "lv": "lav", "mk": "mkd",
	"ml": "mal", "mn": "mon", "mr": "mar", "ms": "msa", "mt": "mlt", "my": "mya",
	"nb": "nor", "ne": "nep", "nl": "nld", "nn": "nor", "no": "nor", "pa": "pan",
	"pl": "pol", "pt": "pt", "ro": "ron", "ru": "rus", "si": "sin", "sk": "slk",
	"sl": "slv", "so": "som", "sq": "sqi", "sr": "srp", "sv": "swe", "sw": "swa",
	"ta": "tam", "te": "tel", "th": "tha", "tl": "tgl", "tr": "tur", "uk": "ukr",
	"ur": "urd", "uz": "uzb", "vi": "vie", "yi": "yid", "zh": "zho",
	"iw": "heb", "in": "ind", "fil": "tgl", // the old codes of Hebrew and Indonesian; Filipino is Tagalog to TVDB

	"alb": "sqi", "arm": "hye", "baq": "eus", "bur": "mya", "chi": "zho", "cze": "ces",
	"dut": "nld", "fre": "fra", "geo": "kat", "ger": "deu", "gre": "ell", "ice": "isl",
	"mac": "mkd", "may": "msa", "per": "fas", "rum": "ron", "slo": "slk", "tib": "bod",
	"wel":  "cym",
	"zhtw": "zhtw", // TVDB's own code for Chinese of Taiwan
}

// Regions where Portuguese is the Portuguese of Portugal.
var portugalPortuguese = []string{"pt", "ao", "mz", "cv", "gw", "st", "tl", "mo"}

// tvdbLanguage maps a BCP 47 tag ("en-US", "pt_BR", "zh-Hant") or a language
// code to the code TVDB uses ("eng", "pt", "zho"). A code of three letters that
// is not in the table is taken to be TVDB's already. A language it does not
// know gives "". Only the language and, for Chinese and Portuguese, the region
// or script count: TVDB has one Chinese for the mainland and one for Taiwan
// and Hong Kong, and one Portuguese for Brazil and one for Portugal.
func tvdbLanguage(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	tag = strings.ReplaceAll(tag, "_", "-")
	lang, rest, _ := strings.Cut(tag, "-")
	subtags := strings.Split(rest, "-")
	switch lang {
	case "zh":
		if slices.ContainsFunc(subtags, func(s string) bool { return s == "tw" || s == "hk" || s == "mo" || s == "hant" }) {
			return "zhtw"
		}
	case "pt":
		if slices.ContainsFunc(subtags, func(s string) bool { return slices.Contains(portugalPortuguese, s) }) {
			return "por"
		}
	}
	if code, ok := tvdbLanguages[lang]; ok {
		return code
	}
	if len(lang) == 3 && isLetters(lang) {
		return lang
	}
	return ""
}

func isLetters(s string) bool {
	for _, r := range s {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return s != ""
}

// TVDBError is a failed TVDB request. It never contains the key, the PIN or
// the token. It is what IsTransient, IsNotFound, IsUnauthorized and RetryDelay
// look for: errors.As finds it as an *Error.
type TVDBError struct {
	Op         string // "GET /series/81189/extended"
	Status     int    // HTTP status; 0 when there was no response
	Message    string // TVDB's message, or the cause
	Transient  bool   // worth retrying later: network, timeout, 429, 5xx, a broken response
	RetryAfter time.Duration
	Err        error // the cause, when there was no response
}

func (e *TVDBError) Error() string {
	if e == nil {
		return "tvdb: <nil>"
	}
	var b strings.Builder
	b.WriteString("tvdb ")
	b.WriteString(e.Op)
	if e.Status != 0 {
		b.WriteString(": " + strconv.Itoa(e.Status) + " " + http.StatusText(e.Status))
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	return b.String()
}

func (e *TVDBError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// As lets errors.As(err, &*Error) see a TVDBError as the *Error that the TMDB
// client's helpers (IsTransient, IsNotFound, IsUnauthorized, RetryDelay) read,
// so a failure of either provider is classified by the same code.
//
// A 403 that is not a refusal of the key or PIN (tvdbRejected) is about one
// record, say a show for subscribers only. IsUnauthorized would call it a bad key
// and the worker would end its pass on it, so it is handed out as a final failure
// with no status: neither a refusal, nor a 404, nor transient. The worker then
// puts that one item off and goes on. The TVDBError itself keeps its 403.
func (e *TVDBError) As(target any) bool {
	t, ok := target.(**Error)
	if !ok || e == nil {
		return false
	}
	status, msg := e.Status, e.Message
	if status == http.StatusForbidden && msg != tvdbRejected {
		status = 0
		msg = strings.TrimSuffix("forbidden for this record: "+msg, ": ")
	}
	*t = &Error{Op: e.Op, Status: status, Message: msg, Transient: e.Transient, RetryAfter: e.RetryAfter, Err: e.Err}
	return true
}

// ErrTVDBTruncated means a list came back incomplete: it has more pages than
// are followed, or fewer entries than TVDB counted in it. The list is not
// returned: one that stops short would pass for the whole of it.
var ErrTVDBTruncated = errors.New("metadata: a TVDB list is incomplete: it has more pages than are followed, or fewer entries than TVDB counted")

// tvdbRateLimited is the error of a call refused because TVDB asked us to stay away.
func tvdbRateLimited(op string, wait time.Duration) *TVDBError {
	return &TVDBError{Op: op, Status: http.StatusTooManyRequests, Transient: true, RetryAfter: wait, Message: "rate limited"}
}

// tvdbRefused is the error of a key or PIN TVDB does not accept: final, not transient.
func tvdbRefused(op string, status int) *TVDBError {
	return &TVDBError{Op: op, Status: status, Message: tvdbRejected}
}

// acquire waits for a free slot, for any pause TVDB asked for and for a token
// of the bucket, and returns the function that gives the slot back.
func (c *TVDBClient) acquire(ctx context.Context) (release func(), err error) {
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
			// A pause that began while this call queued for a slot, longer than we
			// are willing to sit out: give the caller the 429 at once.
			release()
			return nil, tvdbRateLimited("", wait)
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

func (c *TVDBClient) pauseLeft() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pauseUntil.Sub(c.now())
}

func (c *TVDBClient) pause(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if until := c.now().Add(d); until.After(c.pauseUntil) {
		c.pauseUntil = until
	}
}

// redact removes the key, the PIN and the tokens from text that came from
// elsewhere (a cause, a body). The longest go first, so that a secret that
// contains another is not left half showing.
func (c *TVDBClient) redact(s string) string {
	c.authMu.Lock()
	secrets := append([]string{c.key, c.pin}, c.seen...)
	c.authMu.Unlock()
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	for _, k := range secrets {
		if k == "" {
			continue
		}
		for _, f := range []string{k, url.QueryEscape(k), url.PathEscape(k)} {
			s = strings.ReplaceAll(s, f, "***")
		}
	}
	return s
}

// cachedToken is the token while it is young enough to use.
func (c *TVDBClient) cachedToken() (string, bool) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if c.token == "" || c.now().Sub(c.tokenAt) >= tvdbTokenLife {
		return "", false
	}
	return c.token, true
}

func (c *TVDBClient) setToken(tok string) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	c.token, c.tokenAt = tok, c.now()
	c.rejectErr = nil
	c.seen = append(c.seen, tok)
	if len(c.seen) > maxTVDBTokens {
		c.seen = c.seen[len(c.seen)-maxTVDBTokens:]
	}
}

// rejection is the refusal of the key or PIN, or of a token just made, while it
// is remembered: a bad key or PIN is not tried again on every call. It reads as
// the error of op.
func (c *TVDBClient) rejection(op string) error {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if c.rejectErr == nil || !c.now().Before(c.rejectUntil) {
		return nil
	}
	e := *c.rejectErr
	e.Op = op
	return &e
}

func (c *TVDBClient) reject(err *TVDBError) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	e := *err
	c.rejectErr, c.rejectUntil = &e, c.now().Add(tvdbRejectTTL)
}

// bearer is the token to send, logging in when there is none worth keeping.
func (c *TVDBClient) bearer(ctx context.Context) (string, error) {
	if tok, ok := c.cachedToken(); ok {
		return tok, nil
	}
	return c.login(ctx, "", false)
}

// login logs in and returns the token. Calls are serialised, and a call that
// waited finds the token the one before it got: stale is the token the caller
// has just seen refused, and a different one in the cache is used as it is.
// force logs in whatever is cached (Check).
func (c *TVDBClient) login(ctx context.Context, stale string, force bool) (string, error) {
	const op = "POST /login"
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("tvdb %s: %w", op, err)
	}
	select {
	case c.loginSlot <- struct{}{}:
	case <-ctx.Done():
		return "", fmt.Errorf("tvdb %s: %w", op, ctx.Err())
	}
	defer func() { <-c.loginSlot }()

	if c.key == "" {
		return "", &TVDBError{Op: op, Status: http.StatusUnauthorized, Message: "no TVDB key is set"}
	}
	if !force {
		if tok, ok := c.cachedToken(); ok && tok != stale {
			return tok, nil
		}
		if err := c.rejection(op); err != nil {
			return "", err
		}
	}

	// The PIN is left out when there is none: a licensed key, or a user-supported
	// one that works without, is logged in with the key alone.
	creds := struct {
		APIKey string `json:"apikey"`
		PIN    string `json:"pin,omitempty"`
	}{c.key, c.pin}
	reqBody, err := json.Marshal(creds)
	if err != nil {
		return "", &TVDBError{Op: op, Message: "cannot encode the login", Err: errors.New("encoding failed")}
	}
	status, body, err := c.exchange(ctx, op, http.MethodPost, "/login", nil, reqBody, "")
	if err != nil {
		return "", err
	}
	switch status {
	case http.StatusOK:
		var out struct {
			Token string `json:"token"`
		}
		if err := c.decode(op, status, body, &out, nil); err != nil {
			return "", err
		}
		if out.Token == "" {
			return "", &TVDBError{Op: op, Status: status, Transient: true, Message: "malformed response: no token"}
		}
		c.setToken(out.Token)
		c.log.LogAttrs(ctx, slog.LevelDebug, "tvdb logged in")
		return out.Token, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		err := tvdbRefused(op, status)
		c.reject(err)
		return "", err
	default:
		return "", c.errorFor(ctx, op, status, body)
	}
}

// Check logs in, whatever token is held: the cheapest call that shows whether
// the key and PIN work. It always asks TVDB, even right after a refusal; its
// answer, good or bad, is what later calls go by.
func (c *TVDBClient) Check(ctx context.Context) error {
	_, err := c.login(ctx, "", true)
	return err
}

// get fetches path (below the base URL) with the query parameters q and decodes
// the "data" of the answer into out, and its "links" into links when that is not
// nil. A token TVDB refuses (401) is renewed, once, and the request sent again;
// a second 401 means the key or PIN is no good, and is remembered for a minute.
// A 403 is about that record: it is returned as it is, with TVDB's message.
func (c *TVDBClient) get(ctx context.Context, path string, q url.Values, out any, links *tvdbLinks) error {
	op := "GET " + path
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("tvdb %s: %w", op, err)
	}
	if err := c.rejection(op); err != nil {
		return err
	}
	token, err := c.bearer(ctx)
	if err != nil {
		return err
	}
	for renewed := false; ; renewed = true {
		status, body, err := c.exchange(ctx, op, http.MethodGet, path, q, nil, token)
		if err != nil {
			return err
		}
		switch {
		case status == http.StatusOK:
			return c.decode(op, status, body, out, links)
		case status != http.StatusUnauthorized:
			return c.errorFor(ctx, op, status, body)
		case renewed:
			err := tvdbRefused(op, status)
			c.reject(err)
			return err
		}
		c.log.LogAttrs(ctx, slog.LevelInfo, "tvdb token refused, logging in again", slog.String("path", path), slog.Int("status", status))
		if token, err = c.login(ctx, token, false); err != nil {
			return err
		}
	}
}

// exchange sends one request through the limiter and returns the status and
// body of TVDB's answer, whatever the status. A 429 is waited out and tried
// again a few times, as TMDB's are; one that cannot be is an error.
func (c *TVDBClient) exchange(ctx context.Context, op, method, path string, q url.Values, reqBody []byte, token string) (int, []byte, error) {
	if c.baseErr != "" {
		return 0, nil, &TVDBError{Op: op, Message: c.baseErr}
	}
	target := c.base + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	for attempt := 0; ; attempt++ {
		// Fail at once, rather than sit out a long pause, when TVDB asked us to
		// stay away for longer than we are willing to wait.
		if wait := c.pauseLeft(); wait > c.maxWait {
			return 0, nil, tvdbRateLimited(op, wait)
		}
		release, err := c.acquire(ctx)
		if err != nil {
			if rl, ok := errors.AsType[*TVDBError](err); ok {
				rl.Op = op
				return 0, nil, rl
			}
			return 0, nil, fmt.Errorf("tvdb %s: %w", op, err)
		}
		start := c.now()
		status, header, body, err := c.do(ctx, method, target, reqBody, token)
		var wait time.Duration
		if err == nil && status == http.StatusTooManyRequests {
			// Record the pause before the slot is given back: a queued call that takes
			// the slot in between must see it, or it sends a second request.
			wait = retryAfter(header.Get("Retry-After"), c.now())
			c.pause(wait)
		}
		release()
		if err != nil {
			if errors.Is(err, errTVDBTooLarge) {
				// Asking again gets the same answer: final.
				return 0, nil, &TVDBError{Op: op, Status: status, Message: "response too large"}
			}
			return 0, nil, c.failure(ctx, op, err)
		}
		c.log.LogAttrs(ctx, slog.LevelDebug, "tvdb request", slog.String("path", path),
			slog.Int("status", status), slog.Duration("took", c.now().Sub(start)))

		if status != http.StatusTooManyRequests {
			return status, body, nil
		}
		c.log.LogAttrs(ctx, slog.LevelWarn, "tvdb rate limited", slog.String("path", path),
			slog.Duration("retry_after", wait), slog.Int("attempt", attempt+1))
		if attempt < maxRateRetries && wait <= c.maxWait {
			continue
		}
		return 0, nil, &TVDBError{Op: op, Status: status, Transient: true, RetryAfter: wait, Message: c.message(body)}
	}
}

// do performs one attempt under the per-attempt timeout. The token goes in a
// header and the key and PIN in the body, so nothing secret is in the URL that
// net/http puts in its errors.
func (c *TVDBClient) do(ctx context.Context, method, target string, reqBody []byte, token string) (status int, header http.Header, body []byte, err error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var rd io.Reader
	if reqBody != nil {
		rd = bytes.NewReader(reqBody)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ply")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
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
		return resp.StatusCode, resp.Header, nil, errTVDBTooLarge
	}
	return resp.StatusCode, resp.Header, body, nil
}

// decode reads a 200 answer: its "data" into out and its "links" into links.
func (c *TVDBClient) decode(op string, status int, body []byte, out any, links *tvdbLinks) error {
	env := struct {
		Data  any       `json:"data"`
		Links tvdbLinks `json:"links"`
	}{Data: out}
	if err := json.Unmarshal(body, &env); err != nil {
		return c.unreadable(op, status, err)
	}
	if links != nil {
		*links = env.Links
	}
	return nil
}

// unreadable is the error of a 200 answer that cannot be read. A body that is
// cut short or is not JSON may be better next time, so it is transient; JSON of
// a shape that does not fit says that TVDB's answers have changed, and asking
// again will not help.
func (c *TVDBClient) unreadable(op string, status int, err error) error {
	cause := causeOf(err, c.redact(err.Error()))
	if _, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		return &TVDBError{Op: op, Status: status, Message: "unexpected response shape", Err: cause}
	}
	return &TVDBError{Op: op, Status: status, Transient: true, Message: "malformed response", Err: cause}
}

// errorFor is the error of an answer that is not a 200.
func (c *TVDBClient) errorFor(ctx context.Context, op string, status int, body []byte) error {
	msg := c.message(body)
	if status >= 300 && status < 400 {
		msg = "redirect not followed"
	}
	e := &TVDBError{Op: op, Status: status, Transient: status >= 500 || status == http.StatusRequestTimeout || status == http.StatusTooEarly, Message: msg}
	if status >= 500 {
		c.log.LogAttrs(ctx, slog.LevelWarn, "tvdb server error", slog.String("op", op), slog.Int("status", status))
	}
	return e
}

// failure turns a transport error into a TVDBError without the URL net/http
// puts in its errors. A cancelled caller is not a failure of TVDB: that error
// is returned as it is.
func (c *TVDBClient) failure(ctx context.Context, op string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("tvdb %s: %w", op, ctx.Err())
	}
	if ue, ok := errors.AsType[*url.Error](err); ok {
		err = ue.Err
	}
	msg := tidyMessage(c.redact(err.Error()), maxTVDBMessage)
	if errors.Is(err, context.DeadlineExceeded) {
		msg = "timed out"
	}
	// The cause is kept for errors.Is/As but only in a form that cannot print a secret.
	return &TVDBError{Op: op, Transient: true, Message: msg, Err: causeOf(err, msg)}
}

// message extracts TVDB's message from an error body.
func (c *TVDBClient) message(body []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	return tidyMessage(c.redact(e.Message), maxTVDBMessage)
}

// tidyMessage makes text from outside fit in an error: valid UTF-8, on one
// line, without control characters, and at most max bytes, cut between runes.
func tidyMessage(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	return s
}

// Search searches /search for series. year 0 means any. The language is not a
// filter here: TVDB's language parameter restricts a search to shows whose
// primary language it is, so every result is returned, each with its names and
// overviews in other languages in Translations and Overviews.
func (c *TVDBClient) Search(ctx context.Context, query string, year int) ([]TVDBSearchResult, error) {
	q := url.Values{"query": {query}, "type": {"series"}}
	if year > 0 {
		q.Set("year", strconv.Itoa(year))
	}
	var out []TVDBSearchResult
	if err := c.get(ctx, "/search", q, &out, nil); err != nil {
		return nil, err
	}
	return out, nil
}

// SearchRemoteID looks a title up by the id it has at another site, such as an
// IMDb id like "tt0903747", with /search/remoteid/{id}.
func (c *TVDBClient) SearchRemoteID(ctx context.Context, remoteID string) ([]TVDBRemoteMatch, error) {
	remoteID = strings.TrimSpace(remoteID)
	if remoteID == "" {
		return nil, errors.New("tvdb: no remote id to look up")
	}
	var out []TVDBRemoteMatch
	if err := c.get(ctx, "/search/remoteid/"+url.PathEscape(remoteID), nil, &out, nil); err != nil {
		return nil, err
	}
	return out, nil
}

// SeriesExtended fetches /series/{id}/extended with every translation, the
// artworks and the characters.
func (c *TVDBClient) SeriesExtended(ctx context.Context, id int64) (*TVDBSeries, error) {
	op := "GET /series/" + strconv.FormatInt(id, 10) + "/extended"
	q := url.Values{"meta": {"translations"}, "short": {"false"}}
	var out TVDBSeries
	if err := c.get(ctx, "/series/"+strconv.FormatInt(id, 10)+"/extended", q, &out, nil); err != nil {
		return nil, err
	}
	if out.ID == 0 {
		return nil, &TVDBError{Op: op, Status: http.StatusOK, Transient: true, Message: "malformed response: no series"}
	}
	return &out, nil
}

// Episodes fetches every episode of a series with /series/{id}/episodes, one
// page after another until TVDB says there is no next. seasonType is the order:
// "official" (as aired), "dvd", "absolute", "default", "alternate" or
// "regional". lang is a BCP 47 tag or a TVDB code; empty is the client's own
// language. Names and overviews TVDB has no translation of come in the original.
//
// Each episode is returned once. TVDB says how many entries the whole list has
// (links.total_items); when the episodes that came do not add up to it, which
// happens when the list changes under the paging, the answer is
// ErrTVDBTruncated and not a list with holes.
func (c *TVDBClient) Episodes(ctx context.Context, id int64, seasonType, lang string) (*TVDBEpisodes, error) {
	seasonType = strings.ToLower(strings.TrimSpace(seasonType))
	if !slices.Contains(tvdbSeasonTypes, seasonType) {
		return nil, fmt.Errorf("tvdb: unknown season type %q", seasonType)
	}
	path := "/series/" + strconv.FormatInt(id, 10) + "/episodes/" + seasonType + "/" + c.langCode(lang)
	op := "GET " + path
	var all TVDBEpisodes
	seen := map[int64]bool{}
	var total int
	var counted bool
	for page := 0; ; page++ {
		var raw json.RawMessage
		var links tvdbLinks
		if err := c.get(ctx, path, url.Values{"page": {strconv.Itoa(page)}}, &raw, &links); err != nil {
			return nil, err
		}
		series, episodes, err := c.episodesPage(op, raw)
		if err != nil {
			return nil, err
		}
		if page == 0 {
			all.Series = series
		}
		for _, e := range episodes {
			if e.ID != 0 {
				if seen[e.ID] {
					continue
				}
				seen[e.ID] = true
			}
			all.Episodes = append(all.Episodes, e)
		}
		if n, ok := links.total(); ok {
			total, counted = n, true
		}
		if links.Next == "" {
			if counted && len(all.Episodes) != total {
				c.log.LogAttrs(ctx, slog.LevelWarn, "tvdb episodes do not add up", slog.String("path", path),
					slog.Int("got", len(all.Episodes)), slog.Int("counted", total))
				return nil, ErrTVDBTruncated
			}
			return &all, nil
		}
		if page+1 >= maxTVDBPages {
			return nil, ErrTVDBTruncated
		}
	}
}

// episodesPage reads a page of episodes in either of TVDB's shapes: with a
// language the series record is "data" itself, with "episodes" inside it; with
// none "data" is {series, episodes}.
func (c *TVDBClient) episodesPage(op string, raw json.RawMessage) (TVDBSeries, []TVDBEpisode, error) {
	var page tvdbEpisodesPage
	if err := json.Unmarshal(raw, &page); err != nil {
		return TVDBSeries{}, nil, c.unreadable(op, http.StatusOK, err)
	}
	if page.Series != nil && page.Series.ID != 0 {
		return *page.Series, page.Episodes, nil
	}
	var series TVDBSeries
	if err := json.Unmarshal(raw, &series); err != nil {
		return TVDBSeries{}, nil, c.unreadable(op, http.StatusOK, err)
	}
	return series, page.Episodes, nil
}

// Translation fetches /series/{id}/translations/{lang}: the name and overview
// in one language. TVDB answers 404 (IsNotFound) for a language it has none of.
func (c *TVDBClient) Translation(ctx context.Context, id int64, lang string) (*TVDBTranslation, error) {
	var out TVDBTranslation
	path := "/series/" + strconv.FormatInt(id, 10) + "/translations/" + c.langCode(lang)
	if err := c.get(ctx, path, nil, &out, nil); err != nil {
		return nil, err
	}
	return &out, nil
}

// Person fetches /people/{id}/extended.
func (c *TVDBClient) Person(ctx context.Context, id int64) (*TVDBPerson, error) {
	op := "GET /people/" + strconv.FormatInt(id, 10) + "/extended"
	var out TVDBPerson
	if err := c.get(ctx, "/people/"+strconv.FormatInt(id, 10)+"/extended", nil, &out, nil); err != nil {
		return nil, err
	}
	if out.ID == 0 {
		return nil, &TVDBError{Op: op, Status: http.StatusOK, Transient: true, Message: "malformed response: no person"}
	}
	return &out, nil
}

// Updates returns the ids of the series that were updated since the given time,
// following every page of /updates. Each id is listed once, in the order TVDB
// first mentions it. A record that says it is not about a series, or has no id,
// is left out. since must be a real time: the feed from the beginning of time
// is not what anyone means.
func (c *TVDBClient) Updates(ctx context.Context, since time.Time) ([]int64, error) {
	if since.IsZero() || since.Unix() <= 0 {
		return nil, errors.New("tvdb: Updates needs the time to look back to")
	}
	var ids []int64
	seen := map[int64]bool{}
	for page := 0; ; page++ {
		q := url.Values{
			"since":  {strconv.FormatInt(since.Unix(), 10)},
			"type":   {"series"},
			"action": {"update"},
			"page":   {strconv.Itoa(page)},
		}
		var out []tvdbUpdate
		var links tvdbLinks
		if err := c.get(ctx, "/updates", q, &out, &links); err != nil {
			return nil, err
		}
		for _, u := range out {
			if u.RecordID > 0 && u.isSeries() && !seen[u.RecordID] {
				seen[u.RecordID] = true
				ids = append(ids, u.RecordID)
			}
		}
		if links.Next == "" {
			return ids, nil
		}
		if page+1 >= maxTVDBPages {
			return nil, ErrTVDBTruncated
		}
	}
}

const (
	tvdbImageBase    = "https://" + TVDBImageHost
	maxTVDBImagePath = 512
)

// TVDBImagePath returns the path of a TVDB image with its leading slash
// ("/banners/posters/81189-1.jpg"), from that path (the slash is optional) or
// from a full artworks.thetvdb.com address; ok is false for anything else. A
// path is plain: letters, digits and "/", "_", ".", "-" only, with no empty
// segment, no "." or ".." segment and no ".." anywhere. An address must be to
// exactly the artworks host, without a port, user, query or fragment. A ref with
// no scheme and no leading slash whose first segment looks like a host
// ("evil.com/x.jpg") is refused rather than taken for a directory. Nothing that
// comes from TVDB can steer a download elsewhere.
func TVDBImagePath(ref string) (path string, ok bool) {
	p := ref
	for _, scheme := range []string{"https://", "http://"} {
		if len(ref) < len(scheme) || !strings.EqualFold(ref[:len(scheme)], scheme) {
			continue
		}
		rest := ref[len(scheme):]
		if len(rest) < len(TVDBImageHost) || !strings.EqualFold(rest[:len(TVDBImageHost)], TVDBImageHost) {
			return "", false
		}
		p = rest[len(TVDBImageHost):]
		if !strings.HasPrefix(p, "/") { // "artworks.thetvdb.com.evil.com", ":8080", "@evil.com"
			return "", false
		}
		break
	}
	if !strings.HasPrefix(p, "/") {
		if first, _, more := strings.Cut(p, "/"); more && strings.Contains(first, ".") {
			return "", false
		}
		p = "/" + p
	}
	if len(p) > maxTVDBImagePath || strings.HasSuffix(p, "/") || strings.Contains(p, "..") {
		return "", false
	}
	for i := 0; i < len(p); i++ {
		ch := p[i]
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '/' || ch == '_' || ch == '.' || ch == '-' {
			continue
		}
		return "", false
	}
	for seg := range strings.SplitSeq(p[1:], "/") {
		if seg == "" || seg == "." {
			return "", false
		}
	}
	return p, true
}

// ValidTVDBImagePath reports whether ref is a TVDB image that may be served: a
// plain path or an address on the artworks host, as TVDBImagePath defines.
func ValidTVDBImagePath(ref string) bool {
	_, ok := TVDBImagePath(ref)
	return ok
}

// TVDBImageURL is the address of an image: ref is TVDB's path or full address.
// small asks for the thumbnail, the same name with "_t" before the extension
// ("/banners/posters/81189-1_t.jpg"). A ref that is not a plain TVDB image
// gives "".
func TVDBImageURL(ref string, small bool) string {
	p, ok := TVDBImagePath(ref)
	if !ok {
		return ""
	}
	if small {
		p = tvdbThumbnail(p)
	}
	return tvdbImageBase + p
}

// tvdbThumbnail is the path of the thumbnail of the image at p; a thumbnail
// is its own thumbnail.
func tvdbThumbnail(p string) string {
	stem, ext := p, ""
	if i := strings.LastIndexByte(p, '.'); i > strings.LastIndexByte(p, '/')+1 {
		stem, ext = p[:i], p[i:]
	}
	if strings.HasSuffix(stem, "_t") {
		return p
	}
	return stem + "_t" + ext
}
