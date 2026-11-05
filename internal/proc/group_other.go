//go:build !unix

package proc

import "os/exec"

// OwnGroup does nothing here: there are no process groups.
func OwnGroup(cmd *exec.Cmd) {}
