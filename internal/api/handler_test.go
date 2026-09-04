package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sucopay/sucopay/internal/api"
	"github.com/sucopay/sucopay/internal/credential"
)

func TestHandler_AnswersOnlyTheRoutesItServes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"health", http.MethodGet, "/healthz", http.StatusOK},
		{"readiness", http.MethodGet, "/readyz", http.StatusOK},
		{"the wrong method on readiness", http.MethodPost, "/readyz", http.StatusMethodNotAllowed},
		{"an unknown path", http.MethodGet, "/nothing-here", http.StatusNotFound},
		{"the wrong method on a known path", http.MethodPost, "/healthz", http.StatusMethodNotAllowed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()

			api.Handler(quiet(), nil, nil).ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))

			if rec.Code != c.want {
				t.Errorf("status = %d, want %d", rec.Code, c.want)
			}
		})
	}
}

func TestHandler_HealthzReportsOKAsJSON(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()

	api.Handler(quiet(), nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

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
	t.Parallel()
	rec := httptest.NewRecorder()

	api.Handler(quiet(), nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

// consulted is a credential store whose every lookup fails the test.
type consulted struct{ t *testing.T }

func (c consulted) FindByToken(context.Context, credential.Token) (credential.Credential, error) {
	c.t.Helper()
	c.t.Error("a route that asks for no credential looked one up")
	return credential.Credential{}, credential.ErrNotFound
}

func TestHandler_ServesHealthzAndReadyzWithoutConsultingCredentials(t *testing.T) {
	t.Parallel()
	// A probe carries no credential, and one that did, out of a probe's
	// configuration nobody has kept up, is not looked up either: the route
	// asks for none, and a probe is not what tries the database.
	for _, path := range []string{"/healthz", "/readyz"} {
		for _, presenting := range []bool{false, true} {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, path, nil)
			if presenting {
				r.Header.Set("Authorization", "Bearer "+string(credential.New()))
			}

			api.Handler(quiet(), nil, consulted{t}).ServeHTTP(rec, r)

			if rec.Code != http.StatusOK {
				t.Errorf("%s, presenting a token: %v: status = %d, want 200", path, presenting, rec.Code)
			}
		}
	}
}
