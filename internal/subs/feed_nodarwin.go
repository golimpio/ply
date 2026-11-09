//go:build !darwin

package subs

import "os"

// readAheadOff does nothing here: the reading ahead of other kernels was not measured
// (the feed still reads a chunk at a time, and stops when ffmpeg does).
func readAheadOff(*os.File) error { return nil }
