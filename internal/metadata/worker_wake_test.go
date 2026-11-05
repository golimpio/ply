package metadata_test

import (
	"sync/atomic"
	"testing"

	"ply/internal/metadata"
)

// OnWake is called once for each way of waking the worker, in addition to waking
// it; with no hook nothing changes.
func TestWorkerOnWakeFiresOnNotifyNotifySidecarsJobsAndReload(t *testing.T) {
	var woke atomic.Int32
	e := newWEnv(t, func(o *metadata.WorkerOptions) { o.OnWake = func() { woke.Add(1) } })

	for _, c := range []struct {
		name string
		call func()
	}{
		{"Notify", e.w.Notify},
		{"NotifySidecars", func() { e.w.NotifySidecars([]int64{7}) }},
		{"RefreshItem", func() { e.w.RefreshItem(7) }},
		{"RefreshShow", func() { e.w.RefreshShow("Some Show") }},
		{"RefreshAll", e.w.RefreshAll},
		{"Reload", e.w.Reload},
	} {
		before := woke.Load()
		c.call()
		if got := woke.Load() - before; got != 1 {
			t.Errorf("%s called the hook %d times, want 1", c.name, got)
		}
	}

	// Positive control: nothing to tell the worker about is no wake.
	before := woke.Load()
	e.w.NotifySidecars(nil)
	if woke.Load() != before {
		t.Error("NotifySidecars with no ids called the hook")
	}

	// Without a hook the same calls are harmless.
	plain := newWEnv(t)
	plain.w.Notify()
	plain.w.NotifySidecars([]int64{1})
	plain.w.RefreshAll()
}
