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
	"path/filepath"
	"syscall"
	"time"

	"ply/internal/api"
	"ply/internal/art"
	"ply/internal/config"
	"ply/internal/discovery"
	"ply/internal/library"
	"ply/internal/probe"
	"ply/internal/store"
	"ply/internal/stream"
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

	ffmpegBin := stream.ResolveFFmpeg(cfg.FFmpeg)
	streams, err := stream.NewManager(stream.Options{
		FFmpeg:      ffmpegBin,
		FFprobe:     cfg.FFprobe,
		IdleTimeout: streamIdle(log),
		Cache:       st,
		Logger:      log,
	})
	if err != nil {
		return err
	}
	defer streams.Close() // also run explicitly on shutdown; Close is idempotent

	artGen, err := art.New(ctx, art.Options{Dir: filepath.Join(cfg.Data, "art"), FFmpeg: ffmpegBin})
	if err != nil {
		log.Warn("artwork disabled", "err", err)
		artGen = nil
	}

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
		Streams: streams,
		Art:     artGen,
	})
	if artGen != nil {
		go pruneArtAfterScans(ctx, scanner, st, artGen, log)
	}
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
	log.Info("ply listening", "ffmpeg", ffmpegBin, "version", version, "name", cfg.Name, "id", id, "data", cfg.Data,
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
	// Kill every ffmpeg and drop the temp dirs first: requests waiting on
	// segments return at once instead of holding the HTTP shutdown up.
	streams.Close()
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

// streamIdle is how long a streaming session may go without a request before
// its ffmpeg is killed: 60s, or PLY_STREAM_IDLE (a Go duration) if set.
func streamIdle(log *slog.Logger) time.Duration {
	v := os.Getenv("PLY_STREAM_IDLE")
	if v == "" {
		return 0 // the stream package default
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		log.Warn("ignoring invalid PLY_STREAM_IDLE", "value", v)
		return 0
	}
	return d
}

// pruneArtAfterScans removes cached artwork of items that are gone or changed
// each time a scan finishes. The scanner has no completion hook, so its
// status is polled.
func pruneArtAfterScans(ctx context.Context, sc api.ScanController, st *store.Store, gen *art.Generator, log *slog.Logger) {
	var seen time.Time
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s := sc.Status(ctx)
		if s.Running || s.LastFinished == nil || !s.LastFinished.After(seen) {
			continue
		}
		seen = *s.LastFinished
		states, err := st.FileStates(ctx)
		if err != nil {
			log.Warn("artwork prune: list items", "err", err)
			continue
		}
		live := make(map[int64]int64, len(states))
		for _, fs := range states {
			live[fs.ID] = fs.MTime
		}
		n, err := gen.Prune(live)
		if err != nil {
			log.Warn("artwork prune", "err", err)
		}
		if n > 0 {
			log.Info("artwork pruned", "files", n)
		}
	}
}
