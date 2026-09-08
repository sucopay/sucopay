package evm

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/adapter/chain"
)

// answering is a server that replies to every call with body, under status.
func answering(t *testing.T, status int, body string, header map[string]string) *client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for name, value := range header {
			w.Header().Set(name, value)
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return opened(t, server.URL)
}

// opened returns a client on an endpoint, for the tests that have one.
func opened(t *testing.T, endpoint string) *client {
	t.Helper()
	c, err := newClient(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCall_SendsWhatJSONRPCExpectsAndReadsTheResult(t *testing.T) {
	t.Parallel()
	var got struct {
		method    string
		params    []any
		version   string
		agent     string
		mediaType string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			JSONRPC string `json:"jsonrpc"`
			Method  string `json:"method"`
			Params  []any  `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		got.method, got.params, got.version = body.Method, body.Params, body.JSONRPC
		got.agent, got.mediaType = r.Header.Get("user-agent"), r.Header.Get("content-type")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x89"}`)
	}))
	t.Cleanup(server.Close)

	var chainID quantity
	if err := opened(t, server.URL).call(t.Context(), "eth_chainId", []any{"latest", true}, &chainID); err != nil {
		t.Fatal(err)
	}

	if chainID != 0x89 {
		t.Errorf("the result read as %d, want %d", chainID, 0x89)
	}
	if got.method != "eth_chainId" || len(got.params) != 2 {
		t.Errorf("the server was asked %q with %v", got.method, got.params)
	}
	if got.version != "2.0" {
		t.Errorf("the request says jsonrpc %q", got.version)
	}
	if !strings.Contains(got.agent, "suco") {
		t.Errorf("the request says it is %q", got.agent)
	}
	if got.mediaType != "application/json" {
		t.Errorf("the request is %q", got.mediaType)
	}
}

func TestCall_ReadsTheErrorAProviderSendsWhateverTheStatusIs(t *testing.T) {
	t.Parallel()
	const body = `{"jsonrpc":"2.0","id":1,"error":{"code":-32701,"message":"query returned more than 10000 results"}}`
	cases := []struct {
		name    string
		status  int
		body    string
		wide    bool
		limited bool
	}{
		{"an error under 200", http.StatusOK, body, true, false},
		{"an error under 400", http.StatusBadRequest, body, true, false},
		{"a bare 400", http.StatusBadRequest, "", true, false},
		{"an error under 429", http.StatusTooManyRequests, body, false, true},
		{"a bare 429", http.StatusTooManyRequests, "", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var result quantity
			err := answering(t, c.status, c.body, nil).call(t.Context(), "eth_getLogs", nil, &result)

			if err == nil {
				t.Fatal("the call went through")
			}
			if wide := errors.Is(err, chain.ErrTooWide); wide != c.wide {
				t.Errorf("too wide = %v, want %v: %v", wide, c.wide, err)
			}
			var limited chain.RateLimited
			if errors.As(err, &limited) != c.limited {
				t.Errorf("rate limited = %v, want %v: %v", !c.limited, c.limited, err)
			}
			if c.body != "" && !strings.Contains(err.Error(), "10000 results") {
				t.Errorf("the error does not carry what the provider said: %v", err)
			}
			if c.body != "" && !strings.Contains(err.Error(), "-32701") {
				t.Errorf("the error does not carry the code: %v", err)
			}
		})
	}
}

func TestCall_ReadsRetryAfterOnlyWhenItIsAWholeNumberOfSeconds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		header string
		want   time.Duration
	}{
		{"7", 7 * time.Second},
		{"900", 900 * time.Second},
		{"Wed, 21 Oct 2026 07:28:00 GMT", 0},
		{"", 0},
		{"-1", 0},
	}
	for _, c := range cases {
		t.Run("Retry-After: "+c.header, func(t *testing.T) {
			var result quantity
			err := answering(t, http.StatusTooManyRequests, "", map[string]string{"Retry-After": c.header}).
				call(t.Context(), "eth_chainId", nil, &result)

			var limited chain.RateLimited
			if !errors.As(err, &limited) {
				t.Fatalf("call gave %v, want a RateLimited", err)
			}
			if limited.RetryAfter != c.want {
				t.Errorf("RetryAfter = %s, want %s", limited.RetryAfter, c.want)
			}
		})
	}
}

