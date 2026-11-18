package stream

import (
	"fmt"
	"slices"
	"testing"
)

func TestKeyframeScanProcessPriority(t *testing.T) {
	// ps reports the child's scheduling band. Poll because the policy takes
	// effect on a scheduling decision after Start returns to the parent.
	for _, tc := range []struct {
		name      string
		nice      int
		condition string
	}{
		{name: "foreground", condition: "-gt 20"},
		{name: "background", nice: 10, condition: "-le 4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := fakeFFprobe(t, fmt.Sprintf(`
attempt=0
while [ "$attempt" -lt 100 ]; do
  attempt=$((attempt + 1))
  priority=$(ps -o pri= -p $$)
  if [ "$priority" -eq "$priority" ] && [ "$priority" %s ]; then
    printf '1.000000,K__,\n'
    exit 0
  fi
  sleep 0.03
done
echo "unexpected process priority: $priority" >&2
exit 1`, tc.condition))
			keyframes, err := ExtractKeyframes(t.Context(), probe, "unused.mp4", tc.nice)
			if err != nil || !slices.Equal(keyframes, []float64{1}) {
				t.Fatalf("keyframes = %v, err = %v", keyframes, err)
			}
		})
	}
}
