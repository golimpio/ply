//go:build !unix

package library

// deviceOf is not known here: mount boundaries inside a root are not
// remembered, and only the rules for roots protect an unmounted volume.
func deviceOf(string) (uint64, bool) { return 0, false }
