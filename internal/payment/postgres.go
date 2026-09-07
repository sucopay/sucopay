package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sucopay/sucopay/internal/postgres"
)

// storeTimeout bounds one statement. A repository call that hangs holds a
// connection and whatever lease brought it here, so it fails instead.
const storeTimeout = 10 * time.Second

// Postgres stores payments in PostgreSQL.
//
// Create and Save each own a transaction. Whatever has to be written with a
// payment goes inside it rather than being left to whoever called: the outbox
// row for the event the change produced, and the audit row recording that it
// changed. Neither table exists yet. Owning the transaction is the part that
// cannot be added afterwards, because a caller holding its own could always
// commit half of what has to be whole.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres returns a repository backed by pool.
func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Amounts are carried as decimal digits rather than as a number. The column is
// numeric(78, 0) because an amount in an asset's smallest unit does not fit in
// bigint, and the driver has no type that holds one exactly either.
func digits(m Money) string { return m.Amount().String() }

// Create stores a payment nothing has stored before.
func (s *Postgres) Create(ctx context.Context, account AccountID, p *Payment) (err error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	metadata, err := json.Marshal(p.Metadata())
	if err != nil {
		return fmt.Errorf("payment %s: metadata: %w", p.ID(), err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("payment %s: %w", p.ID(), err)
	}
	defer func() {
		unwind, stop := context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
		defer stop()
		rollback := tx.Rollback(unwind)
		if rollback != nil && !errors.Is(rollback, pgx.ErrTxClosed) && err == nil {
			err = fmt.Errorf("payment %s: %w", p.ID(), rollback)
		}
	}()

	asset := p.Asset()
	_, err = tx.Exec(ctx, `
		insert into payments (
			id, account_id, asset_network, asset_reference, asset_symbol,
			asset_decimals, amount, destination, status, metadata,
			created_at, expires_at
		) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		p.ID(), account, asset.Network(), asset.Reference(), asset.Symbol(),
		asset.Decimals(), digits(p.Amount()), p.Destination(), p.Status(), metadata,
		p.CreatedAt(), p.ExpiresAt())
	if err != nil {
		return fmt.Errorf("payment %s: %w", p.ID(), err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("payment %s: %w", p.ID(), err)
	}
	return nil
}

// Find reads a payment, and the revision it was read at.
func (s *Postgres) Find(ctx context.Context, account AccountID, id ID) (*Payment, Revision, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	var (
		stored             Stored
		network, reference string
		symbol             string
		decimals           uint8
		amount             string
		received           *string
		metadata           []byte
		version            int64
	)
	// Filtered on the account as well as the identifier. A payment belonging
	// to somebody else is not found rather than found and refused, so a caller
	// that forgot to check cannot tell one from a payment that never existed.
	err := s.pool.QueryRow(ctx, `
		select asset_network, asset_reference, asset_symbol, asset_decimals,
		       amount, received, destination, status, metadata,
		       created_at, expires_at, version
		  from payments
		 where account_id = $1 and id = $2`, account, id).
		Scan(&network, &reference, &symbol, &decimals, &amount, &received,
			&stored.Destination, &stored.Status, &metadata,
			&stored.CreatedAt, &stored.ExpiresAt, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, Revision{}, fmt.Errorf("%s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, Revision{}, fmt.Errorf("payment %s: %w", id, err)
	}

	asset, err := NewAsset(Network(network), reference, symbol, decimals)
	if err != nil {
		return nil, Revision{}, fmt.Errorf("payment %s: %w", id, err)
	}
	if stored.Amount, err = ParseMoney(asset, amount); err != nil {
		return nil, Revision{}, fmt.Errorf("payment %s: %w", id, err)
	}
	if received != nil {
		if stored.Received, err = ParseMoney(asset, *received); err != nil {
			return nil, Revision{}, fmt.Errorf("payment %s: %w", id, err)
		}
	}
	if err := json.Unmarshal(metadata, &stored.Metadata); err != nil {
		return nil, Revision{}, fmt.Errorf("payment %s: metadata: %w", id, err)
	}
	stored.ID = id

	p, err := Restore(stored)
	if err != nil {
		return nil, Revision{}, fmt.Errorf("payment %s: %w", id, err)
	}
	return p, Revision{id: string(id), at: version}, nil
}

// Save writes back a payment that was read.
//
// Status and received are the only columns it writes, because they are the only
// fields any move changes. A test holds the two lists together, so a field added
// to the aggregate and not to this fails rather than being dropped.
func (s *Postgres) Save(ctx context.Context, account AccountID, p *Payment, at Revision) (err error) {
	if p == nil {
		return errors.New("payment: nothing to save")
	}
	// Two zero values pass this, because an empty identifier equals an empty
	// identifier. The where clause below is what refuses them: no row carries
	// an empty id.
	if at.id != string(p.ID()) {
		return fmt.Errorf("payment %s: %w, which belongs to %s", p.ID(), ErrStale, at.id)
	}
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	// Owning the transaction is not the whole of what the outbox needs: its row
	// carries the name of an event, and nothing in this signature says which
	// move produced this state. That parameter is still to come.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("payment %s: %w", p.ID(), err)
	}
	defer func() {
		// After a commit this reports the transaction already closed, which is
		// the ordinary path rather than a failure. Anything else leaves one
		// open, holding a row lock until the pool reaps the connection, and
		// saying nothing would hide that from the only caller who could act.
		//
		// Detached from ctx so that a deadline which caused the failure does not
		// also defeat the rollback, and given one of its own because a rollback
		// that hangs holds the lock it was meant to release.
		unwind, stop := context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
		defer stop()
		rollback := tx.Rollback(unwind)
		if rollback != nil && !errors.Is(rollback, pgx.ErrTxClosed) && err == nil {
			err = fmt.Errorf("payment %s: %w", p.ID(), rollback)
		}
	}()

	var received *string
	if r := p.Received(); r.IsSet() {
		d := digits(r)
		received = &d
	}
	tag, err := tx.Exec(ctx, `
		update payments
		   set status = $4, received = $5, version = version + 1
		 where account_id = $1 and id = $2 and version = $3`,
		account, p.ID(), at.at, p.Status(), received)
	if err != nil {
		return fmt.Errorf("payment %s: %w", p.ID(), err)
	}
	// Nothing matched. Either the version moved or the account is not the one
	// that owns this payment, and the two are the same answer on purpose: only
	// a caller that read this payment can hold a revision for it, so the second
	// is its own bug rather than somebody else probing.
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("payment %s: %w", p.ID(), ErrStale)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("payment %s: %w", p.ID(), err)
	}
	return nil
}

// attemptColumns are what a row holds of an attempt, in the order the reads
// below scan them.
const attemptColumns = `id, scheme, network, key, authorizer, valid_before,
                        status, created_at, revision`

// Issue stores an attempt nothing has stored before.
func (s *Postgres) Issue(ctx context.Context, account AccountID, a *Attempt) (err error) {
	if a == nil {
		return errors.New("payment: nothing to issue")
	}
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("attempt %s: %w", a.ID(), err)
	}
	defer func() {
		unwind, stop := context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
		defer stop()
		rollback := tx.Rollback(unwind)
		if rollback != nil && !errors.Is(rollback, pgx.ErrTxClosed) && err == nil {
			err = fmt.Errorf("attempt %s: %w", a.ID(), rollback)
		}
	}()

	_, err = tx.Exec(ctx, `
		insert into attempts (
			account_id, payment_id, id, scheme, network, key, authorizer,
			valid_before, status, created_at
		) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		account, a.PaymentID(), a.ID(), a.Scheme(), a.Network(), a.Key(),
		a.Authorizer(), a.ValidBefore(), a.Status(), a.CreatedAt())
	if err != nil {
		// Constrained first, and on the way out as well as on the way into the
		// switch: what the driver raises holds the values that clashed, one of
		// which is the key this attempt was issued to spend.
		refused := postgres.Constrained(err)
		var broken postgres.Constraint
		if errors.As(refused, &broken) {
			switch broken.Name {
			case "attempts_network_key_key":
				return fmt.Errorf("attempt %s: %w", a.ID(), ErrKeyTaken)
			case "attempts_one_live_per_payment":
				return fmt.Errorf("attempt %s: %w", a.ID(), ErrAttemptLive)
			}
		}
		return fmt.Errorf("attempt %s: %w", a.ID(), refused)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("attempt %s: %w", a.ID(), err)
	}
	return nil
}

// FindAttempt reads one attempt of a payment, and the revision it was read at.
func (s *Postgres) FindAttempt(ctx context.Context, account AccountID, payment ID, id AttemptID) (*Attempt, Revision, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	// Filtered on the account for the reason Find is: an attempt of another
	// account is not found rather than found and refused.
	row := s.pool.QueryRow(ctx, `
		select `+attemptColumns+`
		  from attempts
		 where account_id = $1 and payment_id = $2 and id = $3`, account, payment, id)
	return scanAttempt(row, payment)
}

// Live reads the attempt of a payment that could still be paid.
func (s *Postgres) Live(ctx context.Context, account AccountID, payment ID) (*Attempt, Revision, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	// The statuses are the ones attempts_one_live_per_payment is over, so at
	// most one row can match, and a status added to one belongs in the other.
	row := s.pool.QueryRow(ctx, `
		select `+attemptColumns+`
		  from attempts
		 where account_id = $1 and payment_id = $2
		   and status in ('issued', 'confirming')`, account, payment)
	a, at, err := scanAttempt(row, payment)
	if errors.Is(err, ErrNotFound) {
		return nil, Revision{}, false, nil
	}
	if err != nil {
		return nil, Revision{}, false, err
	}
	return a, at, true, nil
}

// SaveAttempt writes back an attempt that was read.
//
// Status and authorizer are the only columns it writes, because they are the
// only fields a move changes. A test holds the two lists together.
func (s *Postgres) SaveAttempt(ctx context.Context, account AccountID, a *Attempt, at Revision) (err error) {
	if a == nil {
		return errors.New("payment: nothing to save")
	}
	if at.id != string(a.ID()) {
		return fmt.Errorf("attempt %s: %w, which belongs to %s", a.ID(), ErrStale, at.id)
	}
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("attempt %s: %w", a.ID(), err)
	}
	defer func() {
		unwind, stop := context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
		defer stop()
		rollback := tx.Rollback(unwind)
		if rollback != nil && !errors.Is(rollback, pgx.ErrTxClosed) && err == nil {
			err = fmt.Errorf("attempt %s: %w", a.ID(), rollback)
		}
	}()

	tag, err := tx.Exec(ctx, `
		update attempts
		   set status = $5, authorizer = $6, revision = revision + 1
		 where account_id = $1 and payment_id = $2 and id = $3 and revision = $4`,
		account, a.PaymentID(), a.ID(), at.at, a.Status(), a.Authorizer())
	if err != nil {
		return fmt.Errorf("attempt %s: %w", a.ID(), err)
	}
	// Nothing matched: the revision moved, or the account is not the one the
	// attempt belongs to. One answer for both, for the reason Save gives.
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("attempt %s: %w", a.ID(), ErrStale)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("attempt %s: %w", a.ID(), err)
	}
	return nil
}

// scanAttempt reads one row of attemptColumns and rebuilds the attempt.
func scanAttempt(row pgx.Row, payment ID) (*Attempt, Revision, error) {
	var (
		stored   StoredAttempt
		revision int64
	)
	err := row.Scan(&stored.ID, &stored.Scheme, &stored.Network, &stored.Key,
		&stored.Authorizer, &stored.ValidBefore, &stored.Status, &stored.CreatedAt,
		&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, Revision{}, fmt.Errorf("%s: %w", payment, ErrNotFound)
	}
	if err != nil {
		return nil, Revision{}, fmt.Errorf("attempt of %s: %w", payment, err)
	}
	stored.PaymentID = payment

	a, err := RestoreAttempt(stored)
	if err != nil {
		return nil, Revision{}, fmt.Errorf("attempt %s: %w", stored.ID, err)
	}
	return a, Revision{id: string(stored.ID), at: revision}, nil
}
