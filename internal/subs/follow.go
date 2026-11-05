package subs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"
)

// followEvery is how often a Follower looks for what ffmpeg has written since.
var followEvery = 100 * time.Millisecond

// Follower reads the WebVTT of a track while ffmpeg is still writing it. Read gives
// out whole cues only, waits for more while the run goes on, and ends with io.EOF
// when the file is complete, or with the run's error: what it gave out before is
// valid WebVTT, cut short.
//
// A cue is whole when a blank line follows it. ffmpeg's muxer writes that line before
// the next cue, not after the last, so the last cue of what has been written would
// wait for the next one (the one-cue lag: a film with a cue every few minutes would
// show each when the next began). A cue that ends in a line feed, and that the file
// has not grown past for a poll's time, is therefore given out as it is, with the blank
// line added: a muxer flushes a cue in one write, so a cue still being written does not
// stay in this state for a poll.
//
// A browser parses a <track> as it arrives, so the viewer sees the cues of the first
// minutes long before the last of a film has been read.
type Follower struct {
	ctx      context.Context // the reader's: ends the wait, not the conversion
	c        *call
	name     string
	dst, tmp string

	f        *os.File
	grewAt   time.Time // when the file last had something new for us
	gen      int32     // the start of the run whose file f is
	pend     []byte    // read from the file, not yet a whole cue
	out      []byte    // whole cues not yet given out
	released int       // bytes of the file moved to out so far
	skip     int       // bytes of a run's new start to leave out: already given out by the first
	end      error     // set once the run is over and everything is out
	closed   bool
}

// Done is closed when the conversion is over, with the file or without.
func (f *Follower) Done() <-chan struct{} { return f.c.done }

// Result is the cached file of a finished conversion, or why there is none. Call it
// once Done is closed.
func (f *Follower) Result() (string, error) {
	if err := f.c.errFor(f.name); err != nil {
		return "", err
	}
	return f.dst, nil
}

// Close releases the file, and the run: when no request waits for it any more it is
// background work again. The conversion goes on.
func (f *Follower) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	if f.c != doneCall {
		f.c.ctl.unwait()
	}
	if f.f == nil {
		return nil
	}
	err := f.f.Close()
	f.f = nil
	return err
}

func (f *Follower) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if len(f.out) > 0 {
			n := copy(p, f.out)
			f.out = f.out[n:]
			return n, nil
		}
		if f.end != nil {
			return 0, f.end
		}
		select {
		case <-f.c.done:
			f.pull(true) // after done: everything the run wrote is in the file
			f.finish()
			continue
		default:
		}
		f.pull(false)
		if len(f.out) > 0 {
			continue
		}
		t := time.NewTimer(followEvery)
		select {
		case <-f.c.done:
		case <-t.C:
		case <-f.ctx.Done():
			t.Stop()
			return 0, f.ctx.Err()
		}
		t.Stop()
	}
}

// pull reads what has been written since the last look, and moves the whole cues
// to out. final says the run is over: nothing more will come, and the tail is left
// to finish.
func (f *Follower) pull(final bool) {
	if g := f.c.gen.Load(); g != f.gen {
		// The run was started again (with the first track alone): what was read is of a
		// file that is gone. The new file begins as the old did; what was given out of it
		// is skipped.
		if f.f != nil {
			_ = f.f.Close()
			f.f = nil
		}
		f.pend, f.skip, f.gen = nil, f.released, g
	}
	if f.f == nil {
		file, err := os.Open(f.tmp)
		if errors.Is(err, fs.ErrNotExist) {
			file, err = os.Open(f.dst) // done and renamed already
		}
		if err != nil {
			return // not written yet
		}
		f.f = file
	}
	var buf [32 << 10]byte
	grew := false
	for {
		n, err := f.f.Read(buf[:])
		if n > 0 {
			grew = true
			f.grewAt = time.Now()
			f.pend = append(f.pend, buf[:n]...)
		}
		if err != nil || n == 0 {
			break
		}
	}
	if f.skip > 0 { // what is left to skip leaves pend empty: nothing for the one-cue rule
		n := min(f.skip, len(f.pend))
		f.pend, f.skip = f.pend[n:], f.skip-n
	}
	if i := bytes.LastIndex(f.pend, []byte("\n\n")); i >= 0 {
		f.out = append(f.out, f.pend[:i+2]...)
		f.released += i + 2
		f.pend = append(f.pend[:0], f.pend[i+2:]...)
	}
	if !final && !grew && time.Since(f.grewAt) >= followEvery &&
		bytes.HasSuffix(f.pend, []byte("\n")) && bytes.Contains(f.pend, []byte("-->")) {
		f.out = append(f.out, f.pend...)
		f.out = append(f.out, '\n')
		f.released += len(f.pend)
		f.pend = f.pend[:0]
	}
}

// finish ends the stream once the run is over. A complete file gives out its tail;
// a failed run leaves its last, unfinished cue unsaid.
func (f *Follower) finish() {
	switch err := f.c.errFor(f.name); {
	case err != nil:
		f.end = err
	case f.f == nil:
		f.end = fmt.Errorf("subs: the converted file is gone: %w", fs.ErrNotExist)
	default:
		f.out = append(f.out, f.pend...)
		f.end = io.EOF
	}
	f.pend = nil
}
