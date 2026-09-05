package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/credential"
)

// These tests run serve and ask it over its listener, as a merchant's server
// does: with a token credential new wrote, for a payment in an asset that
// asset accept recorded a destination for. What reached the database, and
// what the log says, is read from outside.

// served is serve running over a deployment whose one account accepts jpyc
// paid to theAddress.
type served struct {
	deployment
	base string
	halt func() string
	once sync.Once
	log  string
}

// payable starts serve over a deployment listing jpyc, accepted and paid to
// theAddress. more is written after the document's assets section.
func payable(t *testing.T, more string) *served {
	t.Helper()
	d := deployed(t)
	port := freePort(t)
	document(t, fmt.Sprintf("listen:\n  port: %d\n%s%s%s", port, namingADatabase(), anAssetOn("local", ""), more))
	if _, _, err := runArgs(t, "asset", "accept", "jpyc", theAddress); err != nil {
		t.Fatal(err)
	}
	_, halt := serving(t)
	s := &served{deployment: d, base: fmt.Sprintf("http://127.0.0.1:%d", port), halt: halt}
	t.Cleanup(func() { s.stop() })
	return s
}

// stop stops serve, once, and returns everything it wrote.
func (s *served) stop() string {
	s.once.Do(func() { s.log = s.halt() })
	return s.log
}

// answered is what one request was answered with.
type answered struct {
	status   int
	location string
	body     []byte
}

// object reads the body as the JSON object it is.
func (a answered) object(t *testing.T) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(a.body, &body); err != nil {
		t.Fatalf("the body is not a JSON object: %v\n%s", err, a.body)
	}
	return body
}

// ask sends one request. token is the credential presented, or nothing.
func (s *served) ask(t *testing.T, method, path string, token credential.Token, body string) answered {
	t.Helper()
	r, err := http.NewRequestWithContext(t.Context(), method, s.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+string(token))
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	read, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return answered{resp.StatusCode, resp.Header.Get("Location"), read}
}

// create asks for the payment body describes and returns what it was
// answered with, which has to be the payment.
func (s *served) create(t *testing.T, token credential.Token, body string) answered {
	t.Helper()
	a := s.ask(t, http.MethodPost, "/payments", token, body)
	if a.status != http.StatusCreated {
		t.Fatalf("POST /payments = %d, want %d:\n%s", a.status, http.StatusCreated, a.body)
	}
	if !strings.HasPrefix(a.location, "/payments/") {
		t.Fatalf("Location = %q, want the payment's path", a.location)
	}
	return a
}

// aPayment is a body asking for a payment nothing is wrong with.
const aPayment = `{"asset":"jpyc","amount":"1000"}`

