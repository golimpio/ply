package config

import (
	"bytes"
	"strings"
	"testing"
)

// The certificate and its key come together, from the flags or the environment; the host is
// only for them.
func TestTLSFlags(t *testing.T) {
	for _, n := range []string{TLSCertEnv, TLSKeyEnv, TLSHostEnv} {
		t.Setenv(n, "")
	}
	c, err := Parse([]string{"-data", t.TempDir()}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if c.TLSCert != "" || c.TLSKey != "" || c.TLSHost != "" {
		t.Errorf("defaults: %q %q %q, want plain HTTP", c.TLSCert, c.TLSKey, c.TLSHost)
	}

	c, err = Parse([]string{"-data", t.TempDir(), "-tls-cert", "c.pem", "-tls-key", "k.pem", "-tls-host", "ply.example.com"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if c.TLSCert != "c.pem" || c.TLSKey != "k.pem" || c.TLSHost != "ply.example.com" {
		t.Errorf("flags: %q %q %q", c.TLSCert, c.TLSKey, c.TLSHost)
	}

	for _, args := range [][]string{
		{"-tls-cert", "c.pem"},
		{"-tls-key", "k.pem"},
		{"-tls-host", "ply.example.com"},
	} {
		if _, err := Parse(append([]string{"-data", t.TempDir()}, args...), &bytes.Buffer{}); err == nil {
			t.Errorf("%q was taken", args)
		}
	}

	t.Setenv(TLSCertEnv, "env-c.pem")
	t.Setenv(TLSKeyEnv, "env-k.pem")
	c, err = Parse([]string{"-data", t.TempDir()}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if c.TLSCert != "env-c.pem" || c.TLSKey != "env-k.pem" {
		t.Errorf("environment: %q %q", c.TLSCert, c.TLSKey)
	}

	var out bytes.Buffer
	if _, err := Parse([]string{"-h"}, &out); err == nil {
		t.Fatal("-h should stop the parse")
	}
	for _, want := range []string{"-tls-cert", "-tls-key", "-tls-host", TLSCertEnv} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("usage does not mention %q", want)
		}
	}
}
