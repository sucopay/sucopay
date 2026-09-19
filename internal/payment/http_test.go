package payment_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/iotest"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sucopay/sucopay/internal/checkout"
	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/refund"
)

// example is a body that writes every key a body may hold, with a deadline
// two hours off so that the one a request leaves out can be told from it.
const example = `{"asset": "jpyc", "amount": "1000", "expires_at": "2026-09-01T14:00:00Z", "metadata": {"order": "A-1"}}`

// listed is what a document lists, for the tests: assets by the name a
// request writes.
type listed map[string]payment.Asset

func (l listed) Asset(name string) (payment.Asset, bool) {
	a, ok := l[name]
	return a, ok
}

// acceptance is one account accepting one asset.
type acceptance struct {
	account payment.AccountID
	asset   payment.Asset
}

// accepting is what accounts accept, for the tests, or an error to answer
// with instead of it.
type accepting struct {
	paidTo map[acceptance]payment.Address
	err    error
}

func (a *accepting) Destination(_ context.Context, account payment.AccountID, asset payment.Asset) (payment.Address, bool, error) {
	if a.err != nil {
		return "", false, a.err
	}
	destination, ok := a.paidTo[acceptance{account, asset}]
	return destination, ok, nil
}

func address(t *testing.T, digit string) payment.Address {
	t.Helper()
	a, err := payment.ParseAddress("0x" + strings.Repeat(digit, 40))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// handler is one over a fresh database, listing jpyc and usdc, with the first
// account accepting jpyc and nobody accepting usdc.
type handler struct {
	h        *payment.HTTP
	accepted *accepting
	store    *payment.Postgres
	pool     *pgxpool.Pool
}

func served(t *testing.T) *handler {
	t.Helper()
	svc, store, pool := serving(t)
	accepted := &accepting{paidTo: map[acceptance]payment.Address{{first, jpyc(t)}: address(t, "c")}}
	return &handler{
		h: payment.NewHTTP(svc, listed{"jpyc": jpyc(t), "usdc": usdc(t)}, accepted,
			checkout.NewLinks([32]byte{9}, "k1", "https://pay.example"),
			refund.NewLinks([32]byte{8}, "k1", "https://pay.example")),
		accepted: accepted,
		store:    store,
		pool:     pool,
	}
}

func (f *handler) create(t *testing.T, account payment.AccountID, body io.Reader) (*httptest.ResponseRecorder, error) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/payments", body)
	return w, f.h.Create(w, r, account)
}

// keyed posts body under an idempotency key, which is the header a merchant
// sends so that a retry opens one payment.
func (f *handler) keyed(t *testing.T, account payment.AccountID, key, body string) (*httptest.ResponseRecorder, error) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/payments", strings.NewReader(body))
	r.Header.Set(payment.IdempotencyKeyHeader, key)
	return w, f.h.Create(w, r, account)
}

func (f *handler) read(t *testing.T, account payment.AccountID, id string) (*httptest.ResponseRecorder, error) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/payments/"+id, nil)
	r.SetPathValue("id", id)
	return w, f.h.Read(w, r, account)
}

// created posts body as the first account and returns the payment's
// identifier, failing the test on any other answer.
func (f *handler) created(t *testing.T, body string) string {
	t.Helper()
	w, err := f.create(t, first, strings.NewReader(body))
	if err != nil || w.Code != http.StatusCreated {
		t.Fatalf("create answered %d, %v:\n%s", w.Code, err, w.Body)
	}
	return decoded(t, w)["id"].(string)
}

