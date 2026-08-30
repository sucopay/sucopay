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

const shutdownWait = 5 * time.Second

func TestListen_ReportsAPortThatIsAlreadyTaken(t *testing.T) {
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

func TestServer_ServesUntilItsContextIsCancelled(t *testing.T) {
	s := serving(t, api.Handler())

	resp, err := http.Get("http://" + s.Addr() + "/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestServer_AddrCarriesTheChosenPort(t *testing.T) {
	s := serving(t, api.Handler())

	_, port, err := net.SplitHostPort(s.Addr())
	if err != nil {
		t.Fatalf("addr %q: %v", s.Addr(), err)
	}
	if port == "0" || port == "" {
		t.Errorf("port = %q, want the port the kernel chose", port)
	}
}

func TestServer_StopsWhenItsContextIsCancelled(t *testing.T) {
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
