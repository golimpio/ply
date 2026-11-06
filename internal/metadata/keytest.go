package metadata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strconv"
	"strings"
	"time"
)

// The last test of a provider's key is kept with a fingerprint of the key it was made with, and
// counts only while that is the key in use: a key that is stored, replaced or removed, or that a
// flag or the environment now shadows, makes it stale, with nothing to remember to reset.
//
// It is kept in memory (Settings.tests), and also in the settings table, as one row per provider,
// settingKeyTestPrefix + the provider, whose value is "<fingerprint> <unix seconds> <ok|failed>" (one
// value, so that a reader never sees the time of one test with the result of another), but only
// when every secret that the fingerprint covers is itself kept in that table (keyTestPersists).
// A key or a PIN from a flag, the environment or the build is not, and a short PIN next to an
// unsalted fingerprint of it would be open to a guess made offline by whoever has the database. A
// result that is only in memory is lost at a restart; so be it.
const settingKeyTestPrefix = "keytest_"

// keyFingerprintBytes is how much of the SHA-256 a fingerprint keeps (hex-encoded, twice as long).
const keyFingerprintBytes = 8

// keyTestProviders are the providers whose key can be tested.
var keyTestProviders = []string{SourceTMDB, SourceTVDB, SourceMDBList}

// KeyTest is the last test of the key in use for a provider.
type KeyTest struct {
	// At is when it was made, in unix seconds; 0 when the key in use was never tested, or has
	// changed since.
	At int64
	// OK is whether the test passed; false when At is 0.
	OK bool
}

// keyTestRec is a test as Settings keeps it in memory.
type keyTestRec struct {
	fp string
	at int64
	ok bool
}

// fingerprint identifies a key (for TVDB, the key and the PIN, which are tested together)
// without keeping it: a prefix of a SHA-256 of the provider and the secrets. It is "" for no key.
// It is never logged or returned.
func fingerprint(provider string, secrets ...string) string {
	if len(secrets) == 0 || secrets[0] == "" {
		return ""
	}
	h := sha256.New()
	io.WriteString(h, provider)
	for _, secret := range secrets {
		h.Write([]byte{0}) // keys and PINs have no NUL, so the parts cannot run together
		io.WriteString(h, secret)
	}
	return hex.EncodeToString(h.Sum(nil)[:keyFingerprintBytes])
}

// keyFingerprint is the fingerprint of the key in use for a provider ("" for none, or for another
// provider). TVDB's covers the PIN too: its test logs in with both.
func (r Resolved) keyFingerprint(provider string) string {
	switch provider {
	case SourceTMDB:
		return fingerprint(provider, r.key)
	case SourceTVDB:
		return fingerprint(provider, r.tvdbKey, r.tvdbPIN)
	case SourceMDBList:
		return fingerprint(provider, r.mdblistKey)
	}
	return ""
}

// keyTestPersists reports whether the test of the key in use for a provider may go in the
// settings table: when each secret its fingerprint covers (for TVDB the key and, if there is one,
// the PIN) comes from that table.
func (r Resolved) keyTestPersists(provider string) bool {
	switch provider {
	case SourceTMDB:
		return r.KeySource == KeyFromSettings
	case SourceTVDB:
		return r.TVDBKeySource == KeyFromSettings && (r.tvdbPIN == "" || r.TVDBPINSource == KeyFromSettings)
	case SourceMDBList:
		return r.MDBListKeySource == KeyFromSettings
	}
	return false
}

// KeyTest returns the last test of the key r has in use for a provider: the zero KeyTest when
// there is no key, it was never tested, or the key (or, for TVDB, the PIN) is not the one the test
// was made with. r is what Resolve says now.
//
// A row in the settings table that cannot be this key's, because the key in use is not wholly kept
// there (keyTestPersists), is removed: it is from a time when it was.
func (s *Settings) KeyTest(ctx context.Context, r Resolved, provider string) KeyTest {
	want := r.keyFingerprint(provider)
	if want == "" {
		return KeyTest{}
	}
	s.testMu.Lock()
	defer s.testMu.Unlock()
	if rec, ok := s.tests[provider]; ok && rec.fp == want {
		return KeyTest{At: rec.at, OK: rec.ok}
	}
	if !r.keyTestPersists(provider) {
		s.dropKeyTestRow(ctx, provider)
		return KeyTest{}
	}
	return parseKeyTestRow(s.setting(ctx, settingKeyTestPrefix+provider), want)
}

