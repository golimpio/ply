//go:build unix

package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A group of processes standing in for an ffmpeg that a ply left behind: a shell
// that leads its own process group and has a helper in it, like ffmpeg with the
// processes it starts. It is a child of the test, so it is waited for as soon as
// it dies (a zombie would still answer kill(pid, 0)); that is init's job for the
// real thing.
type orphan struct {
	leader, helper int
	waited         chan struct{}
}

func startOrphan(t *testing.T) orphan {
	t.Helper()
	helperFile := filepath.Join(t.TempDir(), "helper.pid")
	cmd := exec.Command("sh", "-c", fmt.Sprintf("sleep 300 & echo $! > %[1]q.tmp; mv %[1]q.tmp %[1]q; wait", helperFile))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	o := orphan{leader: cmd.Process.Pid, waited: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(o.waited) }()
	t.Cleanup(func() { // whatever a failing test leaves behind
		_ = syscall.Kill(-o.leader, syscall.SIGKILL)
		if o.helper > 0 {
			_ = syscall.Kill(o.helper, syscall.SIGKILL)
		}
		<-o.waited
	})
	waitUntil(t, "the helper process to start", 10*time.Second, func() bool {
		b, err := os.ReadFile(helperFile)
		if err != nil {
			return false
		}
		o.helper, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil
	})
	if !pidAlive(o.leader) || !pidAlive(o.helper) || groupOf(o.leader) != o.leader { // positive control
		t.Fatalf("setup: leader %d (group %d) alive=%v, helper %d alive=%v", o.leader, groupOf(o.leader), pidAlive(o.leader), o.helper, pidAlive(o.helper))
	}
	return o
}

// entry is what the registry would say of the orphan.
func (o orphan) entry(t *testing.T, sid, dir string) regEntry {
	t.Helper()
	start, err := processStart(o.leader)
	if err != nil {
		t.Fatal(err)
	}
	return regEntry{SID: sid, Item: 7, Title: "Movie", PID: o.leader, PGID: o.leader, Start: start, Program: "ffmpeg", Dir: dir}
}

func (o orphan) wantDead(t *testing.T, what string) {
	t.Helper()
	waitUntil(t, "the orphan's leader to be gone after "+what, 5*time.Second, func() bool { return !pidAlive(o.leader) })
	waitUntil(t, "the orphan's helper to be gone after "+what, 5*time.Second, func() bool { return !pidAlive(o.helper) })
}

func (o orphan) wantAlive(t *testing.T, what string) {
	t.Helper()
	time.Sleep(200 * time.Millisecond) // a kill is not instantaneous
	if !pidAlive(o.leader) || !pidAlive(o.helper) || strings.HasPrefix(procState(o.leader), "Z") {
		t.Errorf("the orphan (leader %d, helper %d) was killed: %s", o.leader, o.helper, what)
	}
}

func writeRegistryFile(t *testing.T, dir string, e regEntry) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, e.SID+".json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// A new ply kills the ffmpeg groups that the registry names, wherever its own
// temp directory is, and whatever state they are in, and clears away what they
// had.
func TestStartupSweepKillsWhatTheRegistryNames(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		name := map[bool]string{false: "running", true: "stopped"}[stopped]
		t.Run(name, func(t *testing.T) {
			o := startOrphan(t)
			if stopped {
				if err := syscall.Kill(-o.leader, syscall.SIGSTOP); err != nil {
					t.Fatal(err)
				}
				waitUntil(t, "the leader to stop", 5*time.Second, func() bool { return strings.HasPrefix(procState(o.leader), "T") })
			}
			root, reg := t.TempDir(), filepath.Join(t.TempDir(), "sessions")
			dir := filepath.Join(root, "2147483000", "abc") // where the dead ply made it
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			file := writeRegistryFile(t, reg, o.entry(t, "abc", dir))

			m, err := NewManager(Options{TempRoot: root, RegistryDir: reg})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			o.wantDead(t, "the startup sweep")
			if exists(file) {
				t.Error("the registry file of the dead session is still there")
			}
			if exists(dir) {
				t.Error("the directory of the dead session is still there")
			}
		})
	}
}

