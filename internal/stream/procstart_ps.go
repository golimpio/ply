//go:build unix && !darwin && !linux

package stream

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
)

// processStart returns a token that says when process pid started: equal tokens
// for one pid mean one process, so a pid that has been reused is told from the
// process it was. It fails if there is no such process. (Here it is ps's
// lstart, to the second.)
func processStart(pid int) (string, error) {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", err
	}
	start := strings.TrimSpace(string(out))
	if start == "" {
		return "", errors.New("no such process")
	}
	return start, nil
}
