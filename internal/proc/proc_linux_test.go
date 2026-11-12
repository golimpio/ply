package proc

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// On Linux the kernel kills ffmpeg when ply dies: without Pdeathsig an ffmpeg
// outlives a ply that is killed.
func TestChildDiesWithItsParent(t *testing.T) {
	cmd := exec.Command("ffmpeg")
	OwnGroup(cmd)
	a := cmd.SysProcAttr
	if a == nil || a.Pdeathsig != syscall.SIGKILL {
		t.Errorf("SysProcAttr = %+v, want Pdeathsig SIGKILL", a)
	}
	if !a.Setpgid {
		t.Error("the child does not get a process group of its own")
	}
}

// lockedBuffer is what a helper process says, read while it is still talking.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func procState(pid int) string {
	out, _ := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output()
	return strings.TrimSpace(string(out))
}

// A helper process, not a test: it starts a process the way a session starts
// ffmpeg (OwnGroup, Start), says its pid, and then goes on churning the Go runtime
// for ever: goroutines that lock their threads and exit, which retires those
// threads, and collections. A child whose Pdeathsig is tied to a thread that retires
// would be killed by that, with its parent alive.
func TestHelperStartsAProcessLikeASession(t *testing.T) {
	if os.Getenv("PLY_TEST_PDEATHSIG_HELPER") == "" {
		t.Skip("helper process of TestAProcessStartedLikeASessionDiesWithItsParent")
	}
	// (OwnGroup sets Cancel, which only a command made with CommandContext may have.)
	cmd := exec.CommandContext(context.Background(), "sleep", "300")
	OwnGroup(cmd)
	if err := Start(cmd, func(error) {}); err != nil {
		t.Fatal(err)
	}
	fmt.Println(cmd.Process.Pid)
	for {
		runtime.GC()
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				runtime.LockOSThread() // never unlocked: the thread ends with the goroutine
				time.Sleep(time.Millisecond)
			})
		}
		wg.Wait()
		time.Sleep(5 * time.Millisecond)
	}
}

// The real thing: a process started through Start lives as long as its parent
// does, whatever the runtime does with its threads, and dies with it when it is
// killed (kill -9), at once and without anybody's help.
func TestAProcessStartedLikeASessionDiesWithItsParent(t *testing.T) {
	helper := exec.Command(os.Args[0], "-test.run=^TestHelperStartsAProcessLikeASession$")
	helper.Env = append(os.Environ(), "PLY_TEST_PDEATHSIG_HELPER=1")
	said := &lockedBuffer{}
	helper.Stdout, helper.Stderr = said, said
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	helperDone := make(chan struct{})
	var child int
	t.Cleanup(func() {
		_ = helper.Process.Kill()
		<-helperDone
		if child > 1 {
			_ = syscall.Kill(-child, syscall.SIGKILL)
		}
	})
	go func() { _ = helper.Wait(); close(helperDone) }()
	// The helper says the pid of its child on a line of its own.
	deadline := time.After(20 * time.Second)
	for child == 0 {
		if pid, err := strconv.Atoi(strings.TrimSpace(firstLine(said.String()))); err == nil {
			child = pid
			break
		}
		select {
		case <-helperDone:
			t.Fatalf("the helper ended before it started its process; it said:\n%s", said.String())
		case <-deadline:
			t.Fatalf("the helper did not start its process; it said:\n%s", said.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	gone := func() bool { return syscall.Kill(child, 0) != nil || strings.HasPrefix(procState(child), "Z") }

	// Alive while the parent is, through a second of the runtime retiring threads.
	time.Sleep(time.Second)
	if gone() {
		t.Fatalf("the child (pid %d) died although its parent is alive", child)
	}

	if err := helper.Process.Kill(); err != nil { // kill -9: no handler, no cleanup
		t.Fatal(err)
	}
	waitUntil(t, "the child to die with its parent", 2*time.Second, gone)
}
