package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ply/internal/api"
	"ply/internal/config"
)

func TestControlURL(t *testing.T) {
	for _, tc := range []struct {
		addr  string
		https bool
		want  string
	}{
		{"0.0.0.0:8420", false, "http://127.0.0.1:8420"},
		{"[::]:8420", false, "http://127.0.0.1:8420"},
		{"192.168.1.5:8420", true, "https://192.168.1.5:8420"},
		{"[::1]:9000", false, "http://[::1]:9000"},
	} {
		a, err := net.ResolveTCPAddr("tcp", tc.addr)
		if err != nil {
			t.Fatal(err)
		}
		if got := controlURL(a, tc.https); got != tc.want {
			t.Errorf("controlURL(%s, %v) = %s, want %s", tc.addr, tc.https, got, tc.want)
		}
	}
}

func TestControlFileIsTheOwnersAndRemovedOnlyByItsWriter(t *testing.T) {
	data := t.TempDir()
	a, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:8420")
	mine, err := newControl(a, "", "")
	if err != nil {
		t.Fatal(err)
	}
	remove, err := mine.write(data)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(controlPath(data))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("control.json is %o, want 600: it holds the key", perm)
	}
	if got, err := readControl(data); err != nil || got != mine {
		t.Fatalf("read %+v, %v; want %+v", got, err, mine)
	}
	// Another ply started with the same data directory since: its file stays.
	theirs, _ := newControl(a, "", "")
	if _, err := theirs.write(data); err != nil {
		t.Fatal(err)
	}
	remove()
	if got, err := readControl(data); err != nil || got.Key != theirs.Key {
		t.Fatalf("after the first ply's remove: %+v, %v; want the second ply's file", got, err)
	}
	theirs.removeFrom(data)
	if _, err := os.Stat(controlPath(data)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the writer's remove left the file: %v", err)
	}
}

// fakeRunning serves the stop endpoint of a ply whose control file is in data: a stop removes the
// file after a moment, as the server does when it has exited.
type fakeRunning struct {
	data   string
	mu     sync.Mutex
	forced []bool
}

func (f *fakeRunning) stops() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.forced...)
}

func (f *fakeRunning) options(c *control) api.Options {
	return api.Options{ControlKey: c.Key, Stop: func(force bool) {
		f.mu.Lock()
		f.forced = append(f.forced, force)
		f.mu.Unlock()
		time.AfterFunc(100*time.Millisecond, func() { c.removeFrom(f.data) })
	}}
}