// paymentRows counts the payments written, under any account.
func paymentRows(t *testing.T, d deployment) int {
	t.Helper()
	var n int
	if err := d.pool.Conns().QueryRow(t.Context(), `select count(*) from payments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestServe_RefusesAPaymentFromNobodyAndWritesNothing(t *testing.T) {
	s := payable(t, "")

	a := s.ask(t, http.MethodPost, "/payments", "", aPayment)

	if a.status != http.StatusUnauthorized {
		t.Errorf("POST /payments = %d, want %d", a.status, http.StatusUnauthorized)
	}
	if n := paymentRows(t, s.deployment); n != 0 {
		t.Errorf("%d payments written for nobody, want none", n)
	}
}

func TestServe_ACredentialThatReadsReadsAPaymentItMayNotCreate(t *testing.T) {
	s := payable(t, "")
	_, writes := newCredential(t, s.deployment, "--read-write")
	_, reads := newCredential(t, s.deployment, "--read-only")
	created := s.create(t, writes, aPayment)

	refused := s.ask(t, http.MethodPost, "/payments", reads, aPayment)
	read := s.ask(t, http.MethodGet, created.location, reads, "")

	if refused.status != http.StatusForbidden {
		t.Errorf("POST /payments with a credential that reads = %d, want %d", refused.status, http.StatusForbidden)
	}
	if read.status != http.StatusOK {
		t.Errorf("GET %s with a credential that reads = %d, want %d:\n%s", created.location, read.status, http.StatusOK, read.body)
	}
	if !bytes.Equal(read.body, created.body) {
		t.Errorf("read back %s, want what was created: %s", read.body, created.body)
	}
	if n := paymentRows(t, s.deployment); n != 1 {
		t.Errorf("%d payments written, want the one", n)
	}
}

func TestServe_AnswersAnotherAccountsPaymentAsItAnswersNone(t *testing.T) {
	s := payable(t, "")
	_, first := newCredential(t, s.deployment, "--read-write")
	created := s.create(t, first, aPayment)
	// A second account, and a credential of it, written the way the schema
	// writes the first: nothing issues an account yet.
	const other = credential.AccountID("00000000-0000-0000-0000-000000000002")
	if _, err := s.pool.Conns().Exec(t.Context(), `insert into accounts (id, name) values ($1, 'second')`, other); err != nil {
		t.Fatal(err)
	}
	_, second, err := s.store.Create(t.Context(), other, credential.ReadWrite, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	theirs := s.ask(t, http.MethodGet, created.location, second, "")
	nobodys := s.ask(t, http.MethodGet, "/payments/"+strings.Repeat("f", 32), second, "")

	if theirs.status != http.StatusNotFound {
		t.Errorf("GET %s as the other account = %d, want %d", created.location, theirs.status, http.StatusNotFound)
	}
	if nobodys.status != http.StatusNotFound {
		t.Errorf("GET a payment nobody has = %d, want %d", nobodys.status, http.StatusNotFound)
	}
	if !bytes.Equal(theirs.body, nobodys.body) {
		t.Errorf("another account's payment is answered with %s and no payment with %s; want the same body", theirs.body, nobodys.body)
	}
}

func TestServe_RefusesAnAssetTheAccountDoesNotAcceptNamingTheField(t *testing.T) {
	s := payable(t, "  usdc:\n    network: local\n"+
		"    reference: \"0x0000000000000000000000000000000000000002\"\n"+
		"    symbol: USDC\n    decimals: 6\n")
	_, writes := newCredential(t, s.deployment, "--read-write")

	a := s.ask(t, http.MethodPost, "/payments", writes, `{"asset":"usdc","amount":"1000"}`)

	if a.status != http.StatusBadRequest {
		t.Errorf("POST /payments in an asset not accepted = %d, want %d:\n%s", a.status, http.StatusBadRequest, a.body)
	}
	body := a.object(t)
	problems, _ := body["problems"].([]any)
	var fields []string
	for _, p := range problems {
		problem, _ := p.(map[string]any)
		fields = append(fields, fmt.Sprint(problem["field"]))
	}
	if !slices.Contains(fields, "asset") {
		t.Errorf("problems name the fields %q, want asset among them:\n%s", fields, a.body)
	}
	if n := paymentRows(t, s.deployment); n != 0 {
		t.Errorf("%d payments written in an asset not accepted, want none", n)
	}
}

func TestServe_AcceptingAnAssetAgainMovesNewPaymentsAndLeavesOldOnes(t *testing.T) {
	const elsewhere = "0x00000000000000000000000000000000000000bb"
	s := payable(t, "")
	_, writes := newCredential(t, s.deployment, "--read-write")
	before := s.create(t, writes, aPayment)

	if _, _, err := runArgs(t, "asset", "accept", "jpyc", elsewhere); err != nil {
		t.Fatal(err)
	}
	old := s.ask(t, http.MethodGet, before.location, writes, "")
	after := s.create(t, writes, aPayment)

	if old.status != http.StatusOK {
		t.Fatalf("GET %s = %d, want %d:\n%s", before.location, old.status, http.StatusOK, old.body)
	}
	if got := old.object(t)["destination"]; got != theAddress {
		t.Errorf("a payment made before the asset was accepted again is paid to %v, want %s", got, theAddress)
	}
	if got := after.object(t)["destination"]; got != elsewhere {
		t.Errorf("a payment made after is paid to %v, want %s", got, elsewhere)
	}
}

// requestID reads the request_id of a text log line.
var requestID = regexp.MustCompile(`request_id=(\S+)`)

func TestServe_AnswersUnavailableWhenThePaymentsTableIsGoneAndLogsWhy(t *testing.T) {
	s := payable(t, "")
	_, writes := newCredential(t, s.deployment, "--read-write")
	created := s.create(t, writes, aPayment)
	// serve's pool cannot be closed from here, so the database is taken away
	// from under it. credentials stay, so the request is admitted and it is
	// the payment that cannot be served.
	if _, err := s.pool.Conns().Exec(t.Context(), `drop table payments`); err != nil {
		t.Fatal(err)
	}

	posted := s.ask(t, http.MethodPost, "/payments", writes, aPayment)
	got := s.ask(t, http.MethodGet, created.location, writes, "")
	log := s.stop()

	for name, a := range map[string]answered{"POST /payments": posted, "GET " + created.location: got} {
		if a.status != http.StatusServiceUnavailable {
			t.Errorf("%s = %d, want %d", name, a.status, http.StatusServiceUnavailable)
		}
		if strings.TrimSpace(string(a.body)) != `{"error":"unavailable"}` {
			t.Errorf("%s answered %s, want the one word and nothing of the reason", name, a.body)
		}
	}
	// The line saying why, and the line saying the request was served, are
	// about the same request, and the log says so.
	var reasons, served []string
	for _, line := range strings.Split(log, "\n") {
		id := requestID.FindStringSubmatch(line)
		switch {
		case id == nil:
		case strings.Contains(line, "could not serve the request"):
			reasons = append(reasons, id[1])
		case strings.Contains(line, "status=503"):
			served = append(served, id[1])
		}
	}
	if len(reasons) != 2 || len(served) != 2 {
		t.Fatalf("the log has %d lines giving a reason and %d lines serving a 503, want 2 and 2:\n%s", len(reasons), len(served), log)
	}
	for i := range reasons {
		if reasons[i] != served[i] {
			t.Errorf("reason %d is under request %s, and the 503 under %s", i, reasons[i], served[i])
		}
	}
	if !strings.Contains(log, "payments") {
		t.Errorf("the log does not say what could not be reached:\n%s", log)
	}
}

func TestServe_KeepsMetadataOutOfTheLog(t *testing.T) {
	// A value a merchant puts in metadata is theirs: an order number, or a
	// customer's. It is answered back to whoever made the payment, and reaches
	// nothing else, whatever the request came to.
	const value = "order-4711-for-somebody"
	s := payable(t, "log:\n  format: json\n")
	_, writes := newCredential(t, s.deployment, "--read-write")
	carrying := func(amount string) string {
		return fmt.Sprintf(`{"asset":"jpyc","amount":%q,"metadata":{"order":%q}}`, amount, value)
	}

	created := s.ask(t, http.MethodPost, "/payments", writes, carrying("1000"))
	refused := s.ask(t, http.MethodPost, "/payments", writes, carrying("-1"))
	if _, err := s.pool.Conns().Exec(t.Context(), `drop table payments`); err != nil {
		t.Fatal(err)
	}
	failed := s.ask(t, http.MethodPost, "/payments", writes, carrying("1000"))
	log := s.stop()

	for name, c := range map[string]struct {
		a    answered
		want int
	}{"a payment": {created, http.StatusCreated}, "a refused body": {refused, http.StatusBadRequest}, "a database gone": {failed, http.StatusServiceUnavailable}} {
		if c.a.status != c.want {
			t.Errorf("%s = %d, want %d:\n%s", name, c.a.status, c.want, c.a.body)
		}
	}
	if !strings.Contains(string(created.body), value) {
		t.Errorf("the payment does not carry its metadata back:\n%s", created.body)
	}
	if strings.Contains(log, value) {
		t.Errorf("a metadata value reached the log:\n%s", log)
	}
	if !strings.Contains(log, `"msg":"served"`) {
		t.Errorf("the log is not the JSON the document asked for:\n%s", log)
	}
}
