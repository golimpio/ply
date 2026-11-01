package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A fake ffmpeg: logs its arguments, then writes one GOP fragment every 30 ms
// from -start_number on, forever (like a copy remux that never reaches EOF).
// Fragments are renamed into place like ffmpeg's temp_file flag does.
const fakeScript = `#!/bin/sh
echo "$@" >> "%[1]s"
n=0; init=init.mp4
while [ $# -gt 0 ]; do
  case "$1" in
    -start_number) n=$2;;
    -hls_fmp4_init_filename) init=$2;;
  esac
  shift
done
cp "%[2]s" "$init"
i=$n
while :; do
  f=$(printf 'gop%%05d.m4s' $i)
  cp "%[3]s" "$f.tmp"
  mv "$f.tmp" "$f"
  i=$((i+1))
  sleep 0.03
done
`

var (
	fakeStyp = mkBox("styp", 1, 2, 3, 4)
	fakeGOP  = cat(fakeStyp, mkBox("moof", 9, 9, 9), mkBox("mdat", make([]byte, 100)...))
	fakeInit = cat(mkBox("ftyp", 1, 2, 3, 4), mkBox("moov", 5, 6, 7, 8))
)

type fake struct {
	ffmpeg string // path of the fake binary
	log    string // one line of arguments per start
}

