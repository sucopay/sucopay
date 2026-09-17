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
	mux.HandleFunc("GET /payments/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	const token = "9f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"

	rec := httptest.NewRecorder()
	record(log, mux).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/checkout/"+token, nil))
	record(log, mux).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/payments/0123", nil))

	got := lines.String()
	if strings.Contains(got, token) {
		t.Errorf("the log carries the token:\n%s", got)
	}
	if !strings.Contains(got, "/checkout/{token}") {
		t.Errorf("the log does not name the route by its pattern:\n%s", got)
	}
	if !strings.Contains(got, "/payments/0123") {
		t.Errorf("a route with no token lost its path:\n%s", got)
	}
}
