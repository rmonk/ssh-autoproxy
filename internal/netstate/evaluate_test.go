package netstate

import (
	"testing"

	"ssh-autoproxy/internal/config"
)

func TestEvaluate(t *testing.T) {
	homeProfile := config.DirectProfile{
		Name:    "home-wifi",
		SSID:    "HomeWiFi",
		Gateway: "10.20.0.1",
		Subnet:  "10.20.0.0/21",
	}
	ssidOnly := config.DirectProfile{Name: "ssid-only", SSID: "CoffeeShop"}

	tests := []struct {
		name          string
		profiles      []config.DirectProfile
		state         NetState
		wantProfile   string
		wantProxyNeed bool
	}{
		{
			name:          "full match",
			profiles:      []config.DirectProfile{homeProfile},
			state:         NetState{SSID: "HomeWiFi", Gateway: "10.20.0.1", Address: "10.20.2.222"},
			wantProfile:   "home-wifi",
			wantProxyNeed: false,
		},
		{
			name:          "ssid matches but gateway does not - AND semantics",
			profiles:      []config.DirectProfile{homeProfile},
			state:         NetState{SSID: "HomeWiFi", Gateway: "10.0.0.1", Address: "10.0.0.5"},
			wantProfile:   "",
			wantProxyNeed: true,
		},
		{
			name:          "address outside configured subnet",
			profiles:      []config.DirectProfile{homeProfile},
			state:         NetState{SSID: "HomeWiFi", Gateway: "10.20.0.1", Address: "10.0.0.5"},
			wantProfile:   "",
			wantProxyNeed: true,
		},
		{
			name:          "no profiles configured - always away",
			profiles:      nil,
			state:         NetState{SSID: "HomeWiFi", Gateway: "10.20.0.1", Address: "10.20.2.222"},
			wantProfile:   "",
			wantProxyNeed: true,
		},
		{
			name:          "first match wins",
			profiles:      []config.DirectProfile{ssidOnly, homeProfile},
			state:         NetState{SSID: "CoffeeShop"},
			wantProfile:   "ssid-only",
			wantProxyNeed: false,
		},
		{
			name:          "second profile matches when first does not",
			profiles:      []config.DirectProfile{ssidOnly, homeProfile},
			state:         NetState{SSID: "HomeWiFi", Gateway: "10.20.0.1", Address: "10.20.2.222"},
			wantProfile:   "home-wifi",
			wantProxyNeed: false,
		},
		{
			name:          "empty profile never matches",
			profiles:      []config.DirectProfile{{Name: "empty"}},
			state:         NetState{SSID: "anything"},
			wantProfile:   "",
			wantProxyNeed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, proxyNeeded := Evaluate(tt.profiles, tt.state)
			if profile != tt.wantProfile {
				t.Errorf("profile = %q, want %q", profile, tt.wantProfile)
			}
			if proxyNeeded != tt.wantProxyNeed {
				t.Errorf("proxyNeeded = %v, want %v", proxyNeeded, tt.wantProxyNeed)
			}
		})
	}
}

func TestAddressInSubnetEdges(t *testing.T) {
	tests := []struct {
		address string
		cidr    string
		want    bool
	}{
		{"10.20.2.222", "10.20.0.0/21", true},
		{"10.20.8.1", "10.20.0.0/21", false}, // just outside a /21
		{"10.20.7.255", "10.20.0.0/21", true},
		{"", "10.20.0.0/21", false},
		{"10.20.2.222", "not-a-cidr", false},
		{"10.0.0.1", "10.0.0.1/32", true},
	}
	for _, tt := range tests {
		got := addressInSubnet(tt.address, tt.cidr)
		if got != tt.want {
			t.Errorf("addressInSubnet(%q, %q) = %v, want %v", tt.address, tt.cidr, got, tt.want)
		}
	}
}