func newFake(t *testing.T) fake {
	t.Helper()
	dir := t.TempDir()
	f := fake{ffmpeg: filepath.Join(dir, "ffmpeg"), log: filepath.Join(dir, "args.log")}
	gop, init := filepath.Join(dir, "gop.tpl"), filepath.Join(dir, "init.tpl")
	for p, b := range map[string][]byte{gop: fakeGOP, init: fakeInit} {
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := fmt.Sprintf(fakeScript, f.log, init, gop)
	if err := writeExecutable(f.ffmpeg, script); err != nil {
		t.Fatal(err)
	}
	return f
}

// starts returns the arguments of every ffmpeg start so far.
func (f fake) starts() []string {
	b, _ := os.ReadFile(f.log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// procs lists the running fake ffmpeg processes.
func (f fake) procs(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, f.ffmpeg) {
			found = append(found, strings.TrimSpace(l))
		}
	}
	return found
}

// fixedKeyframes is a cache with a keyframe every 2 s for 1200 s (200
// segments of 3 GOPs).
type fixedKeyframes struct{}

func (fixedKeyframes) Keyframes(context.Context, int64) ([]float64, bool, error) {
	return evenKeyframes(600, 2), true, nil
}
func (fixedKeyframes) SetKeyframes(context.Context, int64, []float64) error { return nil }

func fakeManager(t *testing.T, f fake, mod func(*Options)) *Manager {
	t.Helper()
	o := Options{
		FFmpeg: f.ffmpeg, TempRoot: t.TempDir(), Cache: fixedKeyframes{},
		Tick: 20 * time.Millisecond, WaitTimeout: 10 * time.Second,
	}
	if mod != nil {
		mod(&o)
	}
	m, err := NewManager(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func fakeRequest() Request {
	return Request{ItemID: 1, Path: "/media/x.mkv", Container: "mkv", DurationSec: 1200,
		VideoIndex: 0, VideoCodec: "hevc", AudioIndex: 1, AudioCodec: "eac3", AudioCopy: true}
}

func waitUntil(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSegmentIsItsFragmentsConcatenated(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, nil)
	s, err := m.Create(context.Background(), fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	seg, err := s.OpenSegment(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer seg.Close()
	// Three GOPs: the first keeps its styp, the others lose theirs.
	want := int64(3*len(fakeGOP) - 2*len(fakeStyp))
	if seg.Size() != want {
		t.Errorf("segment size = %d, want %d", seg.Size(), want)
	}
	b, err := io.ReadAll(seg)
	if err != nil || int64(len(b)) != want {
		t.Fatalf("read %d bytes, %v", len(b), err)
	}
	if string(b[4:8]) != "styp" {
		t.Errorf("segment starts with %q, want styp", b[4:8])
	}
	if n := strings.Count(string(b), "styp"); n != 1 {
		t.Errorf("styp appears %d times, want 1", n)
	}
	if n := strings.Count(string(b), "moof"); n != 3 {
		t.Errorf("moof appears %d times, want 3", n)
	}
	if _, err := s.OpenSegment(context.Background(), 200); !errors.Is(err, ErrNoSegment) {
		t.Errorf("segment 200 = %v, want ErrNoSegment", err)
	}
	if _, err := s.OpenSegment(context.Background(), -1); !errors.Is(err, ErrNoSegment) {
		t.Errorf("segment -1 = %v, want ErrNoSegment", err)
	}
}

func TestSeekRestartsFFmpeg(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, nil)
	ctx := context.Background()
	s, err := m.Create(ctx, fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	open := func(idx int) {
		t.Helper()
		seg, err := s.OpenSegment(ctx, idx)
		if err != nil {
			t.Fatalf("segment %d: %v", idx, err)
		}
		seg.Close()
	}

	open(0)
	open(1)
	open(2)
	if n := len(f.starts()); n != 1 {
		t.Fatalf("sequential playback started ffmpeg %d times: %v", n, f.starts())
	}
	if first := f.starts()[0]; strings.Contains(first, "-ss") || !strings.Contains(first, "-start_number 0 ") {
		t.Errorf("first start = %q", first)
	}

	// Far away: segment 100 begins at GOP 300 = 600 s.
	open(100)
	starts := f.starts()
	if len(starts) != 2 {
		t.Fatalf("seek did not restart ffmpeg: %v", starts)
	}
	if !strings.Contains(starts[1], "-ss 600.140 ") || !strings.Contains(starts[1], "-start_number 300 ") {
		t.Errorf("seek start = %q", starts[1])
	}
	open(101) // just ahead of the new run: waits for it, no restart
	if n := len(f.starts()); n != 2 {
		t.Errorf("next segment restarted ffmpeg: %v", f.starts())
	}

	// Back where we were: the old files were removed by the seek, so this is
	// another restart.
	open(1)
	if starts := f.starts(); len(starts) != 3 || !strings.Contains(starts[2], "-start_number 3 ") {
		t.Errorf("seek back = %v", starts)
	}
	if n := len(f.procs(t)); n != 1 {
		t.Errorf("%d ffmpeg processes running, want exactly 1", n)
	}
}

func TestFragmentsServedOnlyWhenComplete(t *testing.T) {
	// An ffmpeg that never writes anything; the fragments are put in place
	// by hand, the way a half-finished write would look.
	sleeper := filepath.Join(t.TempDir(), "ffmpeg")
	if err := writeExecutable(sleeper, "#!/bin/sh\nexec sleep 60\n"); err != nil {
		t.Fatal(err)
	}
	m := fakeManager(t, fake{ffmpeg: sleeper}, func(o *Options) { o.WaitTimeout = 400 * time.Millisecond })
	s, err := m.Create(context.Background(), fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	put := func(g int, b []byte) {
		t.Helper()
		if err := os.WriteFile(s.gopPath(g), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put(0, fakeGOP)
	put(1, fakeGOP[:len(fakeGOP)-10]) // cut off inside the mdat
	put(2, fakeGOP)

	if _, err := s.OpenSegment(context.Background(), 0); !errors.Is(err, ErrTimeout) {
		t.Fatalf("segment with a truncated fragment = %v, want ErrTimeout", err)
	}
	put(1, fakeGOP) // the write finishes
	seg, err := s.OpenSegment(context.Background(), 0)
	if err != nil {
		t.Fatalf("segment with all fragments complete: %v", err)
	}
	seg.Close()
}

func TestSessionLimit(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, func(o *Options) { o.MaxSessions = 2 })
	var ids []string
	for i := 0; i < 2; i++ {
		s, err := m.Create(context.Background(), fakeRequest())
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.ID)
	}
	if _, err := m.Create(context.Background(), fakeRequest()); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("third session = %v, want ErrTooManySessions", err)
	}
	if !m.Delete(ids[0]) {
		t.Fatal("Delete failed")
	}
	if _, err := m.Create(context.Background(), fakeRequest()); err != nil {
		t.Errorf("session after a free slot: %v", err)
	}
	if ids[0] == ids[1] || len(ids[0]) < 20 {
		t.Errorf("session ids not unique/long enough: %v", ids)
	}
}

func TestConcurrentCreateRespectsLimit(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, func(o *Options) { o.MaxSessions = 3 })
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, full := 0, 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Create(context.Background(), fakeRequest())
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrTooManySessions):
				full++
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if ok != 3 || full != 7 {
		t.Errorf("created %d, refused %d; want 3 and 7", ok, full)
	}
}

func TestIdleTimeoutAndTouch(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, func(o *Options) { o.IdleTimeout = 400 * time.Millisecond })
	s, err := m.Create(context.Background(), fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	dir := s.Dir()
	waitUntil(t, "ffmpeg running", 5*time.Second, func() bool { return len(f.procs(t)) == 1 }) // positive control

	// Requests keep it alive well past the timeout.
	end := time.Now().Add(900 * time.Millisecond)
	for time.Now().Before(end) {
		s.Playlist("")
		time.Sleep(50 * time.Millisecond)
	}
	if m.Count() != 1 {
		t.Fatal("session ended while in use")
	}

	waitUntil(t, "idle kill", 5*time.Second, func() bool { return m.Count() == 0 })
	if p := f.procs(t); len(p) != 0 {
		t.Errorf("ffmpeg survived the idle kill: %v", p)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("session dir survived: %v", err)
	}
}

func TestThrottlePausesAndRequestResumes(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, func(o *Options) { o.ThrottleHigh, o.ThrottleLow = 3, 2 })
	s, err := m.Create(context.Background(), fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	state := func() string {
		s.mu.Lock()
		pid := s.run.cmd.Process.Pid
		s.mu.Unlock()
		out, _ := exec.Command("ps", "-o", "state=", "-p", fmt.Sprint(pid)).Output()
		return strings.TrimSpace(string(out))
	}
	// Nobody asks for segments: ffmpeg runs ahead and is stopped.
	waitUntil(t, "SIGSTOP", 5*time.Second, func() bool { return strings.HasPrefix(state(), "T") })
	s.mu.Lock()
	r := s.run
	paused, next := r.paused, r.next
	s.mu.Unlock()
	if !paused {
		t.Error("run.paused not set")
	}
	time.Sleep(200 * time.Millisecond)
	s.mu.Lock()
	s.advanceLocked()
	if s.run.next != next {
		t.Errorf("a paused ffmpeg kept producing: %d -> %d", next, s.run.next)
	}
	s.mu.Unlock()

	// A request beyond what exists resumes it (a stopped process cannot make
	// the fragment) and is served without a restart.
	want := s.plan.SegmentOfGOP(next) + 1
	seg, err := s.OpenSegment(context.Background(), want)
	if err != nil {
		t.Fatal(err)
	}
	seg.Close()
	if n := len(f.starts()); n != 1 {
		t.Errorf("ffmpeg restarted %d times, want a resume", n-1)
	}
}

func TestCloseKillsEverything(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, nil)
	var dirs []string
	for i := 0; i < 3; i++ {
		s, err := m.Create(context.Background(), fakeRequest())
		if err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, s.Dir())
	}
	waitUntil(t, "three ffmpeg running", 5*time.Second, func() bool { return len(f.procs(t)) == 3 }) // positive control
	m.Close()
	if p := f.procs(t); len(p) != 0 {
		t.Errorf("ffmpeg survived Close: %v", p)
	}
	for _, d := range append(dirs, m.Dir()) {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("%s survived Close: %v", d, err)
		}
	}
	if _, err := m.Create(context.Background(), fakeRequest()); !errors.Is(err, ErrClosed) {
		t.Errorf("Create after Close = %v", err)
	}
	m.Close() // idempotent
}

