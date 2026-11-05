package tier

import (
	"fmt"
	"strings"
	"syscall"
)

// networkFS are the file systems that read over the network: two shares of one
// server compete for the same link (on the owner's Wi-Fi, the bottleneck), so they
// count as one disk.
var networkFS = map[string]bool{"smbfs": true, "nfs": true, "afpfs": true, "webdav": true}

// diskOf names the disk path is on: "net:<server>" for a network share, else
// "fs:<fsid>".
func diskOf(path string) (string, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return "", false
	}
	if networkFS[cString(st.Fstypename[:])] {
		if host := serverOf(cString(st.Mntfromname[:])); host != "" {
			return "net:" + host, true
		}
	}
	return fmt.Sprintf("fs:%d:%d", st.Fsid.Val[0], st.Fsid.Val[1]), true
}

// serverOf is the server of a mount source: "//user@nas._smb._tcp.local/Movies"
// (smbfs, afpfs) or "nas.local:/export/movies" (nfs) give the host, lower case.
func serverOf(from string) string {
	if rest, ok := strings.CutPrefix(from, "//"); ok {
		host, _, _ := strings.Cut(rest, "/")
		if _, after, found := strings.Cut(host, "@"); found {
			host = after
		}
		host, _, _ = strings.Cut(host, ":") // a port
		return strings.ToLower(host)
	}
	if host, _, ok := strings.Cut(from, ":"); ok && !strings.Contains(host, "/") {
		return strings.ToLower(host)
	}
	return ""
}

func cString(b []int8) string {
	var sb strings.Builder
	for _, c := range b {
		if c == 0 {
			break
		}
		sb.WriteByte(byte(c))
	}
	return sb.String()
}
