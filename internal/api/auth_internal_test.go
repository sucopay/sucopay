package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sucopay/sucopay/internal/credential"
	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
)

// The instance serves no route yet that asks for a credential, so these tests
// serve three of their own, over a payment repository and registered as
// Handler registers the instance's. What admit promises is that the account a
// credential names is the account a handler asks the repository about, and
// only a repository at the end of a route shows that.

// The account the schema creates, and a second one made here. One account can
// never show that a credential keeps a request to its own.
const (
	first = credential.AccountID("00000000-0000-0000-0000-000000000001")
	other = credential.AccountID("00000000-0000-0000-0000-000000000002")
)

// key is 32 bytes any reader can see, and keyID is what the store under test
// is configured with.
const (
	key   = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	keyID = "k1"
)

var now = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

// fixture is the routes over a database of the test's own.
type fixture struct {
	t        *testing.T
	log      bytes.Buffer
	pool     *pgxpool.Pool
	store    *credential.Postgres
	payments *payment.Postgres
	handler  http.Handler
	// reached counts the requests a handler saw, and seen is the credential
	// the last of them found in its context. Handlers write these and tests
	// read them on one goroutine: a recorder serves a request in the caller.
	reached int
	seen    credential.Credential
}

// served returns the routes over a migrated database with both accounts in
// it, and credentials looked up in that database.
func served(t *testing.T) *fixture {
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
		`insert into accounts (id, name) values ($1, 'second')`, other); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, pool: pool.Conns(), payments: payment.NewPostgres(pool.Conns())}
	f.store = credential.NewPostgres(f.pool, parsed(t), keyID)
	f.serve(f.store)
	return f
}

// serve registers the routes behind credentials, as Handler registers the
// instance's, and starts the log over.
func (f *fixture) serve(credentials Credentials) {
	f.log.Reset()
	log := slog.New(slog.NewTextHandler(&f.log, nil))
	a := auth{log: log, credentials: credentials}
	service := payment.NewService(f.payments, func() time.Time { return now })
	mux := http.NewServeMux()
	for _, r := range []route{
		{pattern: "POST /payments", needs: write, handle: f.opening(service)},
		{pattern: "GET /payments/{id}", needs: read, handle: f.finding()},
		{pattern: "POST /payments/{id}/await", needs: write, handle: f.awaiting(service)},
	} {
		mux.HandleFunc(r.pattern, a.admit(r.needs, r.handle))
	}
	f.handler = record(log, mux)
}

// account reads which account a request is from, as a handler of the
// instance will: from the credential in the context, converted, since a
// credential says which account and a payment is of one.
func (f *fixture) account(w http.ResponseWriter, r *http.Request) (payment.AccountID, bool) {
	f.reached++
	c, ok := credential.FromContext(r.Context())
	if !ok {
		http.Error(w, "no credential in the context", http.StatusInternalServerError)
		return "", false
	}
	f.seen = c
	return payment.AccountID(c.Account), true
}

// opening opens a payment of the account and answers its identifier.
func (f *fixture) opening(service *payment.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		account, ok := f.account(w, r)
		if !ok {
			return
		}
		p, err := service.Open(r.Context(), account, request(f.t))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"id": string(p.ID())})
	}
}

// finding reads one payment of the account.
func (f *fixture) finding() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		account, ok := f.account(w, r)
		if !ok {
			return
		}
		_, _, err := f.payments.Find(r.Context(), account, payment.ID(r.PathValue("id")))
		answer(w, err)
	}
}

// awaiting moves one payment of the account on to awaiting payment: a write
// to a payment that exists, which opening is not.
func (f *fixture) awaiting(service *payment.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		account, ok := f.account(w, r)
		if !ok {
			return
		}
		_, err := service.Await(r.Context(), account, payment.ID(r.PathValue("id")))
		answer(w, err)
	}
}

// answer is what a handler says about what the repository said. That a
// payment of another account is not found rather than forbidden is the
// repository's promise, tested with it.
func answer(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, payment.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// do serves one request, with authorization in the header of that name, or
// with no such header when it is empty.
func (f *fixture) do(method, target, authorization string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, r)
	return rec
}

// bearing is the header a client presents token under.
func bearing(token credential.Token) string { return "Bearer " + string(token) }

