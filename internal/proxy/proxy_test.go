package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ssh-autoproxy/internal/config"
)

func TestTableDecide(t *testing.T) {
	routes := []config.Route{
		{Name: "home", HostPatterns: []string{"*.lan", "nas"}, HostSubnets: []string{"10.20.0.0/21"},
			SocksProxy: &config.SocksProxyConfig{Bind: "127.0.0.1", Port: 1080}},
		{Name: "work", HostPatterns: []string{"*.corp", "*.lan"},
			SocksProxy: &config.SocksProxyConfig{Bind: "127.0.0.1", Port: 1081}},
		{Name: "no-socks", HostPatterns: []string{"*.other"}},
	}
	table := NewTable(routes)

	// No state recorded yet: fail safe toward the tunnel.
	if d := table.Decide("x.lan"); d != (Decision{Route: "home", Upstream: "127.0.0.1:1080"}) {
		t.Errorf("before Update: Decide(x.lan) = %+v, want home via tunnel", d)
	}

	table.Update(map[string]bool{"home": true, "work": false})
	tests := []struct {
		host string
		want Decision
	}{
		{"printer.lan", Decision{Route: "home", Upstream: "127.0.0.1:1080"}},
		{"PRINTER.LAN.", Decision{Route: "home", Upstream: "127.0.0.1:1080"}}, // case, trailing dot
		{"nas", Decision{Route: "home", Upstream: "127.0.0.1:1080"}},
		{"10.20.3.4", Decision{Route: "home", Upstream: "127.0.0.1:1080"}},
		{"::ffff:10.20.3.4", Decision{Route: "home", Upstream: "127.0.0.1:1080"}},
		{"10.20.8.1", Decision{}},             // outside the /21
		{"git.corp", Decision{Route: "work"}}, // route currently direct
		{"thing.other", Decision{}},           // route without socks_proxy is ignored
		{"example.com", Decision{}},           // matches nothing
		{"lan", Decision{}},                   // "*.lan" needs a dot
		{"evil.lan.example.com", Decision{}},  // pattern is anchored
	}
	for _, tt := range tests {
		if got := table.Decide(tt.host); got != tt.want {
			t.Errorf("Decide(%q) = %+v, want %+v", tt.host, got, tt.want)
		}
	}
}

// testEnv runs a proxy Server with one route ("home", covering *.lan and
// 127.0.0.0/8) whose upstream is a fake ssh -D, plus a direct echo server.
// Each side prefixes its echoes so tests can see which path was taken.
type testEnv struct {
	server     *Server
	table      *Table
	proxyAddr  string
	directAddr string // echo server, reachable directly

	upstream *fakeUpstream
}

type fakeUpstream struct {
	addr   string
	refuse byte // non-zero: reply with this SOCKS5 failure code

	mu       sync.Mutex
	requests []string
}

func (f *fakeUpstream) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	env := &testEnv{upstream: &fakeUpstream{}}
	env.directAddr = listen(t, func(c net.Conn) { echo(c, bufio.NewReader(c), "direct:") })
	env.upstream.addr = listen(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		host, port, err := readSocksRequest(br, c)
		if err != nil {
			return
		}
		env.upstream.mu.Lock()
		env.upstream.requests = append(env.upstream.requests, net.JoinHostPort(host, strconv.Itoa(port)))
		env.upstream.mu.Unlock()
		if env.upstream.refuse != 0 {
			writeSocksReply(c, env.upstream.refuse)
			return
		}
		writeSocksReply(c, repSucceeded)
		echo(c, br, "tunnel:")
	})

	upHost, upPort, _ := net.SplitHostPort(env.upstream.addr)
	port, _ := strconv.Atoi(upPort)
	env.table = NewTable([]config.Route{{
		Name:         "home",
		HostPatterns: []string{"*.lan"},
		HostSubnets:  []string{"127.0.0.0/8"},
		SocksProxy:   &config.SocksProxyConfig{Bind: upHost, Port: port},
	}})
	env.table.Update(map[string]bool{"home": true})

	env.server = NewServer(env.table, false)
	if err := env.server.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	env.proxyAddr = env.server.Addr().String()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = env.server.Shutdown(ctx)
	})
	return env
}

// listen serves handle on a loopback port until the test ends.
func listen(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				handle(c)
			}()
		}
	}()
	return ln.Addr().String()
}

func echo(w io.Writer, r *bufio.Reader, prefix string) {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		if _, err := io.WriteString(w, prefix+line); err != nil {
			return
		}
	}
}

// roundTrip sends one line over conn (reading via r) and returns the echo.
func roundTrip(t *testing.T, conn net.Conn, r *bufio.Reader, msg string) string {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, msg+"\n"); err != nil {
		t.Fatal(err)
	}
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("reading echo: %v", err)
	}
	return strings.TrimSuffix(line, "\n")
}

func socksDial(t *testing.T, proxyAddr, host string, port int) (net.Conn, error) {
	t.Helper()
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := socksConnect(conn, host, port); err != nil {
		conn.Close()
		return nil, err
	}
	t.Cleanup(func() { conn.Close() })
	return conn, nil
}

func splitPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(p)
	return host, port
}

