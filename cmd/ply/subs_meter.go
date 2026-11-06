package main

import (
	"fmt"
	"sync"
	"time"
)

const (
	// subsReading is what a subtitle extraction says it is doing.
	subsReading = "Reading the file"

	// subsReportEvery is the least time between two updates of the job from ffmpeg's progress,
	// which comes twice a second: the Activity page and its stream do not need more.
	subsReportEvery = 2 * time.Second

	// The speed is that of the last etaWindow of running time (a shorter span than etaMinSpan
	// says too little), smoothed: the position of a sparse subtitle track moves in steps.
	etaWindow    = time.Minute
	etaMinSpan   = 5 * time.Second
	etaSmoothing = 0.3

	// maxLeft is the longest time left that is said: a speed near 0 would make it astronomical.
	maxLeft = 365 * 24 * time.Hour
)

// subsMeter turns what an extraction says (how far into the film ffmpeg has read, and why it
// waits) into what the Activity page shows: a bar, and "Reading the file: 43%, about 12 min
// left". ffmpeg reads the container whole, so how far into the film its last subtitle packet
// is stands for how much of the file it has read.
//
// The time left is the rest of the film at the speed of the last minute of running time.
// The time ffmpeg is suspended for a play (wait) does not count, or a pause of an hour would
// make the next minute look an hour long.
type subsMeter struct {
	set   func(done, total int, note string) // jobs.Run.Progress
	now   func() time.Time
	every time.Duration // least time between two updates from read
	film  time.Duration // how long the film is; 0 if not known: the bar then does not move

	mu      sync.Mutex
	started time.Time
	shown   time.Time     // the last update from read; zero before the first
	paused  time.Time     // since when the run waits; zero if it does not
	idle    time.Duration // time spent waiting in the waits that are over
	why     string        // what it waits for now
	pos     time.Duration // how far into the film ffmpeg has read
	has     bool          // pos is known
	first   sample
	window  []sample // the samples of the last etaWindow, and the one before them
	speed   float64  // seconds of film per second of running, smoothed; 0 if not known
}

// sample is a position at a time of the running clock (the time since the start that
// the run has not spent waiting).
type sample struct{ at, pos time.Duration }

// newSubsMeter reports to set, and starts by saying that the file is being read.
func newSubsMeter(set func(done, total int, note string), film time.Duration) *subsMeter {
	return newSubsMeterAt(set, film, time.Now)
}

// newSubsMeterAt is newSubsMeter with the clock a test sets.
func newSubsMeterAt(set func(done, total int, note string), film time.Duration, now func() time.Time) *subsMeter {
	m := &subsMeter{set: set, now: now, every: subsReportEvery, film: film}
	m.render()
	return m
}

// begin starts the clock of the run at the first thing it hears from ffmpeg: the job may have
// waited for the disk before that (Yield), and that is not time spent reading.
func (m *subsMeter) begin(now time.Time) {
	if m.started.IsZero() {
		m.started = now
	}
}

// read is told how far into the film ffmpeg has read. It is the progress callback of the
// Generator and comes often; the job is updated at most every m.every.
func (m *subsMeter) read(at time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.begin(now)
	m.pos, m.has = at, true
	if !m.shown.IsZero() && now.Sub(m.shown) < m.every {
		return
	}
	m.shown = now
	if m.paused.IsZero() { // a line written just before ffmpeg was stopped says nothing of its speed
		m.measure(now)
	}
	m.render()
}

// wait is told why the run is suspended, and "" when it goes on (the note callback of the
// Generator). The reason is shown at once, in place of the progress, which stays on the bar.
func (m *subsMeter) wait(why string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.begin(now)
	switch {
	case why != "" && m.paused.IsZero():
		m.paused = now
	case why == "" && !m.paused.IsZero():
		m.idle += now.Sub(m.paused)
		m.paused = time.Time{}
	}
	m.why = why
	m.render()
}

// running is the time the run has been going, not counting the waits that are over (it is
// not asked while one is going on).
func (m *subsMeter) running(now time.Time) time.Duration {
	return now.Sub(m.started) - m.idle
}

// measure adds the position to the samples and works out the speed.
func (m *subsMeter) measure(now time.Time) {
	s := sample{m.running(now), m.pos}
	if n := len(m.window); n > 0 && s.pos < m.window[n-1].pos {
		// Read again from the start (a run with several tracks failed and was tried with one):
		// what was measured says nothing about this.
		m.window, m.speed = nil, 0
	}
	if len(m.window) == 0 {
		m.first = s
	}
	m.window = append(m.window, s)
	for len(m.window) > 1 && m.window[1].at <= s.at-etaWindow {
		m.window = m.window[1:]
	}
	v := speedBetween(m.window[0], s)
	if v <= 0 { // nothing moved in the last minute (a sparse track): the speed since the start
		v = speedBetween(m.first, s)
	}
	switch {
	case v <= 0:
	case m.speed == 0:
		m.speed = v
	default:
		m.speed += etaSmoothing * (v - m.speed)
	}
}

// speedBetween is the seconds of film read per second of running between two samples, 0
// if they are too close to say.
func speedBetween(a, b sample) float64 {
	if b.at-a.at < etaMinSpan {
		return 0
	}
	return (b.pos - a.pos).Seconds() / (b.at - a.at).Seconds()
}

// render tells the job where things are. m.mu is held.
func (m *subsMeter) render() {
	done, total, note := 0, 0, subsReading
	if m.film > 0 && m.has {
		// Never 100% while ffmpeg is still reading: the packets of a film's last minutes can
		// come with times past the length that was probed.
		pct := min(int(min(m.pos, m.film)*100/m.film), 99)
		done, total = pct, 100
		note = fmt.Sprintf("%s: %d%%", subsReading, pct)
		if m.speed > 0 {
			// A speed near 0 (a long stall) is a time that does not fit a Duration.
			left := maxLeft
			if secs := max(m.film-m.pos, 0).Seconds() / m.speed; secs < maxLeft.Seconds() {
				left = time.Duration(secs * float64(time.Second))
			}
			note += ", " + timeLeft(left)
		}
	}
	if m.why != "" {
		note = "Waiting: " + m.why
	}
	m.set(done, total, note)
}

// timeLeft says how long is left, roughly, and in as few words: "about 12 min left",
// "about 1 h 20 min left".
func timeLeft(d time.Duration) string {
	mins := int((d + 30*time.Second) / time.Minute)
	switch {
	case mins < 1:
		return "less than a minute left"
	case mins < 60:
		return fmt.Sprintf("about %d min left", mins)
	}
	mins = (mins + 2) / 5 * 5 // an hour away is not known to the minute
	h, m := mins/60, mins%60
	switch {
	case h >= 100:
		return fmt.Sprintf("about %d days left", (h+12)/24)
	case m == 0:
		return fmt.Sprintf("about %d h left", h)
	}
	return fmt.Sprintf("about %d h %d min left", h, m)
}