func TestCall_RefusesAnAnswerHoldingMoreThanItAskedFor(t *testing.T) {
	t.Parallel()
	list := func(n int) string {
		items := make([]string, n)
		for i := range items {
			items[i] = `{"address":"0x0000000000000000000000000000000000000001"}`
		}
		return `{"jsonrpc":"2.0","id":1,"result":[` + strings.Join(items, ",") + `]}`
	}
	var held []json.RawMessage

	if err := answering(t, http.StatusOK, list(maxItems), nil).
		call(t.Context(), "eth_getLogs", nil, &held); err != nil {
		t.Fatalf("an answer of %d items was refused: %v", maxItems, err)
	}
	if len(held) != maxItems {
		t.Errorf("read %d items, want %d", len(held), maxItems)
	}

	err := answering(t, http.StatusOK, list(maxItems+1), nil).
		call(t.Context(), "eth_getLogs", nil, &held)

	if !errors.Is(err, chain.ErrTooWide) {
		t.Errorf("an answer of %d items gave %v, want %v", maxItems+1, err, chain.ErrTooWide)
	}
}

func TestCall_StopsReadingAtTheLimitAndSaysSo(t *testing.T) {
	t.Parallel()
	const (
		limit  = 1 << 10
		block  = 4 << 10
		blocks = 1 << 11
	)
	cases := []struct {
		name  string
		write func(t *testing.T, w http.ResponseWriter, wrote *atomic.Int64)
		// stops says the server could not write everything, which is what a
		// reader that stopped leaves behind. A compressed body is small on the
		// wire whatever it swells to, so only the plain one shows this.
		stops bool
	}{
		{"a body longer than the limit", func(t *testing.T, w http.ResponseWriter, wrote *atomic.Int64) {
			t.Helper()
			filling := strings.Repeat("a", block)
			for range blocks {
				n, err := fmt.Fprint(w, filling)
				wrote.Add(int64(n))
				if err != nil {
					return
				}
			}
		}, true},
		{"a small body that swells past it", func(t *testing.T, w http.ResponseWriter, wrote *atomic.Int64) {
			t.Helper()
			w.Header().Set("content-encoding", "gzip")
			zip := gzip.NewWriter(w)
			filling := strings.Repeat("a", block)
			for range blocks {
				n, err := zip.Write([]byte(filling))
				wrote.Add(int64(n))
				if err != nil {
					return
				}
			}
			_ = zip.Close()
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var wrote atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				c.write(t, w, &wrote)
			}))
			t.Cleanup(server.Close)
			reader := opened(t, server.URL)
			reader.maxBody = limit

			var result quantity
			err := reader.call(t.Context(), "eth_chainId", nil, &result)

			if err == nil {
				t.Fatal("a body past the limit was read whole")
			}
			// The bound is what refused it. A reader without one would have
			// read the whole body and failed on it not being JSON.
			if !strings.Contains(err.Error(), fmt.Sprintf("longer than %d bytes", limit)) {
				t.Errorf("the call failed on something else: %v", err)
			}
			if written := wrote.Load(); c.stops && written >= block*blocks {
				t.Errorf("the server wrote all %d bytes, so the reader did not stop", written)
			}
		})
	}
}

func TestCall_GivesUpOnAServerThatDoesNotAnswer(t *testing.T) {
	t.Parallel()
	// Long enough that a call which waited for it would be plainly over its
	// deadline, and short enough that the test does not.
	const slower = 2 * time.Second
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(slower)
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x1"}`)
	}))
	t.Cleanup(server.Close)
	slow := opened(t, server.URL)
	slow.timeout = 50 * time.Millisecond

	var result quantity
	started := time.Now()
	err := slow.call(t.Context(), "eth_chainId", nil, &result)

	if err == nil {
		t.Fatal("a server that answered late was waited for")
	}
	if waited := time.Since(started); waited >= slower {
		t.Errorf("the call waited %s, and the deadline was %s", waited, slow.timeout)
	}
}

