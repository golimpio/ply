package stream

import (
	"bufio"
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
// outlives a ply that is killed (TestKilledServerLeavesNoFFmpeg checks that it
// does not, with a real crash).
func TestFFmpegDiesWithItsParent(t *testing.T) {
	cmd := exec.Command("ffmpeg")
	ownGroup(cmd)
	a := cmd.SysProcAttr
	if a == nil || a.Pdeathsig != syscall.SIGKILL {
		t.Errorf("SysProcAttr = %+v, want Pdeathsig SIGKILL", a)
	}
	if !a.Setpgid {
		t.Error("ffmpeg does not get a process group of its own")
	}
}

// A helper process, not a test: it starts a process the way a session starts
// ffmpeg (ownGroup, startProcess), says its pid, and then goes on churning the Go
// runtime for ever: goroutines that lock their threads and exit, which retires
// those threads, and collections. A child whose Pdeathsig is tied to a thread
// that retires would be killed by that, with its parent alive.
func TestHelperStartsAProcessLikeASession(t *testing.T) {
	if os.Getenv("PLY_TEST_PDEATHSIG_HELPER") == "" {
		t.Skip("helper process of TestAProcessStartedLikeASessionDiesWithItsParent")
	}
	cmd := exec.Command("sleep", "300")
	ownGroup(cmd)
	if err := startProcess(cmd, func(error) {}); err != nil {
		t.Fatal(err)
	}
	fmt.Println(cmd.Process.Pid)
	for {
		runtime.GC()
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				runtime.LockOSThread() // never unlocked: the thread ends with the goroutine
				time.Sleep(time.Millisecond)
			}()
		}
		wg.Wait()
		time.Sleep(5 * time.Millisecond)
	}
}

// The real thing: a process started through startProcess lives as long as its
// parent does, whatever the runtime does with its threads, and dies with it when
// it is killed (kill -9), at once and without anybody's help.
func TestAProcessStartedLikeASessionDiesWithItsParent(t *testing.T) {
	helper := exec.Command(os.Args[0], "-test.run=^TestHelperStartsAProcessLikeASession$")
	helper.Env = append(os.Environ(), "PLY_TEST_PDEATHSIG_HELPER=1")
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
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
	line := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if _, err := strconv.Atoi(strings.TrimSpace(sc.Text())); err == nil {
				line <- sc.Text()
				break
			}
		}
		_, _ = bufio.NewReader(stdout).Discard(1 << 20)
	}()
	go func() { _ = helper.Wait(); close(helperDone) }()
	select {
	case l := <-line:
		child, _ = strconv.Atoi(strings.TrimSpace(l))
	case <-time.After(20 * time.Second):
		t.Fatal("the helper did not start its process")
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
