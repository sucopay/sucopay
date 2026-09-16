package credential_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sucopay/sucopay/internal/credential"
	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
)

// The account the schema creates, and a second one made here. One account can
// never show that a credential is of an account.
const (
	first = credential.AccountID("00000000-0000-0000-0000-000000000001")
	other = credential.AccountID("00000000-0000-0000-0000-000000000002")
)

// keyID is the identifier the store under test is configured with.
const keyID = "k1"

// opened returns a migrated database with the second account in it, and the
// pool over it.
func opened(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := postgres.Open(t.Context(), dsn)
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
	return pool.Conns()
}

// store returns a store over a fresh database, and the pool behind it for the
// tests that have to look at a row the store would not return.
func store(t *testing.T) (*credential.Postgres, *pgxpool.Pool) {
	t.Helper()
	pool := opened(t, postgrestest.Fresh(t))
	return credential.NewPostgres(pool, parsed(t, key), keyID), pool
}

// created stores a credential and returns what a caller would hold after
// doing so.
func created(t *testing.T, s *credential.Postgres, account credential.AccountID, access credential.Access, now time.Time) (credential.ID, credential.Token) {
	t.Helper()
	id, token, err := s.Create(t.Context(), account, access, now)
	if err != nil {
		t.Fatal(err)
	}
	return id, token
}

var sometime = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

func TestStore_StoresNothingOfTheToken(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	id, token := created(t, s, first, credential.ReadOnly, sometime)

	// The whole row as text, so that the token is looked for in every column
	// and not in the one it is expected to be missing from.
	var row string
	if err := pool.QueryRow(t.Context(), `select c::text from credentials c where id = $1`, id).Scan(&row); err != nil {
		t.Fatal(err)
	}

	// bytea prints as hexadecimal, so the token's bytes would print as the
	// token, and its text would print as the text in hexadecimal.
	if strings.Contains(row, string(token)) {
		t.Error("the row holds the token")
	}
	if strings.Contains(row, hex.EncodeToString([]byte(token))) {
		t.Error("the row holds the token's text as bytes")
	}
}

func TestStore_StoresTheHashOfTheTokenUnderTheConfiguredKey(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	id, token := created(t, s, first, credential.ReadOnly, sometime)

	var (
		hash  []byte
		under string
	)
	if err := pool.QueryRow(t.Context(), `select hash, key_id from credentials where id = $1`, id).Scan(&hash, &under); err != nil {
		t.Fatal(err)
	}

	if want := credential.Hash(parsed(t, key), token); !bytes.Equal(hash, want) {
		t.Errorf("hash = %x, want %x: the token was not hashed under the configured key", hash, want)
	}
	if under != keyID {
		t.Errorf("key_id = %q, want %q", under, keyID)
	}
}

func TestStore_FindsByTokenWhatCreateMadeForEachAccount(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	ofFirst, tokenOfFirst := created(t, s, first, credential.ReadOnly, sometime)
	ofOther, tokenOfOther := created(t, s, other, credential.ReadWrite, sometime)

	for _, c := range []struct {
		token credential.Token
		want  credential.Credential
	}{
		{tokenOfFirst, credential.Credential{ID: ofFirst, Scope: credential.ScopeAccount, Account: first, Access: credential.ReadOnly, KeyID: keyID}},
		{tokenOfOther, credential.Credential{ID: ofOther, Scope: credential.ScopeAccount, Account: other, Access: credential.ReadWrite, KeyID: keyID}},
	} {
		got, err := s.FindByToken(t.Context(), c.token)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("found %+v, want %+v", got, c.want)
		}
	}
}

func TestStore_ReadsEveryAccountACredentialCanBeOf(t *testing.T) {
	t.Parallel()
	// The one the schema makes and the one the tests add, so that a read
	// stopping at the first row is told apart from one reading them all.
	s, _ := store(t)

	got, err := s.Accounts(t.Context())

	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if want := []credential.AccountID{first, other}; !slices.Equal(got, want) {
		t.Errorf("accounts = %v, want %v", got, want)
	}
}

