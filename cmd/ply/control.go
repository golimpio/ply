package main

// ply -stop (docs/DESIGN.md, "Stopping"): a running ply leaves its address and a random key in
// <data>/control.json, readable by its owner alone; ply -stop reads them and asks that server,
// through its API, to stop.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"ply/internal/api"
	"ply/internal/config"
)

const (
	// controlName is the file in the data directory that ply -stop reads.
	controlName = "control.json"
	// stopWithin is how long a nice stop is waited for before the server quits anyway.
	stopWithin = 15 * time.Second
	// forceStopWithin is how long a forced stop (ply -stop -force) waits for the nice one.
	forceStopWithin = 5 * time.Second
	// exitGrace is how much longer than the server's own deadline ply -stop waits for its exit.
	exitGrace = 5 * time.Second
)

// control is what control.json holds.
type control struct {
	PID     int    `json:"pid"`
	URL     string `json:"url"` // the server's own address, never a proxy's
	Key     string `json:"key"` // the credential of the stop (api.ControlHeader)
	TLSHost string `json:"tlsHost,omitempty"`
	TLSCert string `json:"tlsCert,omitempty"` // the PEM chain served: ply -stop checks the server against it
}

func controlPath(data string) string { return filepath.Join(data, controlName) }

// newControl is the control of this process, serving on addr, with a new key.
func newControl(addr net.Addr, tlsHost, tlsCert string) (control, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return control{}, err
	}
	c := control{PID: os.Getpid(), URL: controlURL(addr, tlsHost != ""), Key: hex.EncodeToString(key), TLSHost: tlsHost}
	if tlsCert != "" {
		abs, err := filepath.Abs(tlsCert) // ply -stop may run from another directory
		if err != nil {
			return control{}, err
		}
		c.TLSCert = abs
	}
	return c, nil
}

// controlURL is the address ply -stop calls for a server listening on addr: loopback for one that
// listens on every address, the address itself otherwise.
func controlURL(addr net.Addr, https bool) string {
	host, port := "127.0.0.1", "0"
	if a, ok := addr.(*net.TCPAddr); ok {
		port = fmt.Sprint(a.Port)
		if !a.IP.IsUnspecified() && a.IP != nil {
			host = a.IP.String()
		}
	}
	scheme := "http"
	if https {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

// write saves c in data, whole or not at all. remove deletes it unless another ply wrote its own
// since: it is called on every way out, a forced one included.
func (c control) write(data string) (remove func(), err error) {
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	path := controlPath(data)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	return func() { c.removeFrom(data) }, nil
}

// removeFrom deletes data's control file if it is c's.
func (c control) removeFrom(data string) {
	if cur, err := readControl(data); err == nil && cur.Key == c.Key {
		os.Remove(controlPath(data))
	}
}

func readControl(data string) (control, error) {
	b, err := os.ReadFile(controlPath(data))
	if err != nil {
		return control{}, err
	}
	var c control
	if err := json.Unmarshal(b, &c); err != nil {
		return control{}, fmt.Errorf("%s: %w", controlPath(data), err)
	}
	return c, nil
}

// client is the HTTP client that talks to c's server: straight to it, never through a proxy, and,
// over HTTPS, to the server that serves the certificate in c.TLSCert (a certificate the system may
// not trust, or one that has expired: a stop must work anyway).
func (c control) client() (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	if c.TLSCert != "" {
		raw, err := os.ReadFile(c.TLSCert)
		if err != nil {
			return nil, err
		}
		var served [][]byte
		for {
			var b *pem.Block
			if b, raw = pem.Decode(raw); b == nil {
				break
			}
			if b.Type == "CERTIFICATE" {
				served = append(served, b.Bytes)
			}
		}
		tr.TLSClientConfig = &tls.Config{
			ServerName: c.TLSHost,
			// The chain is checked below, against the certificate file itself.
			InsecureSkipVerify: true,
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) > 0 && slices.ContainsFunc(served, func(der []byte) bool {
					return bytes.Equal(der, cs.PeerCertificates[0].Raw)
				}) {
					return nil
				}
				return fmt.Errorf("the server's certificate is not the one in %s", c.TLSCert)
			},
		}
	}
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}, nil
}

// stopRunning is ply -stop: it asks the ply running with cfg.Data to stop and waits for it to exit.
func stopRunning(ctx context.Context, cfg config.Config, out io.Writer) error {
	c, err := readControl(cfg.Data)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no ply is running with the data directory %s", cfg.Data)
	}
	if err != nil {
		return err
	}
	client, err := c.client()
	if err != nil {
		return err
	}
	u, within := c.URL+"/api/server/stop", stopWithin
	if cfg.Force {
		u, within = u+"?force=true", forceStopWithin
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set(api.ControlHeader, c.Key)
	resp, err := client.Do(req)
	if errors.Is(err, syscall.ECONNREFUSED) {
		// A ply that was killed leaves its file: nothing listens there any more.
		c.removeFrom(cfg.Data)
		return fmt.Errorf("no ply is running with the data directory %s (pid %d left %s behind; removed)",
			cfg.Data, c.PID, controlName)
	}
	if err != nil {
		return fmt.Errorf("ply (pid %d) at %s: %w", c.PID, c.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		var e struct{ Error string }
		json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e)
		return fmt.Errorf("ply (pid %d) at %s refused the stop: %s %s", c.PID, c.URL, resp.Status, e.Error)
	}
	fmt.Fprintf(out, "Stopping ply (pid %d, %s)...\n", c.PID, c.URL)
	if err := waitForExit(ctx, cfg.Data, c, within+exitGrace); err != nil {
		if !cfg.Force {
			return fmt.Errorf("%w; ply -stop -force quits it in %s", err, forceStopWithin)
		}
		return err
	}
	fmt.Fprintln(out, "Stopped.")
	return nil
}

// waitForExit waits for c's server to remove its control file, the last thing it does.
func waitForExit(ctx context.Context, data string, c control, within time.Duration) error {
	deadline := time.Now().Add(within)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if cur, err := readControl(data); errors.Is(err, os.ErrNotExist) || err == nil && cur.Key != c.Key {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("ply (pid %d) has not exited after %s", c.PID, within)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// apiStop says that a stop came through the API, on the terminal too when it shows ply's own
// messages (tty).
func apiStop(log *slog.Logger, tty, force bool) {
	if force {
		log.Info("shutting down: asked by ply -stop -force", "within", forceStopWithin)
	} else {
		log.Info("shutting down: asked by ply -stop")
	}
	if !tty {
		return
	}
	if force {
		fmt.Fprintf(os.Stderr, "\nShutting down (ply -stop -force: quitting in %s at most)...\n", forceStopWithin)
	} else {
		fmt.Fprintln(os.Stderr, "\nShutting down (ply -stop)... (press Ctrl-C to quit immediately)")
	}
}

// stopRequests are the stops asked for through the API, nice or forced; one of each is enough.
type stopRequests struct{ nice, force chan struct{} }

func newStopRequests() stopRequests {
	return stopRequests{nice: make(chan struct{}, 1), force: make(chan struct{}, 1)}
}

// ask is api.Options.Stop: it does not block.
func (s stopRequests) ask(force bool) {
	ch := s.nice
	if force {
		ch = s.force
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}
