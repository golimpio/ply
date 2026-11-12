package stream

import (
	"io"
	"os"
)

const tsPacketSize = 188

// checkTSFile is the transport-stream counterpart of checkFile: ffmpeg's
// temp_file rename establishes completion, and packet boundaries catch corrupt
// or partial output before it is served. Each legacy segment is one GOP.
func checkTSFile(path string) (fileInfo, bool) {
	f, err := os.Open(path)
	if err != nil {
		return fileInfo{}, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() < 3*tsPacketSize || fi.Size()%tsPacketSize != 0 {
		return fileInfo{}, false
	}
	var buf [tsPacketSize * 64]byte
	for {
		n, err := io.ReadFull(f, buf[:])
		if n%tsPacketSize != 0 || (err != nil && err != io.EOF && err != io.ErrUnexpectedEOF) {
			return fileInfo{}, false
		}
		for at := 0; at < n; at += tsPacketSize {
			p := buf[at : at+tsPacketSize]
			control := (p[3] >> 4) & 3
			if p[0] != 0x47 || p[1]&0x80 != 0 || control == 0 {
				return fileInfo{}, false
			}
			if control&2 != 0 && ((control == 2 && p[4] != 183) || (control == 3 && p[4] > 182)) {
				return fileInfo{}, false
			}
		}
		if err != nil {
			break
		}
	}
	return fileInfo{path: path, size: fi.Size()}, true
}