func TestStore_DoesNotSayWhyATokenIsNotFound(t *testing.T) {
	t.Parallel()
	// Whoever presents a token that is not found learns that and nothing else.
	// A different answer for a revoked one would say which tokens used to
	// work, and one for another key's would say what the deployment was
	// reconfigured from.
	s, pool := store(t)

	revoked, tokenOfRevoked := created(t, s, first, credential.ReadOnly, sometime)
	if err := s.Revoke(t.Context(), revoked, sometime); err != nil {
		t.Fatal(err)
	}
	// The same key under another identifier: the hash matches and the column
	// does not, which is what a deployment whose key_id was retyped sees.
	_, tokenOfAnotherKey := created(t, credential.NewPostgres(pool, parsed(t, key), "k2"), first, credential.ReadOnly, sometime)

	var errs []error
	for _, c := range []struct {
		name  string
		token credential.Token
	}{
		{"revoked", tokenOfRevoked},
		{"made under another key", tokenOfAnotherKey},
		{"nobody issued", credential.New()},
	} {
		_, err := s.FindByToken(t.Context(), c.token)
		if !errors.Is(err, credential.ErrNotFound) {
			t.Fatalf("%s: err = %v, want ErrNotFound", c.name, err)
		}
		errs = append(errs, err)
	}
	for _, err := range errs[1:] {
		if err.Error() != errs[0].Error() {
			t.Errorf("the errors differ, so a caller can tell the cases apart: %q and %q", errs[0], err)
		}
	}
}

func TestStore_KeepsTheTokenOutOfWhatItReports(t *testing.T) {
	t.Parallel()
	// An error is what gets logged, and a request fails with the token in
	// hand. A token in a log is one whoever reads the log holds.
	s, pool := store(t)
	token := credential.New()

	_, notFound := s.FindByToken(t.Context(), token)
	pool.Close()
	_, unreachable := s.FindByToken(t.Context(), token)

	for _, err := range []error{notFound, unreachable} {
		if err == nil {
			t.Fatal("want an error, got none")
		}
		if strings.Contains(err.Error(), string(token)) {
			t.Errorf("the error repeats the token: %v", err)
		}
	}
}

// withOneConnection limits the pool the URL opens to one connection, so that a
// test holding one is holding them all.
func withOneConnection(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("pool_max_conns", "1")
	u.RawQuery = q.Encode()
	return u.String()
}

func TestStore_FailsWithoutReadingWhenNoConnectionIsFree(t *testing.T) {
	t.Parallel()
	// A request is authenticated before anything else, so this wait is what
	// every request pays when the database is behind. It has to end, and it
	// has to end without a connection: one opened for the failure would be
	// one more thing the database is behind on. The other six calls are
	// under the same deadline, so that a list run against a database that
	// is behind ends as well.
	const name = "suco-credential-held-test"
	pool := opened(t, postgrestest.WithName(t, withOneConnection(t, postgrestest.Fresh(t)), name))
	s := credential.NewPostgres(pool, parsed(t, key), keyID)
	id, token := created(t, s, first, credential.ReadOnly, sometime)

	held, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	before := postgrestest.Backends(t, name)
	if before != 1 {
		t.Fatalf("the server sees %d connections, want 1: this cannot tell whether one was held", before)
	}

	calls := methods(t, s, id, token)
	type outcome struct {
		method string
		err    error
		waited time.Duration
	}
	outcomes := make(chan outcome, len(calls))
	for method, call := range calls {
		go func() {
			started := time.Now()
			err := call()
			outcomes <- outcome{method, err, time.Since(started)}
		}()
	}
	for range calls {
		o := <-outcomes
		if o.err == nil {
			t.Errorf("%s succeeded with no connection to run over", o.method)
		}
		if errors.Is(o.err, credential.ErrNotFound) {
			t.Errorf("%s: err = %v: a database that could not be reached was reported as a credential nobody made", o.method, o.err)
		}
		// A second over the store's three: the deadline is the store's own,
		// and pinned here, so that moving it is a change to this claim too.
		if o.waited > 4*time.Second {
			t.Errorf("%s waited %s for a connection, want to have given up at the store's deadline", o.method, o.waited)
		}
	}
	if after := postgrestest.Backends(t, name); after != before {
		t.Errorf("the server sees %d connections after the failures, want %d: one was opened to read a row", after, before)
	}

	held.Release()
	if _, err := s.FindByToken(t.Context(), token); err != nil {
		t.Errorf("the request after the connection was released failed: %v", err)
	}
}

