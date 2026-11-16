package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"ply/internal/hwaccel"
	"ply/internal/jobs"
	"ply/internal/library"
	"ply/internal/metadata"
	"ply/internal/store"
	"ply/internal/trailerdl"
	"ply/internal/trailers"
)

// newTrailers is the trailers' downloads (docs/M13-TRAILERS.md), registered with the queue
// (before it runs). Downloads are made in <data>/tmp/trailers, emptied at each start.
func newTrailers(data, ffmpeg string, st *store.Store, q *jobs.Queue, scanner *library.Scanner, worker *metadata.Worker,
	settings *metadata.Settings, hw *hwaccel.Detector, log *slog.Logger) *trailers.Service {
	tmp := filepath.Join(data, "tmp", "trailers")
	if err := os.RemoveAll(tmp); err != nil {
		log.Warn("trailers: emptying the temp folder", "err", err)
	}
	client := &http.Client{Timeout: 5 * time.Minute} // a request is a chunk of 10 MiB at most
	f := &trailerdl.Fetcher{YouTube: trailerdl.YouTube{HTTP: client}, Vimeo: trailerdl.Vimeo{HTTP: client}, HTTP: client,
		FFmpeg: ffmpeg, Encoder: func() hwaccel.Encoder { return hw.Info().Encoder }}
	s := trailers.New(trailers.Options{Store: st, Queue: q, Scanner: scanner, Videos: worker.Trailers, Fetch: f.Fetch,
		Online:  func(ctx context.Context) bool { return settings.Resolve(ctx).Online() },
		TempDir: tmp, Roots: scanner.Roots, Logger: log})
	q.Register(s.Spec())
	return s
}
