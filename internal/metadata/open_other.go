//go:build !unix

package metadata

import "os"

// OpenRegular opens a file for reading, refusing anything that is not a
// regular file.
func OpenRegular(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, ErrNotRegular
	}
	return f, nil
}
