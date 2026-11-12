package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// remoteEnv is a server with a feed, a TV and a phone registered, served over HTTP for the feeds.
func remoteEnv(t *testing.T, token string) (*env, *httptest.Server) {
	t.Helper()
	e, _ := newActivityEnvToken(t, nil, token)
	ts := httptest.NewServer(e.srv)
	t.Cleanup(ts.Close)
	owner := map[string]string{}
	if token != "" {
		owner["Authorization"] = "Bearer " + token
	}
	for _, id := range []string{"tv-0000001", "phone-0001"} {
		wantStatus(t, e.do("PUT", "/api/devices/"+id, owner, `{"name":"`+id+`"}`), 204)
	}
	return e, ts
}

// commandFeed opens device's feed of commands and reads its snapshot.
func commandFeed(t *testing.T, ts *httptest.Server, device, query string, lastID int64) (*bufio.Reader, feedSnapshot, func()) {
	t.Helper()
	r, closeFeed := openFeed(t, ts.URL+"/api/events?topics=commands&device="+device+query, lastID)
	var snap feedSnapshot
	if lastID == 0 {
		if _, typ, data := readEvent(t, r); typ != "snapshot" || json.Unmarshal([]byte(data), &snap) != nil {
			t.Fatalf("first event %s %s, want a snapshot", typ, data)
		}
	}
	return r, snap, closeFeed
}

func sendCommand(t *testing.T, e *env, to string, hdr map[string]string, body string) int64 {
	t.Helper()
	rec := e.do("POST", "/api/devices/"+to+"/commands", hdr, body)
	wantStatus(t, rec, http.StatusAccepted)
	return decode[struct{ Seq int64 }](t, rec).Seq
}

// A command goes down its device's feed alone; the device's answer comes back to the sender's feed.
func TestACommandReachesItsDeviceAndTheAnswerItsSender(t *testing.T) {
	e, ts := remoteEnv(t, "")
	tv, _, closeTV := commandFeed(t, ts, "tv-0000001", "", 0)
	defer closeTV()
	phone, _, closePhone := commandFeed(t, ts, "phone-0001", "", 0)
	defer closePhone()

	seq := sendCommand(t, e, "tv-0000001", map[string]string{DeviceHeader: "phone-0001"}, `{"type":"seek","positionSec":42}`)
	id, typ, data := readEvent(t, tv)
	var got commandEvent
	if err := json.Unmarshal([]byte(data), &got); err != nil || typ != "command.seek" || id != seq {
		t.Fatalf("the TV got %d %s %s (%v), want command.seek %d", id, typ, data, err, seq)
	}
	if c := got.Command; c.Seq != seq || c.Type != "seek" || c.PositionSec == nil || *c.PositionSec != 42 || c.From != "phone-0001" ||
		c.ExpiresAt.Before(time.Now()) || c.ExpiresAt.After(time.Now().Add(commandTTL)) {
		t.Errorf("command = %+v", c)
	}

	ack := func(device string, s int64) int {
		return e.do("POST", fmt.Sprintf("/api/devices/%s/commands/%d/ack", device, s), map[string]string{DeviceHeader: device}, `{"ok":true}`).Code
	}
	if c := ack("phone-0001", seq); c != 404 {
		t.Errorf("the phone acked the TV's command: %d, want 404", c)
	}
	if c := ack("tv-0000001", seq); c != 204 {
		t.Fatalf("ack = %d", c)
	}
	if c := ack("tv-0000001", seq); c != 404 {
		t.Errorf("acked twice: %d, want 404", c)
	}

	// The phone's first event is the answer: it never saw the TV's command.
	_, typ, data = readEvent(t, phone)
	var answer struct{ Ack CommandAck }
	if err := json.Unmarshal([]byte(data), &answer); err != nil || typ != "command.acked" ||
		answer.Ack != (CommandAck{Seq: seq, Device: "tv-0000001", OK: true}) {
		t.Fatalf("the phone got %s %s, want the TV's answer", typ, data)
	}
	// The answer was the phone's alone: the TV's next event is one for everyone, sent after it.
	e.srv.Jobs.Events().Publish("commands", "marker", nil)
	if _, typ, _ := readEvent(t, tv); typ != "marker" {
		t.Errorf("the TV's next event is %s, want the marker: the answer was not its own", typ)
	}
	readEvent(t, phone) // the phone's marker

	// A command sent by no device (a script) is answered to every feed of the topic.
	script := sendCommand(t, e, "tv-0000001", nil, `{"type":"stop"}`)
	if id, typ, _ := readEvent(t, tv); id != script || typ != "command.stop" {
		t.Fatalf("the TV got %d %s, want the stop %d", id, typ, script)
	}
	if c := ack("tv-0000001", script); c != 204 {
		t.Fatalf("ack = %d", c)
	}
	for name, r := range map[string]*bufio.Reader{"tv": tv, "phone": phone} {
		if _, typ, _ := readEvent(t, r); typ != "command.acked" {
			t.Errorf("%s got %s, want the answer to a script's command", name, typ)
		}
	}
}