func TestCall_KeepsTheEndpointOutOfWhatItReports(t *testing.T) {
	t.Parallel()
	const secret = "6ea1e04c5f2c4d8e9f0a1b2c3d4e5f60"
	answers := map[string]string{
		"a body that is not JSON": "{",
		"a message written around the endpoint": `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,` +
			`"message":"upstream ENDPOINT/v1/KEY refused the request"}}`,
	}
	for name, body := range answers {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, body)
			}))
			t.Cleanup(server.Close)
			endpoint := server.URL + "/v1/" + secret + "?key=" + secret
			c := opened(t, endpoint)
			// The provider writes the endpoint back into its message, which is
			// what a caller would otherwise log.
			if strings.Contains(body, "ENDPOINT") {
				server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					fmt.Fprint(w, strings.ReplaceAll(strings.ReplaceAll(body,
						"ENDPOINT", server.URL), "KEY", secret))
				})
			}

			var result quantity
			err := c.call(t.Context(), "eth_chainId", nil, &result)

			if err == nil {
				t.Fatal("the call went through")
			}
			for _, held := range []string{secret, server.Listener.Addr().String()} {
				if strings.Contains(err.Error(), held) {
					t.Errorf("the error carries %q: %v", held, err)
				}
			}
		})
	}

	t.Run("a message long enough to be cut through the endpoint", func(t *testing.T) {
		// The key sits so that a message cut before it was read would leave
		// the first half of the key in what comes out.
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		t.Cleanup(server.Close)
		endpoint := server.URL + "/v1/" + secret
		padding := strings.Repeat("z", maxMessage-len(server.URL)-len("/v1/")-len(secret)/2)
		server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":%q}}`,
				padding+endpoint+" refused the request")
		})

		var result quantity
		err := opened(t, endpoint).call(t.Context(), "eth_chainId", nil, &result)

		if err == nil {
			t.Fatal("the call went through")
		}
		for _, held := range []string{secret, secret[:len(secret)/2], server.Listener.Addr().String()} {
			if strings.Contains(err.Error(), held) {
				t.Errorf("the error carries %q: %v", held, err)
			}
		}
	})

	t.Run("a server that is not there", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		endpoint := server.URL + "/v1/" + secret
		address := server.Listener.Addr().String()
		server.Close()
		c := opened(t, endpoint)

		var result quantity
		err := c.call(t.Context(), "eth_chainId", nil, &result)

		if err == nil {
			t.Fatal("the call reached a server that is not there")
		}
		for _, held := range []string{secret, address} {
			if strings.Contains(err.Error(), held) {
				t.Errorf("the error carries %q: %v", held, err)
			}
		}
		// The transport writes what it was reaching into its own error, as
		// `Post "https://...": dial tcp ...`. What comes out is the failure
		// alone, without the request that carried the key.
		if strings.Contains(err.Error(), "Post ") {
			t.Errorf("the error carries the request the transport made: %v", err)
		}
	})
}

func TestNewClient_TakesOnlyAnEndpointNobodyOnTheWayCanRead(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		endpoint string
		want     bool
	}{
		{"https anywhere", "https://polygon.example/v1", true},
		{"http on this machine", "http://127.0.0.1:8545", true},
		{"http on localhost", "http://localhost:8545", true},
		{"http anywhere else", "http://polygon.example/v1", false},
		{"a scheme nothing speaks", "ws://polygon.example", false},
		{"a port where a host goes", "https://:8545/v1", false},
		{"what is not a URL", "https://polygon.example:port", false},
		{"nothing at all", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := newClient(c.endpoint)

			if took := err == nil; took != c.want {
				t.Errorf("newClient took it = %v, want %v: %v", took, c.want, err)
			}
			if err != nil && strings.Contains(err.Error(), c.endpoint) && c.endpoint != "" {
				t.Errorf("the refusal repeats the endpoint: %v", err)
			}
		})
	}
}

func TestCall_RefusesACertificateNobodyIssued(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x1"}`)
	}))
	t.Cleanup(server.Close)

	var result quantity
	err := opened(t, server.URL).call(t.Context(), "eth_chainId", nil, &result)

	if err == nil {
		t.Fatal("a certificate nobody issued was accepted")
	}
}
