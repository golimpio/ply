package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"ply/internal/art"
	"ply/internal/jobs"
	"ply/internal/tier"
)

// kindArtGrab makes the frame grab of an item that was put off because a play of it
// was being set up (api.Options.ArtLater): it reads the file, so it waits for the
// disk like any other background read.
const kindArtGrab = "art.grab"

// artKey is the dedupe key of an image's job.
func artKey(src art.Source, k art.Kind) string { return fmt.Sprintf("art:%d:%s", src.ID, k) }

// registerArtGrab declares the kind. d.art nil: no artwork (tests).
//
// Light, like the other jobs that read a little at a time: a grab reads some tens of
// megabytes and must not wait for a full scan. Its Need still holds it while a play
// reads from the same disk, or a software transcode runs.
func registerArtGrab(q *jobs.Queue, d queueDeps) {
	if d.art == nil {
		return
	}
	payload := func(p json.RawMessage) (g art.Grab) {
		_ = json.Unmarshal(p, &g)
		return g
	}
	q.Register(jobs.Spec{
		Kind:     kindArtGrab,
		Priority: jobs.PriorityCache,
		Title: func(p json.RawMessage) string {
			if g := payload(p); g.Src.Path != "" {
				return "Make a picture for " + filepath.Base(g.Src.Path)
			}
			return "Make a picture"
		},
		Need:  func(p json.RawMessage) tier.Need { return tier.Need{CPU: true, Disks: []string{payload(p).Src.Path}} },
		Light: true,
		Run: func(ctx context.Context, r *jobs.Run) error {
			var g art.Grab
			if err := r.Decode(&g); err != nil {
				return jobs.Permanent(err)
			}
			// The job may have waited behind a film for hours, or across a restart. If the file was
			// replaced meanwhile, a picture of the new one would be kept under the old one's name; the
			// scanner gives the item its new mtime, and the page asks again with it. (A file that cannot
			// be stat-ed goes on: the generator tells unavailable from the rest.)
			if fi, serr := os.Stat(g.Src.Path); serr == nil && fi.ModTime().UnixNano() != g.Src.MTime {
				return nil
			}
			_, err := d.art.Path(ctx, g.Src, g.Kind)
			switch {
			case errors.Is(err, art.ErrDeferred):
				return nil // a play began again: the API queued it again
			case errors.Is(err, art.ErrNoFrame):
				return nil // the file has no picture; the generator remembers that
			}
			return err
		},
	})
}

// artAskedWithin is how long a second request for an image that was queued already is
// not passed on: every page that shows the picture asks while a play starts, and the job
// waiting is enough.
const artAskedWithin = 30 * time.Second

// artLater is api.Options.ArtLater: it queues the grab of an image for when the disk
// is free. It does not block the request that asks. A grab that was stopped under way
// (again) is queued whatever was asked a moment ago: that ask was for the one that has
// been stopped.
func artLater(ctx context.Context, q *jobs.Queue, log *slog.Logger) func(art.Source, art.Kind, bool) {
	var mu sync.Mutex
	asked := map[string]time.Time{}
	return func(src art.Source, k art.Kind, again bool) {
		key, now := artKey(src, k), time.Now()
		mu.Lock()
		if t, ok := asked[key]; ok && now.Sub(t) < artAskedWithin && !again {
			mu.Unlock()
			return
		}
		if len(asked) > 64 {
			for old, t := range asked {
				if now.Sub(t) >= artAskedWithin {
					delete(asked, old)
				}
			}
		}
		asked[key] = now
		mu.Unlock()
		go func() {
			if _, err := q.Enqueue(ctx, kindArtGrab, jobs.Opt{Key: key, Payload: art.Grab{Src: src, Kind: k}}); err != nil && ctx.Err() == nil {
				log.Warn("queue: artwork", "item", src.ID, "err", err)
			}
		}()
	}
}
