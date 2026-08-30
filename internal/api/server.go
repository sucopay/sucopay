package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// shutdownGrace is how long in-flight requests have to finish once a shutdown
// begins.
const shutdownGrace = 10 * time.Second

// Server serves an instance until its context is cancelled.
type Server struct {
	http     *http.Server
	listener net.Listener
}

// Listen binds addr and returns a Server that is not yet serving. Binding
// happens here rather than in [Server.Run] so that a port already in use is
// reported before the caller reports that the instance started.
func Listen(addr string, h http.Handler) (*Server, error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}
	return &Server{
		http:     &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second},
		listener: l,
	}, nil
}

// Addr returns the address the server bound, which carries the chosen port
// when addr asked for port 0.
func (s *Server) Addr() string { return s.listener.Addr().String() }

// Run serves until ctx is cancelled, then waits up to ten seconds for
// in-flight requests before returning.
func (s *Server) Run(ctx context.Context) error {
	served := make(chan error, 1)
	go func() {
		err := s.http.Serve(s.listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		served <- err
	}()

	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}

	stopping, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	if err := s.http.Shutdown(stopping); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return <-served
}
