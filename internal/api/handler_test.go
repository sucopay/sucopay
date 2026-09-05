package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// consulted is a credential store holding nothing, whose every call about a
// token fails the test.
type consulted struct{ t *testing.T }

func (c consulted) FindByToken(context.Context, credential.Token) (credential.Credential, error) {
	c.t.Helper()
	c.t.Error("a route that asks for no credential looked one up")
	return credential.Credential{}, credential.ErrNotFound
}

func (c consulted) RecordUse(context.Context, credential.Credential, time.Time) error {
	c.t.Helper()
	c.t.Error("a route that asks for no credential recorded a use of one")
	return nil
}

func (consulted) InForce(context.Context) (credential.InForce, error) {
	return credential.NoneInForce, nil
}

func TestHandler_LooksUpNoTokenAProbePresents(t *testing.T) {
	t.Parallel()
	// A probe carries no credential, and one that did, out of a probe's
	// configuration nobody has kept up, is not looked up either: the route
	// asks for none, and a probe is not what tries the database. With a
	// database, /readyz asks the store what is in force, which is a question
	// about no token.
	reachable := func(context.Context) error { return nil }
	for _, path := range []string{"/healthz", "/readyz"} {
		for _, database := range []api.Ready{nil, reachable} {
			for _, presenting := range []bool{false, true} {
				rec := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodGet, path, nil)
				if presenting {
					r.Header.Set("Authorization", "Bearer "+string(credential.New()))
				}

				api.Handler(quiet(), database, consulted{t}).ServeHTTP(rec, r)

				if rec.Code != http.StatusOK {
					t.Errorf("%s, presenting a token: %v, with a database: %v: status = %d, want 200",
						path, presenting, database != nil, rec.Code)
				}
			}
		}
	}
}

// inForce is a credential store holding nothing to look up, and answering
// what it is told is in force.
type inForce struct {
	what credential.InForce
	err  error
}

func (inForce) FindByToken(context.Context, credential.Token) (credential.Credential, error) {
	return credential.Credential{}, credential.ErrNotFound
}

func (inForce) RecordUse(context.Context, credential.Credential, time.Time) error { return nil }

func (s inForce) InForce(context.Context) (credential.InForce, error) { return s.what, s.err }

func TestReadyz_SaysWhatCredentialsAreInForceAndIsReadyEitherWay(t *testing.T) {
	t.Parallel()
	// A deployment with no credential that writes is one nobody can create
	// a payment in, which every other measure of health calls well. It is
	// still ready: were it not, there would be no reaching it to make one.
	reachable := func(context.Context) error { return nil }
	for _, what := range []credential.InForce{credential.NoneInForce, credential.ReadOnlyInForce, credential.ReadWriteInForce} {
		rec := httptest.NewRecorder()

		api.Handler(quiet(), reachable, inForce{what: what}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

		if rec.Code != http.StatusOK {
			t.Errorf("%s in force: /readyz = %d, want %d", what, rec.Code, http.StatusOK)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("body is not JSON: %v (%q)", err, rec.Body.String())
		}
		if body["credentials"] != string(what) {
			t.Errorf("%s in force: credentials = %q, want %q", what, body["credentials"], what)
		}
	}
}

func TestReadyz_SaysNothingOfCredentialsWhereNoStoreWasBuilt(t *testing.T) {
	t.Parallel()
	// A database and no store over it is a pairing only a test makes, and
	// what it asks of the database it still answers for.
	reachable := func(context.Context) error { return nil }
	rec := httptest.NewRecorder()

	api.Handler(quiet(), reachable, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("/readyz = %d, want %d", rec.Code, http.StatusOK)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, rec.Body.String())
	}
	if body["database"] != "reachable" {
		t.Errorf("database = %q, want %q", body["database"], "reachable")
	}
	if _, said := body["credentials"]; said {
		t.Errorf("body = %s, want nothing said of credentials it has no store to ask", rec.Body)
	}
}

func TestReadyz_IsNotReadyWhenTheStoreCannotBeRead(t *testing.T) {
	t.Parallel()
	// A database that answers a ping and not a query over its own table is
	// not one a request can be authenticated against.
	log := &recorder{}
	reachable := func(context.Context) error { return nil }
	rec := httptest.NewRecorder()

	api.Handler(log.logger(), reachable, inForce{err: errors.New("relation credentials does not exist")}).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/readyz = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if strings.Contains(rec.Body.String(), "relation") {
		t.Errorf("the reason was answered over HTTP: %s", rec.Body)
	}
	if !strings.Contains(log.String(), "relation credentials does not exist") {
		t.Errorf("the reason reached nobody:\n%s", log.String())
	}
}
