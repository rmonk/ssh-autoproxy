// Package proxy implements the daemon's own front-door proxy listener: a
// single fixed loopback port, speaking both SOCKS5 and HTTP CONNECT, that
// the PAC file advertises. Each connection's destination is matched
// against the routes' host_patterns/host_subnets at connect time, and sent
// through that route's ssh -D SOCKS5 forward if the route currently needs
// its jump host, or dialed directly otherwise — including destinations
// that match no route at all, so a client that sends everything here
// (e.g. https_proxy set globally) still works.
package proxy

import (
	"net"
	"net/netip"
	"path"
	"strconv"
	"strings"
	"sync"

	"ssh-autoproxy/internal/config"
)

// Decision is where a single connection should go.
type Decision struct {
	// Route is the name of the matched route, or "" if no route matched.
	Route string
	// Upstream is the route's ssh -D SOCKS5 address to tunnel through, or
	// "" to dial the destination directly.
	Upstream string
}

// Table maps destination hosts to routes and holds each route's current
// direct-vs-proxy state. It's safe for concurrent use: the daemon calls
// Update on network changes while connections call Decide.
type Table struct {
	routes []route

	mu          sync.RWMutex
	proxyNeeded map[string]bool
}

type route struct {
	name     string
	upstream string
	patterns []string
	subnets  []netip.Prefix
}

// NewTable builds a Table from the configured routes. Only routes with a
// socks_proxy take part — the same set the PAC file covers — since a
// route without an ssh -D forward has nothing to tunnel through.
// Routes are matched in config order, first match wins, as in the PAC.
func NewTable(routes []config.Route) *Table {
	t := &Table{proxyNeeded: map[string]bool{}}
	for _, r := range routes {
		if r.SocksProxy == nil {
			continue
		}
		rt := route{
			name:     r.Name,
			upstream: net.JoinHostPort(r.SocksProxy.Bind, strconv.Itoa(r.SocksProxy.Port)),
		}
		for _, p := range r.HostPatterns {
			rt.patterns = append(rt.patterns, strings.ToLower(p))
		}
		for _, s := range r.HostSubnets {
			if prefix, err := netip.ParsePrefix(s); err == nil {
				rt.subnets = append(rt.subnets, prefix.Masked())
			}
		}
		t.routes = append(t.routes, rt)
	}
	return t
}

// Update replaces the per-route proxy-needed state (see netstate.Evaluate).
func (t *Table) Update(proxyNeeded map[string]bool) {
	copied := make(map[string]bool, len(proxyNeeded))
	for k, v := range proxyNeeded {
		copied[k] = v
	}
	t.mu.Lock()
	t.proxyNeeded = copied
	t.mu.Unlock()
}

// Decide picks where a connection to host should go. A route with no
// recorded state yet is treated as needing its jump host, the same
// fail-safe direction check-ssh takes.
func (t *Table) Decide(host string) Decision {
	name, upstream, ok := t.match(host)
	if !ok {
		return Decision{}
	}
	t.mu.RLock()
	needed, known := t.proxyNeeded[name]
	t.mu.RUnlock()
	if known && !needed {
		return Decision{Route: name}
	}
	return Decision{Route: name, Upstream: upstream}
}

// match finds the first route whose host_patterns (shell-style globs, as
// with the PAC file's shExpMatch) match host, or — for a literal IP —
// whose host_subnets contain it. Hostnames are never resolved here: doing
// so would leak internal names to whatever DNS is current, and
// destinations are usually unresolvable locally when away anyway.
func (t *Table) match(host string) (name, upstream string, ok bool) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	addr, addrErr := netip.ParseAddr(host)
	for _, r := range t.routes {
		for _, p := range r.patterns {
			if matched, _ := path.Match(p, host); matched {
				return r.name, r.upstream, true
			}
		}
		if addrErr == nil {
			for _, prefix := range r.subnets {
				if prefix.Contains(addr.Unmap()) {
					return r.name, r.upstream, true
				}
			}
		}
	}
	return "", "", false
}