// created issues a credential of account and returns its token.
func (f *fixture) created(account credential.AccountID, capability credential.Capability) credential.Token {
	f.t.Helper()
	_, token, err := f.store.Create(f.t.Context(), account, capability, now)
	if err != nil {
		f.t.Fatal(err)
	}
	return token
}

// revoked issues a credential of account and revokes it, and returns the
// token that was its.
func (f *fixture) revoked(account credential.AccountID) credential.Token {
	f.t.Helper()
	id, token, err := f.store.Create(f.t.Context(), account, credential.ReadWrite, now)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.Revoke(f.t.Context(), id, now); err != nil {
		f.t.Fatal(err)
	}
	return token
}

// stored writes a payment of account straight into the repository, past
// every route, and returns the path it is at.
func (f *fixture) stored(account credential.AccountID) string {
	f.t.Helper()
	p, err := payment.New(request(f.t), now)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.payments.Create(f.t.Context(), payment.AccountID(account), p); err != nil {
		f.t.Fatal(err)
	}
	return "/payments/" + string(p.ID())
}

// rows counts the payments in the database, of every account.
func (f *fixture) rows() int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(f.t.Context(), `select count(*) from payments`).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// request is a payment that opens.
func request(t *testing.T) payment.Request {
	t.Helper()
	asset, err := payment.NewAsset("polygon", "jpyc-contract", "JPYC", 18)
	if err != nil {
		t.Fatal(err)
	}
	amount, err := payment.ParseMoney(asset, "1000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	destination, err := payment.ParseAddress("0x" + strings.Repeat("ab", 20))
	if err != nil {
		t.Fatal(err)
	}
	return payment.Request{Amount: amount, Destination: destination, ExpiresAt: now.Add(time.Hour)}
}

// parsed is key as the store holds it.
func parsed(t *testing.T) credential.Key {
	t.Helper()
	k, err := credential.ParseKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// expect fails the test unless the response has the status, and unless the
// handler was or was not reached as said. It then starts the count over,
// so that each request a test serves is judged on its own.
func (f *fixture) expect(rec *httptest.ResponseRecorder, status int, reached bool) {
	f.t.Helper()
	if rec.Code != status {
		f.t.Errorf("status %d, want %d; body %s", rec.Code, status, rec.Body)
	}
	if (f.reached > 0) != reached {
		f.t.Errorf("the handler was reached %d times, want reached: %v", f.reached, reached)
	}
	f.reached = 0
}

func TestAdmit_AnswersEveryCredentialItDoesNotFindAlike(t *testing.T) {
	t.Parallel()
	// The header names the scheme and no error, and the body has no reason
	// in it: which of these a request was is what whoever guesses at a token
	// wants to learn from the answer.
	f := served(t)
	revoked := f.revoked(first)
	_, underAnotherKey, err := credential.NewPostgres(f.pool, parsed(t), "k2").Create(t.Context(), first, credential.ReadWrite, now)
	if err != nil {
		t.Fatal(err)
	}
	live := f.created(first, credential.ReadWrite)
	at := f.stored(first)

	want := f.do(http.MethodGet, at, "")
	f.expect(want, http.StatusUnauthorized, false)
	if got := want.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Errorf("WWW-Authenticate is %q, want Bearer", got)
	}
	for _, c := range []struct {
		name          string
		authorization string
	}{
		{"another scheme", "Basic " + string(live)},
		{"the scheme alone", "Bearer"},
		{"the scheme and nothing after the space", "Bearer "},
		{"a tab between the scheme and the token", "Bearer\t" + string(live)},
		{"not the shape of a token", "Bearer not-a-token"},
		{"a token nobody issued", bearing(credential.New())},
		{"a revoked token", bearing(revoked)},
		{"a token made under another key", bearing(underAnotherKey)},
		{"the token spelled in capitals", bearing(credential.Token(strings.ToUpper(string(live))))},
		{"the token behind a second space", "Bearer  " + string(live)},
	} {
		got := f.do(http.MethodGet, at, c.authorization)
		f.expect(got, http.StatusUnauthorized, false)
		if got.Code != want.Code || got.Body.String() != want.Body.String() || !headersEqual(got.Header(), want.Header()) {
			t.Errorf("%s is answered otherwise than no credential:\n%d %v %s\nwant\n%d %v %s",
				c.name, got.Code, got.Header(), got.Body, want.Code, want.Header(), want.Body)
		}
	}
	if strings.Contains(f.log.String(), string(live)) {
		t.Errorf("the log carries the token:\n%s", f.log.String())
	}
}

func headersEqual(a, b http.Header) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if strings.Join(v, "\n") != strings.Join(b[k], "\n") {
			return false
		}
	}
	return true
}

