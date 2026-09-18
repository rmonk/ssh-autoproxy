package pac

import (
	"context"
	"net"
	"net/http"
	"sync"
)

// Server serves the generated PAC file over HTTP. The bind address is
// enforced by config.Validate (loopback only) before this is ever called.
type Server struct {
	path string

	mu         sync.RWMutex
	body       string
	httpServer *http.Server
}

func NewServer(path string) *Server {
	if path == "" {
		path = "/proxy.pac"
	}
	return &Server{path: path}
}

// Update replaces the served PAC content.
func (s *Server) Update(body string) {
	s.mu.Lock()
	s.body = body
	s.mu.Unlock()
}

// Start binds to addr (e.g. "127.0.0.1:8850") and serves in the
// background until Shutdown is called. It returns once the listener is
// bound.
func (s *Server) Start(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc(s.path, func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		body := s.body
		s.mu.RUnlock()
		w.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig")
		_, _ = w.Write([]byte(body))
	})
	httpServer := &http.Server{Handler: mux}
	s.httpServer = httpServer

	go func() {
		_ = httpServer.Serve(ln)
	}()
	return nil
}

// Shutdown stops serving and releases the listener, blocking until it
// does (or ctx expires). Callers that intend to Start again on the same
// address — e.g. after a config reload — must wait for Shutdown to
// return first, or the new bind can race the old listener's release.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}
