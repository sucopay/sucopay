package credential

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// storeTimeout bounds one call, counting the wait for a connection. Every
// request is authenticated under it, so a request that arrives while every
// connection is held fails here, before it has read a credential, rather
// than waiting behind whatever holds them with its own connection to the
// client open.
//
// Three seconds is a first value. What a deployment under load sees between
// asking for a connection and getting one is not measured yet, and the
// number moves when it is. Shorter than payment's ten because nothing here
// is a transaction: one statement over an index, and a call that has not
// had its answer by now is not waiting on the database's work but on its
// availability.
const storeTimeout = 3 * time.Second

// Postgres stores credentials in PostgreSQL, under one key.
//
// The key and its identifier are held here rather than passed to each call.
// Every row this writes is hashed under the key and says so in key_id, and
// every lookup hashes under it and asks for that key_id. A call that could
// name a key would be a call that could look a credential up under the wrong
// one.
//
// No transactions. Each method is one statement, and a statement is atomic on
// its own. payment.Postgres has them for the rows that must be written with a
// payment or not at all; nothing has to be written with a credential.
type Postgres struct {
	pool  *pgxpool.Pool
	key   Key
	keyID string
}

// NewPostgres stores credentials in the database behind pool, hashed under
// key and marked as made under keyID.
func NewPostgres(pool *pgxpool.Pool, key Key, keyID string) *Postgres {
	return &Postgres{pool: pool, key: key, keyID: keyID}
}

// columns is what a Credential is read from, in the order scan reads them.
const columns = `id, scope, account_id, capability, key_id, last_used_at`

// Accounts reads every account a credential can be of, in no order a caller
// may rely on.
//
// What issues a credential asks this before [Postgres.Create]: which accounts
// there are is the database's to say. The first migration writes the one a
// deployment has, and a build carrying a copy of that value would go on
// issuing to it after a migration had written a second.
func (s *Postgres) Accounts(ctx context.Context) ([]AccountID, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	rows, err := s.pool.Query(ctx, `select id from accounts`)
	if err != nil {
		return nil, fmt.Errorf("accounts: %w", err)
	}
	accounts, err := pgx.CollectRows(rows, pgx.RowTo[AccountID])
	if err != nil {
		return nil, fmt.Errorf("accounts: %w", err)
	}
	return accounts, nil
}

// Create stores a credential of one account and returns its token, which then
// exists nowhere else: not in the row, which holds the hash, and not here.
//
// Every credential this makes is of one account, with nothing to say
// otherwise. What issues credentials issues them to an account, and a row of
// the deployment is one a test writes by hand.
func (s *Postgres) Create(ctx context.Context, account AccountID, capability Capability, now time.Time) (ID, Token, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	id, token := NewID(), New()
	_, err := s.pool.Exec(ctx, `
		insert into credentials (id, account_id, scope, capability, hash, key_id, created_at)
		values ($1, $2, $3, $4, $5, $6, $7)`,
		id, account, ScopeAccount, capability, Hash(s.key, token), s.keyID, now)
	if err != nil {
		return "", "", fmt.Errorf("credential %s: %w", id, err)
	}
	return id, token, nil
}

// FindByToken reads the credential a token presents, if there is one: an
// unrevoked row holding the token's hash under this key.
//
// Three cases return [ErrNotFound], and the same one: no row, a revoked row,
// and a row made under another key. A caller cannot tell them apart, and is
// not meant to. The one presenting the token is told nothing, and whoever
// runs the deployment has [Postgres.List], which says which key each row was
// made under.
//
// The token is hashed as it came. Nothing here checks its shape: a value that
// is not a token hashes to something no row holds, and is not found, which
// is the answer to a token nobody issued. A failure to reach the database
// is another error, so that a caller can tell 401 from 503.
func (s *Postgres) FindByToken(ctx context.Context, token Token) (Credential, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	c, err := scan(s.pool.QueryRow(ctx, `
		select `+columns+`
		  from credentials
		 where hash = $1 and key_id = $2 and revoked_at is null`,
		Hash(s.key, token), s.keyID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Credential{}, ErrNotFound
	}
	if err != nil {
		return Credential{}, fmt.Errorf("credential: %w", err)
	}
	return c, nil
}

// List reads every unrevoked credential, the most recently used first and
// those never used last, so that the one a client is presenting sorts above
// the rows a script made and abandoned. Among the never used, the newest
// first.
//
// Rows made under another key are listed, and say so in KeyID. That is how
// a deployment whose key was swapped tells the difference from one whose
// credentials were all revoked.
func (s *Postgres) List(ctx context.Context) ([]Credential, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	rows, err := s.pool.Query(ctx, `
		select `+columns+`
		  from credentials
		 where revoked_at is null
		 order by last_used_at desc nulls last, created_at desc, id`)
	if err != nil {
		return nil, fmt.Errorf("credentials: %w", err)
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Credential, error) { return scan(row) })
	if err != nil {
		return nil, fmt.Errorf("credentials: %w", err)
	}
	return all, nil
}

