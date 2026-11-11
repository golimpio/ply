package watch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// The watch helper is `ply --watch`: a process of its own that holds the
// watches (the descriptors and their kqueue) for the ply that started it.
//
// Why: when a process ends, the kernel lets go of each watched directory, one at
// a time, before the process is gone, and on a network share each one waits for
// the server (an SMB NAS, 2026-11-11: 200 folders, 21 s; handing the descriptors
// to another process still left 5.5 s of kqueue teardown). Nothing in user space
// can hurry that, and the shell waits for it: a ply that quits at once (a second
// Ctrl-C, a crash, kill -9) left the terminal blocked for minutes. In a session of
// its own, the helper is not part of the terminal's job, nor sent its Ctrl-C:
// ply exits at once, and the helper lets go of the watches in the background.
//
// They talk in JSON lines: ply writes helperRequests on the helper's stdin, the
// helper writes helperMessages on its stdout, the first one a hello. The helper
// stops when its stdin closes, which ply's exit does too, however it ends.

// HelperArg is the argument that makes ply the watch helper (main runs HelperMain).
const HelperArg = "--watch"

type helperRequest struct {
	ID  uint64 `json:"id"`
	Op  string `json:"op"` // "add" or "remove"
	Dir string `json:"dir"`
}

// helperMessage is one of: the hello, the reply to request ID, an event, or an
// error of the backend.
type helperMessage struct {
	Hello    bool         `json:"hello,omitempty"`
	Nameless bool         `json:"nameless,omitempty"` // with the hello: events carry no names
	ID       uint64       `json:"id,omitempty"`
	Err      string       `json:"err,omitempty"`
	Errno    int          `json:"errno,omitempty"` // the syscall.Errno under Err, so that errors.Is works across
	Event    *helperEvent `json:"event,omitempty"`
	Broken   string       `json:"broken,omitempty"` // from the backend's errors channel
	Overflow bool         `json:"overflow,omitempty"`
}

type helperEvent struct {
	Dir  string `json:"dir"`
	Name string `json:"name,omitempty"`
	Op   op     `json:"op"`
}

// remoteError is an error of the helper, with its errno when it had one.
type remoteError struct {
	msg   string
	errno syscall.Errno
}

func (e *remoteError) Error() string { return e.msg }

func (e *remoteError) Unwrap() error {
	if e.errno == 0 {
		return nil
	}
	return e.errno
}

// HelperMain runs the watch helper on in and out (stdin and stdout) until in
// closes, and returns the exit code.
func HelperMain(in io.Reader, out io.WriteCloser) int { return helperMain(in, out, localBackend) }

func helperMain(in io.Reader, out io.WriteCloser, mk func() (backend, error)) int {
	// A write after ply is gone fails, instead of killing the helper with SIGPIPE
	// before it has let go of the watches.
	signal.Ignore(syscall.SIGPIPE)
	var (
		mu  sync.Mutex // guards enc and out
		enc = json.NewEncoder(out)
	)
	send := func(m helperMessage) {
		mu.Lock()
		_ = enc.Encode(m) // fails once out is closed: nobody listens any more
		mu.Unlock()
	}
	be, err := mk()
	if err != nil {
		send(helperMessage{Broken: err.Error()})
		return 1
	}
	nb, _ := be.(namelessBackend)
	send(helperMessage{Hello: true, Nameless: nb != nil && nb.nameless()})

	var fwd sync.WaitGroup
	fwd.Go(func() {
		for ev := range be.events() {
			send(helperMessage{Event: &helperEvent{Dir: ev.dir, Name: ev.name, Op: ev.op}})
		}
	})
	fwd.Go(func() {
		for err := range be.errors() {
			send(helperMessage{Broken: err.Error(), Overflow: errors.Is(err, errOverflow)})
		}
	})
	// Requests run side by side: on a network share each add waits for the server.
	dec := json.NewDecoder(in)
	for {
		var r helperRequest
		if err := dec.Decode(&r); err != nil {
			break
		}
		go func() {
			var err error
			switch r.Op {
			case "add":
				err = be.add(r.Dir)
			case "remove":
				err = be.remove(r.Dir)
			default:
				err = fmt.Errorf("watch helper: unknown request %q", r.Op)
			}
			m := helperMessage{ID: r.ID}
			if err != nil {
				m.Err = err.Error()
				if errno, ok := errors.AsType[syscall.Errno](err); ok {
					m.Errno = int(errno)
				}
			}
			send(m)
		}()
	}
	// ply is done with the watches. Its end of the conversation closes first, so
	// that it never waits for them to be let go of, which is the slow part.
	mu.Lock()
	out.Close()
	mu.Unlock()
	be.close()
	fwd.Wait()
	return 0
}

