package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A route reached by a token is logged by its pattern, so that the token,
// which is a key to a payment's outcome, is not written to a log that
// outlives the payment.
func TestRecord_WritesThePatternAndNotThePathOfARouteReachedByAToken(t *testing.T) {
	t.Parallel()
	log, lines := logged()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /checkout/{token}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /refund/{token}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /payments/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	const token = "9f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"

	rec := httptest.NewRecorder()
	record(log, mux).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/checkout/"+token, nil))
	record(log, mux).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/refund/"+token, nil))
	record(log, mux).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/payments/0123", nil))

	got := lines.String()
	if strings.Contains(got, token) {
		t.Errorf("the log carries the token:\n%s", got)
	}
	for _, pattern := range []string{"/checkout/{token}", "/refund/{token}"} {
		if !strings.Contains(got, pattern) {
			t.Errorf("the log does not name %s by its pattern:\n%s", pattern, got)
		}
	}
	if !strings.Contains(got, "/payments/0123") {
		t.Errorf("a route with no token lost its path:\n%s", got)
	}
}

// Headers are what a merchant chooses, and two of them are keys: the
// credential, and the idempotency key a retry is recognised by. A line that
// outlives the request holds neither.
func TestRecord_WritesNoHeaderOfTheRequest(t *testing.T) {
	t.Parallel()
	log, lines := logged()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /payments", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) })
	const (
		key   = "8e03978e-40d5-43e8-bc93-6894a57f9324"
		token = "sk_a_credential_nobody_should_read_here"
	)

	r := httptest.NewRequest(http.MethodPost, "/payments", nil)
	r.Header.Set("Idempotency-Key", key)
	r.Header.Set("Authorization", "Bearer "+token)
	record(log, mux).ServeHTTP(httptest.NewRecorder(), r)

	got := lines.String()
	for _, secret := range []string{key, token} {
		if strings.Contains(got, secret) {
			t.Errorf("the log carries a header the merchant sent:\n%s", got)
		}
	}
	if !strings.Contains(got, "/payments") {
		t.Errorf("the log lost the route:\n%s", got)
	}
}
