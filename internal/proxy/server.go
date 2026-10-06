package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const (
	// handshakeTimeout bounds how long a client may take to send its
	// SOCKS5/CONNECT request, so idle connections can't pile up.
	handshakeTimeout = 10 * time.Second
	dialTimeout      = 10 * time.Second
)

var errConnRefused = syscall.ECONNREFUSED

// Server is the front-door proxy listener.
type Server struct {
	table   *Table
	verbose bool
	dialer  net.Dialer

	mu       sync.Mutex
	listener net.Listener
	conns    map[net.Conn]struct{}
	closed   bool
	wg       sync.WaitGroup
}

// NewServer returns a server routing by table. When verbose is set, each
// connection's routing decision is logged at info level (debug otherwise).
func NewServer(table *Table, verbose bool) *Server {
	return &Server{
		table:   table,
		verbose: verbose,
		dialer:  net.Dialer{Timeout: dialTimeout},
		conns:   map[net.Conn]struct{}{},
	}
}

// Start binds addr and begins serving in the background.
func (s *Server) Start(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()
	s.wg.Add(1)
	go s.serve(ln)
	return nil
}

// Addr returns the bound listener address (useful when started on port 0).
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Shutdown stops accepting, closes every open client and upstream
// connection (their tunnels are being torn down with the daemon's
// supervisors anyway, and closing both ends unblocks every relay), and waits for
// handlers to finish or ctx to expire.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	if s.listener != nil {
		_ = s.listener.Close()
	}
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) serve(ln net.Listener) {
	defer s.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			slog.Warn("proxy accept failed", "error", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if !s.track(conn) {
			_ = conn.Close()
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.untrack(conn)
			s.handle(conn)
		}()
	}
}

func (s *Server) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *Server) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	_ = c.Close()
}

// handle tells SOCKS5 from HTTP by the first byte: SOCKS5 always starts
// with its version byte 0x05, an HTTP request line with an ASCII method.
func (s *Server) handle(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	br := bufio.NewReader(conn)
	first, err := br.Peek(1)
	if err != nil {
		return
	}
	switch {
	case first[0] == socksVersion:
		s.handleSocks(conn, br)
	case first[0] >= 'A' && first[0] <= 'Z':
		s.handleHTTP(conn, br)
	default:
		slog.Debug("proxy: unrecognized protocol", "first_byte", first[0], "client", conn.RemoteAddr())
	}
}

func (s *Server) handleSocks(conn net.Conn, br *bufio.Reader) {
	host, port, err := readSocksRequest(br, conn)
	if err != nil {
		slog.Debug("proxy: bad SOCKS5 request", "error", err, "client", conn.RemoteAddr())
		return
	}
	upstream, err := s.dial(host, port, "socks5")
	if err != nil {
		writeSocksReply(conn, replyCode(err))
		return
	}
	if !s.track(upstream) {
		_ = upstream.Close()
		return
	}
	defer s.untrack(upstream)
	writeSocksReply(conn, repSucceeded)
	_ = conn.SetDeadline(time.Time{})
	relay(conn, br, upstream)
}

// handleHTTP serves HTTP CONNECT. Plain absolute-URI requests (GET
// http://...) aren't proxied: forwarding them properly means rewriting
// requests and managing keep-alive across possibly different hosts, and
// every modern client uses CONNECT for https anyway.
func (s *Server) handleHTTP(conn net.Conn, br *bufio.Reader) {
	req, err := http.ReadRequest(br)
	if err != nil {
		slog.Debug("proxy: bad HTTP request", "error", err, "client", conn.RemoteAddr())
		return
	}
	if req.Method != http.MethodConnect {
		writeHTTPStatus(conn, http.StatusMethodNotAllowed,
			"ssh-autoproxy only supports HTTP CONNECT (or SOCKS5) on this port.\n")
		return
	}
	host, portStr, err := net.SplitHostPort(req.Host)
	port, convErr := strconv.Atoi(portStr)
	if err != nil || convErr != nil || port <= 0 || port > 65535 {
		writeHTTPStatus(conn, http.StatusBadRequest, "CONNECT target must be host:port.\n")
		return
	}
	upstream, err := s.dial(host, port, "http-connect")
	if err != nil {
		writeHTTPStatus(conn, http.StatusBadGateway, err.Error()+"\n")
		return
	}
	if !s.track(upstream) {
		_ = upstream.Close()
		return
	}
	defer s.untrack(upstream)
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	relay(conn, br, upstream)
}

func writeHTTPStatus(w io.Writer, code int, body string) {
	fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, http.StatusText(code), len(body), body)
}

// dial connects to host:port the way the routing table says: through the
// matched route's ssh -D forward, or directly.
func (s *Server) dial(host string, port int, proto string) (net.Conn, error) {
	d := s.table.Decide(host)
	via := "direct"
	if d.Upstream != "" {
		via = "tunnel"
	}
	level := slog.LevelDebug
	if s.verbose {
		level = slog.LevelInfo
	}
	slog.Log(context.Background(), level, "proxy request", "host", host, "port", port, "proto", proto, "route", d.Route, "via", via)

	target := net.JoinHostPort(host, strconv.Itoa(port))
	if d.Upstream == "" {
		conn, err := s.dialer.Dial("tcp", target)
		if err != nil {
			slog.Debug("proxy: direct dial failed", "target", target, "error", err)
		}
		return conn, err
	}

	conn, err := s.dialer.Dial("tcp", d.Upstream)
	if err != nil {
		// Most likely the route's tunnel is down or mid-reconnect. Never
		// fall back to direct: the route said the destination isn't
		// reachable that way.
		slog.Debug("proxy: route tunnel unavailable", "route", d.Route, "upstream", d.Upstream, "error", err)
		return nil, &socksError{repGeneralFailure, fmt.Errorf("route %q tunnel unavailable: %w", d.Route, err)}
	}
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))
	if err := socksConnect(conn, host, port); err != nil {
		_ = conn.Close()
		slog.Debug("proxy: tunneled connect failed", "route", d.Route, "target", target, "error", err)
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// relay copies bytes both ways until both directions finish. Reads from
// the client go through br, since it may already hold bytes the client
// sent right after its request (e.g. a TLS ClientHello). When one side
// stops sending, the other side's write half is closed so it sees EOF.
func relay(client net.Conn, br *bufio.Reader, upstream net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(upstream, br)
		closeWrite(upstream)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, upstream)
		closeWrite(client)
	}()
	wg.Wait()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}
