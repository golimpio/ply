package tier

import "testing"

func TestServerOf(t *testing.T) {
	for from, want := range map[string]string{
		"//gilberto@NAS-Disk3._smb._tcp.local/Movies-3": "nas-disk3._smb._tcp.local",
		"//NAS/Movies":               "nas",
		"//user:pw@nas.local:445/TV": "nas.local",
		"nas.local:/export/movies":   "nas.local",
		"/dev/disk3s1":               "",
		"map auto_home":              "",
	} {
		if got := serverOf(from); got != want {
			t.Errorf("serverOf(%q) = %q, want %q", from, got, want)
		}
	}
}
