// Package tlscert serves the certificate of ply's HTTPS listener (-tls-cert, -tls-key;
// docs/M11-CLIENTS.md, "HTTPS"): a pair of PEM files the owner keeps, read again when they
// change, so a renewed certificate is used without a restart.
package tlscert

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// checkEvery is how often a handshake looks at the files again, at most.
const checkEvery = time.Minute

// Cert is the certificate in use and the files it comes from.
type Cert struct {
	certFile, keyFile string
	host              string
	log               *slog.Logger
	now               func() time.Time

	mu      sync.Mutex
	cert    *tls.Certificate
	stamp   stamp // the files as they were last looked at
	checked time.Time
}

// stamp tells a change of the files: their modification times and sizes (a chain written in two
// steps within one tick of a coarse clock is longer the second time).
type stamp [2]struct{ mod, size int64 }

// Load reads the pair and works out the host name clients connect to: host when it is given (it
// must be one the certificate names; a wildcard certificate needs it), else the certificate's
// first DNS name that is not a wildcard. log hears of a renewal that cannot be used; nil is none.
func Load(certFile, keyFile, host string, log *slog.Logger) (*Cert, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	c := &Cert{certFile: certFile, keyFile: keyFile, log: log, now: time.Now}
	cert, stamp, err := c.read()
	if err != nil {
		return nil, err
	}
	if host == "" {
		for _, n := range cert.Leaf.DNSNames {
			if !strings.HasPrefix(n, "*.") {
				host = n
				break
			}
		}
		if host == "" {
			return nil, fmt.Errorf("%s names no host but wildcards %q: say which with -tls-host", certFile, cert.Leaf.DNSNames)
		}
	} else if err := cert.Leaf.VerifyHostname(host); err != nil {
		return nil, fmt.Errorf("-tls-host %q: %w", host, err)
	}
	c.host, c.cert, c.stamp, c.checked = strings.ToLower(host), cert, stamp, c.now()
	return c, nil
}

// Host is the name clients connect to, which the certificate is for.
func (c *Cert) Host() string { return c.host }

// Names are the DNS names the certificate is for, as -allow-host takes them: a wildcard
// "*.example.com" is ".example.com".
func (c *Cert) Names() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.cert.Leaf.DNSNames))
	for _, n := range c.cert.Leaf.DNSNames {
		out = append(out, strings.ToLower(strings.TrimPrefix(n, "*")))
	}
	return out
}

// GetCertificate is tls.Config.GetCertificate. A pair that changed is read again. One that does
// not parse (half written by a renewal) or is not for the host any more leaves the certificate in
// use as it is, said once in the log, until the files change again; one that cannot be read at
// the moment is tried again at the next look.
func (c *Cert) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now := c.now(); now.Sub(c.checked) >= checkEvery {
		c.checked = now
		if st, err := c.stamps(); err == nil && st != c.stamp {
			cert, st, err := c.read()
			if err == nil {
				err = cert.Leaf.VerifyHostname(c.host)
			}
			switch {
			case err == nil:
				c.cert, c.stamp = cert, st
			case errors.As(err, new(*fs.PathError)):
				// The files could not be read now (a passing error): looked at again next time.
			default:
				c.log.Warn("the changed TLS certificate is not used; the one before stays", "err", err)
				c.stamp = st
			}
		}
	}
	return c.cert, nil
}

func (c *Cert) read() (*tls.Certificate, stamp, error) {
	stamp, err := c.stamps()
	if err != nil {
		return nil, stamp, err
	}
	cert, err := tls.LoadX509KeyPair(c.certFile, c.keyFile)
	if err != nil {
		return nil, stamp, fmt.Errorf("tls certificate: %w", err)
	}
	if cert.Leaf == nil {
		if cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
			return nil, stamp, fmt.Errorf("tls certificate: %w", err)
		}
	}
	if len(cert.Leaf.DNSNames) == 0 {
		return nil, stamp, errors.New("tls certificate: it names no DNS host (subject alternative names)")
	}
	return &cert, stamp, nil
}

func (c *Cert) stamps() (stamp, error) {
	var out stamp
	for i, f := range []string{c.certFile, c.keyFile} {
		fi, err := os.Stat(f)
		if err != nil {
			return out, fmt.Errorf("tls certificate: %w", err)
		}
		out[i].mod, out[i].size = fi.ModTime().UnixNano(), fi.Size()
	}
	return out, nil
}
