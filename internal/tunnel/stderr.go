package tunnel

import (
	"regexp"
	"strings"
)

// ErrorCategory is a fixed classification for ssh key/auth failures,
// used to deduplicate/rate-limit notifications (raw stderr text contains
// incidental hostnames/fingerprints that would defeat naive dedup).
type ErrorCategory string

const (
	CategoryAuthPublicKey       ErrorCategory = "auth_publickey"
	CategoryAuthAgent           ErrorCategory = "auth_agent"
	CategoryHostKeyVerification ErrorCategory = "hostkey_verification_failed"
	CategoryHostKeyChanged      ErrorCategory = "hostkey_changed"
)

var stderrSignatures = []struct {
	substring string
	category  ErrorCategory
}{
	{"REMOTE HOST IDENTIFICATION HAS CHANGED", CategoryHostKeyChanged},
	{"Host key verification failed", CategoryHostKeyVerification},
	{"Permission denied (publickey", CategoryAuthPublicKey},
	{"Could not open a connection to your authentication agent", CategoryAuthAgent},
}

// classifyStderrLine maps a line of ssh stderr output to a fixed error
// category, if it matches one of the known key/auth failure signatures.
// Ordinary connection-refused/timeout noise (expected during backoff)
// returns ok=false and must not trigger a notification.
func classifyStderrLine(line string) (category ErrorCategory, ok bool) {
	for _, sig := range stderrSignatures {
		if strings.Contains(line, sig.substring) {
			return sig.category, true
		}
	}
	return "", false
}

// socksRequestPattern matches ssh's own debug1 logging of a decoded SOCKS4/5
// request on a dynamic (-D) forward, e.g.:
//
//	channel 5: dynamic request: socks5 host example.com port 443 command 1
//
// This line is only present in ssh's stderr when the subprocess itself is
// run with -v; it's how verbose mode observes individual SOCKS requests
// without ssh-autoproxy implementing its own SOCKS server.
var socksRequestPattern = regexp.MustCompile(`dynamic request: socks\d+ host (\S+) port (\d+)`)

// SocksRequest is a single SOCKS CONNECT request decoded from ssh -v stderr
// output.
type SocksRequest struct {
	Host string
	Port string
}

// parseSocksRequest extracts the target host/port from a line of ssh -v
// stderr, if it logs a decoded SOCKS request.
func parseSocksRequest(line string) (SocksRequest, bool) {
	m := socksRequestPattern.FindStringSubmatch(line)
	if m == nil {
		return SocksRequest{}, false
	}
	return SocksRequest{Host: m[1], Port: m[2]}, true
}
