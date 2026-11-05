//go:build !unix

package proc

import (
	"errors"
	"os/exec"
)

// StartReaper is not supported here.
func StartReaper(exe string, pgid int) (*exec.Cmd, error) {
	return nil, errors.New("not supported on this platform")
}
