// Package subs converts text subtitles to WebVTT and caches the result on
// disk, so a browser <track> can show them.
//
// Embedded streams are extracted with one ffmpeg run per stream (which reads
// the whole container once, at reduced priority); sidecar files are converted
// the same way. Concurrent requests for the same track share one run.
package subs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ErrConvert reports that ffmpeg could not turn the subtitle into WebVTT.
var ErrConvert = errors.New("subs: conversion failed")

// textCodecs are the embedded subtitle codecs that become WebVTT.
var textCodecs = map[string]bool{
	"subrip": true, "srt": true, "ass": true, "ssa": true, "mov_text": true, "webvtt": true, "text": true,
}

// IsText reports whether an embedded subtitle codec can be converted to WebVTT.
func IsText(codec string) bool { return textCodecs[strings.ToLower(codec)] }

// Source says which subtitle to convert.
type Source struct {
	ItemID int64
	Key    string // "3" (stream index) or "ext0" (sidecar); part of the cache name
	Input  string // the media file (Stream >= 0) or the sidecar file (Stream < 0)
	Stream int    // ffprobe stream index of the subtitle in Input; negative for a sidecar
	MTime  int64  // unix nanoseconds of Input; part of the cache name
}

// Options configures New.
type Options struct {
	Dir         string        // cache directory, created if missing
	FFmpeg      string        // default "ffmpeg"
	Niceness    int           // unix nice value for ffmpeg; default 10
	Concurrency int           // simultaneous ffmpeg runs; default 2
	Timeout     time.Duration // per conversion; default 10 minutes (a big remux is read end to end)
}

// Generator converts subtitles on demand.
type Generator struct {
	dir      string
	ffmpeg   string
	niceness int
	timeout  time.Duration
	sem      chan struct{}

	mu       sync.Mutex
	inflight map[string]*call
	failed   map[string]error // cache names that could not be generated

	// run executes ffmpeg and returns its stderr. Tests replace it.
	run func(ctx context.Context, args []string) (string, error)
}

type call struct {
	done chan struct{}
	err  error
}

// New returns a Generator caching under o.Dir.
func New(o Options) (*Generator, error) {
	if o.Dir == "" {
		return nil, errors.New("subs: no cache directory")
	}
	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		return nil, err
	}
	if o.FFmpeg == "" {
		o.FFmpeg = "ffmpeg"
	}
	if o.Niceness == 0 {
		o.Niceness = 10
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 2
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Minute
	}
	g := &Generator{
		dir:      o.Dir,
		ffmpeg:   o.FFmpeg,
		niceness: o.Niceness,
		timeout:  o.Timeout,
		sem:      make(chan struct{}, o.Concurrency),
		inflight: map[string]*call{},
		failed:   map[string]error{},
	}
	g.run = g.exec
	return g, nil
}

// cacheName is "<id>-<key>-<mtime>.vtt". A sidecar's name also carries a hash
// of its path: the list of sidecars can change under the same ext<N> key.
func cacheName(src Source) string {
	name := fmt.Sprintf("%d-%s-%d", src.ItemID, src.Key, src.MTime)
	if src.Stream < 0 {
		sum := sha256.Sum256([]byte(src.Input))
		name += "-" + hex.EncodeToString(sum[:4])
	}
	return name + ".vtt"
}

// Path returns the cached WebVTT file for src, converting first if needed.
// A source that failed once is not retried until its mtime changes.
func (g *Generator) Path(ctx context.Context, src Source) (string, error) {
	name := cacheName(src)
	dst := filepath.Join(g.dir, name)
	if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
		return dst, nil
	}

	g.mu.Lock()
	if err, ok := g.failed[name]; ok {
		g.mu.Unlock()
		return "", err
	}
	c, ok := g.inflight[name]
	if !ok {
		c = &call{done: make(chan struct{})}
		g.inflight[name] = c
		// The work outlives the first request: a reload must not restart a
		// conversion that took minutes.
		go g.fill(context.WithoutCancel(ctx), c, name, src, dst)
	}
	g.mu.Unlock()

	select {
	case <-c.done:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if c.err != nil {
		return "", c.err
	}
	return dst, nil
}

