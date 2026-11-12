//go:build !unix

package proc

import "os/exec"

// OwnSession does nothing here: there are no sessions.
func OwnSession(cmd *exec.Cmd) {}

// OwnGroup does nothing here: there are no process groups.
func OwnGroup(cmd *exec.Cmd) {}
