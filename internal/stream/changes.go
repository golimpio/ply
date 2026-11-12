package stream

import "sort"

// A PlayChange is a play starting, changing or ending, for the live feed
// (docs/M11-CLIENTS.md, "Session events").
type PlayChange struct {
	Type   string // PlayStarted, PlayUpdated or PlayEnded
	Play   PlayInfo
	Reason string // why a play ended: EndStopped, EndExpired, EndError, EndShutdown or EndReplaced
}

// The kinds of PlayChange.
const (
	PlayStarted = "started"
	PlayUpdated = "updated" // a heartbeat: where the player is, and whether it is paused
	PlayEnded   = "ended"
)

// Why a play ended.
const (
	EndStopped  = "stopped"  // on purpose (Delete), from any device
	EndExpired  = "expired"  // nothing was heard from it for long enough
	EndError    = "error"    // it could not be set up
	EndShutdown = "shutdown" // the manager is closing
	EndReplaced = "replaced" // the same client asked to play the same item again (StartDirect)
)

// OnPlayChange sets the function told of every play that starts, changes or ends:
// a session or a direct play. Each play is told as started once, then as updated
// for each heartbeat, then as ended once, whichever of the manager's paths noticed
// it (a Delete, the idle timeout, a direct play that ran out, a Create that failed,
// Close). f runs with the manager's lock held, so the changes come in the order
// they happened: it must be quick, must not block, and must not call the manager.
// nil removes it.
func (m *Manager) OnPlayChange(f func(PlayChange)) {
	if f == nil {
		m.onPlay.Store(nil)
		return
	}
	m.onPlay.Store(&f)
}

func (m *Manager) tellLocked(c PlayChange) {
	if f := m.onPlay.Load(); f != nil {
		(*f)(c)
	}
}

// reconcileLocked compares what plays now with what was told, and tells the
// difference: a play that is new has started; one that is gone has ended, for the
// reason end left for it (expired if none: a direct play that ran out). It runs
// whenever what plays may have changed (unlock).
func (m *Manager) reconcileLocked() {
	now := m.playingLocked(nil)
	playing := make(map[string]bool, len(now))
	for _, p := range now {
		playing[p.ID] = true
		_, told := m.announced[p.ID]
		m.announced[p.ID] = p // the latest, for its end
		if !told {
			m.tellLocked(PlayChange{Type: PlayStarted, Play: p})
		}
	}
	var gone []PlayInfo
	for id, p := range m.announced {
		if !playing[id] {
			gone = append(gone, p)
		}
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i].ID < gone[j].ID })
	for _, p := range gone {
		reason := m.endReason[p.ID]
		if reason == "" {
			reason = EndExpired
		}
		delete(m.announced, p.ID)
		m.tellLocked(PlayChange{Type: PlayEnded, Play: p, Reason: reason})
	}
	clear(m.endReason) // a reason is for this round: what was not told (it never started) is moot
}

// updatedLocked tells that the play p was heard from (a heartbeat), if it was told
// as started; one that has ended is not brought back.
func (m *Manager) updatedLocked(p PlayInfo) {
	if _, told := m.announced[p.ID]; !told {
		return
	}
	m.announced[p.ID] = p
	m.tellLocked(PlayChange{Type: PlayUpdated, Play: p})
}
