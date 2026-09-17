package api

import (
	"net/http"
	"slices"
	"testing"

	"github.com/sucopay/sucopay/internal/credential"
	"github.com/sucopay/sucopay/internal/payment"
)

// hooks is a Webhooks that records each call and answers 200, as stub is a
// Payments.
type hooks struct{ stub }

func (h *hooks) List(w http.ResponseWriter, r *http.Request, account payment.AccountID) error {
	return h.serve(w, r, "List", account)
}

func (h *hooks) Update(w http.ResponseWriter, r *http.Request, account payment.AccountID) error {
	return h.serve(w, r, "Update", account)
}

func (h *hooks) Rotate(w http.ResponseWriter, r *http.Request, account payment.AccountID) error {
	return h.serve(w, r, "Rotate", account)
}

func (h *hooks) Delete(w http.ResponseWriter, r *http.Request, account payment.AccountID) error {
	return h.serve(w, r, "Delete", account)
}

func (h *hooks) Test(w http.ResponseWriter, r *http.Request, account payment.AccountID) error {
	return h.serve(w, r, "Test", account)
}

func (h *hooks) Deliveries(w http.ResponseWriter, r *http.Request, account payment.AccountID) error {
	return h.serve(w, r, "Deliveries", account)
}

// hookRoutes is each route that ends in Webhooks, with the method it ends
// in, the identifier it hands on, and whether it writes.
var hookRoutes = []struct {
	method, target, reaches, id string
	writes                      bool
}{
	{http.MethodPost, "/webhook_endpoints", "Create", "", true},
	{http.MethodGet, "/webhook_endpoints", "List", "", false},
	{http.MethodGet, "/webhook_endpoints/" + id, "Read", id, false},
	{http.MethodPatch, "/webhook_endpoints/" + id, "Update", id, true},
	{http.MethodPost, "/webhook_endpoints/" + id + "/secret", "Rotate", id, true},
	{http.MethodDelete, "/webhook_endpoints/" + id, "Delete", id, true},
	{http.MethodPost, "/webhook_endpoints/" + id + "/test", "Test", id, true},
	{http.MethodGet, "/webhook_endpoints/" + id + "/deliveries", "Deliveries", id, false},
}

func TestWebhooks_ServesEachRouteForTheAccountTheCredentialNames(t *testing.T) {
	t.Parallel()
	for _, account := range []credential.AccountID{first, other} {
		for _, route := range hookRoutes {
			t.Run(string(account)+" "+route.method+" "+route.target, func(t *testing.T) {
				t.Parallel()
				h := &hooks{}
				deps := Dependencies{Credentials: held(ofAccount(account, credential.ReadWrite)), Webhooks: h}

				rec := serve(Handler(silent(), deps), route.method, route.target, true)

				if rec.Code != http.StatusOK {
					t.Errorf("status = %d, want 200 from the stub", rec.Code)
				}
				if want := []call{{route.reaches, payment.AccountID(account), route.id}}; !slices.Equal(h.calls, want) {
					t.Errorf("reached %v, want %v", h.calls, want)
				}
			})
		}
	}
}

func TestWebhooks_RefusesAReadOnlyCredentialWhereItWrites(t *testing.T) {
	t.Parallel()
	h := &hooks{}
	deps := Dependencies{Credentials: held(ofAccount(first, credential.ReadOnly)), Webhooks: h}
	handler := Handler(silent(), deps)
	for _, route := range hookRoutes {
		want := http.StatusOK
		if route.writes {
			want = http.StatusForbidden
		}
		if rec := serve(handler, route.method, route.target, true); rec.Code != want {
			t.Errorf("%s %s = %d, want %d", route.method, route.target, rec.Code, want)
		}
	}
}

func TestWebhooks_AnswersUnavailableWhereNoneAreServed(t *testing.T) {
	t.Parallel()
	deps := Dependencies{Credentials: held(ofAccount(first, credential.ReadWrite))}
	for _, route := range hookRoutes {
		if rec := serve(Handler(silent(), deps), route.method, route.target, true); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want %d", route.method, route.target, rec.Code, http.StatusServiceUnavailable)
		}
	}
}
