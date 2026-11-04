//go:build !unix

package stream

import "errors"

func processStart(pid int) (string, error) { return "", errors.New("not supported on this platform") }
