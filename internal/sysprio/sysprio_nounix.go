//go:build !unix

package sysprio

func background(int, int) {}
func foreground(int)      {}
