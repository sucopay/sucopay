package webhook_test

import (
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/webhook"
)

// receiver is a merchant's server: TLS under a certificate for example.com,
// answering as told, and keeping what it received.
type receiver struct {
	server *httptest.Server
	mu     sync.Mutex
	got    []*http.Request
	bodies [][]byte
	answer func(w http.ResponseWriter, r *http.Request)
}

func listening(t *testing.T, answer func(w http.ResponseWriter, r *http.Request)) *receiver {
	t.Helper()
	rc := &receiver{answer: answer}
	rc.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rc.mu.Lock()
		rc.got = append(rc.got, r)
		rc.bodies = append(rc.bodies, body)
		rc.mu.Unlock()
		rc.answer(w, r)
	}))
	rc.server.StartTLS()
	t.Cleanup(rc.server.Close)
	return rc
}

// url is the receiver as a merchant would register it: by the name its
// certificate carries, which the test's resolver answers with the loopback
// address the server listens on.
func (rc *receiver) url(host string) string {
	u, _ := url.Parse(rc.server.URL)
	return "https://" + net.JoinHostPort(host, u.Port()) + "/in"
}

func (rc *receiver) roots() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(rc.server.Certificate())
	return pool
}

func (rc *receiver) received() []*http.Request {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.got
}

// loopback lets the check pass the address the receiver listens on, as an
// operator would allow one endpoint inside.
var loopback = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}

func sending(rc *receiver) *webhook.Sender {
	return webhook.NewSender(answering{"127.0.0.1"}, rc.roots(), time.Now)
}

func delivery() webhook.Delivery {
	return webhook.Delivery{ID: webhook.ID(strings.Repeat("d", 32)), Type: "endpoint.test",
		Body: []byte(`{"type":"endpoint.test","data":{}}`)}
}

func TestSender_DeliversASignedPostTheReceiverVerifies(t *testing.T) {
	t.Parallel()
	rc := listening(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	secret := webhook.NewSecret()
	d := delivery()

	out := sending(rc).Send(t.Context(), webhook.Endpoint{URL: rc.url("example.com")}, loopback, []webhook.Secret{secret}, d)

	if !out.Delivered() || out.Status != http.StatusNoContent || out.Reason != "" {
		t.Fatalf("outcome = %+v, want delivered on 204", out)
	}
	got := rc.received()
	if len(got) != 1 || got[0].Method != http.MethodPost || got[0].URL.Path != "/in" {
		t.Fatalf("received %d requests, want one POST /in", len(got))
	}
	r := got[0]
	if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("User-Agent") != "suco" {
		t.Errorf("Content-Type %q, User-Agent %q", r.Header.Get("Content-Type"), r.Header.Get("User-Agent"))
	}
	if r.Header.Get("Accept-Encoding") != "" {
		t.Errorf("Accept-Encoding %q was sent, want none", r.Header.Get("Accept-Encoding"))
	}
	if r.TLS == nil || r.TLS.Version < 0x0303 {
		t.Error("the request did not come over TLS 1.2 or later")
	}
	if !verify(secret, r.Header.Get("webhook-id"), r.Header.Get("webhook-timestamp"), r.Header.Get("webhook-signature"), rc.bodies[0]) {
		t.Errorf("the receiver does not verify: id %q timestamp %q signature %q",
			r.Header.Get("webhook-id"), r.Header.Get("webhook-timestamp"), r.Header.Get("webhook-signature"))
	}
	if r.Header.Get("webhook-id") != string(d.ID) {
		t.Errorf("webhook-id = %q, want the delivery's", r.Header.Get("webhook-id"))
	}
}

func TestSender_RecordsWhatTheReceiverAnsweredCutShortAndShown(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 5000) + "\u200b" + strings.Repeat("y", 100)
	rc := listening(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, long)
	})

	out := sending(rc).Send(t.Context(), webhook.Endpoint{URL: rc.url("example.com")}, loopback, []webhook.Secret{webhook.NewSecret()}, delivery())

	if out.Delivered() || out.Status != http.StatusInternalServerError {
		t.Errorf("outcome = %+v, want not delivered on 500", out)
	}
	if len(out.Response) > 256+len("...") || !strings.HasPrefix(out.Response, "xxx") || strings.Contains(out.Response, "\u200b") {
		t.Errorf("response = %d bytes %q, want the first 256 shown", len(out.Response), out.Response[:20])
	}
}

func TestSender_DoesNotFollowARedirect(t *testing.T) {
	t.Parallel()
	rc := listening(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/in" {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	out := sending(rc).Send(t.Context(), webhook.Endpoint{URL: rc.url("example.com")}, loopback, []webhook.Secret{webhook.NewSecret()}, delivery())

	if out.Delivered() || out.Status != http.StatusFound {
		t.Errorf("outcome = %+v, want the 302 itself", out)
	}
	if len(rc.received()) != 1 {
		t.Errorf("received %d requests, want the redirect left unfollowed", len(rc.received()))
	}
}

func TestSender_LeavesAReasonAndNoAddressWhereNothingAnswered(t *testing.T) {
	t.Parallel()
	rc := listening(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	secrets := []webhook.Secret{webhook.NewSecret()}
	for what, c := range map[string]struct {
		endpoint webhook.Endpoint
		allowed  []netip.Prefix
		reason   string
	}{
		"inside without an allowance":       {webhook.Endpoint{URL: rc.url("example.com")}, nil, webhook.ReasonDestination},
		"a name the certificate is not for": {webhook.Endpoint{URL: rc.url("other.example")}, loopback, webhook.ReasonConnection},
		"http":                              {webhook.Endpoint{URL: "http://example.com/in"}, loopback, webhook.ReasonDestination},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			out := sending(rc).Send(t.Context(), c.endpoint, c.allowed, secrets, delivery())

			if out.Status != 0 || out.Reason != c.reason || out.Delivered() {
				t.Errorf("outcome = %+v, want reason %s and no status", out, c.reason)
			}
			if strings.Contains(out.Response, "127.0.0.1") {
				t.Errorf("the outcome carries the address: %q", out.Response)
			}
		})
	}
	if got := rc.received(); len(got) != 0 {
		t.Errorf("the receiver was reached %d times, want never", len(got))
	}
}

func TestSender_TreatsAConnectionRefusedAsNoAnswer(t *testing.T) {
	t.Parallel()
	rc := listening(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	target := rc.url("example.com")
	roots := rc.roots()
	rc.server.Close()

	out := webhook.NewSender(answering{"127.0.0.1"}, roots, time.Now).Send(t.Context(),
		webhook.Endpoint{URL: target}, loopback, []webhook.Secret{webhook.NewSecret()}, delivery())

	if out.Status != 0 || out.Reason != webhook.ReasonConnection {
		t.Errorf("outcome = %+v, want reason connection", out)
	}
}
