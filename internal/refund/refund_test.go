package refund_test

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
	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
	"github.com/sucopay/sucopay/internal/refund"
)

const (
	first = payment.AccountID("00000000-0000-0000-0000-000000000001")
	other = payment.AccountID("00000000-0000-0000-0000-000000000002")
	// thePayer is where the payment came from, and so where a refund goes.
	thePayer = "0xpayerpayerpayerpayerpayerpayerpayerpayer"
)

var (
	now   = time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
	key   = [32]byte{5}
	links = refund.NewLinks(key, "k1", "https://pay.example")
)

// opened is a migrated database with a second account, and the pool over it.
func opened(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := postgres.Open(t.Context(), postgrestest.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Conns().Exec(t.Context(),
		`insert into accounts (id, name) values ($1, 'Other Shop')`, other); err != nil {
		t.Fatal(err)
	}
	return pool.Conns()
}

func jpyc(t *testing.T) payment.Asset {
	t.Helper()
	a, err := payment.NewAsset("polygon", "0x431d5dff03120afa4bdf332c61a6e1766ef37bdb", "JPYC", 18)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// refunding stores a payment the chain settled and a refund of half of it,
// which is what a merchant would be signing.
func refunding(t *testing.T, pool *pgxpool.Pool, account payment.AccountID) *payment.Refund {
	t.Helper()
	store := payment.NewPostgres(pool)
	amount, err := payment.ParseMoney(jpyc(t), "1000"+strings.Repeat("0", 18))
	if err != nil {
		t.Fatal(err)
	}
	destination, err := payment.ParseAddress("0xabababababababababababababababababababab")
	if err != nil {
		t.Fatal(err)
	}
	p, err := payment.New(payment.Request{
		Amount: amount, Destination: destination, ExpiresAt: now.Add(15 * time.Minute),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(t.Context(), account, p); err != nil {
		t.Fatal(err)
	}
	// Payable, paid and settled, which is the one state a refund is made
	// against.
	if err := p.Await(); err != nil {
		t.Fatal(err)
	}
	if err := p.Receive(p.Amount()); err != nil {
		t.Fatal(err)
	}
	if err := p.Succeed(); err != nil {
		t.Fatal(err)
	}
	_, at, err := store.Find(t.Context(), account, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), account, p, at, payment.Event{}); err != nil {
		t.Fatal(err)
	}
	half, err := payment.ParseMoney(jpyc(t), "500"+strings.Repeat("0", 18))
	if err != nil {
		t.Fatal(err)
	}
	to, err := payment.ParseAddress(thePayer)
	if err != nil {
		t.Fatal(err)
	}
	refundID, err := payment.NewRefundID()
	if err != nil {
		t.Fatal(err)
	}
	r, err := payment.NewRefund(p, refundID, payment.RefundRequest{
		Amount: half, Destination: to, Token: links.Token(refundID),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRefund(t.Context(), account, r); err != nil {
		t.Fatal(err)
	}
	return r
}

func serving(t *testing.T, pool *pgxpool.Pool, clock func() time.Time) *refund.HTTP {
	t.Helper()
	return refund.NewHTTP(refund.Deps{
		Store: refund.NewPostgres(pool), Refunds: payment.NewPostgres(pool),
		Key: key, KeyID: "k1", ChainIDs: map[string]uint64{"polygon": 137},
		Domain: func(payment.Asset) (string, string) { return "JPY Coin", "1" },
		Now:    clock,
	})
}

// answered is the answer of method to a request for token.
func answered(t *testing.T, method func(http.ResponseWriter, *http.Request) error, token string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/refund/"+token, nil)
	r.SetPathValue("token", token)
	if err := method(rec, r); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestHTTP_AnswersWhatTheMerchantSigns(t *testing.T) {
	t.Parallel()
	pool := opened(t)
	h := serving(t, pool, func() time.Time { return now })
	r := refunding(t, pool, first)

	rec := answered(t, h.State, string(refund.Derive(key, r.ID())))

	if rec.Code != http.StatusOK {
		t.Fatalf("state answered %d:\n%s", rec.Code, rec.Body)
	}
	var state map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state["amount"] != "500" || state["to"] != thePayer {
		t.Errorf("state = %v, want 500 back to the payer", state)
	}
	material, _ := state["authorization"].(map[string]any)
	message, _ := material["message"].(map[string]any)
	domain, _ := material["domain"].(map[string]any)
	// What the wallet signs: out of the wallet the payment was paid to, back
	// to the wallet it came from, for the refund's amount, under its key.
	if message["from"] != "0xabababababababababababababababababababab" || message["to"] != thePayer {
		t.Errorf("message = %v, want from the merchant's wallet to the payer's", message)
	}
	if message["value"] != "500"+strings.Repeat("0", 18) {
		t.Errorf("value = %v, want the refund's amount in the asset's smallest unit", message["value"])
	}
	if message["nonce"] != "0x"+r.Key() {
		t.Errorf("nonce = %v, want the refund's key", message["nonce"])
	}
	if domain["chainId"] != float64(137) || domain["name"] != "JPY Coin" {
		t.Errorf("domain = %v, want the asset's", domain)
	}
	if state["reason"] != nil {
		t.Errorf("reason = %v, want nothing in the way of signing", state["reason"])
	}
}

func TestHTTP_AnswersAnUnknownAMalformedAndAnotherKeysTokenAlike(t *testing.T) {
	t.Parallel()
	pool := opened(t)
	h := serving(t, pool, func() time.Time { return now })
	r := refunding(t, pool, first)

	// A row whose hash somebody wrote by hand, for a token the deployment
	// never derived: found by the hash, and refused by the derivation.
	forged := refunding(t, pool, first)
	planted := refund.Derive([32]byte{8}, forged.ID())
	if _, err := pool.Exec(t.Context(),
		`update refunds set token_hash = $2 where id = $1`, forged.ID(), refund.Hash(planted)); err != nil {
		t.Fatal(err)
	}

	for name, token := range map[string]string{
		"a token nobody derived": string(refund.Derive([32]byte{9}, r.ID())),
		"a planted hash":         string(planted),
		"the refund's id":        r.ID().String(),
		"not a token":            "boom",
		"a token of no refund":   strings.Repeat("ab", 32),
	} {
		t.Run(name, func(t *testing.T) {
			if rec := answered(t, h.State, token); rec.Code != http.StatusNotFound {
				t.Errorf("state answered %d, want 404", rec.Code)
			}
			// The page a person opens answers with a page, not with JSON.
			rec := answered(t, h.Page, token)
			if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "<html") {
				t.Errorf("page answered %d:\n%s", rec.Code, rec.Body)
			}
		})
	}
}

func TestHTTP_StopsAnsweringThirtyDaysAfterTheRefundEnds(t *testing.T) {
	t.Parallel()
	pool := opened(t)
	r := refunding(t, pool, first)
	if _, err := pool.Exec(t.Context(),
		`update refunds set status = 'expired', closed_at = $2 where id = $1`, r.ID(), now); err != nil {
		t.Fatal(err)
	}
	token := string(refund.Derive(key, r.ID()))

	just := serving(t, pool, func() time.Time { return now.Add(refund.PageLife - time.Hour) })
	past := serving(t, pool, func() time.Time { return now.Add(refund.PageLife + time.Hour) })

	if rec := answered(t, just.State, token); rec.Code != http.StatusOK {
		t.Errorf("a day before the page closes: %d", rec.Code)
	}
	if rec := answered(t, past.State, token); rec.Code != http.StatusNotFound {
		t.Errorf("an hour after it closes: %d, want 404", rec.Code)
	}
}

func TestHTTP_SaysWhyNothingCanBeSigned(t *testing.T) {
	t.Parallel()
	pool := opened(t)
	r := refunding(t, pool, first)
	token := string(refund.Derive(key, r.ID()))

	// Past the deadline, the key is dead and the merchant opens another.
	h := serving(t, pool, func() time.Time { return now.Add(payment.RefundLife + time.Minute) })
	rec := answered(t, h.State, token)

	var state map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state["reason"] != refund.ReasonExpired || state["authorization"] != nil {
		t.Errorf("state = %v, want expired and nothing to sign", state)
	}
}

func TestHTTP_CarriesTheHeadersAPageIsServedUnder(t *testing.T) {
	t.Parallel()
	pool := opened(t)
	h := serving(t, pool, func() time.Time { return now })
	r := refunding(t, pool, first)
	token := string(refund.Derive(key, r.ID()))

	for _, rec := range []*httptest.ResponseRecorder{answered(t, h.Page, token), answered(t, h.State, token)} {
		for header, want := range map[string]string{
			"Referrer-Policy":        "no-referrer",
			"X-Content-Type-Options": "nosniff",
			"Cache-Control":          "no-store",
			"X-Frame-Options":        "DENY",
		} {
			if got := rec.Header().Get(header); got != want {
				t.Errorf("%s = %q, want %q", header, got, want)
			}
		}
		if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
			t.Errorf("Content-Security-Policy = %q", got)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("the page is offered to %q", got)
		}
	}
}

func TestHTTP_ServesThePartsAPageIsMadeOfAndListsNoDirectory(t *testing.T) {
	t.Parallel()
	pool := opened(t)
	h := refund.NewHTTP(refund.Deps{
		Store: refund.NewPostgres(pool), Refunds: payment.NewPostgres(pool),
		Key: key, KeyID: "k1", Now: func() time.Time { return now },
		Assets: fstest.MapFS{"app.js": {Data: []byte("export {}")}},
	})

	rec := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/refund-assets/app.js", nil)
	if err := h.Assets(rec, r); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "export {}" {
		t.Errorf("app.js answered %d:\n%s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	if err := h.Assets(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/refund-assets/", nil)); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("a directory answered %d, want 404 rather than a listing", rec.Code)
	}
}