// A command waits for its device: a feed opened later has it in its snapshot, one that comes back
// gets it replayed by seq, and once it is acked it is gone; one that expires goes by itself.
func TestACommandWaitsForItsDevice(t *testing.T) {
	e, ts := remoteEnv(t, "")
	before := e.srv.Jobs.Events().Last()
	play := sendCommand(t, e, "tv-0000001", nil, fmt.Sprintf(`{"type":"play","itemId":%d,"positionSec":5}`, e.mp4))

	_, snap, closeFeed := commandFeed(t, ts, "tv-0000001", "", 0)
	closeFeed()
	if len(snap.Commands) != 1 || snap.Commands[0].Seq != play || snap.Commands[0].ItemID != e.mp4 {
		t.Fatalf("snapshot commands = %+v, want the play", snap.Commands)
	}
	_, other, closeOther := commandFeed(t, ts, "phone-0001", "", 0)
	closeOther()
	if len(other.Commands) != 0 {
		t.Errorf("another device's snapshot has %+v", other.Commands)
	}

	r, _, closeReplay := commandFeed(t, ts, "tv-0000001", "", before)
	if id, typ, _ := readEvent(t, r); id != play || typ != "command.play" {
		t.Errorf("replay = %d %s, want the play %d", id, typ, play)
	}
	closeReplay()

	wantStatus(t, e.do("POST", fmt.Sprintf("/api/devices/tv-0000001/commands/%d/ack", play), nil, `{"ok":false,"error":"nothing to play it with"}`), 204)
	if _, snap, closeFeed := commandFeed(t, ts, "tv-0000001", "", 0); len(snap.Commands) != 0 {
		closeFeed()
		t.Errorf("after the ack: %+v", snap.Commands)
	} else {
		closeFeed()
	}

	short := sendCommand(t, e, "tv-0000001", nil, `{"type":"pause","ttlSec":1}`)
	time.Sleep(1100 * time.Millisecond)
	if c := e.do("POST", fmt.Sprintf("/api/devices/tv-0000001/commands/%d/ack", short), nil, `{"ok":true}`).Code; c != 404 {
		t.Errorf("an expired command was acked: %d", c)
	}
}

func TestCommandsThatAreNotOne(t *testing.T) {
	e, _ := remoteEnv(t, "")
	for body, want := range map[string]int{
		`{"type":"dance"}`:                    400,
		`{"type":"play"}`:                     400,
		`{"type":"play","itemId":999999}`:     404,
		`{"type":"seek"}`:                     400,
		`{"type":"seek","positionSec":-1}`:    400,
		`{"type":"pause","ttlSec":-5}`:        400,
		`not json`:                            400,
		`{"type":"stop"}`:                     202, // control
		`{"type":"resume","ttlSec":86400}`:    202, // the longest is commandMaxTTL
		`{"type":"stop","ttlSec":9300000000}`: 202, // past what a Duration holds: still the longest
	} {
		if rec := e.do("POST", "/api/devices/tv-0000001/commands", nil, body); rec.Code != want {
			t.Errorf("%s = %d %s, want %d", body, rec.Code, rec.Body, want)
		} else if want == 202 {
			if exp := decode[Command](t, rec).ExpiresAt; !exp.After(time.Now()) || exp.After(time.Now().Add(commandMaxTTL+time.Second)) {
				t.Errorf("%s expires at %v: want ahead, and no later than the longest a command lives", body, exp)
			}
		}
	}
	if rec := e.do("POST", "/api/devices/never-seen-01/commands", nil, `{"type":"stop"}`); rec.Code != 404 {
		t.Errorf("a command to a device that never registered = %d, want 404", rec.Code)
	}
	if rec := e.do("POST", "/api/devices/tv-0000001/commands/x/ack", nil, `{}`); rec.Code != 400 {
		t.Errorf("an ack of seq x = %d, want 400", rec.Code)
	}
}

// With pairing, a device sends commands as itself, and acks only its own.
func TestCommandsWithPairedDevices(t *testing.T) {
	e, _ := remoteEnv(t, "s3cret")
	owner := map[string]string{"Authorization": "Bearer s3cret"}
	phone := map[string]string{"Authorization": "Bearer " + pairDevice(t, e, owner, "phone-0001"), DeviceHeader: "tv-0000001"}
	seq := sendCommand(t, e, "tv-0000001", phone, `{"type":"stop"}`)
	if p := e.srv.Jobs.Events().Pending("tv-0000001"); len(p) != 1 || p[0].Data.(commandEvent).Command.From != "phone-0001" {
		t.Errorf("pending = %+v, want the command from the phone, whatever its header said", p)
	}
	ackPath := "/api/devices/tv-0000001/commands/" + strconv.FormatInt(seq, 10) + "/ack"
	if rec := e.do("POST", ackPath, phone, `{"ok":true}`); rec.Code != 403 {
		t.Errorf("the phone acked for the TV: %d, want 403", rec.Code)
	}
	wantStatus(t, e.do("POST", ackPath, owner, `{"ok":true}`), 204) // control: the owner may
}
