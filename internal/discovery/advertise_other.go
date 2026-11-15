//go:build !darwin

package discovery

// advertise uses ply's own responder where no system registrar is borrowed.
func advertise(s Service) (*Advertiser, error) {
	return advertiseMDNS(s)
}
