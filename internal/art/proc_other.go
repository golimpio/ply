//go:build !unix

package art

import "os/exec"

func ownProcessGroup(cmd *exec.Cmd) {}

func setNice(pid, nice int) {}
