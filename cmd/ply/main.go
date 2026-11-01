// Command ply is a local media server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ply/internal/api"
	"ply/internal/config"
	"ply/internal/discovery"
	"ply/internal/library"
	"ply/internal/probe"
	"ply/internal/store"
	"ply/web"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "0.1.0"

// scanNice is the nice value for scan-time ffprobe processes.
const scanNice = 10

func main() {
	if err := run(); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "ply:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Parse(os.Args[1:], os.Stderr)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := os.MkdirAll(cfg.Data, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	id, err := config.ServerID(cfg.Data)
	if err != nil {
		return fmt.Errorf("server id: %w", err)
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Listen before anything advertises the address, so ":0" works.
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port

	if len(cfg.Media) == 0 {
		log.Warn("no -media roots given; the library will stay empty")
	}
	scanner := library.NewScanner(ctx, st, probe.FFprobe{Bin: cfg.FFprobe, Niceness: scanNice}, cfg.Media, log)
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanner.Run(ctx, cfg.ScanInterval)
	}()

	handler := api.New(api.Options{
		Store:   st,
		Scanner: scanner,
		UI:      web.Dist,
		ID:      id,
		Name:    cfg.Name,
		Version: version,
		Token:   cfg.Token,
		Logger:  log,
	})
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	adv, err := discovery.Advertise(cfg.Name, id, port)
	if err != nil {
		log.Warn("mDNS advertisement unavailable", "err", err)
	}
	log.Info("ply listening", "version", version, "name", cfg.Name, "id", id, "data", cfg.Data,
		"auth", cfg.Token != "", "media", cfg.Media)
	for _, u := range discovery.URLs(ln.Addr()) {
		log.Info("reachable at", "url", u)
	}

	var serveFailure error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case serveFailure = <-serveErr:
		stop() // cancels the scanner too
	}

	adv.Shutdown()
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		// Long-lived streams outlived the grace period.
		srv.Close()
	}
	<-scanDone
	scanner.Wait()
	return serveFailure
}