func (f *handler) rows(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(t.Context(), `select count(*) from payments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// decoded reads the body back as JSON, so that a test compares what a client
// reads and not how it was written.
func decoded(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not a JSON object: %v:\n%s", err, w.Body)
	}
	return body
}

// refused reads a 400 back and returns the fields of its problems, in order,
// with "-" for a problem that has none.
func refused(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	if w.Code != http.StatusBadRequest {
		t.Fatalf("answered %d, want 400:\n%s", w.Code, w.Body)
	}
	var body struct {
		Error    string
		Problems []struct{ Field, Message string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not the error shape: %v:\n%s", err, w.Body)
	}
	if body.Error != "invalid" || len(body.Problems) == 0 {
		t.Fatalf("body = %s, want error invalid with problems", w.Body)
	}
	var fields []string
	for _, p := range body.Problems {
		if p.Message == "" {
			t.Errorf("a problem under %q has no message", p.Field)
		}
		if p.Field == "" {
			p.Field = "-"
		}
		fields = append(fields, p.Field)
	}
	return fields
}

func TestHTTP_CreatesAPaymentFromABodyThatWritesEveryKey(t *testing.T) {
	t.Parallel()
	f := served(t)

	w, err := f.create(t, first, strings.NewReader(example))

	if err != nil || w.Code != http.StatusCreated {
		t.Fatalf("answered %d, %v:\n%s", w.Code, err, w.Body)
	}
	got := decoded(t, w)
	id, _ := got["id"].(string)
	if _, err := payment.ParseID(id); err != nil {
		t.Errorf("id %q: %v", id, err)
	}
	if location := w.Header().Get("Location"); location != "/payments/"+id {
		t.Errorf("Location = %q, want /payments/%s", location, id)
	}
	want := map[string]any{
		"id":     id,
		"status": "created",
		"asset": map[string]any{
			"network": "polygon", "reference": "jpyc-contract", "symbol": "JPYC", "decimals": float64(18),
		},
		"amount":      "1000",
		"received":    nil,
		"destination": string(address(t, "c")),
		"metadata":    map[string]any{"order": "A-1"},
		"expires_at":  "2026-09-01T14:00:00Z",
		"created_at":  "2026-09-01T12:00:00Z",
		"return_url":  nil,
		"transfer":    nil,
		"refunded":    "0",
	}
	// The checkout URL is the one thing here not written from the body: a
	// token derived for the payment. Its own test says what it is.
	checkoutURL, _ := got["checkout_url"].(string)
	delete(got, "checkout_url")
	if !strings.HasPrefix(checkoutURL, "https://pay.example/checkout/") {
		t.Errorf("checkout_url = %q, want one under the base URL", checkoutURL)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("body:\n%s\nwant %v", w.Body, want)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if f.rows(t) != 1 {
		t.Errorf("%d rows, want one", f.rows(t))
	}
}

func TestHTTP_FillsInWhatTheBodyLeavesOut(t *testing.T) {
	t.Parallel()
	f := served(t)

	w, err := f.create(t, first, strings.NewReader(`{"asset": "jpyc", "amount": "1"}`))

	if err != nil || w.Code != http.StatusCreated {
		t.Fatalf("answered %d, %v:\n%s", w.Code, err, w.Body)
	}
	got := decoded(t, w)
	if got["expires_at"] != "2026-09-01T12:15:00Z" {
		t.Errorf("expires_at = %v, want a quarter of an hour after created_at", got["expires_at"])
	}
	if !reflect.DeepEqual(got["metadata"], map[string]any{}) {
		t.Errorf("metadata = %v, want an empty object", got["metadata"])
	}
}

func TestHTTP_RefusesAnAssetNotListedOrNotAccepted(t *testing.T) {
	t.Parallel()
	f := served(t)
	for name, tc := range map[string]struct {
		account payment.AccountID
		asset   string
	}{
		"a name the list does not have":    {first, "gold"},
		"an asset nobody accepts":          {first, "usdc"},
		"an asset another account accepts": {other, "jpyc"},
	} {
		t.Run(name, func(t *testing.T) {
			w, err := f.create(t, tc.account, strings.NewReader(fmt.Sprintf(`{"asset": %q, "amount": "1"}`, tc.asset)))
			if err != nil {
				t.Fatal(err)
			}
			if fields := refused(t, w); strings.Join(fields, " ") != "asset" {
				t.Errorf("problems under %q, want asset alone:\n%s", fields, w.Body)
			}
		})
	}
	if f.rows(t) != 0 {
		t.Errorf("%d rows, want none", f.rows(t))
	}
}

func TestHTTP_ReadsTheAmountInTheAssetsUnits(t *testing.T) {
	t.Parallel()
	f := served(t)
	for _, tc := range []struct {
		amount string
		want   int
	}{
		{"1000.5", http.StatusCreated},
		{"0.000000000000000001", http.StatusCreated},
		{"0.0000000000000000001", http.StatusBadRequest},
		{"0", http.StatusBadRequest},
		{"-1", http.StatusBadRequest},
		{"1e3", http.StatusBadRequest},
		{"+1", http.StatusBadRequest},
	} {
		t.Run(tc.amount, func(t *testing.T) {
			w, err := f.create(t, first, strings.NewReader(fmt.Sprintf(`{"asset": "jpyc", "amount": %q}`, tc.amount)))
			if err != nil || w.Code != tc.want {
				t.Fatalf("answered %d, %v; want %d:\n%s", w.Code, err, tc.want, w.Body)
			}
			if tc.want == http.StatusCreated {
				if got := decoded(t, w)["amount"]; got != tc.amount {
					t.Errorf("amount = %v, want %s back as written", got, tc.amount)
				}
				return
			}
			if fields := refused(t, w); strings.Join(fields, " ") != "amount" {
				t.Errorf("problems under %q, want amount alone", fields)
			}
		})
	}
}

func TestHTTP_BoundsTheDeadline(t *testing.T) {
	t.Parallel()
	f := served(t)
	for name, tc := range map[string]struct {
		expiresAt string
		want      int
	}{
		"thirty days off":              {"2026-10-01T12:00:00Z", http.StatusCreated},
		"a second past thirty days":    {"2026-10-01T12:00:01Z", http.StatusBadRequest},
		"before the payment is opened": {"2026-09-01T11:59:59Z", http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			w, err := f.create(t, first, strings.NewReader(fmt.Sprintf(`{"asset": "jpyc", "amount": "1", "expires_at": %q}`, tc.expiresAt)))
			if err != nil || w.Code != tc.want {
				t.Fatalf("answered %d, %v; want %d:\n%s", w.Code, err, tc.want, w.Body)
			}
			if tc.want == http.StatusBadRequest {
				if fields := refused(t, w); strings.Join(fields, " ") != "expires_at" {
					t.Errorf("problems under %q, want expires_at alone", fields)
				}
			}
		})
	}
}

func TestHTTP_ReportsEveryProblemWithTheMetadata(t *testing.T) {
	t.Parallel()
	f := served(t)
	metadata := map[string]string{strings.Repeat("k", payment.MaxMetadataKeyBytes+1): "v"}
	for i := 0; len(metadata) <= payment.MaxMetadataEntries; i++ {
		metadata[fmt.Sprint(i)] = "v"
	}
	body, err := json.Marshal(map[string]any{"asset": "jpyc", "amount": "1", "metadata": metadata})
	if err != nil {
		t.Fatal(err)
	}

	w, err := f.create(t, first, strings.NewReader(string(body)))

	if err != nil {
		t.Fatal(err)
	}
	fields := refused(t, w)
	if len(fields) != 2 || fields[0] != "metadata" || !strings.HasPrefix(fields[1], "metadata.") {
		t.Errorf("problems under %q, want one for the count and one for the key", fields)
	}
}

func TestHTTP_RefusesABodyLargerThanMaxBodyBytes(t *testing.T) {
	t.Parallel()
	f := served(t)
	// Padded with spaces, which JSON allows between tokens, so that the body
	// at the bound is a request in every other respect.
	pad := func(n int) string {
		s := `{"asset": "jpyc", "amount": "1"}`
		return s + strings.Repeat(" ", n-len(s))
	}

	w, err := f.create(t, first, strings.NewReader(pad(payment.MaxBodyBytes+1)))

	if err != nil || w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("one byte over answered %d, %v; want 413:\n%s", w.Code, err, w.Body)
	}
	if body := strings.TrimSpace(w.Body.String()); body != `{"error":"too_large"}` {
		t.Errorf("body is %s, want the one word", body)
	}
	if f.rows(t) != 0 {
		t.Errorf("%d rows, want none", f.rows(t))
	}
	if w, err := f.create(t, first, strings.NewReader(pad(payment.MaxBodyBytes))); err != nil || w.Code != http.StatusCreated {
		t.Errorf("at the bound answered %d, %v; want 201:\n%s", w.Code, err, w.Body)
	}
}

func TestHTTP_RefusesABodyThatEndsEarly(t *testing.T) {
	t.Parallel()
	f := served(t)

	w, err := f.create(t, first, io.MultiReader(strings.NewReader(`{"asset": "jpyc"`), iotest.ErrReader(errors.New("connection reset"))))

	if err != nil {
		t.Fatal(err)
	}
	if fields := refused(t, w); strings.Join(fields, " ") != "-" {
		t.Errorf("problems under %q, want one about the body as a whole", fields)
	}
	// And a key that is not one is answered with it, rather than waiting for
	// the merchant to fix the body and send again.
	w = httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/payments",
		io.MultiReader(strings.NewReader(`{"asset": "jpyc"`), iotest.ErrReader(errors.New("connection reset"))))
	r.Header.Set(payment.IdempotencyKeyHeader, "")
	if err := f.h.Create(w, r, first); err != nil {
		t.Fatal(err)
	}
	if fields := refused(t, w); strings.Join(fields, " ") != payment.IdempotencyKeyHeader+" -" {
		t.Errorf("problems under %q, want the header and the body", fields)
	}
}

func TestHTTP_ReadsBackThePaymentItCreated(t *testing.T) {
	t.Parallel()
	f := served(t)
	created, err := f.create(t, first, strings.NewReader(example))
	if err != nil || created.Code != http.StatusCreated {
		t.Fatalf("create answered %d, %v:\n%s", created.Code, err, created.Body)
	}
	id := strings.TrimPrefix(created.Header().Get("Location"), "/payments/")

	w, err := f.read(t, first, id)

	if err != nil || w.Code != http.StatusOK {
		t.Fatalf("read answered %d, %v:\n%s", w.Code, err, w.Body)
	}
	if w.Body.String() != created.Body.String() {
		t.Errorf("read:\n%swant the body create answered with:\n%s", w.Body, created.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestHTTP_AnswersEveryMissingPaymentAlike(t *testing.T) {
	t.Parallel()
	f := served(t)
	id := f.created(t, example)
	for name, tc := range map[string]struct {
		account payment.AccountID
		id      string
	}{
		"another account's payment": {other, id},
		"a payment nobody opened":   {first, strings.Repeat("f", 32)},
		"an identifier of no shape": {first, "1"},
	} {
		t.Run(name, func(t *testing.T) {
			w, err := f.read(t, tc.account, tc.id)
			if err != nil || w.Code != http.StatusNotFound {
				t.Fatalf("answered %d, %v; want 404:\n%s", w.Code, err, w.Body)
			}
			if body := strings.TrimSpace(w.Body.String()); body != `{"error":"not_found"}` {
				t.Errorf("body is %s, want the one word", body)
			}
		})
	}
}

func TestHTTP_ReportsADatabaseItCannotReachAndWritesNothing(t *testing.T) {
	t.Parallel()
	f := served(t)
	id := f.created(t, example)
	f.pool.Close()

	for name, do := range map[string]func() (*httptest.ResponseRecorder, error){
		"create": func() (*httptest.ResponseRecorder, error) { return f.create(t, first, strings.NewReader(example)) },
		"read":   func() (*httptest.ResponseRecorder, error) { return f.read(t, first, id) },
	} {
		t.Run(name, func(t *testing.T) {
			w, err := do()
			if err == nil {
				t.Fatalf("answered %d with no error:\n%s", w.Code, w.Body)
			}
			if w.Body.Len() != 0 || len(w.Header()) != 0 {
				t.Errorf("wrote %d bytes and headers %v, want nothing", w.Body.Len(), w.Header())
			}
		})
	}
}

func TestHTTP_ReportsAcceptedItCannotReachAndStoresNothing(t *testing.T) {
	t.Parallel()
	f := served(t)
	f.accepted.err = errors.New("closed")

	w, err := f.create(t, first, strings.NewReader(example))

	if err == nil {
		t.Fatalf("answered %d with no error:\n%s", w.Code, w.Body)
	}
	if w.Body.Len() != 0 || len(w.Header()) != 0 {
		t.Errorf("wrote %d bytes and headers %v, want nothing", w.Body.Len(), w.Header())
	}
	if f.rows(t) != 0 {
		t.Errorf("%d rows, want none", f.rows(t))
	}
}

func TestHTTP_PaysAPaymentToTheDestinationAcceptedWhenItWasOpened(t *testing.T) {
	t.Parallel()
	f := served(t)
	before := f.created(t, example)
	f.accepted.paidTo[acceptance{first, jpyc(t)}] = address(t, "d")
	after := f.created(t, example)

	for id, want := range map[string]payment.Address{before: address(t, "c"), after: address(t, "d")} {
		w, err := f.read(t, first, id)
		if err != nil || w.Code != http.StatusOK {
			t.Fatalf("read answered %d, %v:\n%s", w.Code, err, w.Body)
		}
		if got := decoded(t, w)["destination"]; got != string(want) {
			t.Errorf("destination = %v, want %s", got, want)
		}
	}
}

// An event is named for where the payment has got to, and carries the payment
// as a merchant reads it back: one shape, whether they were told or they
// asked.
func TestAnnounce_NamesTheStatusAndCarriesWhatReadAnswers(t *testing.T) {
	t.Parallel()
	f := served(t)
	id := f.created(t, example)
	read, err := f.read(t, first, id)
	if err != nil || read.Code != http.StatusOK {
		t.Fatalf("read answered %d, %v:\n%s", read.Code, err, read.Body)
	}
	p, _, err := f.store.Find(t.Context(), first, payment.ID(id))
	if err != nil {
		t.Fatal(err)
	}

	e, err := payment.Announce(p, nil)

	if err != nil {
		t.Fatalf("Announce = %v, want none", err)
	}
	if e.Name != "payment.created" {
		t.Errorf("Name = %q, want payment.created", e.Name)
	}
	if bytes.HasSuffix(e.Payload, []byte("\n")) {
		t.Errorf("Payload ends in the newline an encoder writes:\n%s", e.Payload)
	}
	var told map[string]any
	if err := json.Unmarshal(e.Payload, &told); err != nil {
		t.Fatalf("Payload is not a JSON object: %v:\n%s", err, e.Payload)
	}
	// Everything a read answers but the checkout URL, which carries the
	// page's token and is kept out of what goes to a merchant's server.
	asked := decoded(t, read)
	if _, has := asked["checkout_url"]; !has {
		t.Fatalf("read answered no checkout_url:\n%s", read.Body)
	}
	delete(asked, "checkout_url")
	if _, has := told["checkout_url"]; has || !reflect.DeepEqual(told, asked) {
		t.Errorf("Payload:\n%s\nwant what read answered, less checkout_url:\n%s", e.Payload, read.Body)
	}

	// And once a transfer has been seen for it, the event carries what the
	// read answers there too.
	transferred(t, f, payment.ID(id))
	p, _, err = f.store.Find(t.Context(), first, payment.ID(id))
	if err != nil {
		t.Fatal(err)
	}
	seen, found, err := f.store.MatchedTransfer(t.Context(), first, payment.ID(id))
	if err != nil || !found {
		t.Fatalf("MatchedTransfer found %v, %v", found, err)
	}
	if e, err = payment.Announce(p, &seen); err != nil {
		t.Fatal(err)
	}
	if read, err = f.read(t, first, id); err != nil {
		t.Fatal(err)
	}
	told, asked = map[string]any{}, decoded(t, read)
	if err := json.Unmarshal(e.Payload, &told); err != nil {
		t.Fatal(err)
	}
	delete(asked, "checkout_url")
	if !reflect.DeepEqual(told["transfer"], asked["transfer"]) || told["transfer"] == nil {
		t.Errorf("the event carries %v, want what the read answers: %v", told["transfer"], asked["transfer"])
	}
	if !reflect.DeepEqual(told, asked) {
		t.Errorf("Payload:\n%s\nwant what read answered, less checkout_url:\n%s", e.Payload, read.Body)
	}
}

func TestAnnounce_NamesTheStatusThePaymentHasNow(t *testing.T) {
	t.Parallel()
	p := open(t)
	if err := p.Await(); err != nil {
		t.Fatal(err)
	}
	if err := p.AwaitFinality(p.ExpiresAt()); err != nil {
		t.Fatal(err)
	}

	e, err := payment.Announce(p, nil)

	if err != nil {
		t.Fatalf("Announce = %v, want none", err)
	}
	if e.Name != "payment.awaiting_finality" {
		t.Errorf("Name = %q, want payment.awaiting_finality", e.Name)
	}
}

// A payment at every bound its metadata has, written in the characters an
// encoder made for a page would write six bytes for, is still one event the
// store takes. A payload goes to a merchant's endpoint and not to a page.
func TestAnnounce_IsWithinTheEventBoundForAPaymentAtItsOwn(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	r := request(t)
	r.Metadata = map[string]string{}
	for i := range payment.MaxMetadataEntries {
		key := fmt.Sprintf("%02d", i) + strings.Repeat("<", payment.MaxMetadataKeyBytes-2)
		r.Metadata[key] = strings.Repeat("&", payment.MaxMetadataValueSize)
	}
	p, err := payment.New(r, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(t.Context(), first, p); err != nil {
		t.Fatal(err)
	}
	_, at, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Await(); err != nil {
		t.Fatal(err)
	}
	e, err := payment.Announce(p, nil)
	if err != nil {
		t.Fatal(err)
	}

	err = s.Save(t.Context(), first, p, at, e)

	if err != nil {
		t.Errorf("Save = %v, want the event taken", err)
	}
}

func TestHTTP_AnswersTheSameCheckoutURLOnCreateAndOnRead(t *testing.T) {
	t.Parallel()
	f := served(t)
	w, err := f.create(t, first, strings.NewReader(example))
	if err != nil || w.Code != http.StatusCreated {
		t.Fatalf("create answered %d, %v:\n%s", w.Code, err, w.Body)
	}
	made := decoded(t, w)
	id, _ := made["id"].(string)

	read, err := f.read(t, first, id)

	if err != nil || read.Code != http.StatusOK {
		t.Fatalf("read answered %d, %v:\n%s", read.Code, err, read.Body)
	}
	url, _ := made["checkout_url"].(string)
	if again, _ := decoded(t, read)["checkout_url"].(string); url == "" || again != url {
		t.Errorf("checkout_url = %q on create and %q on read, want one URL both times", url, again)
	}
	token := strings.TrimPrefix(url, "https://pay.example/checkout/")
	if len(token) != 64 || strings.Contains(token, id) {
		t.Errorf("checkout_url = %q, want a 64-character token that is not the payment's id", url)
	}
}

func TestHTTP_KeepsAReturnURLAndRefusesOneThePageMayNotSendAPayerTo(t *testing.T) {
	t.Parallel()
	f := served(t)
	with := func(returnURL string) string {
		return `{"asset": "jpyc", "amount": "1000", "return_url": ` + returnURL + `}`
	}
	for what, body := range map[string]string{
		"https":     with(`"https://shop.example/orders/42"`),
		"localhost": with(`"http://localhost:3000/done"`),
		"loopback":  with(`"http://127.0.0.1:3000/done"`),
	} {
		w, err := f.create(t, first, strings.NewReader(body))
		if err != nil || w.Code != http.StatusCreated {
			t.Errorf("%s: create answered %d, %v:\n%s", what, w.Code, err, w.Body)
			continue
		}
		if got := decoded(t, w)["return_url"]; got == nil {
			t.Errorf("%s: return_url = %v, want the one given back", what, got)
		}
	}
	for what, body := range map[string]string{
		"http elsewhere":      with(`"http://shop.example/done"`),
		"javascript":          with(`"javascript:alert(1)"`),
		"a username":          with(`"https://user:pw@shop.example/done"`),
		"too long":            with(`"https://shop.example/` + strings.Repeat("p", payment.MaxReturnURLBytes) + `"`),
		"not a string":        with(`42`),
		"an unseen character": with(`"https://shop.example/\u200bdone"`),
	} {
		w, err := f.create(t, first, strings.NewReader(body))
		if err != nil || w.Code != http.StatusBadRequest {
			t.Errorf("%s: create answered %d, %v; want 400", what, w.Code, err)
			continue
		}
		if !strings.Contains(w.Body.String(), `"field":"return_url"`) {
			t.Errorf("%s: problems = %s, want one naming return_url", what, w.Body)
		}
	}
	// Left out, and the answer says so with null rather than leaving it out.
	w, _ := f.create(t, first, strings.NewReader(example))
	if got, has := decoded(t, w)["return_url"]; !has || got != nil {
		t.Errorf("return_url left out = %v (present %t), want null", got, has)
	}
}

// another is the example body with a different amount: the same request as
// far as the account and the asset go, and not the same request.
const another = `{"asset": "jpyc", "amount": "2000", "expires_at": "2026-09-01T14:00:00Z", "metadata": {"order": "A-1"}}`

func TestCreate_AnswersARetryUnderOneKeyWithThePaymentItOpened(t *testing.T) {
	t.Parallel()
	f := served(t)

	opened, err := f.keyed(t, first, "a-key", example)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.keyed(t, first, "a-key", example)
	if err != nil {
		t.Fatal(err)
	}

	if opened.Code != http.StatusCreated || again.Code != http.StatusCreated {
		t.Fatalf("answered %d then %d, want 201 twice:\n%s", opened.Code, again.Code, again.Body)
	}
	if was, is := decoded(t, opened)["id"], decoded(t, again)["id"]; was != is {
		t.Errorf("the retry opened %v, want the payment %v the key opened", is, was)
	}
	if got := f.rows(t); got != 1 {
		t.Errorf("%d payments, want the one the key opened", got)
	}
	if got := again.Header().Get("Idempotent-Replayed"); got != "true" {
		t.Errorf("the retry is marked %q, want true", got)
	}
	if got := opened.Header().Get("Idempotent-Replayed"); got != "" {
		t.Errorf("the first answer is marked %q, want nothing", got)
	}
	if was, is := opened.Header().Get("Location"), again.Header().Get("Location"); was != is {
		t.Errorf("Location = %q, want %q", is, was)
	}
}

func TestCreate_RefusesAKeyThatArrivesWithAnotherBody(t *testing.T) {
	t.Parallel()
	f := served(t)

	w, err := f.keyed(t, first, "a-key", example)
	if err != nil || w.Code != http.StatusCreated {
		t.Fatalf("the first body under the key answered %d, %v", w.Code, err)
	}
	w, err = f.keyed(t, first, "a-key", another)

	if err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("answered %d, want 400:\n%s", w.Code, w.Body)
	}
	if got := refused(t, w); len(got) != 1 || !strings.Contains(got[0], payment.IdempotencyKeyHeader) {
		t.Errorf("problems = %v, want one naming the header", got)
	}
	if got := f.rows(t); got != 1 {
		t.Errorf("%d payments, want the one the key opened", got)
	}
}

func TestCreate_KeysBelongToOneAccount(t *testing.T) {
	t.Parallel()
	f := served(t)
	f.accepted.paidTo[acceptance{other, jpyc(t)}] = address(t, "d")

	mine, err := f.keyed(t, first, "a-key", example)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := f.keyed(t, other, "a-key", example)
	if err != nil {
		t.Fatal(err)
	}

	if mine.Code != http.StatusCreated || theirs.Code != http.StatusCreated {
		t.Fatalf("answered %d and %d, want 201 twice:\n%s", mine.Code, theirs.Code, theirs.Body)
	}
	if was, is := decoded(t, mine)["id"], decoded(t, theirs)["id"]; was == is {
		t.Errorf("both accounts were given payment %v", is)
	}
	if got := f.rows(t); got != 2 {
		t.Errorf("%d payments, want one for each account", got)
	}
}

func TestCreate_OpensAPaymentForEveryRequestThatCarriesNoKey(t *testing.T) {
	t.Parallel()
	f := served(t)

	was, is := f.created(t, example), f.created(t, example)

	if was == is {
		t.Errorf("both requests were given payment %s", is)
	}
	if got := f.rows(t); got != 2 {
		t.Errorf("%d payments, want one for each request", got)
	}
}

func TestCreate_KeepsNoKeyFromARequestItRefused(t *testing.T) {
	t.Parallel()
	f := served(t)

	refusal, err := f.keyed(t, first, "a-key", `{"asset": "jpyc", "amount": "0"}`)
	if err != nil {
		t.Fatal(err)
	}
	w, err := f.keyed(t, first, "a-key", example)

	if err != nil {
		t.Fatal(err)
	}
	if refusal.Code != http.StatusBadRequest {
		t.Fatalf("the body nobody could open answered %d, want 400", refusal.Code)
	}
	if w.Code != http.StatusCreated {
		t.Fatalf("the same key with a body that opens answered %d:\n%s", w.Code, w.Body)
	}
	if got := w.Header().Get("Idempotent-Replayed"); got != "" {
		t.Errorf("the answer is marked %q, want nothing: the key opened nothing before", got)
	}
	if got := f.rows(t); got != 1 {
		t.Errorf("%d payments, want the one the corrected body opened", got)
	}
}

func TestCreate_AnswersTheKeyAndTheBodyInOneRound(t *testing.T) {
	t.Parallel()
	f := served(t)

	w, err := f.keyed(t, first, "", `{"asset": "gold", "amount": "1"}`)

	if err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("answered %d, want 400:\n%s", w.Code, w.Body)
	}
	got := refused(t, w)
	if len(got) != 2 {
		t.Fatalf("problems = %v, want the header and the asset in one answer", got)
	}
	if !strings.Contains(got[0], payment.IdempotencyKeyHeader) || !strings.Contains(got[1], "asset") {
		t.Errorf("problems = %v, want the header first and then the body", got)
	}
	if n := f.rows(t); n != 0 {
		t.Errorf("%d payments, want none", n)
	}
}

func TestCreate_AnswersRequestsThatRaceUnderOneKeyWithOnePayment(t *testing.T) {
	t.Parallel()
	f := served(t)
	const racing = 3

	type answer struct {
		code     int
		id       string
		replayed string
	}
	answers := make(chan answer, racing)
	var start sync.WaitGroup
	start.Add(1)
	for range racing {
		go func() {
			start.Wait()
			w, err := f.keyed(t, first, "a-key", example)
			if err != nil {
				answers <- answer{code: -1}
				return
			}
			var body map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			id, _ := body["id"].(string)
			answers <- answer{w.Code, id, w.Header().Get("Idempotent-Replayed")}
		}()
	}
	start.Done()

	opened, replayed := map[string]int{}, 0
	for range racing {
		got := <-answers
		if got.code != http.StatusCreated {
			t.Errorf("one of the requests answered %d", got.code)
			continue
		}
		opened[got.id]++
		if got.replayed == "true" {
			replayed++
		}
	}
	if len(opened) != 1 {
		t.Errorf("the racing requests were given %d payments, want one: %v", len(opened), opened)
	}
	if replayed != racing-1 {
		t.Errorf("%d answers are marked replayed, want %d", replayed, racing-1)
	}
	if n := f.rows(t); n != 1 {
		t.Errorf("%d payments, want one", n)
	}
}

// keysOf reads the keys of a JSON object in the order they were written, so
// that a test can say where a new one went.
func keysOf(t *testing.T, payload []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(payload))
	if _, err := dec.Token(); err != nil {
		t.Fatalf("payload is not a JSON object: %v:\n%s", err, payload)
	}
	var keys []string
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

func TestAnnounceConfirming_CarriesTheTransferOnceAndAfterWhatWasThereBefore(t *testing.T) {
	t.Parallel()
	p := payable(t)
	seen := payment.Transfer{
		Tx: "0xtx", BlockHeight: 12, BlockHash: "0xblock", BlockTime: now,
		From: "0xpayer", To: string(p.Destination()), Value: "1000", Asset: "jpyc-contract",
	}

	e, err := payment.AnnounceConfirming(p, seen)

	if err != nil {
		t.Fatalf("AnnounceConfirming = %v, want none", err)
	}
	if e.Name != "attempt.confirming" {
		t.Errorf("Name = %q, want attempt.confirming", e.Name)
	}
	// Appended rather than put among the keys a payment already had, so that
	// nothing a merchant reads by position moved under them.
	keys := keysOf(t, e.Payload)
	if n := slices.Index(keys, "transfer"); n <= slices.Index(keys, "return_url") {
		t.Errorf("keys = %v, want transfer after the keys that came before it", keys)
	}
	if n := slices.Index(keys[slices.Index(keys, "transfer")+1:], "transfer"); n >= 0 {
		t.Errorf("keys = %v, want one transfer", keys)
	}
	if n := bytes.Count(e.Payload, []byte(`"transfer":`)); n != 1 {
		t.Errorf("the payload writes transfer %d times:\n%s", n, e.Payload)
	}
	var told struct {
		Transfer map[string]any `json:"transfer"`
	}
	if err := json.Unmarshal(e.Payload, &told); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"tx": "0xtx", "block_height": float64(12), "block_hash": "0xblock",
		"block_time": "2026-09-01T12:00:00Z", "from": "0xpayer", "value": "1000",
	}
	if !reflect.DeepEqual(told.Transfer, want) {
		t.Errorf("transfer = %v, want %v", told.Transfer, want)
	}
}

func TestAnnounce_CarriesNoTransferForAPaymentNothingWasSeenFor(t *testing.T) {
	t.Parallel()
	p := payable(t)

	e, err := payment.Announce(p, nil)

	if err != nil {
		t.Fatalf("Announce = %v, want none", err)
	}
	var told map[string]any
	if err := json.Unmarshal(e.Payload, &told); err != nil {
		t.Fatal(err)
	}
	if got, has := told["transfer"]; !has || got != nil {
		t.Errorf("transfer = %v, has = %v, want it written as null", got, has)
	}
}

func TestRead_AnswersWithTheTransferThatMatchedThePayment(t *testing.T) {
	t.Parallel()
	f := served(t)
	id := f.created(t, example)
	transferred(t, f, payment.ID(id))

	w, err := f.read(t, first, id)

	if err != nil || w.Code != http.StatusOK {
		t.Fatalf("read answered %d, %v:\n%s", w.Code, err, w.Body)
	}
	transfer, _ := decoded(t, w)["transfer"].(map[string]any)
	if transfer["tx"] != "0xtx" || transfer["block_height"] != float64(10) ||
		transfer["from"] != theSigner || transfer["block_hash"] != "block0xtx" {
		t.Errorf("transfer = %v, want the transfer that matched", transfer)
	}
	if _, has := transfer["network"]; has {
		t.Errorf("transfer = %v, want no network: the asset names it", transfer)
	}
	if _, has := transfer["to"]; has {
		t.Errorf("transfer = %v, want no destination: the payment names it", transfer)
	}
}

func TestCreate_AnswersWithTheTransferOnARetryOfAPaymentThatWasPaid(t *testing.T) {
	t.Parallel()
	f := served(t)
	opened, err := f.keyed(t, first, "a-key", example)
	if err != nil || opened.Code != http.StatusCreated {
		t.Fatalf("create answered %d, %v", opened.Code, err)
	}
	if got, has := decoded(t, opened)["transfer"]; !has || got != nil {
		t.Errorf("a payment just opened carries %v, want transfer null", got)
	}
	id := decoded(t, opened)["id"].(string)
	transferred(t, f, payment.ID(id))

	again, err := f.keyed(t, first, "a-key", example)
	read, readErr := f.read(t, first, id)

	if err != nil || again.Code != http.StatusCreated {
		t.Fatalf("the retry answered %d, %v:\n%s", again.Code, err, again.Body)
	}
	if readErr != nil || read.Code != http.StatusOK {
		t.Fatalf("read answered %d, %v", read.Code, readErr)
	}
	// The retry answers the payment the key opened as a read of it answers,
	// and a payment that has been paid since carries its transfer.
	was, is := decoded(t, read)["transfer"], decoded(t, again)["transfer"]
	if is == nil {
		t.Fatalf("the retry carries no transfer:\n%s", again.Body)
	}
	if !reflect.DeepEqual(was, is) {
		t.Errorf("the retry carries %v, want what the read answers: %v", is, was)
	}
}

// transferred makes a payment payable and puts a matched transfer on it, the
// way a round of the chain would.
func transferred(t *testing.T, f *handler, id payment.ID) {
	t.Helper()
	p, at, err := f.store.Find(t.Context(), first, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Await(); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Save(t.Context(), first, p, at, payment.Event{}); err != nil {
		t.Fatal(err)
	}
	a, err := payment.NewAttempt(p, p.CreatedAt())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Issue(t.Context(), first, a); err != nil {
		t.Fatal(err)
	}
	hits, err := f.store.Consumed(t.Context(), p.Network(), []string{a.Key()})
	if err != nil || len(hits) != 1 {
		t.Fatalf("Consumed found %d attempts: %v", len(hits), err)
	}
	if err := recordingAt(t, f.store, f.pool, now, 1, 20, false,
		seenAt(hits[0], "0xtx", 10, payment.Matched)); err != nil {
		t.Fatal(err)
	}
}
