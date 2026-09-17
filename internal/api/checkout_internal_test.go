package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/credential"
)

// pages is a Checkout that records which method was reached and answers
// 200, or fails each with err.
type pages struct {
	reached []string
	err     error
}

func (p *pages) Page(w http.ResponseWriter, r *http.Request) error   { return p.serve(w, "Page") }
func (p *pages) State(w http.ResponseWriter, r *http.Request) error  { return p.serve(w, "State") }
func (p *pages) Assets(w http.ResponseWriter, r *http.Request) error { return p.serve(w, "Assets") }

func (p *pages) serve(w http.ResponseWriter, method string) error {
	p.reached = append(p.reached, method)
	if p.err != nil {
		return p.err
	}
	w.WriteHeader(http.StatusOK)
	return nil
}

const aToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var pageRoutes = []struct{ target, reaches string }{
	{"/checkout/" + aToken, "Page"},
	{"/checkout/" + aToken + "/state", "State"},
	{"/checkout-assets/app.js", "Assets"},
}

// The routes are open: a token in the path is what admits a payer, and the
// handler reads it. No credential is asked for.
func TestCheckout_ServesEachRouteWithoutACredential(t *testing.T) {
	t.Parallel()
	for _, route := range pageRoutes {
		t.Run(route.target, func(t *testing.T) {
			t.Parallel()
			p := &pages{}
			h := Handler(silent(), Dependencies{Credentials: held(ofAccount(first, credential.ReadWrite)), Checkout: p})

			rec := serve(h, http.MethodGet, route.target, false)

			if rec.Code != http.StatusOK || len(p.reached) != 1 || p.reached[0] != route.reaches {
				t.Errorf("status = %d, reached %v; want 200 from %s", rec.Code, p.reached, route.reaches)
			}
		})
	}
}

func TestCheckout_AnswersUnavailableWhereNoPagesAreServedAndWhenOneCannotBe(t *testing.T) {
	t.Parallel()
	none := Handler(silent(), Dependencies{})
	if rec := serve(none, http.MethodGet, "/checkout/"+aToken, false); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("without a database: %d, want 503", rec.Code)
	}
	log, lines := logged()
	failing := Handler(log, Dependencies{Checkout: &pages{err: errors.New("the database went away")}})
	rec := serve(failing, http.MethodGet, "/checkout/"+aToken+"/state", false)
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "went away") {
		t.Errorf("a page that could not be served: %d %s, want 503 naming nothing", rec.Code, rec.Body)
	}
	if !strings.Contains(lines.String(), "went away") {
		t.Errorf("the reason is not in the log:\n%s", lines.String())
	}
}
