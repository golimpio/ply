package tlscert

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"ply/internal/tlscert/tlstest"
)

var writePair = tlstest.Pair

func TestLoadPicksTheHost(t *testing.T) {
	dir := t.TempDir()
	cf, kf := writePair(t, dir, 1, "*.home.example.com", "Ply.Home.Example.com", "other.example.com")
	c, err := Load(cf, kf, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Host() != "ply.home.example.com" {
		t.Errorf("host %q, want the first name that is not a wildcard, lower case", c.Host())
	}
	if got, want := c.Names(), []string{".home.example.com", "ply.home.example.com", "other.example.com"}; !slices.Equal(got, want) {
		t.Errorf("names %q, want %q (a wildcard as -allow-host takes it)", got, want)
	}
	c, err = Load(cf, kf, "tv.home.example.com", nil) // under the wildcard
	if err != nil || c.Host() != "tv.home.example.com" {
		t.Errorf("-tls-host under the wildcard: %v, %q", err, c.Host())
	}
}

func TestLoadRefusesWhatClientsCouldNotVerify(t *testing.T) {
	dir := t.TempDir()
	cf, kf := writePair(t, dir, 1, "*.home.example.com")
	if _, err := Load(cf, kf, "", nil); err == nil || !strings.Contains(err.Error(), "-tls-host") {
		t.Errorf("a wildcard alone without -tls-host: %v, want it asked for", err)
	}
	if _, err := Load(cf, kf, "ply.elsewhere.org", nil); err == nil {
		t.Error("a -tls-host the certificate is not for was taken")
	}
	if _, err := Load(cf, kf, "ply.home.example.com", nil); err != nil { // control
		t.Fatal(err)
	}
	if _, err := Load(cf, filepath.Join(dir, "missing.pem"), "", nil); err == nil {
		t.Error("a missing key file was taken")
	}
	other := t.TempDir()
	_, otherKey := writePair(t, other, 2, "ply.home.example.com")
	if _, err := Load(cf, otherKey, "ply.home.example.com", nil); err == nil {
		t.Error("a key of another certificate was taken")
	}
	noName, noNameKey := writePair(t, t.TempDir(), 3)
	if _, err := Load(noName, noNameKey, "", nil); err == nil {
		t.Error("a certificate for no DNS name was taken")
	}
}

// A renewal rewrites the files; the next handshake after checkEvery uses the new certificate,
// and a pair that does not read yet (the key not written) keeps the old one.
func TestARenewedCertificateIsUsedWithoutARestart(t *testing.T) {
	dir := t.TempDir()
	cf, kf := writePair(t, dir, 1, "ply.example.com")
	c, err := Load(cf, kf, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	c.now = func() time.Time { return now }
	serial := func() int64 {
		t.Helper()
		cert, err := c.GetCertificate(nil)
		if err != nil {
			t.Fatal(err)
		}
		return cert.Leaf.SerialNumber.Int64()
	}
	if s := serial(); s != 1 {
		t.Fatalf("serial %d, want 1", s)
	}

	key, _ := os.ReadFile(kf)
	writePair(t, dir, 2, "ply.example.com")
	later := func(f string) { // a renewal's files are newer, whatever the file system's resolution
		t.Helper()
		if err := os.Chtimes(f, now.Add(time.Hour), now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	later(cf)
	if err := os.WriteFile(kf, key, 0o600); err != nil { // the old key: the pair does not match yet
		t.Fatal(err)
	}
	now = now.Add(checkEvery)
	if s := serial(); s != 1 {
		t.Fatalf("half renewed: serial %d, want the old certificate kept", s)
	}

	writePair(t, dir, 3, "ply.example.com")
	later(cf)
	later(kf)
	if s := serial(); s != 1 {
		t.Fatalf("renewed, within checkEvery of the last look: serial %d, want 1", s)
	}
	now = now.Add(checkEvery)
	if s := serial(); s != 3 {
		t.Fatalf("renewed: serial %d, want 3", s)
	}

	// A certificate for another name: clients connect to the host, so it is not used, and the log
	// says so.
	var logged bytes.Buffer
	c.log = slog.New(slog.NewTextHandler(&logged, nil))
	writePair(t, dir, 4, "other.example.com")
	now = now.Add(checkEvery)
	later(cf)
	later(kf)
	if s := serial(); s != 3 {
		t.Errorf("a renewal for another name: serial %d, want 3 kept", s)
	}
	if !strings.Contains(logged.String(), "not used") {
		t.Errorf("log %q, want the refusal said", logged.String())
	}

	// A renewal that cannot be read at the moment (a passing error, here the key's permissions) is
	// used at the next look once it can, though the files did not change again.
	writePair(t, dir, 5, "ply.example.com")
	now = now.Add(checkEvery)
	later(cf)
	later(kf)
	if err := os.Chmod(kf, 0); err != nil {
		t.Fatal(err)
	}
	if s := serial(); s != 3 {
		t.Errorf("renewal not readable: serial %d, want 3 kept", s)
	}
	if err := os.Chmod(kf, 0o600); err != nil {
		t.Fatal(err)
	}
	now = now.Add(checkEvery)
	if s := serial(); s != 5 {
		t.Errorf("renewal readable again: serial %d, want 5", s)
	}
}
