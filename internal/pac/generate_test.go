package pac

import (
	"strings"
	"testing"

	"ssh-autoproxy/internal/config"
)

func testRoutes() []config.Route {
	return []config.Route{
		{
			Name:         "home-lan",
			JumpHost:     "gateway.example-tailnet.ts.net",
			HostPatterns: []string{"*.lan"},
			HostSubnets:  []string{"10.20.0.0/21"},
			SocksProxy:   &config.SocksProxyConfig{Bind: "127.0.0.1", Port: 1080},
		},
		{
			Name:         "work",
			JumpHost:     "bastion.work.example.com",
			HostPatterns: []string{"*.corp"},
			SocksProxy:   &config.SocksProxyConfig{Bind: "127.0.0.1", Port: 1081},
		},
		{
			Name:         "no-socks",
			JumpHost:     "other.example.com",
			HostPatterns: []string{"*.other"},
			// SocksProxy nil: this route must never appear in the PAC output.
		},
	}
}

func TestGenerateProxyNeeded(t *testing.T) {
	routes := testRoutes()
	out := Generate(routes, map[string]bool{"home-lan": true, "work": false, "no-socks": true})

	if !strings.Contains(out, `shExpMatch(host, "*.lan")`) {
		t.Errorf("missing shExpMatch branch for home-lan:\n%s", out)
	}
	if !strings.Contains(out, `SOCKS5 127.0.0.1:1080; DIRECT`) {
		t.Errorf("home-lan (away) should route through its own SOCKS5 port:\n%s", out)
	}
	if !strings.Contains(out, `isInNet(host, "10.20.0.0", "255.255.248.0")`) {
		t.Errorf("missing isInNet branch with correct netmask:\n%s", out)
	}

	if !strings.Contains(out, `shExpMatch(host, "*.corp")`) {
		t.Errorf("missing shExpMatch branch for work:\n%s", out)
	}
	if strings.Contains(out, "127.0.0.1:1081") {
		t.Errorf("work (direct) must not return its SOCKS5 address:\n%s", out)
	}

	if strings.Contains(out, "*.other") {
		t.Errorf("a route with no SocksProxy must be omitted entirely:\n%s", out)
	}
}

func TestGenerateAllDirect(t *testing.T) {
	routes := testRoutes()
	out := Generate(routes, map[string]bool{"home-lan": false, "work": false})
	if strings.Contains(out, "SOCKS5") {
		t.Errorf("no route should mention SOCKS5 when every route is direct:\n%s", out)
	}
}

func TestGenerateCatchAllIsDirect(t *testing.T) {
	routes := testRoutes()
	out := Generate(routes, map[string]bool{"home-lan": true, "work": true})
	lastReturn := out[strings.LastIndex(out, "return"):]
	if !strings.Contains(lastReturn, `"DIRECT"`) {
		t.Errorf("catch-all branch must be DIRECT, got: %s", lastReturn)
	}
}

func TestCIDRToNetmask(t *testing.T) {
	tests := []struct {
		cidr        string
		wantNetwork string
		wantMask    string
	}{
		{"10.20.0.0/21", "10.20.0.0", "255.255.248.0"},
		{"10.0.0.0/8", "10.0.0.0", "255.0.0.0"},
		{"192.168.1.0/24", "192.168.1.0", "255.255.255.0"},
		{"10.0.0.5/32", "10.0.0.5", "255.255.255.255"},
	}
	for _, tt := range tests {
		network, mask, err := cidrToNetmask(tt.cidr)
		if err != nil {
			t.Fatalf("cidrToNetmask(%q) error: %v", tt.cidr, err)
		}
		if network != tt.wantNetwork || mask != tt.wantMask {
			t.Errorf("cidrToNetmask(%q) = (%q, %q), want (%q, %q)", tt.cidr, network, mask, tt.wantNetwork, tt.wantMask)
		}
	}
}

func TestGenerateInvalidSubnetSkipped(t *testing.T) {
	routes := []config.Route{{
		Name:        "a",
		JumpHost:    "a.example.com",
		HostSubnets: []string{"not-a-cidr"},
		SocksProxy:  &config.SocksProxyConfig{Bind: "127.0.0.1", Port: 1080},
	}}
	out := Generate(routes, map[string]bool{"a": true})
	if strings.Contains(out, "isInNet") {
		t.Errorf("invalid CIDR should be skipped, got:\n%s", out)
	}
}
