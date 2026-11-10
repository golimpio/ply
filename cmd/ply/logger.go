package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mattn/go-isatty"

	"ply/internal/config"
)

const (
	defaultMaxLogSize = 10 * 1024 * 1024 // 10 MB
	defaultMaxBackups = 3
)

// rotatingWriter is a file writer that rotates when the file size exceeds maxSize.
// It is protected by a mutex for thread-safe concurrent writes.
type rotatingWriter struct {
	mu         sync.Mutex
	path       string
	maxSize    int64
	maxBackups int
	size       int64
	file       *os.File
}

func newRotatingWriter(path string, maxSize int64, maxBackups int) (*rotatingWriter, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	var size int64
	if info, err := f.Stat(); err == nil {
		size = info.Size()
	}
	return &rotatingWriter{
		path:       path,
		maxSize:    maxSize,
		maxBackups: maxBackups,
		size:       size,
		file:       f,
	}, nil
}

func (w *rotatingWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return 0, os.ErrClosed
	}

	writeLen := int64(len(p))
	if w.size+writeLen > w.maxSize && w.size > 0 {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}

	n, err = w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingWriter) rotate() error {
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}

	ext := filepath.Ext(w.path)
	prefix := strings.TrimSuffix(w.path, ext)

	for i := w.maxBackups; i >= 1; i-- {
		src := w.path
		if i > 1 {
			src = fmt.Sprintf("%s.%d%s", prefix, i-1, ext)
		}
		dst := fmt.Sprintf("%s.%d%s", prefix, i, ext)
		if i == w.maxBackups {
			_ = os.Remove(dst)
		}
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, dst)
		}
	}

	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	w.file = f
	w.size = 0
	return nil
}

func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		err := w.file.Close()
		w.file = nil
		return err
	}
	return nil
}

func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

func shouldMirrorStderr(logStderr, isTerm bool) bool {
	return logStderr || !isTerm
}

func setupLogger(cfg config.Config) (*slog.Logger, func(), bool) {
	interactive := isTerminal(os.Stderr)
	mirror := shouldMirrorStderr(cfg.LogStderr, interactive)
	log, cleanup := setupLoggerWithOptions(cfg.LogDir, cfg.LogPath(), defaultMaxLogSize, defaultMaxBackups, os.Stderr, mirror)
	return log, cleanup, interactive
}

func setupLoggerWithWriter(logDir, logPath string, maxSize int64, maxBackups int, stderr io.Writer) (*slog.Logger, func()) {
	return setupLoggerWithOptions(logDir, logPath, maxSize, maxBackups, stderr, true)
}

func setupLoggerWithOptions(logDir, logPath string, maxSize int64, maxBackups int, stderr io.Writer, mirrorStderr bool) (*slog.Logger, func()) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		log := slog.New(slog.NewTextHandler(stderr, nil))
		log.Warn("cannot create log directory; logging to stderr only", "dir", logDir, "err", err)
		return log, func() {}
	}

	rot, err := newRotatingWriter(logPath, maxSize, maxBackups)
	if err != nil {
		log := slog.New(slog.NewTextHandler(stderr, nil))
		log.Warn("cannot open log file; logging to stderr only", "path", logPath, "err", err)
		return log, func() {}
	}

	var w io.Writer = rot
	if mirrorStderr && stderr != nil {
		w = io.MultiWriter(stderr, rot)
	}
	log := slog.New(slog.NewTextHandler(w, nil))
	cleanup := func() {
		_ = rot.Close()
	}
	return log, cleanup
}