func TestStore_ReportsADatabaseItCannotReach(t *testing.T) {
	t.Parallel()
	// A list that came back empty from a database that was not reached would
	// read as a deployment with no credentials.
	s, pool := store(t)
	id, token := created(t, s, first, credential.ReadOnly, sometime)
	pool.Close()

	for method, call := range methods(t, s, id, token) {
		err := call()
		if err == nil {
			t.Errorf("%s succeeded over a closed pool", method)
		}
		if errors.Is(err, credential.ErrNotFound) {
			t.Errorf("%s: err = %v: a database that could not be reached was reported as a credential nobody made", method, err)
		}
	}
}

// methods is each call the store has, made with what a stored credential
// gives a caller, for the tests that run all seven against a database they
// cannot reach. The use recorded is of a credential never used, which is the
// one call that has to write.
func methods(t *testing.T, s *credential.Postgres, id credential.ID, token credential.Token) map[string]func() error {
	return map[string]func() error{
		"Accounts":    func() error { _, err := s.Accounts(t.Context()); return err },
		"Create":      func() error { _, _, err := s.Create(t.Context(), first, credential.ReadOnly, sometime); return err },
		"FindByToken": func() error { _, err := s.FindByToken(t.Context(), token); return err },
		"List":        func() error { _, err := s.List(t.Context()); return err },
		"Revoke":      func() error { return s.Revoke(t.Context(), id, sometime) },
		"RecordUse":   func() error { return s.RecordUse(t.Context(), credential.Credential{ID: id}, sometime) },
		"InForce":     func() error { _, err := s.InForce(t.Context()); return err },
	}
}

func TestStore_ReportsARowItCannotRead(t *testing.T) {
	t.Parallel()
	// A list with a row left out reads as that credential not existing, the
	// same as an empty one from a database that was not reached. The column
	// takes infinity and a time.Time cannot hold one, which makes this the
	// one row the schema admits and the store cannot read.
	s, pool := store(t)
	id, _ := created(t, s, first, credential.ReadOnly, sometime)
	if _, err := pool.Exec(t.Context(), `update credentials set last_used_at = 'infinity' where id = $1`, id); err != nil {
		t.Fatal(err)
	}

	got, err := s.List(t.Context())

	if err == nil {
		t.Errorf("listed %+v with no error, want the row it could not read reported", got)
	}
}

