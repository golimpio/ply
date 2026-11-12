package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"ply/internal/config"
	"ply/internal/tlscert"
	"ply/internal/tlscert/tlstest"
)

// The certificate's names join the host check's (a public domain's name is refused otherwise),
// and its host is the one clients are told to connect to.
func TestLoadTLSAllowsTheCertificatesNames(t *testing.T) {
	cf, kf := tlstest.Pair(t, t.TempDir(), 1, "ply.home.example.org", "*.media.example.org")
	cfg := config.Config{TLSCert: cf, TLSKey: kf, AllowHosts: []string{"proxy.example.net"}}
	cert, err := loadTLS(&cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Host() != "ply.home.example.org" {
		t.Errorf("host %q", cert.Host())
	}
	if want := []string{"proxy.example.net", "ply.home.example.org", ".media.example.org"}; !slices.Equal(cfg.AllowHosts, want) {
		t.Errorf("allowed hosts %q, want %q", cfg.AllowHosts, want)
	}

	plain := config.Config{AllowHosts: []string{"proxy.example.net"}}
	if cert, err := loadTLS(&plain, nil); cert != nil || err != nil || len(plain.AllowHosts) != 1 {
		t.Errorf("without -tls-cert: %v, %v, %q; want plain HTTP and the hosts as they were", cert, err, plain.AllowHosts)
	}
	bad := config.Config{TLSCert: cf, TLSKey: cf}
	if _, err := loadTLS(&bad, nil); err == nil {
		t.Error("a certificate given as its own key was taken")
	}
}

// startServe serves a handler that answers with the protocol it was asked over, as ply's
// listener does (serve), and returns the listener's address.
func startServe(t *testing.T, cert *tlscert.Cert) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, r.Proto) })}
	done := make(chan error, 1)
	go func() { done <- serve(srv, ln, cert) }()
	t.Cleanup(func() {
		srv.Close()
		if err := <-done; err != http.ErrServerClosed {
			t.Errorf("serve: %v", err)
		}
	})
	return ln.Addr().String()
}

// With a certificate, ply speaks HTTPS, and HTTP/2 on it: the feed then shares one connection
// with everything else. Without one, plain HTTP as before.
func TestServeSpeaksHTTP2OverTLS(t *testing.T) {
	cf, kf := tlstest.Pair(t, t.TempDir(), 1, "ply.lan.test")
	cert, err := tlscert.Load(cf, kf, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	addr := startServe(t, cert)

	pemBytes, err := os.ReadFile(cf)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(pemBytes)
	client := func(serverName string) *http.Client {
		return &http.Client{Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{RootCAs: roots, ServerName: serverName},
			ForceAttemptHTTP2: true,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr) // the name leads here
			},
		}}
	}
	get := func(c *http.Client, url string) (string, error) {
		resp, err := c.Get(url)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return string(b), err
	}

	if proto, err := get(client("ply.lan.test"), "https://ply.lan.test/"); err != nil || proto != "HTTP/2.0" {
		t.Errorf("by the certificate's name: %q, %v; want HTTP/2.0", proto, err)
	}
	if _, err := get(client("other.lan.test"), "https://other.lan.test/"); err == nil {
		t.Error("a name the certificate is not for was verified")
	}
	if body, err := get(http.DefaultClient, "http://"+addr+"/"); err == nil && strings.HasPrefix(body, "HTTP/") {
		t.Errorf("plain HTTP was served beside HTTPS: %q", body)
	}

	plain := startServe(t, nil) // control: without a certificate, plain HTTP
	if proto, err := get(http.DefaultClient, "http://"+plain+"/"); err != nil || proto != "HTTP/1.1" {
		t.Errorf("without a certificate: %q, %v; want plain HTTP/1.1", proto, err)
	}
}
