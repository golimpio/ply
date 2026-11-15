//go:build darwin

package discovery

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// advertise registers through macOS's own mDNSResponder with `dns-sd -R`. The
// Go responder cannot share UDP 5353 with mDNSResponder on macOS 27: it binds
// and joins, but neither its announcements nor its queries are seen (the same
// reason the receiver browse is empty). dns-sd is the system's registrar, so
// clients see the service exactly as they see every other Bonjour service.
func advertise(s Service) (*Advertiser, error) {
	instance := fitLabel(strings.ReplaceAll(s.Name, ".", "-"), "")
	args := []string{"-R", instance, ServiceType, "local", strconv.Itoa(s.Port)}
	args = append(args, txtRecords(s)...)
	cmd := exec.Command("/usr/bin/dns-sd", args...)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("dns-sd -R: %w", err)
	}
	return &Advertiser{cmd: cmd}, nil
}
