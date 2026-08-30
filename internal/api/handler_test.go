package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sucopay/sucopay/internal/api"
)

func TestHandler_AnswersOnlyTheRoutesItServes(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"health", http.MethodGet, "/healthz", http.StatusOK},
		{"an unknown path", http.MethodGet, "/nothing-here", http.StatusNotFound},
		{"the wrong method on a known path", http.MethodPost, "/healthz", http.StatusMethodNotAllowed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()

			api.Handler().ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))

			if rec.Code != c.want {
				t.Errorf("status = %d, want %d", rec.Code, c.want)
			}
		})
	}
}

func TestHandler_HealthzReportsOKAsJSON(t *testing.T) {
	rec := httptest.NewRecorder()

	api.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("content-type = %q, want application/json", got)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, rec.Body.String())
	}
	if body["status"] != "ok" {
		t.Errorf("status = %q, want ok", body["status"])
	}
}

// TestHandler_TellsBrowsersNotToSniffTheContentType keeps a JSON body from
// being interpreted as something else by a browser that disagrees with the
// header.
func TestHandler_TellsBrowsersNotToSniffTheContentType(t *testing.T) {
	rec := httptest.NewRecorder()

	api.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}
