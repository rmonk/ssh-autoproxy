package netstate

import (
	"net"

	"ssh-autoproxy/internal/config"
)

// Evaluate checks NetState against the configured direct-network profiles
// in order and returns the first one that matches (all of its non-empty
// fields must match — AND semantics) along with proxyNeeded: false when a
// profile matched (destinations reachable directly), true otherwise
// ("away").
func Evaluate(profiles []config.DirectProfile, state NetState) (matched string, proxyNeeded bool) {
	for _, p := range profiles {
		if profileMatches(p, state) {
			return p.Name, false
		}
	}
	return "", true
}

func profileMatches(p config.DirectProfile, state NetState) bool {
	if p.SSID == "" && p.Gateway == "" && p.Subnet == "" {
		return false // a profile with no criteria can't meaningfully match
	}
	if p.SSID != "" && p.SSID != state.SSID {
		return false
	}
	if p.Gateway != "" && p.Gateway != state.Gateway {
		return false
	}
	if p.Subnet != "" && !addressInSubnet(state.Address, p.Subnet) {
		return false
	}
	return true
}

func addressInSubnet(address, cidr string) bool {
	if address == "" {
		return false
	}
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(address)
	if ip == nil {
		return false
	}
	return ipnet.Contains(ip)
}
