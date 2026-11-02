//go:build !unix

package store

import "os"

func ownerOf(os.FileInfo) (int, bool) { return 0, false }
