package payment_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/payment"
)

// refund posts a refund of a payment, under an idempotency key when one is
// given.
func (f *handler) refund(t *testing.T, account payment.AccountID, id, key, body string) (*httptest.ResponseRecorder, error) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"/payments/"+id+"/refunds", strings.NewReader(body))
	r.SetPathValue("id", id)
	if key != "" {
		r.Header.Set(payment.IdempotencyKeyHeader, key)
	}
	return w, f.h.Refund(w, r, account)
}

// readRefund reads one refund back.
func (f *handler) readRefund(t *testing.T, account payment.AccountID, id, refund string) (*httptest.ResponseRecorder, error) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/payments/"+id+"/refunds/"+refund, nil)
	r.SetPathValue("id", id)
	r.SetPathValue("refund", refund)
	return w, f.h.ReadRefund(w, r, account)
}

// refundable is a payment of the harness that a transfer paid and the chain
// settled, which is the only kind a refund can be made against.
func refundable(t *testing.T, f *handler) string {
	t.Helper()
	id := f.created(t, example)
	transferred(t, f, payment.ID(id))
	// The observation put what arrived on the payment; what is left is the
	// chain settling it, which the finality worker does.
	p, at, err := f.store.Find(t.Context(), first, payment.ID(id))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Succeed(); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Save(t.Context(), first, p, at, payment.Event{}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRefund_AnswersWithTheRefundAndWhereToSignIt(t *testing.T) {
	t.Parallel()
	f := served(t)
	id := refundable(t, f)

	w, err := f.refund(t, first, id, "", `{"amount": "250"}`)

	if err != nil || w.Code != http.StatusCreated {
		t.Fatalf("refund answered %d, %v:\n%s", w.Code, err, w.Body)
	}
	body := decoded(t, w)
	if body["amount"] != "250" || body["status"] != "created" || body["payment"] != id {
		t.Errorf("body = %v, want 250 created against %s", body, id)
	}
	// Where the money goes is read off the transfer that paid, not asked for.
	if body["destination"] != theSigner {
		t.Errorf("destination = %v, want %s, who paid", body["destination"], theSigner)
	}
	url, _ := body["refund_url"].(string)
	if !strings.HasPrefix(url, "https://pay.example/refund/") {
		t.Errorf("refund_url = %q, want one under the base URL", url)
	}
	// The key the authorisation spends is the page's, and no field of this.
	for _, key := range []string{"key", "nonce", "token"} {
		if _, has := body[key]; has {
			t.Errorf("the body carries %q: %v", key, body)
		}
	}
	if location := w.Header().Get("Location"); location != "/payments/"+id+"/refunds/"+body["id"].(string) {
		t.Errorf("Location = %q", location)
	}
}

func TestRefund_SendsBackEverythingLeftWhenTheBodyNamesNoAmount(t *testing.T) {
	t.Parallel()
	f := served(t)
	id := refundable(t, f)

	part, err := f.refund(t, first, id, "", `{"amount": "400"}`)
	if err != nil || part.Code != http.StatusCreated {
		t.Fatalf("the first refund answered %d, %v", part.Code, err)
	}
	rest, err := f.refund(t, first, id, "", ``)

	if err != nil || rest.Code != http.StatusCreated {
		t.Fatalf("the rest answered %d, %v:\n%s", rest.Code, err, rest.Body)
	}
	if got := decoded(t, rest)["amount"]; got != "600" {
		t.Errorf("amount = %v, want the 600 that was left", got)
	}
	// And the payment says so.
	read, err := f.read(t, first, id)
	if err != nil {
		t.Fatal(err)
	}
	if got := decoded(t, read)["refunded"]; got != "1000" {
		t.Errorf("refunded = %v, want all of what arrived", got)
	}
}

func TestRefund_RefusesMoreThanThePaymentHasLeft(t *testing.T) {
	t.Parallel()
	f := served(t)
	id := refundable(t, f)

	w, err := f.refund(t, first, id, "", `{"amount": "1000.000000000000000001"}`)

	if err != nil {
		t.Fatal(err)
	}
	if fields := refused(t, w); len(fields) != 1 || fields[0] != "amount" {
		t.Errorf("problems = %v, want one naming amount", fields)
	}
	if got := decoded(t, w)["problems"].([]any)[0].(map[string]any)["message"]; !strings.Contains(got.(string), "1000") {
		t.Errorf("message = %v, want what is left named", got)
	}
}

func TestRefund_RefusesAPaymentTheChainHasNotSettled(t *testing.T) {
	t.Parallel()
	f := served(t)
	unpaid := f.created(t, example)

	w, err := f.refund(t, first, unpaid, "", `{"amount": "1"}`)

	if err != nil {
		t.Fatal(err)
	}
	if fields := refused(t, w); len(fields) != 1 || fields[0] != "status" {
		t.Errorf("problems = %v, want one naming status", fields)
	}
	if n := f.refunds(t); n != 0 {
		t.Errorf("%d refunds, want none", n)
	}
}

func TestRefund_ReadsBackWhatItAnswered(t *testing.T) {
	t.Parallel()
	f := served(t)
	id := refundable(t, f)
	made, err := f.refund(t, first, id, "", `{"amount": "1"}`)
	if err != nil || made.Code != http.StatusCreated {
		t.Fatalf("refund answered %d, %v", made.Code, err)
	}
	refund := decoded(t, made)["id"].(string)

	w, err := f.readRefund(t, first, id, refund)

	if err != nil || w.Code != http.StatusOK {
		t.Fatalf("read answered %d, %v:\n%s", w.Code, err, w.Body)
	}
	if got, want := decoded(t, w), decoded(t, made); got["id"] != want["id"] ||
		got["amount"] != want["amount"] || got["refund_url"] != want["refund_url"] {
		t.Errorf("read %v, want what the refund answered %v", got, want)
	}
	// Somebody else's refund is not found, and neither is one nobody made.
	if w, err := f.readRefund(t, other, id, refund); err != nil || w.Code != http.StatusNotFound {
		t.Errorf("another account read the refund: %d, %v", w.Code, err)
	}
	if w, err := f.readRefund(t, first, id, strings.Repeat("a", 32)); err != nil || w.Code != http.StatusNotFound {
		t.Errorf("a refund nobody made: %d, %v", w.Code, err)
	}
}

func TestRefund_AnswersARetryUnderOneKeyWithTheRefundItOpened(t *testing.T) {
	t.Parallel()
	f := served(t)
	id := refundable(t, f)

	opened, err := f.refund(t, first, id, "a-key", `{"amount": "100"}`)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.refund(t, first, id, "a-key", `{"amount": "100"}`)
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.refund(t, first, id, "a-key", `{"amount": "200"}`)

	if err != nil {
		t.Fatal(err)
	}
	if opened.Code != http.StatusCreated || again.Code != http.StatusCreated {
		t.Fatalf("answered %d then %d, want 201 twice", opened.Code, again.Code)
	}
	if was, is := decoded(t, opened)["id"], decoded(t, again)["id"]; was != is {
		t.Errorf("the retry opened %v, want the refund %v the key opened", is, was)
	}
	if got := again.Header().Get("Idempotent-Replayed"); got != "true" {
		t.Errorf("the retry is marked %q, want true", got)
	}
	if fields := refused(t, other); len(fields) != 1 || fields[0] != payment.IdempotencyKeyHeader {
		t.Errorf("another body under the key: %v, want one naming the header", fields)
	}
	if n := f.refunds(t); n != 1 {
		t.Errorf("%d refunds, want the one the key opened", n)
	}
}

func TestRefund_RefusesABodyItDoesNotRead(t *testing.T) {
	t.Parallel()
	f := served(t)
	id := refundable(t, f)

	w, err := f.refund(t, first, id, "", `{"amount": "1", "destination": "0xab"}`)

	if err != nil {
		t.Fatal(err)
	}
	if fields := refused(t, w); len(fields) != 1 || fields[0] != "destination" {
		t.Errorf("problems = %v, want one naming the key nobody may send", fields)
	}
}

func (f *handler) refunds(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(t.Context(), `select count(*) from refunds`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A key names one request, and the payment a refund is against is in the path
// rather than in the body. Sending a used key to another payment would
// otherwise be answered with a refund of the first one, leaving the payment
// the merchant named with nothing and the merchant with a 201.
func TestRefund_RefusesAKeyAlreadyUsedToRefundAnotherPayment(t *testing.T) {
	t.Parallel()
	f := served(t)
	one, other := refundable(t, f), refundable(t, f)

	opened, err := f.refund(t, first, one, "one-key", `{"amount": "100"}`)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Code != http.StatusCreated {
		t.Fatalf("the first answered %d:\n%s", opened.Code, opened.Body)
	}

	again, err := f.refund(t, first, other, "one-key", `{"amount": "100"}`)

	if err != nil {
		t.Fatal(err)
	}
	if fields := refused(t, again); len(fields) != 1 || fields[0] != payment.IdempotencyKeyHeader {
		t.Errorf("problems = %v, want one naming the header", fields)
	}
	if n := f.refunds(t); n != 1 {
		t.Errorf("%d refunds, want the one the key opened", n)
	}
}
