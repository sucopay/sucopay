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
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sucopay/sucopay/internal/credential"
	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
	"github.com/sucopay/sucopay/internal/problem"
)

// These tests serve three routes of their own, over a payment repository and
// registered as Handler registers the instance's, rather than the instance's
// routes. What admit promises is that the account a credential names is the
// account a handler asks the repository about, and only a repository at the
// end of a route shows that; the instance's routes end in a [Payments] that a
// test of them stands in for.

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
	log      journal
	logger   *slog.Logger
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

// journal is the lines a logger wrote. A test that serves over a listener
// has them written on the serving goroutine, so the buffer is guarded.
type journal struct {
	mu    sync.Mutex
	lines bytes.Buffer
}

func (j *journal) Write(b []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.lines.Write(b)
}

func (j *journal) String() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.lines.String()
}

func (j *journal) Reset() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.lines.Reset()
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
	f.logger = log
	a := auth{log: log, credentials: credentials}
	// No attempts and no positions: the routes these tests admit or refuse
	// open, read and await payments, and none of them issues.
	service := payment.NewService(f.payments, nil, nil, nil, func() time.Time { return now })
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
		problem.JSON(w, http.StatusCreated, map[string]string{"id": string(p.ID())})
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
		problem.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
func (f *fixture) created(account credential.AccountID, access credential.Access) credential.Token {
	f.t.Helper()
	_, token, err := f.store.Create(f.t.Context(), account, access, now)
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
		insert into credentials (id, account_id, scope, access, hash, key_id, created_at)
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

func TestAdmit_AsksAHeadRequestForWhatItAsksAGet(t *testing.T) {
	t.Parallel()
	// The mux serves HEAD with the handler of the GET pattern, and admit asks
	// the same of it: a body left out is a payment read all the same.
	f := served(t)
	token := f.created(first, credential.ReadOnly)
	at := f.stored(first)

	f.expect(f.do(http.MethodHead, at, ""), http.StatusUnauthorized, false)
	f.expect(f.do(http.MethodHead, at, bearing(token)), http.StatusOK, true)
}

// held is a credential store holding one credential, which any token
// presents, and recording no use.
type held credential.Credential

func (h held) FindByToken(context.Context, credential.Token) (credential.Credential, error) {
	return credential.Credential(h), nil
}

func (held) RecordUse(context.Context, credential.Credential, time.Time) error { return nil }

func (h held) InForce(context.Context) (credential.InForce, error) {
	if h.Access == credential.ReadWrite {
		return credential.ReadWriteInForce, nil
	}
	return credential.ReadOnlyInForce, nil
}

func TestAdmit_TreatsAnAccessNoRouteMayStateAsTheStrictest(t *testing.T) {
	t.Parallel()
	// A route literal that leaves needs out carries the zero access, and the
	// test of the table refuses it. Past that test, it admits what a route
	// that writes admits, and no more.
	readOnly := held{Scope: credential.ScopeAccount, Account: first, Access: credential.ReadOnly}
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

// recorded is a credential store holding one credential, which any token
// presents, and keeping every use it is told of. When set, lookup is what
// finding the credential fails with, and refusal what recording a use does.
type recorded struct {
	credential credential.Credential
	lookup     error
	refusal    error
	uses       []credential.Credential
	at         []time.Time
}

func (s *recorded) FindByToken(context.Context, credential.Token) (credential.Credential, error) {
	return s.credential, s.lookup
}

func (s *recorded) RecordUse(_ context.Context, c credential.Credential, now time.Time) error {
	s.uses = append(s.uses, c)
	s.at = append(s.at, now)
	return s.refusal
}

func (s *recorded) InForce(ctx context.Context) (credential.InForce, error) {
	return held(s.credential).InForce(ctx)
}

func TestAdmit_RecordsTheUseOfEveryCredentialItFinds(t *testing.T) {
	t.Parallel()
	// Found is used, whether or not the route admits it: a read-only
	// credential tried on a route that writes is in someone's hands, and
	// whose hands credentials are in is what last_used_at is read to learn.
	// A token nobody issued has no row to be recorded in.
	f := served(t)
	at := f.stored(first)
	readOnly := credential.Credential{ID: credential.NewID(), Scope: credential.ScopeAccount, Account: first, Access: credential.ReadOnly, KeyID: keyID, LastUsedAt: now}
	for _, c := range []struct {
		name   string
		store  *recorded
		method string
		target string
		status int
		uses   int
	}{
		{"a credential the route admits", &recorded{credential: readOnly}, http.MethodGet, at, http.StatusOK, 1},
		{"a credential the route refuses", &recorded{credential: readOnly}, http.MethodPost, "/payments", http.StatusForbidden, 1},
		{"a token nobody issued", &recorded{lookup: credential.ErrNotFound}, http.MethodGet, at, http.StatusUnauthorized, 0},
	} {
		f.serve(c.store)
		before := time.Now()

		f.expect(f.do(c.method, c.target, bearing(credential.New())), c.status, c.status == http.StatusOK)

		if len(c.store.uses) != c.uses {
			t.Errorf("%s: %d uses recorded, want %d", c.name, len(c.store.uses), c.uses)
			continue
		}
		for i, use := range c.store.uses {
			if use != readOnly {
				t.Errorf("%s: recorded a use of %+v, want of the credential found, %+v", c.name, use, readOnly)
			}
			if at := c.store.at[i]; at.Before(before) || at.After(time.Now()) {
				t.Errorf("%s: recorded a use at %s, want the time the request was served", c.name, at)
			}
		}
	}
}

func TestAdmit_ServesARequestWhoseUseItCouldNotRecord(t *testing.T) {
	t.Parallel()
	// When a credential was last used is read by whoever runs the
	// deployment, and a request is not wrong for going unrecorded. The
	// reason goes to the log, under the request's identifier.
	f := served(t)
	at := f.stored(first)
	f.serve(&recorded{
		credential: credential.Credential{ID: credential.NewID(), Scope: credential.ScopeAccount, Account: first, Access: credential.ReadOnly, KeyID: keyID},
		refusal:    errors.New("no connection was free"),
	})
	token := credential.New()

	f.expect(f.do(http.MethodGet, at, bearing(token)), http.StatusOK, true)

	line := f.line("could not record the credential's use")
	for _, want := range []string{"no connection was free", "request_id="} {
		if !strings.Contains(line, want) {
			t.Errorf("the line does not carry %s:\n%s", want, line)
		}
	}
	if strings.Contains(f.log.String(), string(token)) {
		t.Errorf("the log carries the token:\n%s", f.log.String())
	}
}

// line is the one line of the log that carries text, and fails the test
// when none or more than one does.
func (f *fixture) line(text string) string {
	f.t.Helper()
	var found []string
	for _, line := range strings.Split(strings.TrimSpace(f.log.String()), "\n") {
		if strings.Contains(line, text) {
			found = append(found, line)
		}
	}
	if len(found) != 1 {
		f.t.Fatalf("%d lines carry %q, want 1:\n%s", len(found), text, f.log.String())
	}
	return found[0]
}

// lastUsed reads when the credential token presents was last used.
func (f *fixture) lastUsed(token credential.Token) time.Time {
	f.t.Helper()
	c, err := f.store.FindByToken(f.t.Context(), token)
	if err != nil {
		f.t.Fatal(err)
	}
	return c.LastUsedAt
}

func TestAdmit_RecordsAUseOnceAnHourOverTheStore(t *testing.T) {
	t.Parallel()
	// Over the store itself: the first request records its use, another
	// within the hour does not move it, and one an hour on records again.
	f := served(t)
	token := f.created(first, credential.ReadOnly)
	at := f.stored(first)
	// The column holds microseconds.
	before := time.Now().Truncate(time.Microsecond)

	f.expect(f.do(http.MethodGet, at, bearing(token)), http.StatusOK, true)
	usedAt := f.lastUsed(token)
	if usedAt.Before(before) {
		t.Errorf("last used at %s, want the time of the request, not before %s", usedAt, before)
	}

	f.expect(f.do(http.MethodGet, at, bearing(token)), http.StatusOK, true)
	if got := f.lastUsed(token); !got.Equal(usedAt) {
		t.Errorf("a second request within the hour moved last used from %s to %s", usedAt, got)
	}

	c, err := f.store.FindByToken(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `update credentials set last_used_at = $2 where id = $1`, c.ID, usedAt.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	f.expect(f.do(http.MethodGet, at, bearing(token)), http.StatusOK, true)
	if got := f.lastUsed(token); !got.After(usedAt) {
		t.Errorf("a request an hour on left last used at %s", got)
	}
}

// serverWait is how long a test waits for the server it started to stop.
// Without it a server that did not stop would hold the package until the
// go test deadline.
const serverWait = 15 * time.Second

// serving serves the routes over a port the kernel chooses, the way a
// deployment is served. A panic in a handler is then net/http's to recover
// and record. The server stops with the test.
func (f *fixture) serving() *Server {
	f.t.Helper()
	s, err := Listen("127.0.0.1:0", f.handler, f.logger)
	if err != nil {
		f.t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(f.t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	f.t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				f.t.Errorf("run: %v", err)
			}
		case <-time.After(serverWait):
			f.t.Error("the server did not stop after its context was cancelled")
		}
	})
	return s
}

// fell is what the store panics with in the test below, and what the record
// of the panic is looked for by.
const fell = "the store fell over"

// panicking is a store that panics when one token is looked up, as a store
// over a database might for a reason nothing here foresaw.
type panicking struct {
	Credentials
	on credential.Token
}

func (p panicking) FindByToken(ctx context.Context, token credential.Token) (credential.Credential, error) {
	if token == p.on {
		panic(fell)
	}
	return p.Credentials.FindByToken(ctx, token)
}

func TestAdmit_KeepsWhatWasPresentedOutOfEveryRecord(t *testing.T) {
	t.Parallel()
	// Three requests are served over a listener, so that the third is
	// recorded as a deployment records it: net/http recovers the panic and
	// writes it, with the stack, to the server's log. The log is read once
	// all three were answered.
	f := served(t)
	live := f.created(first, credential.ReadWrite)
	unissued := credential.New()
	doomed := credential.New()
	at := f.stored(first)
	f.serve(panicking{Credentials: f.store, on: doomed})
	url := "http://" + f.serving().Addr() + at
	client := &http.Client{Timeout: serverWait}
	for _, tc := range []struct {
		path   string
		token  credential.Token
		status int
	}{
		{"failure", unissued, http.StatusUnauthorized},
		{"success", live, http.StatusOK},
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", bearing(tc.token))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Fatalf("%s was answered %d, want %d", tc.path, resp.StatusCode, tc.status)
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", bearing(doomed))
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
		t.Fatalf("the request whose lookup panicked was answered %d, want the connection closed", resp.StatusCode)
	}

	record := f.log.String()
	for _, want := range []string{"panic serving", fell} {
		if !strings.Contains(record, want) {
			t.Fatalf("the log does not record the panic by %q:\n%s", want, record)
		}
	}
	for _, token := range []credential.Token{unissued, live, doomed} {
		if strings.Contains(record, string(token)) {
			t.Errorf("the log carries a token that was presented:\n%s", record)
		}
	}
}
