package api

// Pairing (docs/M11-CLIENTS.md, "Pairing"):
//
//	POST   /api/pairing             the owner asks for a code to show: {code, expiresAt}
//	POST   /api/pair                a device sends the code it was given: {token}, once
//	DELETE /api/devices/{id}/token  the owner revokes a device
//
// All three are private (see private); the first and the last are the owner's (see ownerOnly), and
// the second needs no credential: the code is it.

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"ply/internal/store"
	"ply/internal/stream"
)

const (
	pairingCodeFor   = 5 * time.Minute
	pairingMaxMisses = 5 // wrong codes before the code is spent
)

// pairingCode is the code a device may pair with now: one at a time, for pairingCodeFor, once.
type pairingCode struct {
	mu      sync.Mutex
	code    string
	expires time.Time
	misses  int
}

// take reports whether code is the one in force, and spends it when it is. A wrong code counts
// against it.
func (p *pairingCode) take(code string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.code == "" || !now.Before(p.expires) {
		p.code = ""
		return false
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(p.code)) != 1 {
		if p.misses++; p.misses >= pairingMaxMisses {
			p.code = ""
		}
		return false
	}
	p.code = ""
	return true
}

type pairingCodeResponse struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expiresAt"`
	// ExpiresIn is the seconds it has left as the server answers: a page counts down from it,
	// whatever its own clock says.
	ExpiresIn int `json:"expiresIn"`
}

// handlePairingCode is POST /api/pairing: a new code, in place of any before it.
func (s *Server) handlePairingCode(w http.ResponseWriter, r *http.Request) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	p := &s.pairing
	p.mu.Lock()
	p.code, p.expires, p.misses = fmt.Sprintf("%06d", n), s.clock().Add(pairingCodeFor), 0
	out := pairingCodeResponse{Code: p.code, ExpiresAt: p.expires.UTC(), ExpiresIn: int(pairingCodeFor / time.Second)}
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

type pairRequest struct {
	Code string `json:"code"`
	ID   string `json:"id"`
	deviceRequest
}

// handlePair is POST /api/pair: the device that sends the code in force is registered and paired,
// and gets its token, which the server keeps only as its hash.
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	var req pairRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !store.ValidDeviceID(req.ID) {
		writeError(w, http.StatusBadRequest, "a device id is 8 to 64 letters, digits, - or _")
		return
	}
	if !s.pairing.take(req.Code, s.clock()) {
		writeError(w, http.StatusForbidden, "wrong or expired code: ask for a new one in Settings › Devices")
		return
	}
	b := make([]byte, 32)
	rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)
	d := deviceFrom(r, req.ID, req.deviceRequest)
	s.pairs.change.Lock()
	defer s.pairs.change.Unlock()
	if err := s.Store.PairDevice(r.Context(), d, tokenHash(token), s.clock().Unix()); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.pairs.set(d.ID, tokenHash(token))
	s.devices.put(stream.Device{ID: d.ID, Name: d.Name, Kind: d.Kind})
	s.Logger.Info("device paired", "device", d.ID, "name", d.Name)
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

// handleDeviceRevoke is DELETE /api/devices/{id}/token: 204, or 404 for a device that is not
// paired. Its token, the URL tokens made for it and what it has open (its feed) stop at once.
func (s *Server) handleDeviceRevoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.pairs.change.Lock()
	defer s.pairs.change.Unlock()
	ok, err := s.Store.RevokeDevice(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "no paired device "+id)
		return
	}
	s.pairs.set(id, "")
	s.Logger.Info("device revoked", "device", id)
	w.WriteHeader(http.StatusNoContent)
}
