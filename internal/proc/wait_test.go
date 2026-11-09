package proc

import (
	"sync"
	"testing"
	"time"
)

func TestAwait(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	start := time.Now()
	if Await(&wg, 100*time.Millisecond) {
		t.Fatal("Await reported done while the group had one left")
	}
	if d := time.Since(start); d < 100*time.Millisecond || d > 2*time.Second {
		t.Errorf("Await gave up after %v, want about 100ms", d)
	}

	wg.Done() // positive control: a group that is done is reported as done, at once
	start = time.Now()
	if !Await(&wg, 10*time.Second) {
		t.Fatal("Await did not see the group done")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Await of a done group took %v", d)
	}
}