// helperBackend is the backend of a watch helper process.
type helperBackend struct {
	cmd      *exec.Cmd
	unnamed  bool // its events carry no names
	ev       chan event
	errc     chan error
	done     chan struct{} // closed by close
	readDone chan struct{}
	stop     sync.Once

	wmu   sync.Mutex // guards stdin and enc
	stdin io.WriteCloser
	enc   *json.Encoder

	mu      sync.Mutex
	closed  bool
	next    uint64
	pending map[uint64]chan error
}

// startHelper starts this program as the watch helper.
func startHelper() (backend, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("watch helper: %w", err)
	}
	return startHelperCmd(exec.Command(exe, HelperArg))
}

// helperQuit is how long close waits for the helper to stop talking before it
// kills it: the helper stops at once unless it is stuck.
const helperQuit = 5 * time.Second

func startHelperCmd(cmd *exec.Cmd) (backend, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // not in the terminal's job, nor sent its Ctrl-C
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("watch helper: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("watch helper: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("watch helper: %w", err)
	}
	dec := json.NewDecoder(stdout)
	var hello helperMessage
	if err := dec.Decode(&hello); err != nil || !hello.Hello {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if hello.Broken != "" {
			return nil, fmt.Errorf("watch helper: %s", hello.Broken)
		}
		return nil, fmt.Errorf("watch helper did not start (%v)", err)
	}
	b := &helperBackend{
		cmd:      cmd,
		unnamed:  hello.Nameless,
		ev:       make(chan event, 256),
		errc:     make(chan error, 4),
		done:     make(chan struct{}),
		readDone: make(chan struct{}),
		stdin:    stdin,
		enc:      json.NewEncoder(stdin),
		pending:  map[uint64]chan error{},
	}
	go b.read(dec)
	// The helper may take minutes to let go of the watches after close: it is
	// reaped whenever it ends, not waited for.
	go func() {
		<-b.readDone // Wait closes stdout: not before the reads are done
		_ = cmd.Wait()
	}()
	return b, nil
}

func (b *helperBackend) read(dec *json.Decoder) {
	defer close(b.readDone)
	defer close(b.ev)
	defer close(b.errc)
	defer b.fail()
	for {
		var m helperMessage
		if err := dec.Decode(&m); err != nil {
			return // closed, or the helper died: the closed channels tell the watcher
		}
		switch {
		case m.ID != 0:
			var err error
			if m.Err != "" {
				err = &remoteError{msg: m.Err, errno: syscall.Errno(m.Errno)}
			}
			b.mu.Lock()
			ch := b.pending[m.ID]
			delete(b.pending, m.ID)
			b.mu.Unlock()
			if ch != nil {
				ch <- err
			}
		case m.Event != nil:
			select {
			case b.ev <- event{dir: m.Event.Dir, name: m.Event.Name, op: m.Event.Op}:
			case <-b.done:
			}
		case m.Broken != "":
			err := error(&remoteError{msg: m.Broken})
			if m.Overflow {
				err = errOverflow
			}
			select {
			case b.errc <- err:
			case <-b.done:
			}
		}
	}
}

// fail answers the requests still waiting: there will be no reply.
func (b *helperBackend) fail() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for id, ch := range b.pending {
		ch <- errBackendClosed
		delete(b.pending, id)
	}
}

func (b *helperBackend) call(op, dir string) error {
	ch := make(chan error, 1)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errBackendClosed
	}
	b.next++
	id := b.next
	b.pending[id] = ch
	b.mu.Unlock()

	b.wmu.Lock()
	err := b.enc.Encode(helperRequest{ID: id, Op: op, Dir: dir})
	b.wmu.Unlock()
	if err != nil {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return errBackendClosed
	}
	return <-ch
}

func (b *helperBackend) add(dir string) error    { return b.call("add", dir) }
func (b *helperBackend) remove(dir string) error { return b.call("remove", dir) }
func (b *helperBackend) nameless() bool          { return b.unnamed }
func (b *helperBackend) events() <-chan event    { return b.ev }
func (b *helperBackend) errors() <-chan error    { return b.errc }

// close tells the helper to stop and returns once it has stopped talking, which
// is at once: it lets go of the watches after that, on its own.
func (b *helperBackend) close() error {
	b.stop.Do(func() {
		b.mu.Lock()
		b.closed = true
		close(b.done)
		b.mu.Unlock()

		b.wmu.Lock()
		_ = b.stdin.Close()
		b.wmu.Unlock()
		select {
		case <-b.readDone:
		case <-time.After(helperQuit):
			// Stuck. Killed, it may still take minutes to be gone (that is the
			// point of the helper): not waited for.
			_ = b.cmd.Process.Kill()
		}
	})
	return nil
}
