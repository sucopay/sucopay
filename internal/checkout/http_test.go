package checkout_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sucopay/sucopay/internal/checkout"
	"github.com/sucopay/sucopay/internal/payment"
)

// reading is a deployment observing polygon.
type reading map[string]string

func (r reading) Words() (map[string]string, map[string]string) { return r, nil }

func serving(t *testing.T, pool *pgxpool.Pool) *checkout.HTTP {
	t.Helper()
	return checkout.NewHTTP(checkout.Deps{
		Store: checkout.NewPostgres(pool), Payments: payment.NewPostgres(pool),
		Key: key, KeyID: "k1", ChainIDs: map[string]uint64{"polygon": 137},
		Chains: reading{"polygon": "observing"}, Watched: watching{},
		Now: func() time.Time { return now },
	})
}

// answered is the answer of method to a request for token.
func answered(t *testing.T, method func(http.ResponseWriter, *http.Request) error, token string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/checkout/"+token, nil)
	r.SetPathValue("token", token)
	if err := method(rec, r); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestHTTP_AnswersAnUnknownAnExpiredAndAMalformedTokenAlike(t *testing.T) {
	t.Parallel()
	pool := opened(t)
	h := serving(t, pool)
	p := paid(t, pool, first, "")
	if _, err := pool.Exec(t.Context(), `update payments set status = 'expired', closed_at = $2 where id = $1`, p.ID(), now.Add(-checkout.PageLife-time.Hour)); err != nil {
		t.Fatal(err)
	}
	// A row whose hash somebody wrote by hand, for a token the deployment
	// never derived: found by the hash, and refused by the derivation.
	forged := paid(t, pool, first, "")
	planted := checkout.Derive([32]byte{8}, forged.ID())
	if _, err := pool.Exec(t.Context(), `update payments set checkout_hash = $2 where id = $1`, forged.ID(), checkout.Hash(planted)); err != nil {
		t.Fatal(err)
	}
	for what, token := range map[string]string{
		"unknown":   string(checkout.Derive([32]byte{7}, p.ID())),
		"expired":   string(checkout.Derive(key, p.ID())),
		"malformed": "not-a-token",
		"the id":    string(p.ID()),
		"planted":   string(planted),
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			state := answered(t, h.State, token)
			if state.Code != http.StatusNotFound || strings.TrimSpace(state.Body.String()) != `{"error":"not_found"}` {
				t.Errorf("state = %d %s, want the one 404", state.Code, state.Body)
			}
			page := answered(t, h.Page, token)
			if page.Code != http.StatusNotFound || !strings.Contains(page.Header().Get("Content-Type"), "text/html") ||
				!strings.Contains(page.Body.String(), "Nothing to pay here") {
				t.Errorf("page = %d %s, want an HTML 404", page.Code, page.Header().Get("Content-Type"))
			}
			for _, rec := range []*httptest.ResponseRecorder{state, page} {
				if rec.Header().Get("Referrer-Policy") != "no-referrer" || rec.Header().Get("Content-Security-Policy") == "" {
					t.Errorf("a 404 lacks the page's headers: %v", rec.Header())
				}
			}
		})
	}
}

