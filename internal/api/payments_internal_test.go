package api

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/credential"
	"github.com/sucopay/sucopay/internal/payment"
)

// These tests serve the instance's routes over a [Payments] that records what
// reached it: which method, for which account. What a route promises is that
// what reaches Payments is the account the credential names, and only that.
// What Payments answers with is its own to show, and [payment.HTTP] shows it.

// call is one call a stub took: which method, for which account, and the
// identifier the route was given, under the name [payment.HTTP] reads it by.
type call struct {
	method  string
	account payment.AccountID
	id      string
}

// stub is a Payments that records each call and answers 200, or fails each
// call with err.
type stub struct {
	calls []call
	err   error
}

func (s *stub) Create(w http.ResponseWriter, r *http.Request, account payment.AccountID) error {
	return s.serve(w, r, "Create", account)
}

func (s *stub) Read(w http.ResponseWriter, r *http.Request, account payment.AccountID) error {
	return s.serve(w, r, "Read", account)
}

func (s *stub) serve(w http.ResponseWriter, r *http.Request, method string, account payment.AccountID) error {
	s.calls = append(s.calls, call{method, account, r.PathValue("id")})
	if s.err != nil {
		return s.err
	}
	w.WriteHeader(http.StatusOK)
	return nil
}

// ofAccount is a credential of account with access, as the store would
// hand one back.
func ofAccount(account credential.AccountID, access credential.Access) credential.Credential {
	return credential.Credential{
		ID: credential.NewID(), Scope: credential.ScopeAccount, Account: account,
		Access: access, KeyID: keyID,
	}
}

// instance is the instance as Handler builds it, holding credential c for
// whatever token is presented, and serving payments over p.
func instance(log *slog.Logger, c credential.Credential, p Payments) http.Handler {
	return Handler(log, Dependencies{Credentials: held(c), Payments: p})
}

// serve sends one request to h, presenting a token or not.
func serve(h http.Handler, method, target string, presenting bool) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(method, target, nil)
	if presenting {
		r.Header.Set("Authorization", bearing(credential.New()))
	}
	h.ServeHTTP(rec, r)
	return rec
}

func silent() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func logged() (*slog.Logger, *journal) {
	j := &journal{}
	return slog.New(slog.NewTextHandler(j, nil)), j
}

// id is what the route to one payment is given, and hands on.
var id = strings.Repeat("0", 32)

// routesServed is each route that ends in Payments, with the method of
// Payments it ends in and the identifier it hands on.
var routesServed = []struct {
	method, target, reaches, id string
}{
	{http.MethodPost, "/payments", "Create", ""},
	{http.MethodGet, "/payments/" + id, "Read", id},
}

func TestPayments_ServesEachRouteForTheAccountTheCredentialNames(t *testing.T) {
	t.Parallel()
	for _, account := range []credential.AccountID{first, other} {
		for _, route := range routesServed {
			t.Run(string(account)+" "+route.method+" "+route.target, func(t *testing.T) {
				t.Parallel()
				p := &stub{}

				rec := serve(instance(silent(), ofAccount(account, credential.ReadWrite), p), route.method, route.target, true)

				if rec.Code != http.StatusOK {
					t.Errorf("status = %d, want 200 from the stub", rec.Code)
				}
				if want := []call{{route.reaches, payment.AccountID(account), route.id}}; !slices.Equal(p.calls, want) {
					t.Errorf("reached %v, want %v", p.calls, want)
				}
			})
		}
	}
}

func TestPayments_RefusesAReadOnlyCredentialWhereItWritesAndPassesItWhereItReads(t *testing.T) {
	t.Parallel()
	p := &stub{}
	h := instance(silent(), ofAccount(first, credential.ReadOnly), p)

	if rec := serve(h, http.MethodPost, "/payments", true); rec.Code != http.StatusForbidden {
		t.Errorf("POST /payments = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if rec := serve(h, http.MethodGet, "/payments/"+id, true); rec.Code != http.StatusOK {
		t.Errorf("GET /payments/{id} = %d, want 200 from the stub", rec.Code)
	}

	if want := []call{{"Read", payment.AccountID(first), id}}; !slices.Equal(p.calls, want) {
		t.Errorf("reached %v, want %v", p.calls, want)
	}
}

func TestPayments_ReachesNothingWithoutACredential(t *testing.T) {
	t.Parallel()
	for _, route := range routesServed {
		t.Run(route.method+" "+route.target, func(t *testing.T) {
			t.Parallel()
			p := &stub{}

			rec := serve(instance(silent(), ofAccount(first, credential.ReadWrite), p), route.method, route.target, false)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
			if len(p.calls) != 0 {
				t.Errorf("reached %v, want nothing", p.calls)
			}
		})
	}
}

func TestPayments_ReportsWhatItCouldNotReachAsUnavailable(t *testing.T) {
	t.Parallel()
	for _, route := range routesServed {
		t.Run(route.method+" "+route.target, func(t *testing.T) {
			t.Parallel()
			log, j := logged()
			p := &stub{err: errors.New("closed pool")}

			rec := serve(instance(log, ofAccount(first, credential.ReadWrite), p), route.method, route.target, true)

			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("content-type = %q, want application/json", got)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"unavailable"}` {
				t.Errorf("body = %s, want the one word and nothing of the reason", got)
			}
			// The line carrying the reason is the one carrying the request's
			// identifier: the "served" line carries one as well, and shows
			// nothing about this one.
			var reasoned string
			for _, line := range strings.Split(j.String(), "\n") {
				if strings.Contains(line, "closed pool") {
					reasoned = line
				}
			}
			if reasoned == "" {
				t.Fatalf("the reason reached nobody:\n%s", j.String())
			}
			if !strings.Contains(reasoned, "request_id=") {
				t.Errorf("the reason was logged without the request's identifier: %s", reasoned)
			}
		})
	}
}

func TestPayments_AnswersUnavailableWhereNoneAreServed(t *testing.T) {
	t.Parallel()
	// An instance configured without a database has no Payments, and admit
	// finds no credential there either. The route answers for itself all the
	// same, for the day it is declared open.
	for _, route := range routesServed {
		t.Run(route.method+" "+route.target, func(t *testing.T) {
			t.Parallel()

			rec := serve(instance(silent(), ofAccount(first, credential.ReadWrite), nil), route.method, route.target, true)

			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"unavailable"}` {
				t.Errorf("body = %s, want the one word", got)
			}
		})
	}
}

func TestForAccount_AnswersARequestCarryingNoCredentialAsUnauthorized(t *testing.T) {
	t.Parallel()
	// Behind admit no such request arrives. A route declared open would let
	// one through, and this is what it meets.
	p := &stub{}
	rec := httptest.NewRecorder()

	forAccount(silent(), Payments(p), Payments.Create).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/payments", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if len(p.calls) != 0 {
		t.Errorf("reached %v, want nothing", p.calls)
	}
}
