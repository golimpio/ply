//go:build !darwin

package proc

// ReaperWanted says that a ply on this system does not start a reaper: Linux has
// Pdeathsig, and the others have the registry sweep at the next start.
const ReaperWanted = false
