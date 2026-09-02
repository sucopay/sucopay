package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// storeTimeout bounds one statement. A repository call that hangs holds a
// connection and whatever lease brought it here, so it fails instead.
const storeTimeout = 10 * time.Second

// Postgres stores payments in PostgreSQL.
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
func (s *Postgres) Create(ctx context.Context, account AccountID, p *Payment) error {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	metadata, err := json.Marshal(p.Metadata())
	if err != nil {
		return fmt.Errorf("payment %s: metadata: %w", p.ID(), err)
	}
	asset := p.Asset()
	_, err = s.pool.Exec(ctx, `
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
	return p, Revision{id: id, at: version}, nil
}

// Save writes back a payment that was read.
//
// Status and received are the only columns it writes, because they are the only
// fields any move changes. TestRepository_EveryFieldOfAPaymentIsAccountedFor
// fails if that stops being true.
func (s *Postgres) Save(ctx context.Context, account AccountID, p *Payment, at Revision) (err error) {
	if p == nil {
		return errors.New("payment: nothing to save")
	}
	// Two zero values pass this, because an empty identifier equals an empty
	// identifier. The where clause below is what refuses them: no row carries
	// an empty id.
	if at.id != p.ID() {
		return fmt.Errorf("payment %s: %w, which belongs to %s", p.ID(), ErrStale, at.id)
	}
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	// One transaction, so that what has to be written with a payment is written
	// here rather than by whoever called. Neither the outbox nor the audit
	// table exists yet, and owning the transaction is not the whole of what
	// they need: an outbox row carries the name of the event, and nothing in
	// this signature says which move produced this state. That parameter is
	// still to come.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("payment %s: %w", p.ID(), err)
	}
	defer func() {
		// After a commit this says the transaction is already closed, which is
		// the ordinary path rather than a failure. Anything else leaves one
		// open, holding a row lock until the pool reaps the connection, and
		// returning nothing would hide that from the only caller who could act.
		// Detached from ctx, so a deadline that caused the failure does not also
		// defeat the rollback, but given one of its own: a rollback that hangs
		// holds the row lock it was meant to release.
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
