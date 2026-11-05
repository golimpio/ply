//go:build !unix

package subs

func setNice(pid, nice int) {}

func setForeground(pid int) {}

// Without process groups a conversion cannot be suspended: it runs on.
func pauseGroup(pid int)  {}
func resumeGroup(pid int) {}
