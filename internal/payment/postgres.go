package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// queries is what a statement runs on: the pool, or a transaction a caller
// opened. The methods that write with the rest of a round take the caller's
// transaction, and the ones that stand alone use the pool.
type queries interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// unwind rolls a transaction back and reports the rollback under what, and
// only where nothing else went wrong.
//
// After a commit it reports the transaction already closed, which is the
// ordinary path rather than a failure. Anything else leaves one open, holding
// a row lock until the pool reaps the connection, and saying nothing would
// hide that from the only caller who could act.
//
// Detached from ctx so that a deadline which caused the failure does not also
// defeat the rollback, and given one of its own because a rollback that hangs
// holds the lock it was meant to release.
func unwind(ctx context.Context, tx pgx.Tx, what string, err error) error {
	back, stop := context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
	defer stop()
	rollback := tx.Rollback(back)
	if rollback != nil && !errors.Is(rollback, pgx.ErrTxClosed) && err == nil {
		return fmt.Errorf("%s: %w", what, rollback)
	}
	return err
}

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
	defer func() { err = unwind(ctx, tx, fmt.Sprintf("payment %s", p.ID()), err) }()

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
	defer func() { err = unwind(ctx, tx, fmt.Sprintf("payment %s", p.ID()), err) }()

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
	defer func() { err = unwind(ctx, tx, fmt.Sprintf("attempt %s", a.ID()), err) }()

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

	return findAttempt(ctx, s.pool, account, payment, id)
}