// pids get reused: a process that has the pid of the recorded ffmpeg but did not
// start when it did is somebody else's and is left alone.
func TestStartupSweepSparesAReusedPid(t *testing.T) {
	reused, genuine := startOrphan(t), startOrphan(t)
	root, reg := t.TempDir(), filepath.Join(t.TempDir(), "sessions")
	e := reused.entry(t, "reused", filepath.Join(root, "1", "reused"))
	e.Start = "not-" + e.Start // what was recorded is not this process
	reusedFile := writeRegistryFile(t, reg, e)
	genuineFile := writeRegistryFile(t, reg, genuine.entry(t, "genuine", filepath.Join(root, "1", "genuine")))

	m, err := NewManager(Options{TempRoot: root, RegistryDir: reg})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	genuine.wantDead(t, "the startup sweep") // positive control: the sweep ran, and kills what it should
	reused.wantAlive(t, "its pid is in the registry, but the process did not start when the registry says")
	if exists(reusedFile) || exists(genuineFile) {
		t.Errorf("registry files left after the sweep: %v %v", exists(reusedFile), exists(genuineFile))
	}
}

// A ply that is still running is not to have its ffmpeg killed by one that starts
// beside it (the old one being replaced by the new one, say).
func TestStartupSweepLeavesAnotherLivePlyAlone(t *testing.T) {
	owner := exec.Command("sleep", "300")
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	ownerDone := make(chan struct{})
	go func() { _ = owner.Wait(); close(ownerDone) }()
	t.Cleanup(func() { _ = owner.Process.Kill(); <-ownerDone })
	ownerStart, err := processStart(owner.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}

	o, reused := startOrphan(t), startOrphan(t)
	root, reg := t.TempDir(), filepath.Join(t.TempDir(), "sessions")
	e := o.entry(t, "theirs", filepath.Join(root, "1", "theirs"))
	e.Owner, e.OwnerStart = owner.Process.Pid, ownerStart
	file := writeRegistryFile(t, reg, e)
	// The pid of the owner of this one is alive, but it is another process that has
	// it now (it did not start when the file says): its ply is gone.
	r := reused.entry(t, "reused", filepath.Join(root, "1", "reused"))
	r.Owner, r.OwnerStart = owner.Process.Pid, "not-"+ownerStart
	writeRegistryFile(t, reg, r)

	m, err := NewManager(Options{TempRoot: root, RegistryDir: reg})
	if err != nil {
		t.Fatal(err)
	}
	reused.wantDead(t, "the startup sweep (its owner's pid is somebody else's now)") // positive control: the sweep ran
	o.wantAlive(t, "its ply is still running")
	if !exists(file) {
		t.Error("the registry file of a running ply was removed")
	}
	m.Close()

	// Once that ply is gone, the next start does find it (positive control).
	_ = owner.Process.Kill()
	<-ownerDone
	m, err = NewManager(Options{TempRoot: root, RegistryDir: reg})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	o.wantDead(t, "its ply died")
}

