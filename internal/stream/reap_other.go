//go:build !unix

package stream

import (
	"errors"
	"os/exec"
)

func startReaper(exe string, pgid int) (*exec.Cmd, error) {
	return nil, errors.New("not supported on this platform")
}
