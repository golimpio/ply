package main

import (
	"strings"
	"testing"
	"time"
)

// meterRig is a subsMeter on a clock the test moves, and what it told the job.
type meterRig struct {
	t     *testing.T
	m     *subsMeter
	clock time.Time
	calls []meterCall
}

type meterCall struct {
	done, total int
	note        string
}

func newMeterRig(t *testing.T, film time.Duration) *meterRig {
	t.Helper()
	r := &meterRig{t: t, clock: time.Unix(1_700_000_000, 0)}
	r.m = newSubsMeterAt(func(done, total int, note string) {
		r.calls = append(r.calls, meterCall{done, total, note})
	}, film, func() time.Time { return r.clock })
	return r
}

// at moves the clock by d, then says ffmpeg is at pos of the film.
func (r *meterRig) at(d, pos time.Duration) {
	r.clock = r.clock.Add(d)
	r.m.read(pos)
}

func (r *meterRig) last() meterCall {
	r.t.Helper()
	if len(r.calls) == 0 {
		r.t.Fatal("the job was told nothing")
	}
	return r.calls[len(r.calls)-1]
}

func (r *meterRig) want(done, total int, note string) {
	r.t.Helper()
	if got := r.last(); got != (meterCall{done, total, note}) {
		r.t.Errorf("job shows %d of %d %q, want %d of %d %q", got.done, got.total, got.note, done, total, note)
	}
}

// steady reads for n updates of 10 s each, a minute of film in each (a speed of 6).
func (r *meterRig) steady(n int, from time.Duration) time.Duration {
	pos := from
	for range n {
		pos += time.Minute
		r.at(10*time.Second, pos)
	}
	return pos
}

const testFilm = 100 * time.Minute

// Before ffmpeg says anything the bar does not know; then it shows the share read and the time
// left at the speed so far.
func TestSubsMeterShowsHowFarAndHowLong(t *testing.T) {
	r := newMeterRig(t, testFilm)
	r.want(0, 0, "Reading the file")

	r.at(10*time.Second, time.Minute)
	r.want(1, 100, "Reading the file: 1%") // one sample is no speed
	r.steady(11, time.Minute)
	// 12 minutes of film in 2 minutes: 88 are left, at a minute in 10 s.
	r.want(12, 100, "Reading the file: 12%, about 15 min left")
}

// The speed is that of the last minute, smoothed: a share that slows down is believed, but not
// at once.
func TestSubsMeterFollowsASlowingShare(t *testing.T) {
	r := newMeterRig(t, testFilm)
	pos := r.steady(30, 0) // 5 minutes at 6x
	if r.m.speed < 5.99 || r.m.speed > 6.01 {
		t.Fatalf("setup: speed %v, want 6", r.m.speed)
	}
	// Then half as fast: 30 s of film in 10 s.
	pos += 30 * time.Second
	r.at(10*time.Second, pos)
	// The last minute has one reading at 3 among those at 6 (5.5), and the speed moves only a
	// part of the way to it.
	if r.m.speed >= 6 || r.m.speed < 5.7 {
		t.Errorf("one slow reading made the speed %v: not smoothed (6 before, 3 now)", r.m.speed)
	}
	for range 60 { // ten minutes more
		pos += 30 * time.Second
		r.at(10*time.Second, pos)
	}
	if r.m.speed < 2.9 || r.m.speed > 3.1 {
		t.Errorf("speed after ten minutes at 3x = %v", r.m.speed)
	}
}