// parseKeyTestRow reads a row of the settings table: the test it holds if it is of the key with the
// fingerprint want and well formed, and the zero KeyTest if not.
func parseKeyTestRow(row, want string) KeyTest {
	f := strings.Fields(row)
	if len(f) != 3 || f[0] != want || (f[2] != "ok" && f[2] != "failed") {
		return KeyTest{}
	}
	at, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil || at <= 0 {
		return KeyTest{}
	}
	return KeyTest{At: at, OK: f[2] == "ok"}
}

// recordKeyTest keeps the result of a test of the key with the given fingerprint, if that is still
// the key in use: a test that was slow, and ended after the key had been changed, says nothing of the
// new one and must not take the place of its result. A caller that went away (ctx done) did not get
// an answer, so nothing is kept for it. Not being able to keep the result is logged and goes no
// further: it is a note about a key, not part of the change.
func (s *Settings) recordKeyTest(ctx context.Context, provider, fp string, ok bool) {
	if ctx.Err() != nil {
		return
	}
	s.testMu.Lock()
	defer s.testMu.Unlock()
	s.recordKeyTestLocked(ctx, s.Resolve(ctx), provider, fp, ok)
}

// recordKeyTestLocked is recordKeyTest with testMu held and now, what Resolve says, in hand.
func (s *Settings) recordKeyTestLocked(ctx context.Context, now Resolved, provider, fp string, ok bool) {
	if now.keyFingerprint(provider) != fp {
		return
	}
	if s.tests == nil {
		s.tests = map[string]keyTestRec{}
	}
	at := time.Now().Unix()
	s.tests[provider] = keyTestRec{fp: fp, at: at, ok: ok}
	if !now.keyTestPersists(provider) {
		s.dropKeyTestRow(ctx, provider) // it is only in memory, and no row can stand for it
		return
	}
	state := "failed"
	if ok {
		state = "ok"
	}
	if err := s.o.Store.SetSetting(ctx, settingKeyTestPrefix+provider, fp+" "+strconv.FormatInt(at, 10)+" "+state); err != nil {
		s.o.Logger.Warn("could not keep the result of a key test", "provider", provider, "err", err)
	}
}

// dropKeyTestRow removes the row of a provider, if there is one. testMu is held.
func (s *Settings) dropKeyTestRow(ctx context.Context, provider string) {
	if ctx.Err() != nil {
		return
	}
	name := settingKeyTestPrefix + provider
	if _, there, err := s.o.Store.GetSetting(ctx, name); err != nil || !there {
		return
	}
	if err := s.o.Store.DeleteSetting(ctx, name); err != nil {
		s.o.Logger.Warn("could not clear the result of a key test", "provider", provider, "err", err)
	}
}

// failedCheck keeps a check that failed while a change was being made, as a failed test of the key
// checked, which counts only if that is the key in use (the key was not stored: saved again, it is
// the one in use). It hands err back.
func (s *Settings) failedCheck(ctx context.Context, provider, fp string, err error) error {
	s.recordKeyTest(ctx, provider, fp, false)
	return err
}

// afterUpdate settles the tests once a change has been stored. before is what was in use. A
// provider whose key in use is now another has no test (the result would be stale anyway; this
// makes the old one go, so that it does not come back if the old key does). passed holds, by
// provider, the fingerprint of a key that Update checked and that passed: it is kept as the test
// of the key in use if it is that key (recordKeyTestLocked looks), and not if a flag or the
// environment shadows it.
func (s *Settings) afterUpdate(ctx context.Context, before Resolved, passed map[string]string) {
	s.testMu.Lock()
	defer s.testMu.Unlock()
	after := s.Resolve(ctx)
	if ctx.Err() != nil {
		return // the reads may have failed, and an empty Resolved is not a change of keys
	}
	for _, p := range keyTestProviders {
		if before.keyFingerprint(p) != after.keyFingerprint(p) {
			delete(s.tests, p)
			s.dropKeyTestRow(ctx, p)
		}
		if fp := passed[p]; fp != "" {
			s.recordKeyTestLocked(ctx, after, p, fp, true)
		}
	}
}