func TestStore_ListsUnrevokedCredentialsMostRecentlyUsedFirst(t *testing.T) {
	t.Parallel()
	// The order is what tells a credential a client is presenting from the
	// rows a script made and abandoned, when there are hundreds of the latter.
	s, pool := store(t)
	made := sometime.Add(-3 * time.Hour)
	usedNow, _ := created(t, s, first, credential.ReadWrite, made)
	usedEarlier, _ := created(t, s, other, credential.ReadOnly, made)
	revoked, _ := created(t, s, first, credential.ReadOnly, made)
	// The older one is made first, so that the order rows were written in is
	// not the order wanted.
	neverUsedOlder, _ := created(t, s, first, credential.ReadOnly, made)
	neverUsedNewer, _ := created(t, s, first, credential.ReadOnly, made.Add(time.Hour))
	for _, c := range []struct {
		id credential.ID
		at time.Time
	}{
		{usedNow, sometime},
		{usedEarlier, sometime.Add(-time.Hour)},
		{revoked, sometime.Add(-time.Minute)},
	} {
		if _, err := pool.Exec(t.Context(), `update credentials set last_used_at = $2 where id = $1`, c.id, c.at); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Revoke(t.Context(), revoked, sometime); err != nil {
		t.Fatal(err)
	}

	got, err := s.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	want := []credential.Credential{
		{ID: usedNow, Scope: credential.ScopeAccount, Account: first, Access: credential.ReadWrite, KeyID: keyID, LastUsedAt: sometime},
		{ID: usedEarlier, Scope: credential.ScopeAccount, Account: other, Access: credential.ReadOnly, KeyID: keyID, LastUsedAt: sometime.Add(-time.Hour)},
		{ID: neverUsedNewer, Scope: credential.ScopeAccount, Account: first, Access: credential.ReadOnly, KeyID: keyID},
		{ID: neverUsedOlder, Scope: credential.ScopeAccount, Account: first, Access: credential.ReadOnly, KeyID: keyID},
	}
	if len(got) != len(want) {
		t.Fatalf("listed %d credentials, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if !same(got[i], want[i]) {
			t.Errorf("credential %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// same compares two credentials with the time by Equal, since one read back
// carries the driver's location.
func same(a, b credential.Credential) bool {
	if !a.LastUsedAt.Equal(b.LastUsedAt) {
		return false
	}
	a.LastUsedAt, b.LastUsedAt = time.Time{}, time.Time{}
	return a == b
}

func TestStore_ListsACredentialMadeUnderAnotherKey(t *testing.T) {
	t.Parallel()
	// A deployment whose key was swapped has every request fail. The list is
	// where that shows: each row names the key it was made under.
	s, pool := store(t)
	id, _ := created(t, credential.NewPostgres(pool, parsed(t, key), "k2"), first, credential.ReadOnly, sometime)

	got, err := s.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0].ID != id || got[0].KeyID != "k2" {
		t.Errorf("listed %+v, want the one credential, saying it was made under k2", got)
	}
}

func TestStore_RevokesOnceHoweverOftenItIsAsked(t *testing.T) {
	t.Parallel()
	// Two operators reacting to one leak both run revoke. The second is not a
	// failure, and it does not move the time the first one is recorded at.
	s, pool := store(t)
	id, _ := created(t, s, first, credential.ReadOnly, sometime)

	if err := s.Revoke(t.Context(), id, sometime); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(t.Context(), id, sometime.Add(time.Hour)); err != nil {
		t.Errorf("the second revoke failed: %v", err)
	}

	var at time.Time
	if err := pool.QueryRow(t.Context(), `select revoked_at from credentials where id = $1`, id).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if !at.Equal(sometime) {
		t.Errorf("revoked_at = %s, want %s, the time of the first revoke", at, sometime)
	}
}

func TestStore_ReportsNotFoundForRevokingWhatNobodyMade(t *testing.T) {
	t.Parallel()
	s, _ := store(t)

	err := s.Revoke(t.Context(), credential.NewID(), sometime)

	if !errors.Is(err, credential.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// found reads the credential token presents.
func found(t *testing.T, s *credential.Postgres, token credential.Token) credential.Credential {
	t.Helper()
	c, err := s.FindByToken(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestStore_RecordsAUseOnceAnHourHoweverOftenItIsAsked(t *testing.T) {
	t.Parallel()
	// Every call is given the value read before any of them wrote, which is
	// what N workers of one client hold after reading the row in the same
	// moment. What keeps the writes to one an hour is then the statement,
	// with nothing left to the caller.
	s, _ := store(t)
	_, token := created(t, s, first, credential.ReadOnly, sometime.Add(-time.Hour))
	unused := found(t, s, token)

	for _, c := range []struct {
		name string
		at   time.Time
		want time.Time
	}{
		{"the first use", sometime, sometime},
		{"a use within the hour", sometime.Add(59 * time.Minute), sometime},
		{"a use an hour on", sometime.Add(61 * time.Minute), sometime.Add(61 * time.Minute)},
	} {
		if err := s.RecordUse(t.Context(), unused, c.at); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := found(t, s, token).LastUsedAt; !got.Equal(c.want) {
			t.Errorf("%s: last used at %s, want %s", c.name, got, c.want)
		}
	}
}

// workers is how many of one client record a use in the same moment. More
// than the pool has connections, so that some wait for one and some for the
// row.
const workers = 16

func TestStore_WritesOnceWhenUsesAreRecordedTogether(t *testing.T) {
	t.Parallel()
	// Without the hour in the statement's where clause, every update after
	// the first waits on the row lock and then writes; with it, each re-reads
	// the row the winner wrote, matches nothing, and returns. The row read
	// back afterwards holds the last write and says nothing of how many
	// there were, so the database counts them.
	s, pool := store(t)
	_, token := created(t, s, first, credential.ReadOnly, sometime.Add(-time.Hour))
	unused := found(t, s, token)
	counting(t, pool)

	var group sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		group.Go(func() { errs <- s.RecordUse(t.Context(), unused, sometime) })
	}
	group.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if n := written(t, pool); n != 1 {
		t.Errorf("%d writes of last_used_at, want 1", n)
	}
	if got := found(t, s, token).LastUsedAt; !got.Equal(sometime) {
		t.Errorf("last used at %s, want %s", got, sometime)
	}
}

// counting has the database count every row an update of last_used_at
// writes, in a table of the test's own.
func counting(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `
		create table writes (id uuid not null);
		create function noting() returns trigger language plpgsql as $$
		begin
			insert into writes (id) values (new.id);
			return null;
		end $$;
		create trigger noting after update of last_used_at on credentials
			for each row execute function noting()`); err != nil {
		t.Fatal(err)
	}
}

// written is how many rows updates of last_used_at have written since
// counting began.
func written(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), `select count(*) from writes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestStore_RecordsAUseWithinTheHourWithoutTheDatabase(t *testing.T) {
	t.Parallel()
	// The value FindByToken read is what decides, and the row is not read
	// again to decide it. With the pool's one connection held, a use within
	// the hour of the one recorded is done at once, and a use an hour on
	// waits for the connection and fails.
	const name = "suco-credential-recorded-test"
	pool := opened(t, postgrestest.WithName(t, withOneConnection(t, postgrestest.Fresh(t)), name))
	s := credential.NewPostgres(pool, parsed(t, key), keyID)
	_, token := created(t, s, first, credential.ReadOnly, sometime.Add(-time.Hour))
	if err := s.RecordUse(t.Context(), found(t, s, token), sometime); err != nil {
		t.Fatal(err)
	}
	used := found(t, s, token)
	held, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	started := time.Now()
	err = s.RecordUse(t.Context(), used, sometime.Add(59*time.Minute))
	waited := time.Since(started)

	if err != nil {
		t.Errorf("a use within the hour failed with no connection free: %v", err)
	}
	if waited > time.Second {
		t.Errorf("a use within the hour waited %s, want done without a connection", waited)
	}
	if err := s.RecordUse(t.Context(), used, sometime.Add(61*time.Minute)); err == nil {
		t.Error("a use an hour on succeeded with no connection to write over")
	}
}

func TestStore_InForceIsNoneOverATableNothingWasMadeIn(t *testing.T) {
	t.Parallel()
	s, _ := store(t)

	got, err := s.InForce(t.Context())

	if err != nil {
		t.Fatal(err)
	}
	if got != credential.NoneInForce {
		t.Errorf("in force = %q over an empty table, want %q", got, credential.NoneInForce)
	}
}

func TestStore_InForceIsReadOnlyWhenNothingInForceMayWrite(t *testing.T) {
	t.Parallel()
	// A credential that writes was made and revoked: what is in force is
	// what may be presented, and a revoked row may not.
	s, _ := store(t)
	created(t, s, first, credential.ReadOnly, sometime)
	id, _ := created(t, s, first, credential.ReadWrite, sometime)
	if err := s.Revoke(t.Context(), id, sometime); err != nil {
		t.Fatal(err)
	}

	got, err := s.InForce(t.Context())

	if err != nil {
		t.Fatal(err)
	}
	if got != credential.ReadOnlyInForce {
		t.Errorf("in force = %q with one read-only credential and one revoked, want %q", got, credential.ReadOnlyInForce)
	}
}

func TestStore_InForceIsReadWriteWhenOneInForceMayWrite(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	created(t, s, first, credential.ReadOnly, sometime)
	created(t, s, first, credential.ReadWrite, sometime)

	got, err := s.InForce(t.Context())

	if err != nil {
		t.Fatal(err)
	}
	if got != credential.ReadWriteInForce {
		t.Errorf("in force = %q with a credential that writes, want %q", got, credential.ReadWriteInForce)
	}
}

func TestStore_InForceCountsACredentialMadeUnderAnotherKey(t *testing.T) {
	t.Parallel()
	// A deployment whose key was swapped refuses every request and still
	// holds a credential that writes. Which key each row was made under is
	// what tells that apart, and it is what List shows.
	s, pool := store(t)
	created(t, credential.NewPostgres(pool, parsed(t, key), "k2"), first, credential.ReadWrite, sometime)

	got, err := s.InForce(t.Context())

	if err != nil {
		t.Fatal(err)
	}
	if got != credential.ReadWriteInForce {
		t.Errorf("in force = %q with a credential that writes made under k2, want %q", got, credential.ReadWriteInForce)
	}
}

func TestStore_InForceIsNoneWhenEveryCredentialWasRevoked(t *testing.T) {
	t.Parallel()
	// A deployment whose every credential was revoked is one with none, and
	// it says so: a row out of force is not one that may be presented.
	s, _ := store(t)
	id, _ := created(t, s, first, credential.ReadOnly, sometime)
	if err := s.Revoke(t.Context(), id, sometime); err != nil {
		t.Fatal(err)
	}

	got, err := s.InForce(t.Context())

	if err != nil {
		t.Fatal(err)
	}
	if got != credential.NoneInForce {
		t.Errorf("in force = %q after the one credential was revoked, want %q", got, credential.NoneInForce)
	}
}
