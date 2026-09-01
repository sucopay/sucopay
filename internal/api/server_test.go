package api_test

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/api"
)

const (
	// shutdownWait is how long a test waits for Run to return. It is well
	// above the grace period the server gives in-flight requests.
	shutdownWait = 15 * time.Second
	// replyWait is how long a test waits for a response. Without it a server
	// that stops answering would hold the package until the go test deadline.
	replyWait = 10 * time.Second
)

// serving starts a server on a port the kernel chooses and stops it when the
// test ends.
func serving(t *testing.T, h http.Handler) *api.Server {
	t.Helper()
	s, err := api.Listen("127.0.0.1:0", h)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("run: %v", err)
			}
		case <-time.After(shutdownWait):
			t.Error("the server did not stop after its context was cancelled")
		}
	})
	return s
}

func get(t *testing.T, url string) *http.Response {
	t.Helper()
	client := &http.Client{Timeout: replyWait}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestListen_ReportsAPortThatIsAlreadyTaken(t *testing.T) {
	t.Parallel()
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	_, err = api.Listen(held.Addr().String(), api.Handler())

	if err == nil {
		t.Fatal("want an error, got none")
	}
	if !strings.Contains(err.Error(), held.Addr().String()) {
		t.Errorf("error does not name the address: %v", err)
	}
}

func TestServer_ServesTheHandlerItWasGiven(t *testing.T) {
	t.Parallel()
	// A handler the package does not build, so a server answering out of its
	// own routes rather than the one it was handed would fail here.
	own := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	s := serving(t, own)

	resp := get(t, "http://"+s.Addr()+"/healthz")

	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusTeapot)
	}
}

func TestListen_AddrCarriesTheChosenPort(t *testing.T) {
	t.Parallel()
	s, err := api.Listen("127.0.0.1:0", api.Handler())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = s.Close() }()

	_, port, err := net.SplitHostPort(s.Addr())

	if err != nil {
		t.Fatalf("addr %q: %v", s.Addr(), err)
	}
	if port == "0" || port == "" {
		t.Errorf("port = %q, want the port the kernel chose", port)
	}
}

func TestServer_StopsWhenItsContextIsCancelled(t *testing.T) {
	t.Parallel()
	s, err := api.Listen("127.0.0.1:0", api.Handler())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run: %v", err)
		}
	case <-time.After(shutdownWait):
		t.Fatal("the server did not stop")
	}
}

// TestServer_LetsAnInFlightRequestFinishAfterCancellation is what separates a
// shutdown from a close. A deployment cancels the context of a server that is
// still answering, and a payment request cut off mid-response leaves the caller
// unable to tell what happened.
func TestServer_LetsAnInFlightRequestFinishAfterCancellation(t *testing.T) {
	t.Parallel()
	// The handler stays busy for well past the moment the shutdown begins, so
	// that a server which closed connections instead of draining them would
	// cut this response off rather than win a race with it.
	const busy = 300 * time.Millisecond
	entered := make(chan struct{})
	slow := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		time.Sleep(busy)
		w.WriteHeader(http.StatusTeapot)
	})
	s, err := api.Listen("127.0.0.1:0", slow)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- s.Run(ctx) }()

	type reply struct {
		status int
		err    error
	}
	answered := make(chan reply, 1)
	go func() {
		client := &http.Client{Timeout: replyWait}
		resp, err := client.Get("http://" + s.Addr() + "/")
		if err != nil {
			answered <- reply{err: err}
			return
		}
		defer resp.Body.Close()
		answered <- reply{status: resp.StatusCode}
	}()

	<-entered
	cancel()

	select {
	case got := <-answered:
		if got.err != nil {
			t.Fatalf("the request begun before the shutdown did not finish: %v", got.err)
		}
		if got.status != http.StatusTeapot {
			t.Errorf("status = %d, want %d", got.status, http.StatusTeapot)
		}
	case <-time.After(replyWait):
		t.Fatal("the request begun before the shutdown never got a response")
	}

	select {
	case err := <-served:
		if err != nil {
			t.Errorf("run: %v", err)
		}
	case <-time.After(shutdownWait):
		t.Fatal("the server did not stop")
	}
}

func TestClose_ReleasesThePort(t *testing.T) {
	t.Parallel()
	s, err := api.Listen("127.0.0.1:0", api.Handler())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := s.Addr()

	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	again, err := api.Listen(addr, api.Handler())
	if err != nil {
		t.Fatalf("the port is still held after Close: %v", err)
	}
	_ = again.Close()
}
