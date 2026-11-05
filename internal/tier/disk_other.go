//go:build !unix

package tier

// diskOf cannot tell disks apart here: the same-disk rule does not apply.
func diskOf(string) (string, bool) { return "", false }
