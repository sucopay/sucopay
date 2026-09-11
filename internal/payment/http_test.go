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
	"strings"
	"testing"
	"testing/iotest"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sucopay/sucopay/internal/payment"
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
		h:        payment.NewHTTP(svc, listed{"jpyc": jpyc(t), "usdc": usdc(t)}, accepted),
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

	e, err := payment.Announce(p)

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
	if asked := decoded(t, read); !reflect.DeepEqual(told, asked) {
		t.Errorf("Payload:\n%s\nwant what read answered:\n%s", e.Payload, read.Body)
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

	e, err := payment.Announce(p)

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
	e, err := payment.Announce(p)
	if err != nil {
		t.Fatal(err)
	}

	err = s.Save(t.Context(), first, p, at, e)

	if err != nil {
		t.Errorf("Save = %v, want the event taken", err)
	}
}