func TestAdmit_PutsTheCredentialPresentedInTheContext(t *testing.T) {
	t.Parallel()
	f := served(t)
	for _, account := range []credential.AccountID{first, other} {
		token := f.created(account, credential.ReadOnly)
		want, err := f.store.FindByToken(t.Context(), token)
		if err != nil {
			t.Fatal(err)
		}

		f.expect(f.do(http.MethodGet, f.stored(account), bearing(token)), http.StatusOK, true)

		if f.seen != want {
			t.Errorf("the handler found %+v in the context, want %+v", f.seen, want)
		}
	}
}

func TestAdmit_RefusesAReadOnlyCredentialWhereARouteWritesAndPassesItWhereOneReads(t *testing.T) {
	t.Parallel()
	f := served(t)
	readOnly := f.created(first, credential.ReadOnly)
	readWrite := f.created(first, credential.ReadWrite)
	at := f.stored(first)

	f.expect(f.do(http.MethodPost, "/payments", bearing(readOnly)), http.StatusForbidden, false)
	f.expect(f.do(http.MethodPost, at+"/await", bearing(readOnly)), http.StatusForbidden, false)
	if n := f.rows(); n != 1 {
		t.Errorf("%d payments after a read-only credential asked to open one, want 1", n)
	}
	f.expect(f.do(http.MethodGet, at, bearing(readOnly)), http.StatusOK, true)

	f.expect(f.do(http.MethodPost, "/payments", bearing(readWrite)), http.StatusCreated, true)
	f.expect(f.do(http.MethodPost, at+"/await", bearing(readWrite)), http.StatusOK, true)
	f.expect(f.do(http.MethodGet, at, bearing(readWrite)), http.StatusOK, true)
}

func TestAdmit_ReachesNoPaymentWithoutACredential(t *testing.T) {
	t.Parallel()
	f := served(t)
	at := f.stored(first)

	f.expect(f.do(http.MethodGet, at, ""), http.StatusUnauthorized, false)
	f.expect(f.do(http.MethodPost, "/payments", ""), http.StatusUnauthorized, false)
	f.expect(f.do(http.MethodPost, at+"/await", ""), http.StatusUnauthorized, false)

	if n := f.rows(); n != 1 {
		t.Errorf("%d payments after requests with no credential, want the 1 the test made", n)
	}
}

func TestAdmit_KeepsOneAccountsCredentialOutOfAnothersPayments(t *testing.T) {
	t.Parallel()
	f := served(t)
	mine := f.created(first, credential.ReadWrite)
	theirs := f.created(other, credential.ReadWrite)
	at := f.stored(other)

	f.expect(f.do(http.MethodGet, at, bearing(mine)), http.StatusNotFound, true)
	f.expect(f.do(http.MethodPost, at+"/await", bearing(mine)), http.StatusNotFound, true)
	f.expect(f.do(http.MethodGet, at, bearing(theirs)), http.StatusOK, true)

	// A payment opened with one account's credential is that account's: the
	// other's credential does not reach it either.
	opened := f.do(http.MethodPost, "/payments", bearing(mine))
	f.expect(opened, http.StatusCreated, true)
	var body map[string]string
	if err := json.Unmarshal(opened.Body.Bytes(), &body); err != nil {
		t.Fatalf("the body is not JSON: %v (%q)", err, opened.Body.String())
	}
	f.expect(f.do(http.MethodGet, "/payments/"+body["id"], bearing(theirs)), http.StatusNotFound, true)
	f.expect(f.do(http.MethodGet, "/payments/"+body["id"], bearing(mine)), http.StatusOK, true)
}