// A subtitle track that has no cue for a long time does not move the position: the time left
// grows, towards what the read has averaged since the start, rather than stay where it was.
func TestSubsMeterGrowsTheTimeLeftWhileATrackHasNoCue(t *testing.T) {
	r := newMeterRig(t, testFilm)
	pos := r.steady(12, 0)
	r.want(12, 100, "Reading the file: 12%, about 15 min left") // positive control
	for range 50 {                                              // eight minutes in which ffmpeg writes no packet of the track
		r.at(10*time.Second, pos)
	}
	// 12 minutes in 10 s to 10 minutes: the average is a little over one.
	if r.m.speed < 0.5 || r.m.speed > 1.5 {
		t.Errorf("speed after the stall = %v, want about 1 (the average since the start)", r.m.speed)
	}
	if got := r.last(); got.done != 12 || !strings.Contains(got.note, ", about ") || got.note == "Reading the file: 12%, about 15 min left" {
		t.Errorf("after the stall: %+v", got)
	}
}

// ffmpeg's updates come twice a second; the job is told at most every two seconds.
func TestSubsMeterThrottlesTheJob(t *testing.T) {
	r := newMeterRig(t, testFilm)
	n := len(r.calls)  // the opening note
	for i := range 7 { // at 0.5, 1.0, ... 3.5 s
		r.at(500*time.Millisecond, time.Duration(i+1)*time.Second)
		if i == 0 && len(r.calls) != n+1 {
			t.Fatalf("the first reading was not shown") // positive control: it does show
		}
	}
	// Shown: the first (0.5 s), and the first two seconds after it (2.5 s).
	if got := len(r.calls) - n; got != 2 {
		t.Errorf("%d updates in 3.5 s, want 2: %+v", got, r.calls[n:])
	}
	r.at(500*time.Millisecond, 8*time.Second) // 4.0 s, 1.5 s after the last shown
	if got := len(r.calls) - n; got != 2 {
		t.Errorf("an update 1.5 s after the last was shown")
	}
	r.at(500*time.Millisecond, 9*time.Second) // 4.5 s, 2 s after
	if got := len(r.calls) - n; got != 3 {
		t.Errorf("the update 2 s after the last was not shown (%d updates)", got)
	}
	if got := r.last(); got.done != 0 || got.note != "Reading the file: 0%" {
		// 9 s of a 100 minutes: not yet one percent.
		t.Errorf("last = %+v", got)
	}
}

// A play that suspends ffmpeg, and the hours before it goes on, are not time spent reading: the
// time left after is what it was before.
func TestSubsMeterDoesNotCountTheTimeItWaits(t *testing.T) {
	r := newMeterRig(t, testFilm)
	pos := r.steady(12, 0)
	r.want(12, 100, "Reading the file: 12%, about 15 min left") // positive control

	r.m.wait("a video is playing from the same disk")
	r.want(12, 100, "Waiting: a video is playing from the same disk") // the bar stays
	r.clock = r.clock.Add(time.Hour)
	r.m.wait("") // goes on
	r.want(12, 100, "Reading the file: 12%, about 15 min left")

	r.at(10*time.Second, pos+time.Minute)
	r.want(13, 100, "Reading the file: 13%, about 15 min left") // 87 min of film, at 6x: 14.5 min
	r.steady(5, pos+time.Minute)
	if got := r.last(); got.note != "Reading the file: 18%, about 14 min left" { // 82 at 6x: 13.7
		t.Errorf("a few updates after: %+v", got)
	}
}

// While it waits the job says why, even if the last line of ffmpeg's progress comes in late.
func TestSubsMeterKeepsTheReasonOverTheProgress(t *testing.T) {
	r := newMeterRig(t, testFilm)
	r.steady(12, 0)
	r.m.wait("a video is playing from the same disk")
	r.at(5*time.Second, 13*time.Minute) // written before ffmpeg was stopped, read after
	r.want(13, 100, "Waiting: a video is playing from the same disk")
	r.m.wait("")
	r.want(13, 100, "Reading the file: 13%, about 15 min left")
}

// A reason is told as it comes, whatever the throttle.
func TestSubsMeterTellsAWaitAtOnce(t *testing.T) {
	r := newMeterRig(t, testFilm)
	r.at(time.Second, 3*time.Minute)
	r.clock = r.clock.Add(100 * time.Millisecond) // well within the two seconds
	r.m.wait("a video is playing from the same disk")
	r.want(3, 100, "Waiting: a video is playing from the same disk")
	r.clock = r.clock.Add(100 * time.Millisecond)
	r.m.wait("")
	r.want(3, 100, "Reading the file: 3%")
}

