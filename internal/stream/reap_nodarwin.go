//go:build !darwin

package stream

import "ply/internal/proc"

// reaperWanted says that a ply on this system does not start a reaper: Linux
// has Pdeathsig, and the others have the registry sweep at the next start.
const reaperWanted = proc.ReaperWanted

// ReapMain is the hidden `ply --reap` mode, which only macOS needs.
func ReapMain(args []string) int { return 2 }
