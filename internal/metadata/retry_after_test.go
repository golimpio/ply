package metadata

import (
	"testing"
	"time"
)

// How long a failed fetch is left alone: a day for what the source does not have; else 5 min,
// doubling, at most an hour.
func TestRetryAfter(t *testing.T) {
	for _, c := range []struct {
		attempts int
		missing  bool
		want     time.Duration
	}{
		{0, true, 24 * time.Hour}, {3, true, 24 * time.Hour},
		{0, false, 5 * time.Minute}, {1, false, 10 * time.Minute}, {3, false, 40 * time.Minute}, {4, false, time.Hour}, {50, false, time.Hour},
	} {
		if got := RetryAfter(c.attempts, c.missing); got != c.want {
			t.Errorf("RetryAfter(%d, %v) = %v, want %v", c.attempts, c.missing, got, c.want)
		}
	}
}
