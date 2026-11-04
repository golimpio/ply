package stream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// recCache is a KeyframeCache that remembers every write.
type recCache struct {
	mu     sync.Mutex
	writes []recWrite
	kf     map[int64][]float64
}

type recWrite struct {
	item  int64
	times []float64
}

func (c *recCache) Keyframes(_ context.Context, id int64) ([]float64, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	kf, ok := c.kf[id]
	return kf, ok && len(kf) > 0, nil
}

func (c *recCache) SetKeyframes(_ context.Context, id int64, kf []float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kf == nil {
		c.kf = map[int64][]float64{}
	}
	c.kf[id] = kf
	c.writes = append(c.writes, recWrite{id, kf})
	return nil
}

// of is what was written for item, in order.
func (c *recCache) of(item int64) [][]float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out [][]float64
	for _, w := range c.writes {
		if w.item == item {
			out = append(out, w.times)
		}
	}
	return out
}

func (c *recCache) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.writes)
}

// ---- a fake ffmpeg that writes fragments with the times it is told ----------

// The fake waits for the file "go", then writes the fragments in tplN/ from
// -start_number on, N being the number of times it has been started, with the
// init segment in init.tpl. It does not write fragment I while the file holdI
// exists (for any start of it), or hold<N>_<I> (for the Nth).
const guardScript = `#!/bin/sh
echo "$@" >> "%[1]s"
set=$(wc -l < "%[1]s" | tr -d ' ')
n=0; init=init.mp4
while [ $# -gt 0 ]; do
  case "$1" in
    -start_number) n=$2;;
    -hls_fmp4_init_filename) init=$2;;
  esac
  shift
done
while [ ! -e "%[2]s/go" ]; do sleep 0.01; done
cp "%[2]s/init.tpl" "$init"
i=$n
while [ -e "%[2]s/tpl$set/gop$i.tpl" ]; do
  while [ -e "%[2]s/hold$i" ] || [ -e "%[2]s/hold${set}_$i" ]; do sleep 0.01; done
  f=$(printf 'gop%%05d.m4s' $i)
  cp "%[2]s/tpl$set/gop$i.tpl" "$f.tmp"
  mv "$f.tmp" "$f"
  i=$((i+1))
  sleep 0.01
done
sleep 60
`

// fragInit is an init segment with an audio track (1) and a video track (2).
func fragInit() []byte {
	trak := func(id uint32, handler string, scale uint32) []byte {
		return isoBox("trak", isoFull("tkhd", 0, u32(0), u32(0), u32(id), make([]byte, 68)),
			isoBox("mdia", isoFull("mdhd", 0, u32(0), u32(0), u32(scale), u32(0), u16(0), u16(0)),
				isoFull("hdlr", 0, u32(0), []byte(handler), make([]byte, 12), []byte{0}), isoBox("minf")))
	}
	return cat(ftyp, isoBox("moov", isoFull("mvhd", 0, u32(0), u32(0), u32(1000), u32(0), make([]byte, 80)),
		trak(1, "soun", 48000), trak(2, "vide", 1000)))
}

// fragment is a GOP file whose video starts at ms (the audio track is there
// first, as it is in the fragments ffmpeg writes: a wrong track would be wrong).
func fragment(ms int64, trun []byte) []byte {
	if trun == nil {
		trun = isoFull("trun", 0, u32(1))
	}
	return cat(isoBox("styp", []byte("msdh"), u32(0), []byte("msdhmsix")), isoBox("moof", isoFull("mfhd", 0, u32(1)),
		isoBox("traf", isoFull("tfhd", 0, u32(1)), isoFull("tfdt", 1, u64(uint64(ms*48))), isoFull("trun", 0, u32(1))),
		isoBox("traf", isoFull("tfhd", 0, u32(2)), isoFull("tfdt", 1, u64(uint64(ms))), trun)),
		isoBox("mdat", make([]byte, 100)))
}

// trunOf is a trun that lists the durations (in the units of the video track:
// milliseconds) of its samples.
func trunOf(durs ...int64) []byte {
	parts := [][]byte{{0, 0, 0x01, 0x00}, u32(uint32(len(durs)))}
	for _, d := range durs {
		parts = append(parts, u32(uint32(d)))
	}
	return isoBox("trun", parts...)
}

