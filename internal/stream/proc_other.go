//go:build !unix

package stream

import (
	"errors"
	"os"
	"os/exec"
	"time"
)

func ownGroup(cmd *exec.Cmd) {}

func signalGroup(cmd *exec.Cmd, stop bool) error {
	return errors.New("pausing ffmpeg is not supported on this platform")
}

func setNice(pid, nice int) {}

func processAlive(pid int) bool { return false }

func usage(ps *os.ProcessState) (time.Duration, int64) { return 0, 0 }