func TestAdmit_RefusesACredentialOfTheDeployment(t *testing.T) {
	t.Parallel()
	// Nothing issues one, so the row is written by hand. Every route that
	// asks for a credential is a route of an account, and a credential of
	// none names no account for it to be of.
	f := served(t)
	token := credential.New()
	if _, err := f.pool.Exec(t.Context(), `
		insert into credentials (id, account_id, scope, capability, hash, key_id, created_at)
		values ($1, null, $2, $3, $4, $5, $6)`,
		credential.NewID(), credential.ScopeDeployment, credential.ReadWrite, credential.Hash(parsed(t), token), keyID, now); err != nil {
		t.Fatal(err)
	}
	at := f.stored(first)

	f.expect(f.do(http.MethodGet, at, bearing(token)), http.StatusForbidden, false)
	f.expect(f.do(http.MethodPost, "/payments", bearing(token)), http.StatusForbidden, false)
	if n := f.rows(); n != 1 {
		t.Errorf("%d payments after a credential of the deployment asked to open one, want 1", n)
	}
}

func TestAdmit_RefusesEveryCredentialWhereNoDatabaseHoldsAny(t *testing.T) {
	t.Parallel()
	// An instance configured without a database looks credentials up
	// nowhere, and answers as one whose table is empty would.
	f := served(t)
	token := f.created(first, credential.ReadWrite)
	at := f.stored(first)
	want := f.do(http.MethodGet, at, bearing(credential.New()))
	f.expect(want, http.StatusUnauthorized, false)

	f.serve(nil)
	got := f.do(http.MethodGet, at, bearing(token))

	f.expect(got, http.StatusUnauthorized, false)
	if got.Body.String() != want.Body.String() || !headersEqual(got.Header(), want.Header()) {
		t.Errorf("answered otherwise than a token nobody issued:\n%v %s\nwant\n%v %s",
			got.Header(), got.Body, want.Header(), want.Body)
	}
}

func TestAdmit_ReportsADatabaseItCannotReachAsUnavailable(t *testing.T) {
	t.Parallel()
	// Not 401: the credential was not found to be missing, it was not looked
	// for. The reason goes to the log, under the request's identifier, and
	// not over HTTP.
	f := served(t)
	token := f.created(first, credential.ReadWrite)
	at := f.stored(first)
	f.pool.Close()

	got := f.do(http.MethodGet, at, bearing(token))

	f.expect(got, http.StatusServiceUnavailable, false)
	if body := strings.TrimSpace(got.Body.String()); body != `{"error":"unavailable"}` {
		t.Errorf("body is %s, want the one word", body)
	}
	line := f.log.String()
	for _, want := range []string{"could not look up the credential", "closed pool", "request_id="} {
		if !strings.Contains(line, want) {
			t.Errorf("the log does not carry %s:\n%s", want, line)
		}
	}
	// This is the one line admit writes, and the token was in hand when it
	// was written.
	if strings.Contains(line, string(token)) {
		t.Errorf("the log carries the token:\n%s", line)
	}
}

func TestAdmit_ReadsTheSchemeInEitherCase(t *testing.T) {
	t.Parallel()
	// The scheme is what RFC 9110 lets a client spell as it likes. The token
	// after it is not: see the capitals case among the refusals.
	f := served(t)
	token := f.created(first, credential.ReadOnly)

	f.expect(f.do(http.MethodGet, f.stored(first), "bearer "+string(token)), http.StatusOK, true)
}

// held is a credential store holding one credential, which any token
// presents.
type held credential.Credential

func (h held) FindByToken(context.Context, credential.Token) (credential.Credential, error) {
	return credential.Credential(h), nil
}

func TestAdmit_TreatsAnAccessNoRouteMayStateAsTheStrictest(t *testing.T) {
	t.Parallel()
	// A route literal that leaves needs out carries the zero access, and the
	// test of the table refuses it. Past that test, it admits what a route
	// that writes admits, and no more.
	readOnly := held{Scope: credential.ScopeAccount, Account: first, Capability: credential.ReadOnly}
	a := auth{log: slog.New(slog.NewTextHandler(io.Discard, nil)), credentials: readOnly}
	for _, c := range []struct {
		name  string
		needs access
		want  int
	}{
		{"unstated", unstated, http.StatusForbidden},
		{"read", read, http.StatusOK},
		{"write", write, http.StatusForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Header.Set("Authorization", bearing(credential.New()))

			a.admit(c.needs, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })(rec, r)

			if rec.Code != c.want {
				t.Errorf("status %d, want %d", rec.Code, c.want)
			}
		})
	}
}