// playlistOf is the playlist of s, for a test that expects one.
func playlistOf(t testing.TB, s *Session) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pl, err := s.Playlist(ctx, "")
	if err != nil {
		t.Errorf("playlist: %v", err)
	}
	return pl
}

type guardRig struct {
	dir, ffmpeg, log string
}

func newGuardRig(t *testing.T) guardRig {
	t.Helper()
	dir := t.TempDir()
	g := guardRig{dir: dir, ffmpeg: filepath.Join(dir, "ffmpeg"), log: filepath.Join(dir, "args.log")}
	if err := os.WriteFile(filepath.Join(dir, "init.tpl"), fragInit(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeExecutable(g.ffmpeg, fmt.Sprintf(guardScript, g.log, dir)); err != nil {
		t.Fatal(err)
	}
	return g
}

// fragments sets what the nth start of the fake writes: GOP i starts at times[i]
// seconds of the plan, and so at TimelineOffset more in the fragment.
func (g guardRig) fragments(t *testing.T, nth int, times []float64) {
	t.Helper()
	d := g.tplDir(t, nth)
	for i, tm := range times {
		ms := int64((tm + TimelineOffset) * 1000)
		if err := os.WriteFile(filepath.Join(d, fmt.Sprintf("gop%d.tpl", i)), fragment(ms, nil), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// spans is fragments for fragments that say how long they last: GOP i, of the
// len(durs) there are, lasts durs[i] seconds (in three samples).
func (g guardRig) spans(t *testing.T, nth int, times, durs []float64) {
	t.Helper()
	d := g.tplDir(t, nth)
	for i, dur := range durs {
		ms, total := int64((times[i]+TimelineOffset)*1000), int64(dur*1000)
		quarter := total / 4
		file := fragment(ms, trunOf(quarter, quarter, total-2*quarter))
		if err := os.WriteFile(filepath.Join(d, fmt.Sprintf("gop%d.tpl", i)), file, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func (g guardRig) tplDir(t *testing.T, nth int) string {
	t.Helper()
	d := filepath.Join(g.dir, fmt.Sprintf("tpl%d", nth))
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

// hold keeps the fake from writing fragment i, and so the ones after it, until
// release.
func (g guardRig) hold(t *testing.T, i int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(g.dir, fmt.Sprintf("hold%d", i)), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// holdStart is hold for the nth start of the fake only.
func (g guardRig) holdStart(t *testing.T, nth, i int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(g.dir, fmt.Sprintf("hold%d_%d", nth, i)), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (g guardRig) releaseStart(t *testing.T, nth, i int) {
	t.Helper()
	if err := os.Remove(filepath.Join(g.dir, fmt.Sprintf("hold%d_%d", nth, i))); err != nil {
		t.Fatal(err)
	}
}

func (g guardRig) release(t *testing.T, i int) {
	t.Helper()
	if err := os.Remove(filepath.Join(g.dir, fmt.Sprintf("hold%d", i))); err != nil {
		t.Fatal(err)
	}
}

// open lets the fake write.
func (g guardRig) open(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(g.dir, "go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (g guardRig) starts() []string {
	b, _ := os.ReadFile(g.log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func (g guardRig) manager(t *testing.T, cache KeyframeCache, ffprobe string, mod func(*Options)) (*Manager, *syncBuffer) {
	t.Helper()
	var logs syncBuffer
	o := Options{
		FFmpeg: g.ffmpeg, FFprobe: ffprobe, TempRoot: t.TempDir(), Cache: cache,
		Tick: 20 * time.Millisecond, WaitTimeout: 10 * time.Second, ExtractTimeout: time.Minute,
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	}
	if mod != nil {
		mod(&o)
	}
	m, err := NewManager(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m, &logs
}

// indexedFile is a Matroska file whose cues list the 60 keyframes, 2 s apart,
// of a film of two minutes: audio is stream 0, video stream 1.
func indexedFile(t *testing.T) string {
	t.Helper()
	data := goodMKV(func(f *mkvFile) {
		for i := range f.clusters {
			for j := range f.clusters[i].blocks {
				f.clusters[i].blocks[j].size = 200
			}
		}
	}).build()
	p := filepath.Join(t.TempDir(), "film.mkv")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func indexedRequest(path string, item int64) Request {
	return Request{ItemID: item, Path: path, Container: "mkv", DurationSec: 120, VideoIndex: 1, VideoCodec: "h264",
		AudioIndex: 0, AudioCodec: "aac", AudioCopy: true}
}

// fakeFFprobe prints a scan of n keyframes, step seconds apart.
func scanScript(t *testing.T, n, step int) string {
	t.Helper()
	return fakeFFprobe(t, fmt.Sprintf(`i=0; while [ $i -lt %d ]; do printf '%%d.000000,K__,\n' $((i*%d)); i=$((i+1)); done; echo 0.000000`, n, step))
}

// slowScan is a scan of n keyframes, step seconds apart, that takes delay (a
// sleep argument) and leaves a line in the file calls each time it is run.
func slowScan(t *testing.T, delay string, n, step int) (script, calls string) {
	t.Helper()
	calls = filepath.Join(t.TempDir(), "scans.log")
	script = fakeFFprobe(t, fmt.Sprintf(`echo scan >> "%s"; sleep %s; i=0; while [ $i -lt %d ]; do printf '%%d.000000,K__,\n' $((i*%d)); i=$((i+1)); done; echo 0.000000`, calls, delay, n, step))
	return script, calls
}

func scansOf(calls string) int {
	b, _ := os.ReadFile(calls)
	return strings.Count(string(b), "scan")
}

func equalTimes(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A plan read from the container is cached only once the fragments ffmpeg
// writes have agreed with it, and the playlist waits for that.
func TestPlanGuardConfirmsThenCaches(t *testing.T) {
	rig := newGuardRig(t)
	rig.fragments(t, 1, every(60, 0, 2))
	cache := &recCache{}
	m, logs := rig.manager(t, cache, "/nonexistent/ffprobe", nil)

	s, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7))
	if err != nil {
		t.Fatal(err)
	}
	if s.guard == nil {
		t.Fatal("a plan from the cues has no guard")
	}
	if n := len(cache.of(7)); n != 0 {
		t.Fatalf("%d writes to the cache before a fragment was seen", n)
	}

	rig.open(t)
	done := make(chan string, 1)
	go func() { done <- playlistOf(t, s) }()
	var pl string
	select {
	case pl = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("no playlist")
	}
	if n := strings.Count(pl, "#EXTINF"); n != 20 {
		t.Fatalf("%d segments in the playlist:\n%s", n, pl)
	}
	waitUntil(t, "the index to be cached", 5*time.Second, func() bool { return len(cache.of(7)) == 1 })
	if w := cache.of(7)[0]; !equalTimes(w, every(60, 0, 2)) {
		t.Errorf("cached %v ...", head(w))
	}
	if !strings.Contains(logs.String(), "confirmed by the first fragments") {
		t.Errorf("no word of it in the log:\n%s", logs.String())
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(cache.of(7)); n != 1 {
		t.Errorf("%d writes to the cache", n)
	}
	if _, err := s.OpenSegment(context.Background(), 3); err != nil {
		t.Errorf("segment 3: %v", err)
	}
}

// A second fragment that is not where the plan says the second GOP is shows a
// keyframe the plan lacks (the first fragment starts on a cue, whatever else
// the cues leave out): the plan is rebuilt from a scan before anyone sees it.
func TestPlanGuardRefutesAndRebuilds(t *testing.T) {
	rig := newGuardRig(t)
	wrong := every(60, 0, 2)
	wrong[1] = 1.3 // a keyframe at 1.3 s: the plan has the next one at 2 s
	rig.fragments(t, 1, wrong)
	// What the scan will say, and ffmpeg writes: but for a fragment that is a second
	// off, in the segment after the first. The scan is the plan now; nothing is left
	// to compare it with.
	scanned := every(30, 0, 4)
	scanned[3] = 13
	rig.fragments(t, 2, scanned)
	cache := &recCache{}
	m, logs := rig.manager(t, cache, scanScript(t, 30, 4), nil)
	path := indexedFile(t)

	s, err := m.Create(context.Background(), indexedRequest(path, 7))
	if err != nil {
		t.Fatal(err)
	}
	if s.guard == nil || len(s.Plan().Keyframes) != 60 {
		t.Fatalf("the plan has %d keyframes, and a guard: %v", len(s.Plan().Keyframes), s.guard != nil)
	}
	rig.open(t)

	done := make(chan string, 1)
	go func() { done <- playlistOf(t, s) }()
	var pl string
	select {
	case pl = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("no playlist")
	}
	if n := strings.Count(pl, "#EXTINF"); n != 15 {
		t.Fatalf("%d segments in the playlist, want the 15 of the scan:\n%s", n, pl)
	}
	if kf := s.Plan().Keyframes; !equalTimes(kf, every(30, 0, 4)) {
		t.Errorf("the plan has %v ...", head(kf))
	}
	if !strings.Contains(logs.String(), "disagrees with the stream") {
		t.Errorf("no word of it in the log:\n%s", logs.String())
	}

	// The index never reached the cache: what was there was dropped, and the
	// scan took its place.
	writes := cache.of(7)
	if len(writes) != 2 || len(writes[0]) != 0 || !equalTimes(writes[1], every(30, 0, 4)) {
		t.Errorf("writes to the cache: %d, %v", len(writes), writes)
	}

	// ffmpeg was restarted at the start of the new plan, and serves it.
	waitUntil(t, "the second start", 5*time.Second, func() bool { return len(rig.starts()) >= 2 })
	starts := rig.starts()
	if len(starts) != 2 || !strings.Contains(starts[1], "-start_number 0") {
		t.Errorf("ffmpeg was started as:\n%s", strings.Join(starts, "\n"))
	}
	m2, err := s.OpenSegment(context.Background(), 1)
	if err != nil {
		t.Fatalf("a segment of the new plan: %v", err)
	}
	m2.Close()
	time.Sleep(300 * time.Millisecond) // the odd fragment is there by now, and looked at by the monitor
	if m2, err := s.OpenSegment(context.Background(), 1); err != nil {
		t.Errorf("a session on the plan of a scan fails over a fragment: %v", err)
	} else {
		m2.Close()
	}

	// The file is not read for its index again.
	s2, err := m.Create(context.Background(), indexedRequest(path, 8))
	if err != nil {
		t.Fatal(err)
	}
	if s2.guard != nil || len(s2.Plan().Keyframes) != 30 {
		t.Errorf("the next session: %d keyframes, a guard: %v", len(s2.Plan().Keyframes), s2.guard != nil)
	}
	if w := cache.of(8); len(w) != 1 || len(w[0]) != 30 {
		t.Errorf("the scan of the next session was cached as %v", w)
	}
}

// A fragment that shows the index wrong is a refutation however late it is
// looked at: past the deadline, with the plan given up on in the same breath,
// would be the old plan with a rebuild running under it.
func TestPlanGuardRefutesAfterTheDeadlineToo(t *testing.T) {
	rig := newGuardRig(t)
	wrong := every(60, 0, 2)
	wrong[1] = 1.3
	rig.fragments(t, 1, wrong)
	rig.fragments(t, 2, every(30, 0, 4))
	m, _ := rig.manager(t, &recCache{}, scanScript(t, 30, 4), func(o *Options) {
		o.WaitTimeout = 300 * time.Millisecond
		o.Tick = time.Hour // nobody looks before the playlist is asked for
	})
	s, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7))
	if err != nil {
		t.Fatal(err)
	}
	rig.open(t)
	time.Sleep(700 * time.Millisecond) // the deadline is long past
	if n := strings.Count(playlistOf(t, s), "#EXTINF"); n != 15 {
		t.Errorf("%d segments in the playlist, want the 15 of the scan", n)
	}
}

// Nothing is served from a plan that is not final: a segment (or the init
// segment) asked for before the playlist waits for the guard, and is looked up
// in the plan of the scan.
func TestSegmentsWaitForThePlan(t *testing.T) {
	rig := newGuardRig(t)
	wrong := every(60, 0, 2)
	wrong[1] = 1.3
	rig.fragments(t, 1, wrong)
	rig.fragments(t, 2, every(30, 0, 4))
	m, _ := rig.manager(t, &recCache{}, scanScript(t, 30, 4), nil)
	s, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7))
	if err != nil {
		t.Fatal(err)
	}
	rig.open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Segment 19 is the last of the plan from the index (20 segments), and is not
	// in that of the scan (15).
	if _, err := s.OpenSegment(ctx, 19); !errors.Is(err, ErrNoSegment) {
		t.Fatalf("segment 19: err = %v, want none such", err)
	}
	if n := len(s.Plan().Segments); n != 15 {
		t.Fatalf("%d segments", n)
	}
	if _, err := s.OpenInit(ctx); err != nil {
		t.Errorf("init: %v", err)
	}
}

// Where no fragment turns up in time, the plan stands, unconfirmed and uncached.
func TestPlanGuardGivesUp(t *testing.T) {
	rig := newGuardRig(t) // never opened: ffmpeg writes nothing
	cache := &recCache{}
	m, logs := rig.manager(t, cache, "/nonexistent/ffprobe", func(o *Options) { o.WaitTimeout = 300 * time.Millisecond })

	s, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	pl := playlistOf(t, s)
	if took := time.Since(start); took < 200*time.Millisecond || took > 5*time.Second {
		t.Errorf("the playlist took %v", took)
	}
	if n := strings.Count(pl, "#EXTINF"); n != 20 {
		t.Errorf("%d segments in the playlist", n)
	}
	if !strings.Contains(logs.String(), "not confirmed in time") {
		t.Errorf("no word of it in the log:\n%s", logs.String())
	}
	time.Sleep(100 * time.Millisecond)
	if n := cache.total(); n != 0 {
		t.Errorf("%d writes to the cache", n)
	}
}

// A client that asks for the playlist late finds the fragments that were
// written meanwhile, and the plan is judged by them, not given up on.
func TestPlanGuardJudgesTheFragmentsThatAreThereAfterTheDeadline(t *testing.T) {
	rig := newGuardRig(t)
	rig.fragments(t, 1, every(60, 0, 2))
	cache := &recCache{}
	m, logs := rig.manager(t, cache, "/nonexistent/ffprobe", func(o *Options) {
		o.WaitTimeout = 300 * time.Millisecond
		o.Tick = time.Hour // nobody looks before the playlist is asked for
	})
	s, err := m.Create(context.Background(), indexedRequest(indexedFile(t), 7))
	if err != nil {
		t.Fatal(err)
	}
	rig.open(t)
	time.Sleep(700 * time.Millisecond) // the deadline is long past
	if pl := playlistOf(t, s); strings.Count(pl, "#EXTINF") != 20 {
		t.Fatalf("playlist:\n%s", pl)
	}
	waitUntil(t, "the index to be cached", 5*time.Second, func() bool { return len(cache.of(7)) == 1 })
	if strings.Contains(logs.String(), "not confirmed in time") {
		t.Errorf("given up on:\n%s", logs.String())
	}
}

// A scan is cached when it succeeds, and not when it does not.
func TestScanResultIsCachedOnlyOnSuccess(t *testing.T) {
	rig := newGuardRig(t)
	junk := filepath.Join(t.TempDir(), "film.ts") // not a container with an index
	if err := os.WriteFile(junk, []byte(strings.Repeat("not a media file ", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	req := Request{ItemID: 3, Path: junk, Container: "ts", DurationSec: 120, VideoIndex: 0, VideoCodec: "h264", AudioIndex: 1, AudioCodec: "aac", AudioCopy: true}

	failing := fakeFFprobe(t, "echo scan failed >&2; exit 1")
	cache := &recCache{}
	m, _ := rig.manager(t, cache, failing, nil)
	if _, err := m.Create(context.Background(), req); err == nil {
		t.Fatal("a scan that failed gave a session")
	}
	if n := cache.total(); n != 0 {
		t.Errorf("%d writes to the cache after a failed scan", n)
	}

	m2, _ := rig.manager(t, cache, scanScript(t, 60, 2), nil)
	s, err := m2.Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if s.guard != nil {
		t.Error("a scan has a guard")
	}
	if w := cache.of(3); len(w) != 1 || !equalTimes(w[0], every(60, 0, 2)) {
		t.Errorf("writes to the cache after a scan: %v", w)
	}
}

// The video stream asked for is the one whose index is read: here audio is
// stream 0, and a request for stream 0 is not served by the index of stream 1.
func TestManagerReadsTheIndexOfTheVideoStreamAsked(t *testing.T) {
	rig := newGuardRig(t)
	path := indexedFile(t)
	m, _ := rig.manager(t, nil, "/nonexistent/ffprobe", nil)

	s, err := m.Create(context.Background(), indexedRequest(path, 0)) // VideoIndex 1
	if err != nil {
		t.Fatalf("video stream 1: %v", err)
	}
	if s.guard == nil || len(s.Plan().Keyframes) != 60 {
		t.Errorf("%d keyframes, from the container: %v", len(s.Plan().Keyframes), s.guard != nil)
	}

	req := indexedRequest(path, 0)
	req.VideoIndex = 0 // the audio
	if _, err := m.Create(context.Background(), req); err == nil {
		t.Error("the index of the audio stream served a request for video stream 0")
	}
}

// ---- reading fragments -------------------------------------------------------

func TestFragmentVideoStart(t *testing.T) {
	video := fragVideo{track: 2, scale: 12288}
	trun := func(version byte, flags uint32, fields ...[]byte) []byte {
		return isoBox("trun", append([][]byte{{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}, u32(1)}, fields...)...)
	}
	frag := func(videoTfdt uint64, tfdtVer byte, tr []byte) []byte {
		tfdt := isoFull("tfdt", tfdtVer, u64(videoTfdt))
		if tfdtVer == 0 {
			tfdt = isoFull("tfdt", 0, u32(uint32(videoTfdt)))
		}
		return cat(isoBox("moof", isoFull("mfhd", 0, u32(1)),
			isoBox("traf", isoFull("tfhd", 0, u32(1)), isoFull("tfdt", 1, u64(999999999)), isoFull("trun", 0, u32(1))), // audio
			isoBox("traf", isoFull("tfhd", 0, u32(2)), tfdt, tr)), isoBox("mdat", make([]byte, 50)))
	}
	for _, c := range []struct {
		name string
		file []byte
		want float64
	}{
		{"64-bit base decode time", frag(24576, 1, trun(0, 0)), 2},
		{"32-bit base decode time", frag(24576, 0, trun(0, 0)), 2},
		{"the composition offset of the first sample", frag(24576, 1, trun(0, 0x800, u32(1024))), 2 + 1024.0/12288},
		{"with the fields before it", frag(24576, 1, trun(0, 0x1|0x4|0x100|0x200|0x800, u32(0), u32(0), u32(512), u32(9999), u32(2048))), 2 + 2048.0/12288},
		{"with per-sample flags before it", frag(24576, 1, trun(0, 0x400|0x800, u32(0), u32(1024))), 2 + 1024.0/12288},
		{"a negative offset (version 1)", frag(24576, 1, trun(1, 0x800, i32(-1024))), 2 - 1024.0/12288},
		{"an offset that is not there", frag(24576, 1, trun(0, 0x100, u32(512))), 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "gop.m4s")
			if err := os.WriteFile(p, c.file, 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := fragmentVideoStart(p, video)
			if err != nil {
				t.Fatal(err)
			}
			if d := got - c.want; d > 1e-9 || d < -1e-9 {
				t.Errorf("starts at %v, want %v", got, c.want)
			}
		})
	}

	for name, file := range map[string][]byte{
		"no video fragment":       frag(0, 1, trun(0, 0))[:0],
		"another video track":     cat(isoBox("moof", isoFull("mfhd", 0, u32(1)), isoBox("traf", isoFull("tfhd", 0, u32(7)), isoFull("tfdt", 1, u64(5))))),
		"a fragment without tfdt": cat(isoBox("moof", isoFull("mfhd", 0, u32(1)), isoBox("traf", isoFull("tfhd", 0, u32(2))))),
		"cut off":                 frag(24576, 1, trun(0, 0))[:30],
	} {
		p := filepath.Join(t.TempDir(), "gop.m4s")
		if err := os.WriteFile(p, file, 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := fragmentVideoStart(p, video); err == nil {
			t.Errorf("%s: starts at %v", name, got)
		}
	}
}

func TestReadFragInit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "init.mp4")
	if err := os.WriteFile(p, fragInit(), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := readFragInit(p)
	if err != nil {
		t.Fatal(err)
	}
	if v.track != 2 || v.scale != 1000 {
		t.Errorf("video track %d at %d Hz, want 2 at 1000 (audio is first, at 48000)", v.track, v.scale)
	}
	for name, data := range map[string][]byte{"no moov": ftyp, "no video": cat(ftyp, isoBox("moov")), "nothing": nil} {
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := readFragInit(p); err == nil {
			t.Errorf("%s: a video track", name)
		}
	}
}