// findAttempt reads one attempt through whatever is running the statements.
func findAttempt(ctx context.Context, q queries, account AccountID, payment ID, id AttemptID) (*Attempt, Revision, error) {
	// Filtered on the account for the reason Find is: an attempt of another
	// account is not found rather than found and refused.
	row := q.QueryRow(ctx, `
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
	defer func() { err = unwind(ctx, tx, fmt.Sprintf("attempt %s", a.ID()), err) }()

	if err := saveAttempt(ctx, tx, account, a, at); err != nil {
		return err
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

// saveAttempt writes an attempt's move through whatever is running the
// statements, and reports [ErrStale] when the row is no longer the one that
// was read.
func saveAttempt(ctx context.Context, q queries, account AccountID, a *Attempt, at Revision) error {
	tag, err := q.Exec(ctx, `
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
	return nil
}

// observationColumns are what a row holds of a transfer that was seen.
const observationColumns = `account_id, payment_id, attempt_id, network, key, tx,
                            position, block_height, block_hash, block_time, asset,
                            authorizer, sender, recipient, value, reason,
                            implementation, seen_at, final_at`

// Consumed reads the attempts on a network whose keys were consumed, each with
// the payment it is against and the revision both were read at.
//
// Keyed by network and key, and not by account: a transfer names those two and
// nothing else. The account comes back with the row, and every read and write
// after it is that account's.
func (s *Postgres) Consumed(ctx context.Context, network Network, keys []string) ([]Hit, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	rows, err := s.pool.Query(ctx, `
		select a.account_id, a.payment_id,
		       a.id, a.scheme, a.network, a.key, a.authorizer, a.valid_before,
		       a.status, a.created_at, a.revision,
		       p.asset_network, p.asset_reference, p.asset_symbol, p.asset_decimals,
		       p.amount, p.received, p.destination, p.status, p.metadata,
		       p.created_at, p.expires_at, p.version
		  from attempts a
		  join payments p on p.account_id = a.account_id and p.id = a.payment_id
		 where a.network = $1 and a.key = any($2)`, network, keys)
	if err != nil {
		return nil, fmt.Errorf("attempts on %s: %w", network, err)
	}
	defer rows.Close()

	var hits []Hit
	for rows.Next() {
		var (
			hit      Hit
			id       ID
			stored   StoredAttempt
			revision int64
			row      payer
		)
		if err := rows.Scan(&hit.Account, &id,
			&stored.ID, &stored.Scheme, &stored.Network, &stored.Key, &stored.Authorizer,
			&stored.ValidBefore, &stored.Status, &stored.CreatedAt, &revision,
			&row.network, &row.reference, &row.symbol, &row.decimals, &row.amount,
			&row.received, &row.stored.Destination, &row.stored.Status, &row.metadata,
			&row.stored.CreatedAt, &row.stored.ExpiresAt, &row.version); err != nil {
			return nil, fmt.Errorf("attempts on %s: %w", network, err)
		}
		stored.PaymentID = id
		attempt, err := RestoreAttempt(stored)
		if err != nil {
			return nil, fmt.Errorf("attempt %s: %w", stored.ID, err)
		}
		hit.Attempt, hit.AttemptAt = attempt, Revision{id: string(stored.ID), at: revision}
		row.stored.ID = id
		if hit.Payment, hit.PaymentAt, err = row.payment(); err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("attempts on %s: %w", network, err)
	}
	return hits, nil
}

// Record writes what a round saw, inside the transaction that moves the
// position with it.
//
// A transfer is one row, keyed by the network, the key and the transaction, so
// a transfer that comes back in another block rewrites the row it already has.
// A round that read the finalised range also stamps what it saw as final, and
// marks what it recorded there before and can no longer see as vanished; a
// round that read ahead of finality writes rows and nothing else, because what
// it cannot see may only not have arrived yet.
//
// The attempts move with their rows: matched confirms one, and a row that
// vanished takes one back to issued unless another transfer still matches it.
// Every move is conditioned on the revision the caller read, so a round that
// lost a race reports [ErrStale] and writes nothing.
func (s *Postgres) Record(ctx context.Context, tx pgx.Tx, network Network, first, last uint64, final bool, seen []Seen, now time.Time) error {
	txs := make([]string, 0, len(seen))
	for _, one := range seen {
		if err := recordOne(ctx, tx, network, one, final, now); err != nil {
			return err
		}
		txs = append(txs, one.Transfer.Tx)
	}
	if final {
		if err := vanish(ctx, tx, network, first, last, txs, now); err != nil {
			return err
		}
	}
	for _, one := range seen {
		if one.Reason != Matched {
			continue
		}
		if err := one.Attempt.Confirm(Address(one.Transfer.Authorizer)); err != nil {
			// Already confirming by an earlier round that saw the same
			// transfer. The row is written either way, and the attempt is
			// where it should be.
			continue
		}
		if err := saveAttempt(ctx, tx, one.Account, one.Attempt, one.AttemptAt); err != nil {
			return err
		}
	}
	return nil
}

// recordOne writes one transfer, and stamps it final when the round read the
// finalised range. The first moment it was seen there is what stays: a block
// at or below the final one is not replaced, so a later stamp would only be a
// later reading of the same fact.
//
// The value goes to the column as the chain wrote it, and a value the column
// cannot hold fails the round rather than being read into something else: what
// an asset's smallest unit means is the asset's, and a row here carries the
// digits of whichever asset the transfer was of.
func recordOne(ctx context.Context, q queries, network Network, one Seen, final bool, now time.Time) error {
	t := one.Transfer
	if err := screen(t); err != nil {
		return err
	}
	height, err := blockHeight(t)
	if err != nil {
		return err
	}
	var finalAt *time.Time
	if final {
		finalAt = &now
	}
	if _, err := q.Exec(ctx, `
		insert into observations (`+observationColumns+`)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
		        $16, $17, $18, $19)
		on conflict (network, key, tx) do update
		   set position = $7, block_height = $8, block_hash = $9, block_time = $10,
		       asset = $11, authorizer = $12, sender = $13, recipient = $14,
		       value = $15, reason = $16, implementation = $17, seen_at = $18,
		       final_at = coalesce(observations.final_at, $19)`,
		one.Account, one.Payment.ID(), one.Attempt.ID(), network, t.Key, t.Tx,
		t.Position, height, t.BlockHash, t.BlockTime, t.Asset,
		t.Authorizer, t.From, t.To, t.Value, one.Reason,
		one.Implementation, now, finalAt); err != nil {
		return fmt.Errorf("transfer %s: %w", t.Tx, err)
	}
	return nil
}

// vanish marks what was recorded in a span of finalised blocks and is no
// longer there, and takes the attempts it was matched against back to issued.
func vanish(ctx context.Context, q queries, network Network, first, last uint64, txs []string, now time.Time) error {
	low, high, err := span(first, last)
	if err != nil {
		return err
	}
	rows, err := q.Query(ctx, `
		update observations
		   set reason = $5, seen_at = $6
		 where network = $1 and block_height between $2 and $3
		   and tx <> all($4) and reason <> $5
		returning account_id, payment_id, attempt_id`,
		network, low, high, txs, Vanished, now)
	if err != nil {
		return fmt.Errorf("observations on %s: %w", network, err)
	}
	type gone struct {
		account AccountID
		payment ID
		attempt AttemptID
	}
	var lost []gone
	for rows.Next() {
		var one gone
		if err := rows.Scan(&one.account, &one.payment, &one.attempt); err != nil {
			rows.Close()
			return fmt.Errorf("observations on %s: %w", network, err)
		}
		lost = append(lost, one)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("observations on %s: %w", network, err)
	}

	for _, one := range lost {
		var matched int
		if err := q.QueryRow(ctx, `
			select count(*)
			  from observations
			 where account_id = $1 and payment_id = $2 and attempt_id = $3
			   and reason = $4`,
			one.account, one.payment, one.attempt, Matched).Scan(&matched); err != nil {
			return fmt.Errorf("attempt %s: %w", one.attempt, err)
		}
		if matched > 0 {
			continue
		}
		a, at, err := findAttempt(ctx, q, one.account, one.payment, one.attempt)
		if err != nil {
			return err
		}
		if err := a.Unconfirm(); err != nil {
			// Issued already: nothing was confirmed against this attempt, so
			// there is nothing to take back.
			continue
		}
		if err := saveAttempt(ctx, q, one.account, a, at); err != nil {
			return err
		}
	}
	return nil
}

// blockHeight is a transfer's height as the column holds one, refusing what it
// cannot hold rather than storing a negative.
func blockHeight(t Transfer) (int64, error) {
	if t.BlockHeight > math.MaxInt64 {
		return 0, fmt.Errorf("transfer %s: block %d is more than a bigint holds", t.Tx, t.BlockHeight)
	}
	return int64(t.BlockHeight), nil
}

// span is a range of heights as the column holds them.
func span(first, last uint64) (int64, int64, error) {
	if first > math.MaxInt64 || last > math.MaxInt64 {
		return 0, 0, fmt.Errorf("blocks %d to %d are more than a bigint holds", first, last)
	}
	return int64(first), int64(last), nil
}

// payer is a payment as its columns come back, before the asset and the
// amounts are read into the values the aggregate holds.
type payer struct {
	stored    Stored
	network   string
	reference string
	symbol    string
	decimals  uint8
	amount    string
	received  *string
	metadata  []byte
	version   int64
}

// payment rebuilds what the columns hold, and the revision they were read at.
func (r payer) payment() (*Payment, Revision, error) {
	asset, err := NewAsset(Network(r.network), r.reference, r.symbol, r.decimals)
	if err != nil {
		return nil, Revision{}, fmt.Errorf("payment %s: %w", r.stored.ID, err)
	}
	if r.stored.Amount, err = ParseMoney(asset, r.amount); err != nil {
		return nil, Revision{}, fmt.Errorf("payment %s: %w", r.stored.ID, err)
	}
	if r.received != nil {
		if r.stored.Received, err = ParseMoney(asset, *r.received); err != nil {
			return nil, Revision{}, fmt.Errorf("payment %s: %w", r.stored.ID, err)
		}
	}
	if err := json.Unmarshal(r.metadata, &r.stored.Metadata); err != nil {
		return nil, Revision{}, fmt.Errorf("payment %s: metadata: %w", r.stored.ID, err)
	}
	p, err := Restore(r.stored)
	if err != nil {
		return nil, Revision{}, fmt.Errorf("payment %s: %w", r.stored.ID, err)
	}
	return p, Revision{id: string(r.stored.ID), at: r.version}, nil
}
