//go:build !unix

package probe

func setNice(pid, nice int) {}
