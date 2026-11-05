//go:build !unix

package art

func setNice(pid, nice int) {}
