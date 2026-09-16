package webhook

import (
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

type loopbackResolver struct{}

func (loopbackResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

func TestSender_GivesUpOnAReceiverThatDoesNotAnswerInTime(t *testing.T) {
	t.Parallel()
	// The receiver holds every request until the test is over. Released
	// before the server is closed, since closing waits for its handlers.
	done := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-done }))
	server.StartTLS()
	defer server.Close()
	defer close(done)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	u, _ := url.Parse(server.URL)
	s := NewSender(loopbackResolver{}, roots, time.Now)
	s.timeout = 200 * time.Millisecond

	out := s.Send(t.Context(), Endpoint{URL: "https://example.com:" + u.Port() + "/in"},
		[]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, []Secret{NewSecret()},
		Delivery{ID: ID(strings.Repeat("d", 32)), Body: []byte("{}")})

	if out.Status != 0 || out.Reason != ReasonTimeout {
		t.Errorf("outcome = %+v, want reason timeout", out)
	}
	if out.Took < s.timeout || out.Took > 5*time.Second {
		t.Errorf("took %v, want about the timeout", out.Took)
	}
}
