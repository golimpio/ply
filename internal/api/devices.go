package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"ply/internal/store"
	"ply/internal/stream"
)

// The devices that use ply (docs/M11-CLIENTS.md, "Devices"):
//
//	PUT /api/devices/{id}   a device registers, or says new things of itself: {name, kind, app, version}
//	GET /api/devices        the devices, the most recently seen first
//
// Both are private (see private): they are the household's list of devices. Pairing is in
// pairing.go.

// DeviceHeader carries the id of the device a request comes from, on the app's own
// requests. A browser's EventSource, which cannot set headers, sends ?device=.
const DeviceHeader = "X-Ply-Device"

// deviceID is the device a request comes from: a paired device's own, whatever it says, else
// the one it says; "" for none, or for anything that is not a device id.
func deviceID(r *http.Request) string {
	if p := principalOf(r); !p.owner() {
		return p.device
	}
	id := r.Header.Get(DeviceHeader)
	if id == "" {
		id = r.URL.Query().Get("device")
	}
	if !store.ValidDeviceID(id) {
		return ""
	}
	return id
}

// DeviceRef names the device that started a play.
type DeviceRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
}

func deviceRef(d stream.Device) *DeviceRef {
	if d.ID == "" {
		return nil
	}
	return &DeviceRef{ID: d.ID, Name: d.Name, Kind: d.Kind}
}

// Device is one entry of GET /api/devices.
type Device struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	App       string    `json:"app"`
	Version   string    `json:"version"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	// Paired says the device has a token of its own (docs/M11-CLIENTS.md, "Pairing"), since PairedAt.
	Paired   bool       `json:"paired"`
	PairedAt *time.Time `json:"pairedAt,omitempty"`
}

type deviceRequest struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	App     string `json:"app"`
	Version string `json:"version"`
}

// maxDeviceField is the longest a device may make its name, kind, app or version
// (in characters); what is longer is cut.
const maxDeviceField = 100

func deviceField(v string) string {
	v = strings.TrimSpace(v)
	if utf8.RuneCountInString(v) > maxDeviceField {
		v = string([]rune(v)[:maxDeviceField])
	}
	return v
}

// deviceNames is what the registered devices call themselves, kept in memory for the
// plays they start: read on every play, written on each registration, loaded from
// the store the first time it is needed.
type deviceNames struct {
	mu     sync.Mutex
	loaded bool
	byID   map[string]stream.Device
}

func (n *deviceNames) get(ctx context.Context, st *store.Store, id string) (stream.Device, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.loaded {
		list, err := st.Devices(ctx)
		if err != nil {
			return stream.Device{}, false // tried again next time
		}
		n.byID = make(map[string]stream.Device, len(list))
		for _, d := range list {
			n.byID[d.ID] = stream.Device{ID: d.ID, Name: d.Name, Kind: d.Kind}
		}
		n.loaded = true
	}
	d, ok := n.byID[id]
	return d, ok
}

func (n *deviceNames) put(d stream.Device) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.byID == nil {
		n.byID = map[string]stream.Device{}
	}
	n.byID[d.ID] = d
}

// playDevice is the device of a play that r starts: as it registered, or, for one
// that never did, its id and the name of its User-Agent. Zero for a request that
// names no device. The device is seen now (in the background: the play does not wait
// for the write).
func (s *Server) playDevice(r *http.Request) stream.Device {
	id := deviceID(r)
	if id == "" {
		return stream.Device{}
	}
	d, ok := s.devices.get(r.Context(), s.Store, id)
	if !ok {
		return stream.Device{ID: id, Name: userAgentName(r.UserAgent())}
	}
	ctx := context.WithoutCancel(r.Context())
	go func() {
		if err := s.Store.TouchDevice(ctx, id, time.Now().Unix()); err != nil {
			s.Logger.Warn("recording when a device was seen", "device", id, "err", err)
		}
	}()
	return d
}

// handleDevicePut is PUT /api/devices/{id}: 204. A device that sends no name is
// named after its User-Agent ("Safari").
func (s *Server) handleDevicePut(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !store.ValidDeviceID(id) {
		writeError(w, http.StatusBadRequest, "a device id is 8 to 64 letters, digits, - or _")
		return
	}
	if p := principalOf(r); !p.owner() && p.device != id {
		writeError(w, http.StatusForbidden, "a paired device registers only itself")
		return
	}
	var req deviceRequest
	body := http.MaxBytesReader(w, r.Body, 1<<14)
	if err := json.NewDecoder(body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	d := deviceFrom(r, id, req)
	if err := s.Store.PutDevice(r.Context(), d, time.Now().Unix()); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.devices.put(stream.Device{ID: d.ID, Name: d.Name, Kind: d.Kind})
	w.WriteHeader(http.StatusNoContent)
}

// deviceFrom is the device id says it is, as kept: the fields cut to size, and a device that sends
// no name named after its User-Agent ("Safari").
func deviceFrom(r *http.Request, id string, req deviceRequest) store.Device {
	d := store.Device{ID: id, Name: deviceField(req.Name), Kind: deviceField(req.Kind), App: deviceField(req.App),
		Version: deviceField(req.Version)}
	if d.Name == "" {
		d.Name = userAgentName(r.UserAgent())
	}
	return d
}

// handleDevices is GET /api/devices.
func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.Devices(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]Device, len(list))
	for i, d := range list {
		out[i] = Device{ID: d.ID, Name: d.Name, Kind: d.Kind, App: d.App, Version: d.Version,
			FirstSeen: time.Unix(d.CreatedAt, 0).UTC(), LastSeen: time.Unix(d.LastSeen, 0).UTC(), Paired: d.PairedAt != 0}
		if d.PairedAt != 0 {
			out[i].PairedAt = new(time.Unix(d.PairedAt, 0).UTC())
		}
	}
	writeJSON(w, http.StatusOK, out)
}