func (g *Generator) fill(ctx context.Context, c *call, name string, src Source, dst string) {
	c.err = g.convert(ctx, src, dst)
	g.mu.Lock()
	delete(g.inflight, name)
	if errors.Is(c.err, ErrConvert) {
		g.failed[name] = c.err
	}
	g.mu.Unlock()
	close(c.done)
}

func (g *Generator) convert(ctx context.Context, src Source, dst string) error {
	select {
	case g.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-g.sem }()

	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	tmp := dst + ".tmp"
	defer os.Remove(tmp)

	args := []string{"-nostdin", "-v", "error", "-y"}
	if src.Stream < 0 {
		// ffmpeg exits 0 after dropping the rest of a file that is not
		// UTF-8, so say what it is.
		if enc := legacyCharset(src.Input); enc != "" {
			args = append(args, "-sub_charenc", enc)
		}
	}
	args = append(args, "-i", src.Input)
	if src.Stream >= 0 {
		args = append(args, "-map", "0:"+strconv.Itoa(src.Stream))
	}
	args = append(args, "-c:s", "webvtt", "-f", "webvtt", tmp)

	stderr, err := g.run(ctx, args)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %v: %s", ErrConvert, err, firstLine(stderr))
	}
	if fi, err := os.Stat(tmp); err != nil || fi.Size() == 0 {
		return fmt.Errorf("%w: ffmpeg wrote nothing: %s", ErrConvert, firstLine(stderr))
	}
	return os.Rename(tmp, dst)
}

// legacyCharset returns the encoding to assume for a sidecar that is neither
// UTF-8 nor UTF-16 with a byte order mark, or "" when no flag is needed.
func legacyCharset(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16<<20))
	if err != nil || bytes.HasPrefix(data, []byte{0xFF, 0xFE}) || bytes.HasPrefix(data, []byte{0xFE, 0xFF}) {
		return ""
	}
	if utf8.Valid(data) {
		return ""
	}
	return "WINDOWS-1252" // the usual encoding of old Western European .srt files
}

func (g *Generator) exec(ctx context.Context, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, g.ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	ownProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start %s: %w", g.ffmpeg, err)
	}
	setNice(cmd.Process.Pid, g.niceness) // best effort
	err := cmd.Wait()
	return stderr.String(), err
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// Prune deletes cached files of items that are gone, and of embedded tracks
// whose file has changed. live maps each current item id to its mtime. It
// returns the number of files removed.
func (g *Generator) Prune(live map[int64]int64) (int, error) {
	entries, err := os.ReadDir(g.dir)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if keepCached(e.Name(), live) {
			continue
		}
		if fi, err := e.Info(); strings.HasSuffix(e.Name(), ".tmp") && err == nil && time.Since(fi.ModTime()) < g.timeout {
			continue // being written by an in-flight conversion
		}
		if err := os.Remove(filepath.Join(g.dir, e.Name())); err == nil {
			removed++
		}
	}
	g.mu.Lock()
	for name := range g.failed {
		if !keepCached(name, live) {
			delete(g.failed, name)
		}
	}
	g.mu.Unlock()
	return removed, nil
}

// keepCached reports whether the cache file name still belongs to a live item.
// Sidecar tracks (ext<N>) only need their item to exist; embedded ones also
// need the item's mtime to be unchanged.
func keepCached(name string, live map[int64]int64) bool {
	parts := strings.Split(strings.TrimSuffix(name, ".vtt"), "-")
	if !strings.HasSuffix(name, ".vtt") || len(parts) < 3 {
		return false
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return false
	}
	mtime, ok := live[id]
	if !ok {
		return false
	}
	if strings.HasPrefix(parts[1], "ext") {
		return true
	}
	m, err := strconv.ParseInt(parts[2], 10, 64)
	return err == nil && m == mtime
}
