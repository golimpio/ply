package stream

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// processStart returns a token that says when process pid started: equal tokens
// for one pid mean one process, so a pid that has been reused is told from the
// process it was. It fails if there is no such process. (Field 22 of
// /proc/<pid>/stat is the start time in clock ticks since boot, which a reboot
// starts again from: the token begins with the boot id, so that a pid of the last
// boot that is a process again is not taken for the one that was recorded.)
func processStart(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	// "pid (comm) state ppid ...": the name may hold spaces and parentheses, so
	// the fields are counted from the last ')'.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return "", errors.New("unreadable /proc stat")
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) < 20 {
		return "", errors.New("unreadable /proc stat")
	}
	return bootID() + fields[19], nil // field 22 overall; the first after the name is field 3
}

// bootID is the id of this boot of the system, with a separator; empty where the
// kernel does not say.
func bootID() string {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if id := strings.TrimSpace(string(b)); err == nil && id != "" {
		return id + "/"
	}
	return ""
}
