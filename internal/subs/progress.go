package subs

import (
	"bytes"
	"math"
	"strconv"
	"strings"
	"time"
)

// maxProgressLine is the longest line of ffmpeg's progress that is read; a longer one is
// dropped as it comes, never kept (ffmpeg's lines are a few dozen bytes).
const maxProgressLine = 4096

// progressWriter reads what ffmpeg writes for `-progress pipe:1`: blocks of key=value
// lines, each block ended by progress=continue (progress=end for the last one), about every
// half second. Of a block it keeps how far into the film ffmpeg has got (out_time_us) and
// tells emit when the block is whole; a block that does not know it (out_time_us=N/A,
// before the first packet is written) is not told.
//
// A subtitle track is sparse, and ffmpeg's position is that of the last packet it wrote, so
// the position moves in steps; blocks that repeat it are told as well, so that the one who
// listens can see that time has passed.
type progressWriter struct {
	emit func(at time.Duration)

	line     []byte // the start of a line whose end has not been written yet, at most maxProgressLine bytes
	skipping bool   // the line under way was too long: its rest is dropped, up to its end
	at       time.Duration
	known    bool
}

func (w *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		piece := p
		if i >= 0 {
			piece = p[:i]
		}
		if !w.skipping {
			if len(w.line)+len(piece) > maxProgressLine {
				w.line, w.skipping = w.line[:0], true // not a line of ours
			} else {
				w.line = append(w.line, piece...)
			}
		}
		if i < 0 {
			break
		}
		if !w.skipping {
			w.take(string(w.line))
		}
		w.line, w.skipping = w.line[:0], false
		p = p[i+1:]
	}
	return n, nil
}

func (w *progressWriter) take(line string) {
	key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
	if !ok {
		return
	}
	switch key {
	case "out_time_us", "out_time_ms": // both are microseconds: "ms" is a name ffmpeg kept
		if d, ok := micros(val); ok {
			w.at, w.known = d, true
		}
	case "progress":
		if w.known && w.emit != nil {
			w.emit(w.at)
		}
		w.known = false
	}
}

// micros reads a time in microseconds. ffmpeg writes N/A when it does not know the time
// (and, in older versions, a negative number).
func micros(s string) (time.Duration, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 || n > math.MaxInt64/int64(time.Microsecond) {
		return 0, false
	}
	return time.Duration(n) * time.Microsecond, true
}

// report tells everyone who watches the run how far into the film ffmpeg has got.
// Deliveries are in order and one at a time, so a watcher must not call the Generator.
func (c *call) report(at time.Duration) {
	c.progMu.Lock()
	defer c.progMu.Unlock()
	c.at, c.hasAt = at, true
	for _, fn := range c.watchers {
		fn(at)
	}
}

// watch has fn told how far the run is from now on, starting with where it is (if it has
// begun). fn nil: nobody to tell.
func (c *call) watch(fn func(at time.Duration)) {
	if fn == nil {
		return
	}
	c.progMu.Lock()
	defer c.progMu.Unlock()
	c.watchers = append(c.watchers, fn)
	if c.hasAt {
		fn(c.at)
	}
}
