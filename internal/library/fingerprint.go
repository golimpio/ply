package library

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// fingerprintChunk is how much of each end of a file the fingerprint reads.
const fingerprintChunk = 64 << 10

// fingerprint identifies a file's content cheaply: sha256 of its size and of
// its first and last 64 KiB (the whole file when it is smaller than both). It
// recognises a moved file whose mtime did not survive the move (cp without
// -p, some network shares), where size and mtime alone cannot.
func fingerprint(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := fi.Size()
	h := sha256.New()
	var n [8]byte
	binary.LittleEndian.PutUint64(n[:], uint64(size))
	h.Write(n[:])
	if size <= 2*fingerprintChunk {
		if _, err := io.Copy(h, io.LimitReader(f, size)); err != nil {
			return "", fmt.Errorf("fingerprint %s: %w", path, err)
		}
	} else {
		if _, err := io.Copy(h, io.NewSectionReader(f, 0, fingerprintChunk)); err != nil {
			return "", fmt.Errorf("fingerprint %s: %w", path, err)
		}
		if _, err := io.Copy(h, io.NewSectionReader(f, size-fingerprintChunk, fingerprintChunk)); err != nil {
			return "", fmt.Errorf("fingerprint %s: %w", path, err)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
