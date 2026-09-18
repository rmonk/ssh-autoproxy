package tunnel

import "testing"

func TestClassifyStderrLine(t *testing.T) {
	tests := []struct {
		line string
		want ErrorCategory
		ok   bool
	}{
		{"user@host: Permission denied (publickey).", CategoryAuthPublicKey, true},
		{"Could not open a connection to your authentication agent.", CategoryAuthAgent, true},
		{"Host key verification failed.", CategoryHostKeyVerification, true},
		{"@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @", CategoryHostKeyChanged, true},
		{"ssh: connect to host example.com port 22: Connection refused", "", false},
		{"kex_exchange_identification: read: Connection reset by peer", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		cat, ok := classifyStderrLine(tt.line)
		if ok != tt.ok || cat != tt.want {
			t.Errorf("classifyStderrLine(%q) = (%q, %v), want (%q, %v)", tt.line, cat, ok, tt.want, tt.ok)
		}
	}
}

func TestParseSocksRequest(t *testing.T) {
	tests := []struct {
		line string
		want SocksRequest
		ok   bool
	}{
		{
			"debug1: channel 5: dynamic request: socks5 host example.com port 443 command 1",
			SocksRequest{Host: "example.com", Port: "443"},
			true,
		},
		{
			"debug1: channel 3: dynamic request: socks4 host 10.0.0.1 port 22 command 1",
			SocksRequest{Host: "10.0.0.1", Port: "22"},
			true,
		},
		{"debug1: channel 5: new [dynamic-tcpip]", SocksRequest{}, false},
		{"", SocksRequest{}, false},
	}
	for _, tt := range tests {
		got, ok := parseSocksRequest(tt.line)
		if ok != tt.ok || got != tt.want {
			t.Errorf("parseSocksRequest(%q) = (%+v, %v), want (%+v, %v)", tt.line, got, ok, tt.want, tt.ok)
		}
	}
}
