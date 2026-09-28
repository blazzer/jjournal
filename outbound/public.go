package outbound

import (
	"net"
	"net/netip"

	"journal/render"
)

// PublicAddr reports whether addr is a public unicast address.
func PublicAddr(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	return render.PublicIP(net.IP(addr.AsSlice()))
}
