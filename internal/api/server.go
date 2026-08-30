package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

const (
	// shutdownGrace is how long in-flight requests have to finish once a
	// shutdown begins.
	shutdownGrace = 10 * time.Second
	// readHeaderTimeout bounds a client that opens a connection and sends its
	// request line slowly.
	readHeaderTimeout = 10 * time.Second
	// readTimeout bounds a client that sends its body slowly.
	readTimeout = 30 * time.Second
	// writeTimeout bounds a response. Raise it before serving anything that
	// streams.
	writeTimeout = 30 * time.Second
	// idleTimeout bounds a kept-alive connection that sends nothing.
	idleTimeout = 2 * time.Minute
	// maxHeaderBytes is well above any header suco Pay reads and well below
	// what a client can use to occupy memory.
	maxHeaderBytes = 64 << 10
)

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
		http: &http.Server{
			Handler:           h,
			ReadHeaderTimeout: readHeaderTimeout,
			ReadTimeout:       readTimeout,
			WriteTimeout:      writeTimeout,
			IdleTimeout:       idleTimeout,
			MaxHeaderBytes:    maxHeaderBytes,
		},
		listener: l,
	}, nil
}

// Addr returns the address the server bound, which carries the chosen port
// when addr asked for port 0.
func (s *Server) Addr() string { return s.listener.Addr().String() }

// Close releases the bound port without serving. [Server.Run] closes the
// listener itself, so Close is for a caller that gives up between [Listen] and
// Run.
func (s *Server) Close() error { return s.listener.Close() }

// Run serves until ctx is cancelled, then gives in-flight requests
// shutdownGrace to finish.
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

	// Not deriving from ctx: it is already cancelled, which is what brought us
	// here, and a shutdown deadline taken from it would expire at once.
	stopping, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	if err := s.http.Shutdown(stopping); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return <-served
}
