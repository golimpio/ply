package api

// Remote control (docs/M11-CLIENTS.md, "Remote control"):
//
//	POST /api/devices/{id}/commands            ask a device to do something: {type, itemId?, positionSec?, ttlSec?}
//	                                           → 202 {seq, expiresAt}
//	POST /api/devices/{id}/commands/{seq}/ack  the device answers: {ok, error?} → 204
//
// A command goes down the device's own feed (topic commands, for it alone) and is kept until it is
// acked or expires, so a device that comes back gets it replayed by seq, or in the commands section
// of its snapshot. The answer comes back to the sender's feed as command.acked. Both endpoints are
// private, under /api/devices.

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"ply/internal/events"
	"ply/internal/store"
)

const (
	commandTTL    = 30 * time.Second // what a command lives for when the sender says nothing
	commandMaxTTL = 10 * time.Minute
)

// commandTypes are what a device may be asked to do: command.<type> on its feed.
var commandTypes = map[string]bool{"play": true, "pause": true, "resume": true, "seek": true, "stop": true}

// Command is what a device is asked to do: the payload of a command.* event, {"command": ...},
// and an entry of the commands section of its snapshot.
type Command struct {
	Seq         int64     `json:"seq"` // the event's id: what the ack names
	Type        string    `json:"type"`
	ItemID      int64     `json:"itemId,omitempty"`      // play
	PositionSec *float64  `json:"positionSec,omitempty"` // play (where to start), seek
	From        string    `json:"from,omitempty"`        // the device that sent it, when it named one
	ExpiresAt   time.Time `json:"expiresAt"`             // a device drops a command it gets after this
}

type commandEvent struct {
	Command Command `json:"command"`
}

// CommandAck is the payload of command.acked: {"ack": ...}.
type CommandAck struct {
	Seq    int64  `json:"seq"`
	Device string `json:"device"` // the device that answered
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

type commandRequest struct {
	Type        string   `json:"type"`
	ItemID      int64    `json:"itemId"`
	PositionSec *float64 `json:"positionSec"`
	TTLSec      int      `json:"ttlSec"`
}

// feedHub is the live feed's hub; nil when the feed is not running.
func (s *Server) feedHub() *events.Hub {
	if s.Jobs == nil {
		return nil
	}
	return s.Jobs.Events()
}

// liveFeed is feedHub for a handler: it answers 503 itself when the feed is not running.
func (s *Server) liveFeed(w http.ResponseWriter) *events.Hub {
	hub := s.feedHub()
	if hub == nil {
		writeError(w, http.StatusServiceUnavailable, "the live feed is not running")
	}
	return hub
}

// handleCommand is POST /api/devices/{id}/commands: 202 with the command's seq, 404 for a device
// that never registered or an item that is not there, 400 for a command that is not one.
func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	hub := s.liveFeed(w)
	if hub == nil {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.devices.get(r.Context(), s.Store, id); !ok {
		writeError(w, http.StatusNotFound, "no device "+id)
		return
	}
	var req commandRequest
	if !decodeBody(w, r, &req) {
		return
	}
	switch {
	case !commandTypes[req.Type]:
		writeError(w, http.StatusBadRequest, "type is play, pause, resume, seek or stop")
		return
	case req.Type == "play" && req.ItemID <= 0:
		writeError(w, http.StatusBadRequest, "play needs an itemId")
		return
	case req.Type == "seek" && req.PositionSec == nil:
		writeError(w, http.StatusBadRequest, "seek needs a positionSec")
		return
	case req.PositionSec != nil && *req.PositionSec < 0:
		writeError(w, http.StatusBadRequest, "positionSec is not negative")
		return
	case req.TTLSec < 0:
		writeError(w, http.StatusBadRequest, "ttlSec is not negative")
		return
	}
	if req.Type == "play" {
		if _, err := s.Store.GetEntry(r.Context(), req.ItemID); errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no item "+strconv.FormatInt(req.ItemID, 10))
			return
		} else if err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	ttl := commandTTL
	if req.TTLSec > 0 {
		ttl = time.Duration(min(req.TTLSec, int(commandMaxTTL/time.Second))) * time.Second
	}
	expires := hub.Now().Add(ttl).UTC() // the hub's clock: the one it lets the command go by
	c := Command{Type: req.Type, ItemID: req.ItemID, PositionSec: req.PositionSec, From: deviceID(r), ExpiresAt: expires}
	seq := hub.PublishTo(events.Commands, "command."+req.Type, id, expires, func(seq int64) any {
		c.Seq = seq
		return commandEvent{Command: c}
	})
	writeJSON(w, http.StatusAccepted, map[string]any{"seq": seq, "expiresAt": expires})
}

// handleCommandAck is POST /api/devices/{id}/commands/{seq}/ack: 204, 404 for a command that is
// not the device's to ack (acked already, expired, or another's), 403 for a device acking for
// another.
func (s *Server) handleCommandAck(w http.ResponseWriter, r *http.Request) {
	hub := s.liveFeed(w)
	if hub == nil {
		return
	}
	id := r.PathValue("id")
	if p := principalOf(r); !p.owner() && p.device != id {
		writeError(w, http.StatusForbidden, "a paired device acks only its own commands")
		return
	}
	seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "seq is a number")
		return
	}
	var body struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	e, ok := hub.Ack(seq, id)
	if !ok {
		writeError(w, http.StatusNotFound, "no command "+r.PathValue("seq")+" waits for an answer from "+id)
		return
	}
	from := ""
	if c, ok := e.Data.(commandEvent); ok {
		from = c.Command.From
	}
	ack := CommandAck{Seq: seq, Device: id, OK: body.OK, Error: deviceField(body.Error)}
	hub.PublishTo(events.Commands, "command.acked", from, time.Time{}, func(int64) any { return map[string]CommandAck{"ack": ack} })
	w.WriteHeader(http.StatusNoContent)
}

// pendingCommands is the commands section of device's snapshot: what it has not acked yet.
func pendingCommands(hub *events.Hub, device string) []Command {
	out := []Command{}
	for _, e := range hub.Pending(device) {
		if c, ok := e.Data.(commandEvent); ok {
			out = append(out, c.Command)
		}
	}
	return out
}
