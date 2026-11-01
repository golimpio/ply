//go:build !unix

package subs

import "os/exec"

func ownProcessGroup(cmd *exec.Cmd) {}

func setNice(pid, nice int) {}
