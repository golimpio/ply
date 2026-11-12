package main

import (
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"

	"ply/internal/config"
	"ply/internal/tlscert"
)

// loadTLS loads the certificate of -tls-cert and -tls-key; nil for plain HTTP. The names it is
// for are ours: the host check answers to them too.
func loadTLS(cfg *config.Config, log *slog.Logger) (*tlscert.Cert, error) {
	if cfg.TLSCert == "" {
		return nil, nil
	}
	cert, err := tlscert.Load(cfg.TLSCert, cfg.TLSKey, cfg.TLSHost, log)
	if err != nil {
		return nil, err
	}
	cfg.AllowHosts = append(cfg.AllowHosts, cert.Names()...)
	return cert, nil
}

// serve serves srv on ln: HTTPS with cert when there is one, on which Go speaks HTTP/2 by itself
// (docs/M11-CLIENTS.md, "HTTPS"), and plain HTTP otherwise.
func serve(srv *http.Server, ln net.Listener, cert *tlscert.Cert) error {
	if cert == nil {
		return srv.Serve(ln)
	}
	srv.TLSConfig = &tls.Config{GetCertificate: cert.GetCertificate, MinVersion: tls.VersionTLS12}
	return srv.ServeTLS(ln, "", "")
}
