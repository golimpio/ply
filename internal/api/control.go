package api

import (
	"crypto/subtle"
	"net/http"
	"strconv"
)

// ControlHeader carries the control key of `ply -stop` (docs/DESIGN.md, "Stopping"): a random
// secret the running server writes, readable by its owner alone, to <data>/control.json.
const ControlHeader = "X-Ply-Control"

// stopPath is the endpoint `ply -stop` calls. It is private (no CORS, no cross-site request) and
// outside requireAuth: the control key is its credential, whatever -token says, since whoever can
// read the data directory is the owner of the server.
const stopPath = "/api/server/stop"

// handleStop asks the server to stop: nicely, or, with ?force=true, with 5 s for the nice stop
// before it quits at once. It answers 202 before the stop starts.
func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if s.Stop == nil || s.ControlKey == "" {
		writeError(w, http.StatusServiceUnavailable, "this server cannot be stopped through the API")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(ControlHeader)), []byte(s.ControlKey)) != 1 {
		writeError(w, http.StatusForbidden, "a stop needs the control key of the data directory (ply -stop)")
		return
	}
	force := false
	if v := r.URL.Query().Get("force"); v != "" {
		var err error
		if force, err = strconv.ParseBool(v); err != nil {
			writeError(w, http.StatusBadRequest, "force: want true or false")
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"stopping": true, "force": force})
	s.Logger.Info("stop asked through the API", "force", force, "remote", r.RemoteAddr)
	s.Stop(force)
}