func TestSocksRoutesThroughTunnel(t *testing.T) {
	env := newTestEnv(t)
	conn, err := socksDial(t, env.proxyAddr, "printer.lan", 631)
	if err != nil {
		t.Fatalf("SOCKS5 connect: %v", err)
	}
	if got := roundTrip(t, conn, bufio.NewReader(conn), "hi"); got != "tunnel:hi" {
		t.Errorf("echo = %q, want tunnel:hi", got)
	}
	if seen := env.upstream.seen(); len(seen) != 1 || seen[0] != "printer.lan:631" {
		t.Errorf("upstream saw %v, want [printer.lan:631] (hostname passed through unresolved)", seen)
	}
}

func TestSocksUnmatchedHostGoesDirect(t *testing.T) {
	env := newTestEnv(t)
	// "localhost" matches neither *.lan nor (as a hostname) 127.0.0.0/8.
	_, port := splitPort(t, env.directAddr)
	conn, err := socksDial(t, env.proxyAddr, "localhost", port)
	if err != nil {
		t.Fatalf("SOCKS5 connect: %v", err)
	}
	if got := roundTrip(t, conn, bufio.NewReader(conn), "hi"); got != "direct:hi" {
		t.Errorf("echo = %q, want direct:hi", got)
	}
	if seen := env.upstream.seen(); len(seen) != 0 {
		t.Errorf("upstream should not be used, saw %v", seen)
	}
}

func TestSocksFollowsRouteState(t *testing.T) {
	env := newTestEnv(t)
	host, port := splitPort(t, env.directAddr) // 127.0.0.1 is in the route's subnet

	conn, err := socksDial(t, env.proxyAddr, host, port)
	if err != nil {
		t.Fatalf("SOCKS5 connect: %v", err)
	}
	if got := roundTrip(t, conn, bufio.NewReader(conn), "a"); got != "tunnel:a" {
		t.Errorf("proxy needed: echo = %q, want tunnel:a", got)
	}

	env.table.Update(map[string]bool{"home": false})
	conn, err = socksDial(t, env.proxyAddr, host, port)
	if err != nil {
		t.Fatalf("SOCKS5 connect: %v", err)
	}
	if got := roundTrip(t, conn, bufio.NewReader(conn), "b"); got != "direct:b" {
		t.Errorf("route direct: echo = %q, want direct:b", got)
	}
}

func TestSocksTunnelDown(t *testing.T) {
	env := newTestEnv(t)
	// Point the route at a port nothing listens on, as when its ssh -D
	// forward is down or mid-reconnect.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	env.table.routes[0].upstream = ln.Addr().String()
	ln.Close()

	_, err = socksDial(t, env.proxyAddr, "printer.lan", 80)
	var se *socksError
	if !errors.As(err, &se) || se.code != repGeneralFailure {
		t.Fatalf("err = %v, want SOCKS5 general failure", err)
	}
}

func TestSocksUpstreamRefusalPassedThrough(t *testing.T) {
	env := newTestEnv(t)
	env.upstream.refuse = repConnectionRefused
	_, err := socksDial(t, env.proxyAddr, "printer.lan", 80)
	var se *socksError
	if !errors.As(err, &se) || se.code != repConnectionRefused {
		t.Fatalf("err = %v, want SOCKS5 connection refused", err)
	}
}

func TestSocksRejectsNonConnect(t *testing.T) {
	env := newTestEnv(t)
	conn, err := net.Dial("tcp", env.proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	// Greeting, then a BIND (0x02) request for 1.2.3.4:80.
	_, _ = conn.Write([]byte{5, 1, 0, 5, 2, 0, atypIPv4, 1, 2, 3, 4, 0, 80})
	resp := make([]byte, 12)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatal(err)
	}
	if resp[3] != repCommandNotSupported {
		t.Errorf("reply code = %d, want %d", resp[3], repCommandNotSupported)
	}
}

func httpConnect(t *testing.T, proxyAddr, target, earlyData string) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	// Request and any early payload in a single write, as clients that
	// pipeline the TLS ClientHello do.
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n%s", target, target, earlyData)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	return conn, br, resp
}

func TestHTTPConnect(t *testing.T) {
	env := newTestEnv(t)
	_, port := splitPort(t, env.directAddr)

	tests := []struct {
		target, want string
	}{
		{"printer.lan:631", "tunnel:early"},
		{net.JoinHostPort("localhost", strconv.Itoa(port)), "direct:early"},
	}
	for _, tt := range tests {
		_, br, resp := httpConnect(t, env.proxyAddr, tt.target, "early\n")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("CONNECT %s: status %d", tt.target, resp.StatusCode)
		}
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSuffix(line, "\n"); got != tt.want {
			t.Errorf("CONNECT %s: echo = %q, want %q", tt.target, got, tt.want)
		}
	}
}

func TestHTTPConnectFailureIs502(t *testing.T) {
	env := newTestEnv(t)
	env.upstream.refuse = repHostUnreachable
	_, _, resp := httpConnect(t, env.proxyAddr, "printer.lan:80", "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestHTTPNonConnectRejected(t *testing.T) {
	env := newTestEnv(t)
	conn, err := net.Dial("tcp", env.proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprint(conn, "GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func TestShutdownClosesIdleRelays(t *testing.T) {
	env := newTestEnv(t)
	conn, err := socksDial(t, env.proxyAddr, "printer.lan", 80)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := env.server.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() = %v (an idle relay kept it waiting)", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Shutdown took %v", elapsed)
	}
	if _, err := net.DialTimeout("tcp", env.proxyAddr, 500*time.Millisecond); err == nil {
		t.Error("listener still accepting after Shutdown")
	}
}