func TestHTTP_StateShowsWhatThePageNeedsAndNothingOfTheMetadata(t *testing.T) {
	t.Parallel()
	pool := opened(t)
	h := serving(t, pool)
	p := paid(t, pool, first, "https://shop.example/orders/42")

	rec := answered(t, h.State, string(checkout.Derive(key, p.ID())))

	if rec.Code != http.StatusOK {
		t.Fatalf("state = %d %s", rec.Code, rec.Body)
	}
	for name, want := range map[string]string{
		"Content-Security-Policy": "frame-ancestors 'none'",
		"Referrer-Policy":         "no-referrer",
		"X-Content-Type-Options":  "nosniff",
		"Cache-Control":           "no-store",
	} {
		if got := rec.Header().Get(name); !strings.Contains(got, want) {
			t.Errorf("%s = %q, want it to carry %q", name, got, want)
		}
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("the answer carries a CORS header")
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "awaiting_payment" || body["amount"] != "1000" || body["return_url"] != "https://shop.example/orders/42" {
		t.Errorf("state = %v, want awaiting_payment, 1000 and the return URL", body)
	}
	asset, _ := body["asset"].(map[string]any)
	if asset["chain_id"] != float64(137) || asset["symbol"] != "JPYC" {
		t.Errorf("asset = %v, want JPYC on chain 137", asset)
	}
	merchant, _ := body["merchant"].(map[string]any)
	if merchant["name"] != "first" && merchant["name"] == "" {
		t.Errorf("merchant = %v, want the account's name", merchant)
	}
	for _, absent := range []string{"metadata", "reference", "destination", "id"} {
		if _, has := body[absent]; has {
			t.Errorf("state carries %s", absent)
		}
	}
	if strings.Contains(rec.Body.String(), "A-1") || strings.Contains(rec.Body.String(), "0xabab") {
		t.Errorf("state carries the metadata or the destination:\n%s", rec.Body)
	}
	if body["reason"] != nil || body["attempt"] != nil || body["result"] != nil || body["slower"] != false {
		t.Errorf("state = %v, want no reason, no attempt, no result, not slower", body)
	}
}

func TestHTTP_PageShowsTheStateAndSaysNothingCanBePaidWithoutAssets(t *testing.T) {
	t.Parallel()
	pool := opened(t)
	h := serving(t, pool)
	p := paid(t, pool, first, "https://shop.example/orders/42")

	rec := answered(t, h.Page, string(checkout.Derive(key, p.ID())))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("page = %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	html := rec.Body.String()
	for _, want := range []string{"1000 JPYC", "awaiting_payment", "https://shop.example/orders/42", "serves no payment page"} {
		if !strings.Contains(html, want) {
			t.Errorf("page lacks %q:\n%s", want, html)
		}
	}
	for _, absent := range []string{"A-1", "0xabab", "app.js"} {
		if strings.Contains(html, absent) {
			t.Errorf("page carries %q", absent)
		}
	}
}

// With the deployment's assets, the page is a shell the script fills, and
// the assets are served under their own path as what they are.
func TestHTTP_PageLoadsTheAssetsWhenTheDeploymentHasThem(t *testing.T) {
	t.Parallel()
	pool := opened(t)
	deps := checkout.Deps{
		Store: checkout.NewPostgres(pool), Payments: payment.NewPostgres(pool),
		Key: key, KeyID: "k1", ChainIDs: map[string]uint64{"polygon": 137},
		Chains: reading{"polygon": "observing"}, Watched: watching{},
		Assets: fstest.MapFS{"app.js": {Data: []byte("console.log(1)")}, "app.css": {Data: []byte("main{}")}},
		Now:    func() time.Time { return now },
	}
	h := checkout.NewHTTP(deps)
	p := paid(t, pool, first, "")

	page := answered(t, h.Page, string(checkout.Derive(key, p.ID())))

	html := page.Body.String()
	if !strings.Contains(html, `src="/checkout-assets/app.js"`) || !strings.Contains(html, `href="/checkout-assets/app.css"`) {
		t.Errorf("page does not load the assets:\n%s", html)
	}
	if strings.Contains(html, "serves no payment page") {
		t.Error("page says it serves no payment page")
	}
	rec := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/checkout-assets/app.js", nil)
	if err := h.Assets(rec, r); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "console.log(1)" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("app.js = %d %q %v, want the file as it is, nosniff", rec.Code, rec.Body.String(), rec.Header())
	}
	if rec.Header().Get("Cache-Control") == "no-store" {
		t.Error("an asset is told never to be cached")
	}
	r = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/checkout-assets/../page.html", nil)
	rec = httptest.NewRecorder()
	_ = h.Assets(rec, r)
	if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "doctype") {
		t.Error("a path climbing out of the assets was served")
	}
	for _, dir := range []string{"/checkout-assets/", "/checkout-assets/sub/"} {
		rec = httptest.NewRecorder()
		_ = h.Assets(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, dir, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404 rather than a listing", dir, rec.Code)
		}
	}
}