func startFake(t *testing.T, tls bool) (*fakeRunning, *httptest.Server) {
	t.Helper()
	f := &fakeRunning{data: t.TempDir()}
	c := &control{PID: 4242, Key: "the-key"}
	srv := httptest.NewUnstartedServer(nil)
	srv.Config.Handler = api.New(f.options(c))
	if tls {
		srv.StartTLS()
		var pemBuf bytes.Buffer
		pem.Encode(&pemBuf, &pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
		c.TLSCert = filepath.Join(t.TempDir(), "cert.pem")
		if err := os.WriteFile(c.TLSCert, pemBuf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		c.TLSHost = "example.com"
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	c.URL = controlURL(srv.Listener.Addr(), tls)
	if _, err := c.write(f.data); err != nil {
		t.Fatal(err)
	}
	return f, srv
}

func TestStopRunning(t *testing.T) {
	for _, tc := range []struct {
		name       string
		tls, force bool
	}{{"http", false, false}, {"https", true, false}, {"forced", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := startFake(t, tc.tls)
			var out bytes.Buffer
			err := stopRunning(context.Background(), config.Config{Data: f.data, Stop: true, Force: tc.force}, &out)
			if err != nil {
				t.Fatalf("stopRunning: %v (said %q)", err, out.String())
			}
			if !strings.Contains(out.String(), "pid 4242") || !strings.HasSuffix(out.String(), "Stopped.\n") {
				t.Errorf("said %q, want the pid and Stopped.", out.String())
			}
			if got := f.stops(); len(got) != 1 || got[0] != tc.force {
				t.Errorf("stops asked %v, want one with force %v", got, tc.force)
			}
		})
	}
}

func TestStopRunningRefusesAnotherCertificate(t *testing.T) {
	f, _ := startFake(t, true)
	// The control file names a certificate the server does not serve (httptest's servers all
	// serve the same one: this one is made here).
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"example.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := readControl(f.data)
	var pemBuf bytes.Buffer
	pem.Encode(&pemBuf, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(c.TLSCert, pemBuf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	err = stopRunning(context.Background(), config.Config{Data: f.data, Stop: true}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("stopRunning = %v, want a certificate error", err)
	}
	if got := f.stops(); len(got) != 0 {
		t.Fatalf("the key went to a server with another certificate: stops %v", got)
	}
}

func TestStopRunningWithNoServer(t *testing.T) {
	data := t.TempDir()
	err := stopRunning(context.Background(), config.Config{Data: data, Stop: true}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "no ply is running") {
		t.Fatalf("no control file: %v, want no ply is running", err)
	}
	// A ply that was killed left its file: nothing listens at its address, and its pid is gone.
	stale := closedControl(t)
	stale.PID = deadPID(t)
	if _, err := stale.write(data); err != nil {
		t.Fatal(err)
	}
	err = stopRunning(context.Background(), config.Config{Data: data, Stop: true}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "left control.json behind") {
		t.Fatalf("stale control file: %v, want it reported", err)
	}
	if _, err := os.Stat(controlPath(data)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the stale control file is still there: %v", err)
	}
}

// closedControl is the control of a server whose listener is closed, run by this process.
func closedControl(t *testing.T) control {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c, err := newControl(ln.Addr(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	return c
}

// deadPID is the pid of a process that has exited and been reaped.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if processAlive(cmd.Process.Pid) {
		t.Skipf("pid %d was reused at once", cmd.Process.Pid)
	}
	return cmd.Process.Pid
}

func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatal("this process is not alive")
	}
	if pid := deadPID(t); processAlive(pid) {
		t.Fatalf("exited pid %d is alive", pid)
	}
}

// A ply that is already stopping has closed its listener but still runs: its control file stays,
// and ply -stop waits for its exit.
func TestStopRunningWhileItStops(t *testing.T) {
	for _, force := range []bool{false, true} {
		data := t.TempDir()
		c := closedControl(t) // this process's pid: alive
		if _, err := c.write(data); err != nil {
			t.Fatal(err)
		}
		time.AfterFunc(200*time.Millisecond, func() { c.removeFrom(data) }) // it exits
		var out bytes.Buffer
		err := stopRunning(context.Background(), config.Config{Data: data, Stop: true, Force: force}, &out)
		if err != nil || !strings.Contains(out.String(), "already stopping") || !strings.HasSuffix(out.String(), "Stopped.\n") {
			t.Fatalf("force %v: %v, said %q; want it to wait for the exit", force, err, out.String())
		}
	}
}

func TestStopRunningWithAWrongKey(t *testing.T) {
	f, _ := startFake(t, false)
	c, _ := readControl(f.data)
	c.Key = "not-it"
	if _, err := c.write(f.data); err != nil {
		t.Fatal(err)
	}
	err := stopRunning(context.Background(), config.Config{Data: f.data, Stop: true}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("stopRunning = %v, want the 403", err)
	}
}

func TestWaitForExitGivesUp(t *testing.T) {
	data := t.TempDir()
	a, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:1")
	c, _ := newControl(a, "", "")
	if _, err := c.write(data); err != nil {
		t.Fatal(err)
	}
	err := waitForExit(context.Background(), data, c, 150*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "has not exited") {
		t.Fatalf("waitForExit = %v, want a timeout", err)
	}
}

// Each way to end the server is told apart, and a forced stop gets the short deadline.
func TestStopCauses(t *testing.T) {
	failed := errors.New("listener failed")
	for _, want := range []stopCause{causeSignal, causeStop, causeForceStop, causeServeFailure} {
		sigC, stops, serveErr := make(chan os.Signal, 1), newStopRequests(), make(chan error, 1)
		switch want {
		case causeSignal:
			sigC <- os.Interrupt
		case causeStop:
			stops.ask(false)
		case causeForceStop:
			stops.ask(true)
		case causeServeFailure:
			serveErr <- failed
		}
		got, err := awaitStop(sigC, stops, serveErr)
		if got != want || (err != nil) != (want == causeServeFailure) {
			t.Fatalf("awaitStop = %v, %v; want %v", got, err, want)
		}
		s := shutdownStopper(slog.New(slog.DiscardHandler), got, sigC, stops)
		wantDeadline := stopWithin
		if want == causeForceStop {
			wantDeadline = forceStopWithin
		}
		if s.deadline != wantDeadline || s.hurried != forceStopWithin || s.quick != (<-chan os.Signal)(sigC) ||
			s.hurry != (<-chan struct{})(stops.force) {
			t.Errorf("%v: stopper deadline %s, hurried %s, quick and hurry wired %v %v; want %s, %s, true, true", want,
				s.deadline, s.hurried, s.quick == (<-chan os.Signal)(sigC), s.hurry == (<-chan struct{})(stops.force),
				wantDeadline, forceStopWithin)
		}
	}
}

func TestStopRequestsDoNotBlock(t *testing.T) {
	s := newStopRequests()
	for range 3 {
		s.ask(false)
		s.ask(true)
	}
	if len(s.nice) != 1 || len(s.force) != 1 {
		t.Fatalf("nice %d, force %d queued; want one of each", len(s.nice), len(s.force))
	}
}