// InForce reports what the credentials in force add up to. Rows made under
// another key count, as [Postgres.List] lists them: a deployment whose key
// was swapped holds credentials nothing can present, and telling that apart
// is what the key identifier beside each row List returns is for.
func (s *Postgres) InForce(ctx context.Context) (InForce, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	var some, writes bool
	if err := s.pool.QueryRow(ctx, `
		select exists (select from credentials where revoked_at is null),
		       exists (select from credentials where revoked_at is null and capability = $1)`,
		ReadWrite).Scan(&some, &writes); err != nil {
		return "", fmt.Errorf("credentials: %w", err)
	}
	switch {
	case writes:
		return ReadWriteInForce, nil
	case some:
		return ReadOnlyInForce, nil
	}
	return NoneInForce, nil
}

// Revoke marks a credential as one nothing will find again. Asking twice is
// the same as asking once: the row keeps the time it was first revoked at,
// and neither call fails. [ErrNotFound] is for an ID no row has.
func (s *Postgres) Revoke(ctx context.Context, id ID, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	tag, err := s.pool.Exec(ctx, `
		update credentials
		   set revoked_at = coalesce(revoked_at, $2)
		 where id = $1`,
		id, now)
	if err != nil {
		return fmt.Errorf("credential %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%s: %w", id, ErrNotFound)
	}
	return nil
}

// useInterval is how long a recorded use stands for the uses after it.
// Written on every request, the row would be a lock the requests of one
// client take in turn, and an hour is enough for what last_used_at is read
// for: telling the credentials in use from the ones a script made and
// abandoned.
const useInterval = time.Hour

// RecordUse writes that c was used at now, unless the row already says it
// was used within the hour.
//
// c is what FindByToken returned, and its LastUsedAt is what decides: within
// useInterval of it, nothing is written and the database is not asked. The
// row is not read again to decide. The statement's where clause holds the
// same hour, for the N workers of one client that read an old value in the
// same moment: the second and later updates wait on the row lock, re-read
// the row the winner wrote, match nothing, and return. The cast in that
// clause is for the parser, which would otherwise read $2 beside an
// interval as one.
//
// An update that matches no row is the usual outcome, and nothing tells it
// from an ID no row has. The ID came out of FindByToken a moment ago.
//
// Like every call this one is under storeTimeout, after FindByToken has had
// its own, so the request that writes waits at most twice. That request is
// the first of its credential's hour.
func (s *Postgres) RecordUse(ctx context.Context, c Credential, now time.Time) error {
	if !c.LastUsedAt.IsZero() && now.Sub(c.LastUsedAt) <= useInterval {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	if _, err := s.pool.Exec(ctx, `
		update credentials
		   set last_used_at = $2
		 where id = $1
		   and (last_used_at is null or last_used_at < $2::timestamptz - interval '1 hour')`,
		c.ID, now); err != nil {
		return fmt.Errorf("credential %s: %w", c.ID, err)
	}
	return nil
}

// scan reads one row of columns. The two nullable ones come through pointers,
// since a value type has nothing to be when the column is null.
func scan(row pgx.Row) (Credential, error) {
	var (
		c        Credential
		account  *AccountID
		lastUsed *time.Time
	)
	if err := row.Scan(&c.ID, &c.Scope, &account, &c.Capability, &c.KeyID, &lastUsed); err != nil {
		return Credential{}, err
	}
	if account != nil {
		c.Account = *account
	}
	if lastUsed != nil {
		c.LastUsedAt = *lastUsed
	}
	return c, nil
}
