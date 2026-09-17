package checkout_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// attempted is the answer to a request for an attempt with body.
func attempted(t *testing.T, h *checkout.HTTP, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/checkout/"+token+"/attempts", strings.NewReader(body))
	r.SetPathValue("token", token)
	if err := h.Attempt(rec, r); err != nil {
		t.Fatal(err)
	}
	return rec
}

func servingWith(t *testing.T, pool *pgxpool.Pool, clock func() time.Time, watched checkout.Watched) *checkout.HTTP {
	t.Helper()
	store := payment.NewPostgres(pool)
	return checkout.NewHTTP(checkout.Deps{
		Store: checkout.NewPostgres(pool), Payments: store,
		Service: payment.NewService(store, store, watched, clock),
		Key:     key, KeyID: "k1", ChainIDs: map[string]uint64{"polygon": 137},
		Chains: reading{"polygon": "observing"}, Watched: watched,
		Domain: func(payment.Asset) (string, string) { return "JPY Coin", "1" },
		Now:    clock,
	})
}

type unwatched struct{}

func (unwatched) Has(context.Context, payment.Network) (bool, error) { return false, nil }

func TestHTTP_AttemptIssuesOnceMovesThePaymentAndAnswersTheSameAttemptAgain(t *testing.T) {
	t.Parallel()
	pool := opened(t)
	h := servingWith(t, pool, func() time.Time { return now }, watching{})
	p := paid(t, pool, first, "")
	token := string(checkout.Derive(key, p.ID()))

	made := attempted(t, h, token, "")

	if made.Code != http.StatusCreated {
		t.Fatalf("first = %d %s, want 201", made.Code, made.Body)
	}
	var typed checkout.TypedData
	if err := json.Unmarshal(made.Body.Bytes(), &typed); err != nil {
		t.Fatal(err)
	}
	if typed.Domain.Name != "JPY Coin" || typed.Domain.ChainID != 137 || typed.Message.To != p.Destination() ||
		typed.Message.ValidBefore != strconv.FormatInt(p.ExpiresAt().Unix(), 10) || !strings.HasPrefix(typed.Message.Nonce, "0x") {
		t.Errorf("typed = %+v, want the payment's authorisation under the asset's domain", typed)
	}
	moved, _, err := payment.NewPostgres(pool).Find(t.Context(), first, p.ID())
	if err != nil || moved.Status() != payment.AwaitingPayment {
		t.Errorf("the payment is %s, %v; want awaiting_payment", moved.Status(), err)
	}
	again := attempted(t, h, token, "")
	if again.Code != http.StatusOK || !strings.Contains(again.Body.String(), typed.Message.Nonce) {
		t.Errorf("again = %d %s, want 200 with the same nonce", again.Code, again.Body)
	}
	state := answered(t, h.State, token)
	if !strings.Contains(state.Body.String(), typed.Message.Nonce) {
		t.Errorf("state lacks the live attempt:\n%s", state.Body)
	}
}

func TestHTTP_AttemptRefusesWithAReasonAndLeavesThePaymentAsItWas(t *testing.T) {
	t.Parallel()
	pool := opened(t)
	for what, c := range map[string]struct {
		clock   time.Time
		watched checkout.Watched
		want    string
	}{
		"expired":   {now.Add(20 * time.Minute), watching{}, checkout.ReasonExpired},
		"closing":   {now.Add(14 * time.Minute), watching{}, checkout.ReasonClosing},
		"not ready": {now, unwatched{}, checkout.ReasonNotReady},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			h := servingWith(t, pool, func() time.Time { return c.clock }, c.watched)
			p := paid(t, pool, first, "")

			rec := attempted(t, h, string(checkout.Derive(key, p.ID())), "")

			if rec.Code != http.StatusConflict || strings.TrimSpace(rec.Body.String()) != `{"error":"`+c.want+`"}` {
				t.Errorf("answer = %d %s, want 409 %s", rec.Code, rec.Body, c.want)
			}
			kept, _, err := payment.NewPostgres(pool).Find(t.Context(), first, p.ID())
			if err != nil || kept.Status() != payment.Created {
				t.Errorf("the payment is %s, %v; want created still", kept.Status(), err)
			}
		})
	}
}

func TestHTTP_AttemptReissuesOnceAtThePagesWordAndRefusesTheRest(t *testing.T) {
	t.Parallel()
	pool := opened(t)
	h := servingWith(t, pool, func() time.Time { return now }, watching{})
	p := paid(t, pool, first, "")
	token := string(checkout.Derive(key, p.ID()))
	var issued checkout.TypedData
	if err := json.Unmarshal(attempted(t, h, token, "").Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}

	for what, c := range map[string]struct {
		body string
		code int
	}{
		"not JSON":              {"{", http.StatusBadRequest},
		"an unknown key":        {`{"resend": "x"}`, http.StatusBadRequest},
		"not an id":             {`{"reissue": "nope"}`, http.StatusBadRequest},
		"another attempt's id":  {`{"reissue": "` + strings.Repeat("f", 32) + `"}`, http.StatusBadRequest},
		"a second JSON value":   {`{"reissue": "` + string(issued.ID) + `"} {}`, http.StatusBadRequest},
		"an empty object":       {`{}`, http.StatusOK},
		"the live attempt's id": {`{"reissue": "` + string(issued.ID) + `"}`, http.StatusCreated},
	} {
		t.Run(what, func(t *testing.T) {
			rec := attempted(t, h, token, c.body)
			if rec.Code != c.code {
				t.Errorf("%s = %d %s, want %d", what, rec.Code, rec.Body, c.code)
			}
		})
	}
	var reissued checkout.TypedData
	if err := json.Unmarshal(attempted(t, h, token, "").Body.Bytes(), &reissued); err != nil {
		t.Fatal(err)
	}
	if reissued.ID == issued.ID || reissued.Message.Nonce == issued.Message.Nonce {
		t.Error("the reissue gave the same attempt back")
	}
	if rec := attempted(t, h, token, `{"reissue": "`+string(reissued.ID)+`"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), checkout.ReasonReissued) {
		t.Errorf("a second reissue = %d %s, want 409 reissued", rec.Code, rec.Body)
	}
	state := answered(t, h.State, token)
	if !strings.Contains(state.Body.String(), `"reason":"reissued"`) || !strings.Contains(state.Body.String(), reissued.Message.Nonce) {
		t.Errorf("state after the reissue was used:\n%s", state.Body)
	}
}