func TestInitSurvivesRestart(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, nil)
	ctx := context.Background()
	s, err := m.Create(ctx, fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	read := func() string {
		r, err := s.OpenInit(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		b, _ := io.ReadAll(r)
		return string(b)
	}
	first := read()
	if first != string(fakeInit) {
		t.Fatalf("init = %q", first)
	}
	seg, err := s.OpenSegment(ctx, 150) // restarts ffmpeg, which writes another init
	if err != nil {
		t.Fatal(err)
	}
	seg.Close()
	if len(f.starts()) != 2 {
		t.Fatalf("no restart: %v", f.starts())
	}
	if read() != first {
		t.Error("init changed after a restart")
	}
	s.mu.Lock()
	canon := s.initPath
	s.mu.Unlock()
	if filepath.Base(canon) != "init1.mp4" {
		t.Errorf("served init is %s, want the first run's init1.mp4", canon)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), "init1.mp4")); err != nil {
		t.Error(err)
	}
}

func TestPruneBehindThePlayer(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, func(o *Options) { o.ThrottleHigh, o.ThrottleLow = 100, 50 })
	ctx := context.Background()
	s, err := m.Create(ctx, fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= 9; i++ {
		seg, err := s.OpenSegment(ctx, i)
		if err != nil {
			t.Fatal(err)
		}
		seg.Close()
	}
	// Segment 9 requested: segments before 9-3 = 6 (GOPs < 18) go.
	waitUntil(t, "old fragments pruned", 5*time.Second, func() bool {
		_, err0 := os.Stat(s.gopPath(0))
		_, err17 := os.Stat(s.gopPath(17))
		return os.IsNotExist(err0) && os.IsNotExist(err17)
	})
	if _, err := os.Stat(s.gopPath(18)); err != nil {
		t.Errorf("fragment of segment 6 was pruned: %v", err)
	}
	if n := len(f.starts()); n != 1 {
		t.Errorf("pruning caused %d restarts", n-1)
	}
}

