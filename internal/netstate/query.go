// Package netstate detects the current network (via ip/nmcli) and
// evaluates it against configured "direct network" profiles.
package netstate

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// NetState is a snapshot of the network the machine is currently on.
// Any field may be empty if it couldn't be determined (e.g. no default
// route, or the default interface isn't Wi-Fi) — that's a valid "away"
// state, not an error.
type NetState struct {
	SSID    string
	Gateway string
	Address string
}

const queryTimeout = 2 * time.Second

// Query inspects the current network. Errors are reserved for the
// underlying commands being unusable (not installed, etc.); "no network"
// conditions resolve to a zero-value NetState instead of an error.
func Query() (NetState, error) {
	dev, gateway, err := defaultRoute()
	if err != nil {
		return NetState{}, err
	}
	state := NetState{Gateway: gateway}
	if dev != "" {
		state.Address = interfaceAddress(dev)
		state.SSID = wifiSSID(dev)
	}
	return state, nil
}

var defaultRouteRe = regexp.MustCompile(`\bvia\s+(\S+)\s+dev\s+(\S+)`)

func defaultRoute() (dev, gateway string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ip", "-4", "route", "show", "default").Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return "", "", nil // no default route: legitimately offline
		}
		return "", "", fmt.Errorf("ip route show default: %w", err)
	}
	line := strings.SplitN(string(out), "\n", 2)[0]
	m := defaultRouteRe.FindStringSubmatch(line)
	if m == nil {
		return "", "", nil
	}
	return m[2], m[1], nil
}

var inetRe = regexp.MustCompile(`inet\s+(\d+\.\d+\.\d+\.\d+)/\d+`)

func interfaceAddress(dev string) string {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ip", "-4", "addr", "show", "dev", dev).Output()
	if err != nil {
		return ""
	}
	m := inetRe.FindStringSubmatch(string(out))
	if m == nil {
		return ""
	}
	return m[1]
}

func wifiSSID(dev string) string {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nmcli", "-t", "-f", "DEVICE,ACTIVE,SSID", "dev", "wifi").Output()
	if err != nil {
		return ""
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		fields := splitTerse(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		if fields[0] == dev && fields[1] == "yes" {
			return fields[2]
		}
	}
	return ""
}

// splitTerse splits an `nmcli -t` line on unescaped colons, per nmcli's
// terse-output escaping rules (":" -> "\:", "\" -> "\\").
func splitTerse(line string) []string {
	var fields []string
	var cur strings.Builder
	escaped := false
	for _, r := range line {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == ':':
			fields = append(fields, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	fields = append(fields, cur.String())
	return fields
}