// The sweep removes the directory a registry file names only if it is under the
// temp root: the file is read back from disk and is not to be trusted to point
// anywhere.
func TestSweepDoesNotRemoveDirectoriesOutsideTheTempRoot(t *testing.T) {
	o := startOrphan(t)
	root, reg := t.TempDir(), filepath.Join(t.TempDir(), "sessions")
	outside := t.TempDir()
	keep := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(keep, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRegistryFile(t, reg, o.entry(t, "out", outside))
	m, err := NewManager(Options{TempRoot: root, RegistryDir: reg})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	o.wantDead(t, "the startup sweep") // positive control: the entry was acted on
	if !exists(keep) {
		t.Error("a directory outside the temp root was removed")
	}
}

func TestUnusableRegistryFilesAreRemoved(t *testing.T) {
	reg := t.TempDir()
	junk := filepath.Join(reg, "junk.json")
	incomplete := filepath.Join(reg, "incomplete.json")
	other := filepath.Join(reg, "notes.txt")
	for p, content := range map[string]string{junk: "{not json", incomplete: `{"sid":"x"}`, other: "keep"} {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m, err := NewManager(Options{TempRoot: t.TempDir(), RegistryDir: reg})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if exists(junk) || exists(incomplete) {
		t.Error("registry files that cannot be used were left")
	}
	if !exists(other) {
		t.Error("a file that is not a registry file was removed")
	}
}

// The registry file is there before Create returns, says what is running, follows
// a restart of ffmpeg, and is gone when the process is.
func TestRegistryFollowsTheSession(t *testing.T) {
	f := newFake(t)
	reg := filepath.Join(t.TempDir(), "sessions")
	m := fakeManager(t, f, func(o *Options) { o.RegistryDir = reg })
	req := fakeRequest()
	req.ItemID, req.Title = 42, "A Movie"
	s, err := m.Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	read := func() regEntry {
		t.Helper()
		e, err := m.reg.read(filepath.Join(reg, s.ID+".json"))
		if err != nil {
			t.Fatalf("registry file right after Create: %v", err)
		}
		return e
	}
	first := read()
	s.mu.Lock()
	pid := s.run.cmd.Process.Pid
	s.mu.Unlock()
	start, _ := processStart(pid)
	if first.SID != s.ID || first.PID != pid || first.PGID != pid || first.Start != start || first.Start == "" ||
		first.Item != 42 || first.Title != "A Movie" || first.Dir != s.Dir() || first.Owner != os.Getpid() || first.Program == "" {
		t.Errorf("registry entry = %+v (ffmpeg pid %d started %q, dir %s)", first, pid, start, s.Dir())
	}
	if groupOf(pid) != pid {
		t.Errorf("ffmpeg %d is in group %d, not its own", pid, groupOf(pid))
	}

	// A seek replaces ffmpeg: the file says so, and the old one is gone.
	seg, err := s.OpenSegment(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	seg.Close()
	second := read()
	// (The pid, not the start token: on Linux that is in clock ticks of 10 ms, and a
	// restart takes less than that, so the two ffmpeg can have the same one. A pid
	// that is reused within a tick is not a case: they are given out in turn.)
	if second.PID == first.PID {
		t.Errorf("the registry still names the replaced ffmpeg: %+v", second)
	}
	if pidAlive(first.PID) {
		t.Errorf("replaced ffmpeg %d is still alive", first.PID)
	}
	if !stillRunning(second) {
		t.Errorf("the registry names an ffmpeg that is not running: %+v", second)
	}

	m.Delete(s.ID)
	if exists(filepath.Join(reg, s.ID+".json")) {
		t.Error("the registry file is still there after the session was deleted")
	}
	if pidAlive(second.PID) {
		t.Error("ffmpeg is still alive after the session was deleted")
	}
}

// An ffmpeg that ends by itself (the end of the file) leaves no file behind
// either, although its session goes on.
func TestRegistryFileGoesWhenFFmpegExits(t *testing.T) {
	quick := filepath.Join(t.TempDir(), "ffmpeg")
	if err := writeExecutable(quick, "#!/bin/sh\nexit 0\n"); err != nil {
		t.Fatal(err)
	}
	reg := filepath.Join(t.TempDir(), "sessions")
	m := fakeManager(t, fake{ffmpeg: quick}, func(o *Options) { o.RegistryDir = reg })
	s, err := m.Create(context.Background(), fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the registry file of the finished ffmpeg to be removed", 5*time.Second, func() bool {
		return !exists(filepath.Join(reg, s.ID+".json"))
	})
	if _, ok := m.Get(s.ID); !ok {
		t.Error("the session ended with its ffmpeg")
	}
}

// What the registry holds that no session explains is an ffmpeg that got away.
func TestLeakCheckKillsAGroupNoSessionOwns(t *testing.T) {
	f := newFake(t)
	reg := filepath.Join(t.TempDir(), "sessions")
	m := fakeManager(t, f, func(o *Options) { o.RegistryDir = reg; o.LeakCheck = 20 * time.Millisecond })
	s, err := m.Create(context.Background(), fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(reg, s.ID+".json")

	o := startOrphan(t)
	sessionDir := filepath.Join(m.Dir(), "ghost")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	e := o.entry(t, "ghost", sessionDir)
	e.Owner = os.Getpid() // one of ours, that the manager has forgotten
	e.OwnerStart, _ = processStart(os.Getpid())
	ghost := writeRegistryFile(t, reg, e)

	o.wantDead(t, "the leak check")
	waitUntil(t, "the ghost's registry file to be removed", 5*time.Second, func() bool { return !exists(ghost) })
	if exists(sessionDir) {
		t.Error("the ghost's session directory was left")
	}
	// The session the manager does know is not touched, however often it looks.
	time.Sleep(200 * time.Millisecond)
	if !exists(live) || len(f.procs(t)) != 1 {
		t.Errorf("a live session was disturbed by the leak check: file %v, ffmpeg running: %v", exists(live), f.procs(t))
	}
}

// Anything an earlier version left in os.TempDir()/ply-sessions is still found
// for one release, with the sessions now in the data directory.
func TestLegacyTempDirIsStillSwept(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	sleeper := exec.Command("sleep", "300")
	sleeper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = sleeper.Wait(); close(done) }()
	t.Cleanup(func() { _ = syscall.Kill(-sleeper.Process.Pid, syscall.SIGKILL); <-done })

	session := filepath.Join(tmp, "ply-sessions", "2147483000", "old")
	if err := os.MkdirAll(session, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(session, pidFile), []byte(fmt.Sprintf("%d /usr/bin/sleep\n", sleeper.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}

	data := t.TempDir()
	m, err := NewManager(Options{Data: data})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("the process left by an earlier version was not killed")
	}
	if exists(filepath.Join(tmp, "ply-sessions", "2147483000")) {
		t.Error("the old session directory is still there")
	}
	// The new places.
	if want := filepath.Join(data, "tmp", "sessions", strconv.Itoa(os.Getpid())); m.Dir() != want {
		t.Errorf("session directories are made in %s, want %s", m.Dir(), want)
	}
	if !exists(filepath.Join(data, "sessions")) {
		t.Error("no registry directory in the data directory")
	}
}

// A registry file is read back from disk, and a group is killed on what it says:
// it has to be what ply writes. ffmpeg leads the group it is started in, so a file
// whose pid is not its pgid is not one of ours; one that names a member of a group
// (with the member's own, true, start time) and the group of its leader used to
// kill that group.
func TestAnEntryWhosePidDoesNotLeadItsGroupKillsNothing(t *testing.T) {
	victim, genuine := startOrphan(t), startOrphan(t)
	root, reg := t.TempDir(), filepath.Join(t.TempDir(), "sessions")
	start, err := processStart(victim.helper)
	if err != nil {
		t.Fatal(err)
	}
	forged := writeRegistryFile(t, reg, regEntry{SID: "forged", PID: victim.helper, PGID: victim.leader, Start: start, Program: "ffmpeg"})
	// ... and a member that is named as the leader of a group of its own, which it does not lead.
	same := writeRegistryFile(t, reg, regEntry{SID: "same", PID: victim.helper, PGID: victim.helper, Start: start, Program: "ffmpeg"})
	writeRegistryFile(t, reg, genuine.entry(t, "genuine", filepath.Join(root, "1", "genuine")))

	m, err := NewManager(Options{TempRoot: root, RegistryDir: reg})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	genuine.wantDead(t, "the startup sweep") // positive control: the sweep killed what it should
	victim.wantAlive(t, "a registry file named the group of its leader and the pid of a member")
	if exists(forged) || exists(same) {
		t.Error("files that are not ply's were left")
	}
	if _, err := m.reg.read(writeRegistryFile(t, t.TempDir(), regEntry{SID: "x", PID: 4242, PGID: 4243, Start: "s"})); err == nil {
		t.Error("read accepts a pid that is not its pgid")
	}
}

// The pgid in a file is not to take ply, or what started it, down.
func TestKillGroupNeverTouchesOurOwnGroup(t *testing.T) {
	for _, pgid := range []int{-5, 0, 1, syscall.Getpgrp(), os.Getpid()} {
		if killGroup(pgid) {
			t.Errorf("killGroup(%d) sent a signal", pgid)
		}
	}
	// A registry file that names it (the leader of our group, with its true start time)
	// is read, and found to be running, and still not acted on.
	pg := syscall.Getpgrp()
	start, err := processStart(pg)
	if err != nil {
		t.Skipf("the leader of this process's group (%d) is gone: %v", pg, err)
	}
	reg := filepath.Join(t.TempDir(), "sessions")
	file := writeRegistryFile(t, reg, regEntry{SID: "ours", PID: pg, PGID: pg, Start: start, Program: "go"})
	m, err := NewManager(Options{TempRoot: t.TempDir(), RegistryDir: reg})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if exists(file) { // (and we are here to say so)
		t.Error("the registry file was not cleared")
	}
}

// The registry is acted on with kill(2): it has to be a directory of ours, not a
// link to somebody else's, and not open to others.
func TestTheRegistryDirectoryIsChecked(t *testing.T) {
	o := startOrphan(t)
	target := t.TempDir()
	writeRegistryFile(t, target, o.entry(t, "behind", filepath.Join(t.TempDir(), "1", "x")))
	link := filepath.Join(t.TempDir(), "sessions")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	m, err := NewManager(Options{TempRoot: t.TempDir(), RegistryDir: link})
	if err == nil {
		m.Close()
		t.Fatal("NewManager accepted a symlinked registry directory")
	}
	if !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("err = %v", err)
	}
	o.wantAlive(t, "the registry directory was a link")
	if !exists(filepath.Join(target, "behind.json")) {
		t.Error("a file behind the link was touched")
	}

	// Positive control: the same directory, as a real one, is acted on.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, link); err != nil {
		t.Fatal(err)
	}
	m, err = NewManager(Options{TempRoot: t.TempDir(), RegistryDir: link})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	o.wantDead(t, "the startup sweep")
}

func TestTheRegistryDirectoryIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data", "sessions")
	m, err := NewManager(Options{TempRoot: t.TempDir(), RegistryDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("registry dir: %v, %v; want mode 0700", fi, err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	m, err = NewManager(Options{TempRoot: t.TempDir(), RegistryDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("registry dir mode = %v, want 0700", fi.Mode().Perm())
	}
}

// The directory a file names is removed only if it is below the temp root; the
// root itself is not, however it is spelled.
func TestSweepNeverRemovesTheTempRootItself(t *testing.T) {
	plain, dotted := startOrphan(t), startOrphan(t)
	root, reg := t.TempDir(), filepath.Join(t.TempDir(), "sessions")
	keep := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(keep, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRegistryFile(t, reg, plain.entry(t, "plain", root))
	writeRegistryFile(t, reg, dotted.entry(t, "dotted", filepath.Join(root, "x", "..")))
	m, err := NewManager(Options{TempRoot: root, RegistryDir: reg})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	plain.wantDead(t, "the startup sweep") // positive control: the entries were acted on
	dotted.wantDead(t, "the startup sweep")
	if !exists(keep) {
		t.Error("the temp root was removed")
	}
}

func TestStaleTemporaryRegistryFilesAreCleared(t *testing.T) {
	reg := t.TempDir()
	old, fresh := filepath.Join(reg, ".tmp-old"), filepath.Join(reg, ".tmp-fresh")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	longAgo := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, longAgo, longAgo); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(Options{TempRoot: t.TempDir(), RegistryDir: reg})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if exists(old) {
		t.Error("a temporary file from two hours ago was left")
	}
	if !exists(fresh) {
		t.Error("a temporary file that may be being written was removed")
	}
}

// Where there is a registry, the pid files of the session directories are not
// used to find ffmpeg: what ran there is in the registry. Where there is none
// they are (positive control).
func TestPidFilesAreNotUsedWhereThereIsARegistry(t *testing.T) {
	for _, withRegistry := range []bool{true, false} {
		t.Run(map[bool]string{true: "registry", false: "no registry"}[withRegistry], func(t *testing.T) {
			sleeper := exec.Command("sleep", "300")
			sleeper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := sleeper.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { _ = sleeper.Wait(); close(done) }()
			t.Cleanup(func() { _ = syscall.Kill(-sleeper.Process.Pid, syscall.SIGKILL); <-done })

			root := t.TempDir()
			session := filepath.Join(root, "2147483000", "old")
			if err := os.MkdirAll(session, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(session, pidFile), []byte(fmt.Sprintf("%d /usr/bin/sleep\n", sleeper.Process.Pid)), 0o600); err != nil {
				t.Fatal(err)
			}
			o := Options{TempRoot: root}
			if withRegistry {
				o.RegistryDir = filepath.Join(t.TempDir(), "sessions")
			}
			m, err := NewManager(o)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			if exists(filepath.Join(root, "2147483000")) {
				t.Error("the stale directory was left")
			}
			select {
			case <-done:
				if withRegistry {
					t.Error("the pid file was used to kill a process although the registry is what knows")
				}
			case <-time.After(time.Second):
				if !withRegistry {
					t.Error("without a registry the pid file did not find the process")
				}
			}
		})
	}
}

// The reaper is a process of its own that ply starts for every ffmpeg and ends
// when the ffmpeg has ended, however it ended; it is run by a link named
// ply-reaper to the program, so that killing every "ply" by name does not take it.
func TestTheReaperIsEndedWithItsFFmpeg(t *testing.T) {
	if !reaperWanted {
		t.Skip("only macOS starts reapers")
	}
	for _, name := range []string{"ffmpeg exits by itself", "session deleted"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			pidFile, argsFile := filepath.Join(dir, "reaper.pid"), filepath.Join(dir, "reaper.args")
			standIn := filepath.Join(dir, "stand-in")
			if err := writeExecutable(standIn, fmt.Sprintf("#!/bin/sh\necho \"$0 $@\" > %[2]q\necho $$ > %[1]q.tmp\nmv %[1]q.tmp %[1]q\nexec sleep 600\n", pidFile, argsFile)); err != nil {
				t.Fatal(err)
			}
			body := "#!/bin/sh\nexec sleep 600\n"
			if name == "ffmpeg exits by itself" {
				body = "#!/bin/sh\nsleep 1\nexit 0\n"
			}
			ffmpeg := filepath.Join(dir, "ffmpeg")
			if err := writeExecutable(ffmpeg, body); err != nil {
				t.Fatal(err)
			}
			m := fakeManager(t, fake{ffmpeg: ffmpeg}, func(o *Options) {
				o.Reaper, o.RegistryDir = standIn, filepath.Join(dir, "sessions")
			})
			s, err := m.Create(context.Background(), fakeRequest())
			if err != nil {
				t.Fatal(err)
			}
			var reaper int
			waitUntil(t, "the reaper to start", 10*time.Second, func() bool {
				b, err := os.ReadFile(pidFile)
				if err != nil {
					return false
				}
				reaper, err = strconv.Atoi(strings.TrimSpace(string(b)))
				return err == nil
			})
			args, _ := os.ReadFile(argsFile)
			if !strings.Contains(string(args), "/ply-reaper --reap ") || !strings.Contains(string(args), " "+strconv.Itoa(os.Getpid())) {
				t.Errorf("the reaper was started as %q, want <data>/ply-reaper --reap <pgid> %d", args, os.Getpid())
			}
			if target, err := os.Readlink(filepath.Join(dir, "ply-reaper")); err != nil || target != standIn {
				t.Errorf("ply-reaper is a link to %q (%v), want %q", target, err, standIn)
			}
			if !pidAlive(reaper) { // positive control
				t.Fatalf("the reaper (pid %d) is not running while ffmpeg is", reaper)
			}
			if name == "session deleted" {
				m.Delete(s.ID)
			}
			waitUntil(t, "the reaper to be gone with its ffmpeg", 10*time.Second, func() bool { return !pidAlive(reaper) })
		})
	}
}