func TestStaleDirsRemovedAtStartup(t *testing.T) {
	root := t.TempDir()
	mk := func(name string) string {
		d := filepath.Join(root, name, "session")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(d, "gop00000.m4s"), []byte("x"), 0o644)
		return filepath.Join(root, name)
	}
	dead := mk("2147483000")                   // no such process
	own := mk(fmt.Sprint(os.Getpid()))         // a crashed predecessor with our pid
	junk := mk("not-a-pid")                    // not ours at all
	alive := mk(fmt.Sprint(os.Getppid()))      // another live process: leave it alone
	stray := filepath.Join(root, "stray.file") // loose files are cleared too
	os.WriteFile(stray, []byte("x"), 0o644)

	m, err := NewManager(Options{TempRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for _, p := range []string{dead, junk, stray} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived startup: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(own, "session")); !os.IsNotExist(err) {
		t.Errorf("leftovers under our own pid survived: %v", err)
	}
	if _, err := os.Stat(alive); err != nil {
		t.Errorf("a live process's dir was removed: %v", err)
	}
	if fi, err := os.Stat(m.Dir()); err != nil || !fi.IsDir() {
		t.Errorf("manager dir missing: %v", err)
	}
}

func TestExtractKeyframesParsing(t *testing.T) {
	in := "N/A,K__,\n0.000000,K__,\n0.083000,___,\n2.002000,K_,\n1.500000,_D_,\n4.004000,K__,\n12.500000\n"
	kf, start, err := parseKeyframes(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if start != 12.5 {
		t.Errorf("start = %v", start)
	}
	want := []float64{0, 2.002, 4.004}
	if len(kf) != len(want) {
		t.Fatalf("keyframes = %v, want %v", kf, want)
	}
	for i := range want {
		if kf[i] != want[i] {
			t.Errorf("kf[%d] = %v, want %v", i, kf[i], want[i])
		}
	}
}
