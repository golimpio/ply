package main

import (
	"log/slog"
	"os"
	"time"
)

// A stopStep is one part of shutting down, named for the log.
type stopStep struct {
	name string
	run  func()
}

// stopper runs the shutdown steps in order and says what it is waiting for, so
// a slow stop (a probe on a slow share, a download) never looks like a hang.
type stopper struct {
	log            *slog.Logger
	every          time.Duration // how often a slow step is reported
	deadline       time.Duration // after this the rest is not waited for
	quick          <-chan os.Signal
	onQuick        func()
	reportProgress func(step string, waited time.Duration, left int)
}

// run reports false if it gave up waiting: the process should exit anyway.
func (s stopper) run(steps []stopStep) bool {
	start := time.Now()
	giveUp := time.NewTimer(s.deadline)
	defer giveUp.Stop()
	tick := time.NewTicker(s.every)
	defer tick.Stop()
	for i, st := range steps {
		done := make(chan struct{})
		go func() { defer close(done); st.run() }()
		began := time.Now()
	wait:
		for {
			select {
			case <-done:
				break wait
			case <-s.quick:
				s.log.Warn("interrupted during shutdown; quitting at once")
				if s.onQuick != nil {
					s.onQuick()
				} else {
					os.Exit(130)
				}
				return false
			case <-tick.C:
				waited := time.Since(began).Round(time.Second)
				s.log.Info("still stopping", "step", st.name, "waited", waited,
					"left", len(steps)-i)
				if s.reportProgress != nil {
					s.reportProgress(st.name, waited, len(steps)-i)
				}
			case <-giveUp.C:
				s.log.Warn("not waiting any longer; exiting", "step", st.name,
					"after", time.Since(start).Round(time.Second))
				return false
			}
		}
	}
	s.log.Info("stopped", "took", time.Since(start).Round(time.Millisecond))
	return true
}