// When the run is tried again from the start with fewer tracks the position goes back, and the
// speed of the read that failed does not count for this one.
func TestSubsMeterStartsAgainWhenTheReadDoes(t *testing.T) {
	r := newMeterRig(t, testFilm)
	r.steady(50, 0) // 50 minutes
	r.want(50, 100, "Reading the file: 50%, about 8 min left")

	r.at(10*time.Second, time.Minute) // starts over, at 6x again
	if got := r.last(); got.done != 1 {
		t.Fatalf("after starting over: %+v", got)
	}
	r.at(10*time.Second, 2*time.Minute)
	r.want(2, 100, "Reading the file: 2%, about 16 min left") // 98 minutes of film at a minute in 10 s
}

// Without the length of the film there is no share to show; and past the end of it, or at it,
// the bar waits for the read to finish.
func TestSubsMeterWithoutAFilmLengthOrPastIt(t *testing.T) {
	r := newMeterRig(t, 0)
	r.steady(5, 0)
	r.want(0, 0, "Reading the file")

	r = newMeterRig(t, 10*time.Minute)
	r.steady(5, 0)
	r.want(50, 100, "Reading the file: 50%, about 1 min left") // positive control
	r.at(10*time.Second, 11*time.Minute)
	r.want(99, 100, "Reading the file: 99%, less than a minute left")
}

// The clock of the run starts at the first thing ffmpeg says, not when the job did: an hour at
// the gate before the read (Yield) is not time spent reading.
func TestSubsMeterStartsItsClockWhenFFmpegDoes(t *testing.T) {
	r := newMeterRig(t, testFilm)
	r.clock = r.clock.Add(time.Hour) // the job waited for the disk
	r.at(10*time.Second, time.Minute)
	if got := r.m.window[0].at; got != 0 {
		t.Errorf("first reading is at %v of running, want 0", got)
	}

	// The first thing may be a wait (a play started in the first moments): the clock starts there,
	// and the wait is not running time either.
	r = newMeterRig(t, testFilm)
	r.clock = r.clock.Add(time.Hour)
	r.m.wait("a video is playing from the same disk")
	r.clock = r.clock.Add(30 * time.Minute)
	r.m.wait("")
	r.at(10*time.Second, time.Minute)
	if got := r.m.window[0].at; got != 10*time.Second {
		t.Errorf("first reading after a wait is at %v of running, want 10s", got)
	}
}

// A speed near 0 must not make the time left a number that wraps: it is said as long as it may be.
func TestSubsMeterClampsTheTimeLeft(t *testing.T) {
	r := newMeterRig(t, testFilm)
	r.steady(3, 0)
	r.want(3, 100, "Reading the file: 3%, about 16 min left") // positive control: it says how long, normally

	for _, speed := range []float64{1e-9, 1e-15, 5e-324} {
		r.m.speed = speed
		r.m.wait("a video is playing from the same disk")
		r.m.wait("") // shows it again
		r.want(3, 100, "Reading the file: 3%, about 365 days left")
	}
}

func TestTimeLeft(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{0, "less than a minute left"},
		{29 * time.Second, "less than a minute left"},
		{30 * time.Second, "about 1 min left"},
		{12*time.Minute + 10*time.Second, "about 12 min left"},
		{59 * time.Minute, "about 59 min left"},
		{59*time.Minute + 40*time.Second, "about 1 h left"},
		{81 * time.Minute, "about 1 h 20 min left"},
		{2 * time.Hour, "about 2 h left"},
		{5*time.Hour + 52*time.Minute, "about 5 h 50 min left"},
		{101 * time.Hour, "about 4 days left"},
	} {
		if got := timeLeft(tc.d); got != tc.want {
			t.Errorf("timeLeft(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
