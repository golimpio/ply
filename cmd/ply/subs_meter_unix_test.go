//go:build unix

package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"ply/internal/jobs"
)

// running is the job of kind that is running, if one is.
func (r *ttffRig) running(kind string) (jobs.Job, bool) {
	for _, j := range r.q.Snapshot().Running {
		if j.Kind == kind {
			return j, true
		}
	}
	return jobs.Job{}, false
}

// What ffmpeg says about its progress reaches the Activity page: the bar (of 100) and the
// note, which a play that suspends the read replaces for as long as it lasts, with the bar
// where it was.
func TestAnExtractionShowsHowFarFFmpegHasRead(t *testing.T) {
	r := newTTFFRig(t, `
printf 'out_time_us=N/A\nprogress=continue\nout_time_us=150000000\nprogress=continue\n'
while [ ! -f "$0.go" ]; do sleep 0.02; done
`+subsFake)
	gate := strings.TrimSuffix(r.log, ".log") + ".go"
	e := r.item("nas", "Film (2020).mp4", true) // 600 s long

	showing := func(done int, note func(string) bool) func() bool {
		return func() bool {
			j, ok := r.running(kindSubsPreextract)
			return ok && j.Done == done && j.Total == 100 && note(j.Note)
		}
	}
	reading := func(n string) bool { return n == "Reading the file: 25%" }
	waiting := func(n string) bool { return strings.HasPrefix(n, "Waiting:") && strings.Contains(n, "same disk") }

	r.enqueueSubs(e)
	waitUntil(t, "the job to show a quarter read", 10*time.Second, showing(25, reading))

	r.play(r.file("nas", "playing.mkv"))
	waitUntil(t, "the job to say it waits, with the bar where it was", 5*time.Second, showing(25, waiting))
	r.play()
	waitUntil(t, "the job to read again", 5*time.Second, showing(25, reading))

	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r.waitState(kindSubsPreextract, subsKey(e.ID), jobs.Done)
}
