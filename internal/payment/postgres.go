package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
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
// changed. The audit table does not exist yet. Owning the transaction is the
// part that cannot be added afterwards, because a caller holding its own
// could always commit half of what has to be whole.
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
	var checkoutHash, bodyHash []byte
	var checkoutKeyID, returnURL, key *string
	if c := p.Checkout(); c.IsSet() {
		checkoutHash, checkoutKeyID = c.Hash, &c.KeyID
	}
	if u := p.ReturnURL(); u != "" {
		returnURL = &u
	}
	switch i := p.Idempotency(); {
	case i.IsSet():
		key, bodyHash = &i.Key, i.BodyHash
	case i.Key != "" || len(i.BodyHash) > 0:
		// What the schema refuses as well. Half of it is a caller that lost
		// the other half, and a payment stored without the key it was opened
		// under would answer a retry by opening a second payment.
		return fmt.Errorf("payment %s: half an idempotency key", p.ID())
	}
	_, err = tx.Exec(ctx, `
		insert into payments (
			id, account_id, asset_network, asset_reference, asset_symbol,
			asset_decimals, amount, destination, status, metadata,
			created_at, expires_at, checkout_hash, checkout_key_id, return_url,
			idempotency_key, idempotency_body_hash
		) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
		p.ID(), account, asset.Network(), asset.Reference(), asset.Symbol(),
		asset.Decimals(), digits(p.Amount()), p.Destination(), p.Status(), metadata,
		p.CreatedAt(), p.ExpiresAt(), checkoutHash, checkoutKeyID, returnURL,
		key, bodyHash)
	if err != nil {
		// Constrained first, and on the way out as well: what the driver
		// raises holds the values that clashed, one of which is a key the
		// merchant chose and nothing here publishes.
		refused := postgres.Constrained(err)
		var broken postgres.Constraint
		if errors.As(refused, &broken) && broken.Name == "payments_by_idempotency_key" {
			return fmt.Errorf("payment %s: %w", p.ID(), ErrIdempotencyKeyUsed)
		}
		return fmt.Errorf("payment %s: %w", p.ID(), refused)
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

	return find(ctx, s.pool, account, id)
}

// FindByKey reads the payment that account opened under an idempotency key.
func (s *Postgres) FindByKey(ctx context.Context, account AccountID, key string) (*Payment, Revision, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	var id ID
	// The key alone would find another account's payment, and a merchant
	// chooses their own keys.
	err := s.pool.QueryRow(ctx, `
		select id from payments where account_id = $1 and idempotency_key = $2`, account, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, Revision{}, fmt.Errorf("idempotency key: %w", ErrNotFound)
	}
	if err != nil {
		// The message names no key: an error is what a log keeps, and the key
		// is the merchant's, as the key a payment is paid against is the
		// payer's.
		return nil, Revision{}, fmt.Errorf("reading a payment by its idempotency key: %w", err)
	}
	return find(ctx, s.pool, account, id)
}

// MatchedTransfer reads the transfer that paid a payment: what a merchant is
// answered with, what the page shows a payer, and where a refund of it is
// sent back to.
//
// The first in the order the chain carried them, should more than one have
// been seen. That is the one whose arrival the payment kept: an arrival is
// credited once and the earliest is what credits it, so the address a refund
// goes back to is the address the money in received came from.
//
// Ordered by the block and not by when the row was seen. A round stamps every
// row it writes with one moment, so two transfers read together are the same
// age and which came back was Postgres's to decide; and re-reading a range
// stamps a row again, which would move a refund's destination between one
// round and the next.
//
// A transfer that is no longer on the chain is left out: vanish rewrites the
// reason of the row it wrote, so reading the matched ones leaves it behind.
func (s *Postgres) MatchedTransfer(ctx context.Context, account AccountID, id ID) (Transfer, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	var (
		t      Transfer
		height int64
	)
	err := s.pool.QueryRow(ctx, `
		select asset, key, authorizer, sender, recipient, value,
		       tx, position, block_height, block_hash, block_time
		  from observations
		 where account_id = $1 and payment_id = $2 and reason = $3
		   and attempt_id is not null
		 order by block_height, tx, position limit 1`, account, id, Matched).
		Scan(&t.Asset, &t.Key, &t.Authorizer, &t.From, &t.To, &t.Value,
			&t.Tx, &t.Position, &height, &t.BlockHash, &t.BlockTime)
	if errors.Is(err, pgx.ErrNoRows) {
		return Transfer{}, false, nil
	}
	if err != nil {
		return Transfer{}, false, fmt.Errorf("payment %s: %w", id, err)
	}
	if height < 0 {
		return Transfer{}, false, fmt.Errorf("payment %s: block height %d is below zero", id, height)
	}
	t.BlockHeight = uint64(height)
	t.BlockTime = t.BlockTime.UTC()
	return t, true, nil
}

// find reads one payment through whatever the caller is holding, so that a
// round already inside a transaction reads what that transaction can see.
func find(ctx context.Context, q queries, account AccountID, id ID) (*Payment, Revision, error) {
	var row payer
	// Filtered on the account as well as the identifier. A payment belonging
	// to somebody else is not found rather than found and refused, so a caller
	// that forgot to check cannot tell one from a payment that never existed.
	err := q.QueryRow(ctx, `
		select asset_network, asset_reference, asset_symbol, asset_decimals,
		       amount, received, destination, status, metadata,
		       created_at, expires_at, version,
		       checkout_hash, checkout_key_id, return_url, closed_at,
		       idempotency_key, idempotency_body_hash
		  from payments
		 where account_id = $1 and id = $2`, account, id).
		Scan(&row.network, &row.reference, &row.symbol, &row.decimals, &row.amount,
			&row.received, &row.stored.Destination, &row.stored.Status, &row.metadata,
			&row.stored.CreatedAt, &row.stored.ExpiresAt, &row.version,
			&row.stored.Checkout.Hash, &row.checkoutKeyID, &row.returnURL, &row.closedAt,
			&row.idempotencyKey, &row.stored.Idempotency.BodyHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, Revision{}, fmt.Errorf("%s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, Revision{}, fmt.Errorf("payment %s: %w", id, err)
	}
	row.stored.ID = id
	return row.payment()
}

// Save writes back a payment that was read.
//
// Status and received are the only columns it writes, because they are the only
// fields any move changes. A test holds the two lists together, so a field added
// to the aggregate and not to this fails rather than being dropped.
func (s *Postgres) Save(ctx context.Context, account AccountID, p *Payment, at Revision, e Event) (err error) {
	if p == nil {
		return errors.New("payment: nothing to save")
	}
	// Half an event is a caller that meant to give one. Neither half reaches
	// the database: a body with no name is dropped by the check below, and a
	// name with no body fails the insert with whatever the driver makes of an
	// empty value, taking a change that was fine down with it. Both are the
	// caller's mistake, and both are named as one here.
	if err := checkEvent(p.ID(), e); err != nil {
		return err
	}
	// Two zero values pass this, because an empty identifier equals an empty
	// identifier. The where clause below is what refuses them: no row carries
	// an empty id.
	if at.id != string(p.ID()) {
		return fmt.Errorf("payment %s: %w, which belongs to %s", p.ID(), ErrStale, at.id)
	}
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("payment %s: %w", p.ID(), err)
	}
	defer func() { err = unwind(ctx, tx, fmt.Sprintf("payment %s", p.ID()), err) }()

	if err := savePayment(ctx, tx, account, p, at); err != nil {
		return err
	}
	// After the update and inside the same transaction: a row here describes a
	// change, and the change is refused above when the revision had moved.
	if e.Produced() {
		if err := writeOutbox(ctx, tx, account, p.ID(), e); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("payment %s: %w", p.ID(), err)
	}
	return nil
}

// checkEvent refuses an event that is not one the outbox may hold, before
// anything of the change it came with is written.
func checkEvent(id ID, e Event) error {
	if e.Produced() != (len(e.Payload) > 0) {
		return fmt.Errorf("payment %s: an event was given %s", id, half(e))
	}
	if len(e.Name) > MaxEventNameBytes {
		return fmt.Errorf("payment %s: an event's name is %d bytes, at most %d",
			id, len(e.Name), MaxEventNameBytes)
	}
	if len(e.Payload) > MaxEventBytes {
		return fmt.Errorf("payment %s: an event's body is %d bytes, at most %d",
			id, len(e.Payload), MaxEventBytes)
	}
	// The column takes JSON, so a body that is not JSON fails the insert and
	// takes a change that was fine down with it. What a caller handed over is
	// the caller's mistake and is named as one, rather than reaching the
	// database as whatever the driver makes of it.
	//
	// Asked of an event that produced one, because no body is not JSON either
	// and a change that produced nothing would be refused for a body it never
	// gave. The check above is what leaves those two the only cases.
	if e.Produced() && !json.Valid(e.Payload) {
		return fmt.Errorf("payment %s: an event's body is not JSON", id)
	}
	return nil
}

// writeOutbox puts one event a payment produced where whatever delivers
// events reads them, inside the transaction that made the change. The one
// place the outbox is written, so that every row passed [checkEvent].
func writeOutbox(ctx context.Context, q queries, account AccountID, id ID, e Event) error {
	if err := checkEvent(id, e); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `
		insert into outbox (account_id, payment_id, event, payload)
		values ($1, $2, $3, $4)`,
		account, id, e.Name, e.Payload); err != nil {
		return fmt.Errorf("payment %s: %w", id, err)
	}
	return nil
}

// arrived puts what a transfer carried on the payment it was for.
//
// Only a transfer to the right place in the right asset is an arrival for this
// payment. One that went somewhere else is somebody else's, and one that
// arrived for a payment nothing could be paid for is not applied to it. What
// is short of the amount is an arrival all the same: the difference between
// what was asked for and what came is what makes the shortfall readable.
//
// A payment that already holds an arrival is left where it is. The same
// transfer is seen again whenever a range is read twice or put back, and the
// second reading is the same fact rather than a second arrival.
func arrived(ctx context.Context, q queries, one Seen) error {
	switch one.Reason {
	case Matched, Short:
	default:
		return nil
	}
	// Read inside the transaction rather than taken from the hit, the way what
	// takes an arrival back reads. The hit was read before the round began,
	// and a payment two of this round's transfers are for would otherwise be
	// written twice from two copies that each still say nothing arrived. The
	// second write would lose on the revision and take the whole round with
	// it, and the round after would read the same blocks and lose again.
	p, at, err := find(ctx, q, one.Account, one.Payment.ID())
	if err != nil {
		return fmt.Errorf("transfer %s: %w", one.Transfer.Tx, err)
	}
	// A payment nothing can arrive for is left alone. Judge answers short
	// before it looks at the status, so a transfer short of the amount is
	// judged short whatever the payment has become, and a round that failed on
	// one would never move its cursor past it.
	if !p.CanReceive() || p.Received().IsSet() {
		return nil
	}
	m, err := ParseMoney(p.Asset(), one.Transfer.Value)
	if err != nil {
		// The same value Judge already made what it could of: a value it
		// cannot read is what it calls short, and the row holds it as the
		// chain wrote it. Failing here would abort a round over evidence that
		// is already recorded.
		return nil
	}
	if err := p.Receive(m); err != nil {
		return fmt.Errorf("transfer %s: %w", one.Transfer.Tx, err)
	}
	if err := savePayment(ctx, q, one.Account, p, at); err != nil {
		return fmt.Errorf("transfer %s: %w", one.Transfer.Tx, err)
	}
	return nil
}

// savePayment writes a payment back inside a transaction the caller owns, for
// a round that has more to write with it. [Postgres.Save] is the same write
// with a transaction of its own and the event a move produced; a round produces
// none, because seeing a transfer is not yet a payment having moved.
func savePayment(ctx context.Context, q queries, account AccountID, p *Payment, at Revision) error {
	var received *string
	if r := p.Received(); r.IsSet() {
		d := digits(r)
		received = &d
	}
	// closed_at is written once, when a final status is first saved, and
	// left alone after: a resave of a final payment does not move the time
	// its checkout token's life is counted from.
	tag, err := q.Exec(ctx, `
		update payments
		   set status = $4, received = $5, version = version + 1,
		       closed_at = coalesce(closed_at, case when $6 then now() end)
		 where account_id = $1 and id = $2 and version = $3`,
		account, p.ID(), at.at, p.Status(), received, p.Status().Final())
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
	return nil
}

// half says which part of an event a caller left out.
func half(e Event) string {
	if e.Produced() {
		return "a name and no body"
	}
	return "a body and no name"
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

// Attempted counts the attempts ever issued against a payment.
func (s *Postgres) Attempted(ctx context.Context, account AccountID, payment ID) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	var n int
	if err := s.pool.QueryRow(ctx, `
		select count(*) from attempts where account_id = $1 and payment_id = $2`, account, payment).Scan(&n); err != nil {
		return 0, fmt.Errorf("attempts of %s: %w", payment, err)
	}
	return n, nil
}

// observationColumns are what a row holds of a transfer that was seen.
const observationColumns = `account_id, payment_id, attempt_id, network, key, tx,
                            position, block_height, block_hash, block_time, asset,
                            authorizer, sender, recipient, value, reason,
                            implementation, seen_at, final_at, refund_id`

// RefundsConsumed reads the refunds on a network whose keys were consumed,
// each with the payment it sends back and the revision both were read at.
//
// What [Postgres.Consumed] is on the paying side. Keyed by network and key,
// and not by account: a transfer names those two and nothing else.
func (s *Postgres) RefundsConsumed(ctx context.Context, network Network, keys []string) ([]RefundHit, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	rows, err := s.pool.Query(ctx, `
		select r.account_id, r.payment_id, `+refundColumns+`,
		       p.amount, p.received, p.destination, p.status, p.metadata,
		       p.created_at, p.expires_at, p.version
		  from refunds r
		  join payments p on p.account_id = r.account_id and p.id = r.payment_id
		 where r.network = $1 and r.key = any($2)`, network, keys)
	if err != nil {
		return nil, fmt.Errorf("refunds on %s: %w", network, err)
	}
	defer rows.Close()

	var hits []RefundHit
	for rows.Next() {
		var (
			hit    RefundHit
			refund refunder
			row    payer
		)
		if err := rows.Scan(&hit.Account, &refund.stored.PaymentID,
			&refund.stored.ID, &refund.amount, &refund.stored.Destination, &refund.stored.Scheme,
			&refund.stored.Network, &refund.stored.Key, &refund.stored.ExpiresAt, &refund.stored.Status,
			&refund.stored.Token.Hash, &refund.stored.Token.KeyID, &refund.idempotencyKey,
			&refund.stored.Idempotency.BodyHash, &refund.stored.CreatedAt, &refund.closedAt,
			&refund.version, &refund.network, &refund.reference, &refund.symbol, &refund.decimals,
			&row.amount, &row.received, &row.stored.Destination, &row.stored.Status, &row.metadata,
			&row.stored.CreatedAt, &row.stored.ExpiresAt, &row.version); err != nil {
			return nil, fmt.Errorf("refunds on %s: %w", network, err)
		}
		row.stored.ID = refund.stored.PaymentID
		row.network, row.reference = refund.network, refund.reference
		row.symbol, row.decimals = refund.symbol, refund.decimals
		p, at, err := row.payment()
		if err != nil {
			return nil, err
		}
		r, ratt, err := refund.refund()
		if err != nil {
			return nil, err
		}
		hit.Refund, hit.RefundAt = r, ratt
		hit.Payment, hit.PaymentAt = p, at
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("refunds on %s: %w", network, err)
	}
	return hits, nil
}

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

// Candidates are the transfers on a network that were matched against a
// payment still open for payment, and seen where the chain said it would not
// be replaced, each with the payment they would settle.
//
// Keyed by network and not by account: what decides whether a payment is paid
// runs for a deployment and not for one merchant. The account comes back with
// the row, and every read and write after it is that account's.
//
// In the order of the block and the transaction, from after the place given,
// and at most the number asked for. Deciding asks the endpoints about every
// row it reads, so how many are read is how much a round costs; a caller hands
// back where it stopped so that the rows behind get their turn.
func (s *Postgres) Candidates(ctx context.Context, network Network, from Place,
	limit int) ([]Candidate, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	rows, err := s.pool.Query(ctx, `
		select o.account_id, o.key, o.tx, o.block_height, o.block_hash,
		       o.payment_id,
		       p.asset_network, p.asset_reference, p.asset_symbol, p.asset_decimals,
		       p.amount, p.received, p.destination, p.status, p.metadata,
		       p.created_at, p.expires_at, p.version
		  from observations o
		  join payments p on p.account_id = o.account_id and p.id = o.payment_id
		 where o.network = $1 and o.reason = $2 and o.final_at is not null
		   and p.status = any($3)
		   and (o.block_height, o.tx) > ($4, $5)
		 order by o.block_height, o.tx
		 limit $6`,
		network, Matched, []Status{AwaitingPayment, AwaitingFinality},
		from.BlockHeight, from.Tx, limit)
	if err != nil {
		return nil, fmt.Errorf("observations on %s: %w", network, err)
	}
	defer rows.Close()

	var candidates []Candidate
	for rows.Next() {
		var (
			c   Candidate
			row payer
		)
		if err := rows.Scan(&c.Account, &c.Key, &c.Tx, &c.BlockHeight, &c.BlockHash,
			&row.stored.ID,
			&row.network, &row.reference, &row.symbol, &row.decimals, &row.amount,
			&row.received, &row.stored.Destination, &row.stored.Status, &row.metadata,
			&row.stored.CreatedAt, &row.stored.ExpiresAt, &row.version); err != nil {
			return nil, fmt.Errorf("observations on %s: %w", network, err)
		}
		if c.Payment, c.PaymentAt, err = row.payment(); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("observations on %s: %w", network, err)
	}
	return candidates, nil
}

// dueColumns are the columns a payment is read back from for a sweep of the
// clock, in the order [scanDue] reads them.
const dueColumns = `p.account_id, p.id, p.asset_network, p.asset_reference,
                    p.asset_symbol, p.asset_decimals, p.amount, p.received,
                    p.destination, p.status, p.metadata, p.created_at,
                    p.expires_at, p.version`

// Undecided is how many transfers on a network are waiting for the endpoints
// to be asked about them: the rows [Postgres.Candidates] reads, counted rather
// than read. A number that keeps growing is a deployment whose settling has
// stopped getting anywhere, which is what somebody looking at a deployment
// from outside the process can be told.
//
// The same filter written twice. Counting through Candidates would mean
// reading every row to throw it away, which is the cost this is asked in place
// of.
func (s *Postgres) Undecided(ctx context.Context, network Network) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	var waiting int
	if err := s.pool.QueryRow(ctx, `
		select count(*)
		  from observations o
		  join payments p on p.account_id = o.account_id and p.id = o.payment_id
		 where o.network = $1 and o.reason = $2 and o.final_at is not null
		   and p.status = any($3)`,
		network, Matched, []Status{AwaitingPayment, AwaitingFinality}).Scan(&waiting); err != nil {
		return 0, fmt.Errorf("observations on %s: %w", network, err)
	}
	return waiting, nil
}

// Disagree records that the endpoints were first found to disagree about a
// recorded transfer. A row already saying so keeps the time it says.
func (s *Postgres) Disagree(ctx context.Context, network Network, key, tx string, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	if _, err := s.pool.Exec(ctx, `
		update observations
		   set disagreed_at = coalesce(disagreed_at, $4)
		 where network = $1 and key = $2 and tx = $3`,
		network, key, tx, now); err != nil {
		return fmt.Errorf("transfer %s: %w", tx, err)
	}
	return nil
}

// Agree records that the endpoints no longer disagree about a recorded
// transfer.
func (s *Postgres) Agree(ctx context.Context, network Network, key, tx string) error {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	if _, err := s.pool.Exec(ctx, `
		update observations
		   set disagreed_at = null
		 where network = $1 and key = $2 and tx = $3`,
		network, key, tx); err != nil {
		return fmt.Errorf("transfer %s: %w", tx, err)
	}
	return nil
}

// Disagreeing is how many of the transfers [Postgres.Undecided] counts the
// endpoints disagree about. Nothing settles from a disagreement and it does
// not resolve itself, so the count is what an operator has to go on.
func (s *Postgres) Disagreeing(ctx context.Context, network Network) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	var disagreeing int
	if err := s.pool.QueryRow(ctx, `
		select count(*)
		  from observations o
		  join payments p on p.account_id = o.account_id and p.id = o.payment_id
		 where o.network = $1 and o.reason = $2 and o.final_at is not null
		   and o.disagreed_at is not null and p.status = any($3)`,
		network, Matched, []Status{AwaitingPayment, AwaitingFinality}).Scan(&disagreeing); err != nil {
		return 0, fmt.Errorf("observations on %s: %w", network, err)
	}
	return disagreeing, nil
}

// Open counts the payments on a network that are still open: payable, or
// past their deadline and waiting to learn whether anything arrives. It is
// what the cursor command asks before it skips a range of the chain, since
// any of them may have been paid in that range and would then expire as
// unpaid once the cursor is past its deadline.
func (s *Postgres) Open(ctx context.Context, network Network) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	var open int
	if err := s.pool.QueryRow(ctx, `
		select count(*)
		  from payments
		 where asset_network = $1 and status = any($2)`,
		network, []Status{AwaitingPayment, AwaitingFinality}).Scan(&open); err != nil {
		return 0, fmt.Errorf("payments on %s: %w", network, err)
	}
	return open, nil
}

// OpenRefunds counts the refunds on a network that are still open: signable,
// or past their deadline and waiting to learn whether what was signed
// arrives. What [Postgres.Open] counts on the paying side, and asked before
// the same thing: a range of the chain nobody will read may hold the transfer
// that sends one of them back, and a refund that expires with its transfer
// unread hands its amount back to what the payment can still refund.
func (s *Postgres) OpenRefunds(ctx context.Context, network Network) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	var open int
	if err := s.pool.QueryRow(ctx, `
		select count(*)
		  from refunds
		 where network = $1 and status = any($2)`,
		network, []RefundStatus{RefundCreated, RefundAwaitingFinality}).Scan(&open); err != nil {
		return 0, fmt.Errorf("refunds on %s: %w", network, err)
	}
	return open, nil
}

// Overdue are the payments on a network that are still open for payment and
// have reached the moment they stop being open. Reaching the deadline is
// passing it: it is the moment payment closes, not the last moment it is open.
//
// Keyed by network and not by account, for the reason [Postgres.Candidates]
// gives. The oldest deadline first, and at most the number asked for: what is
// read here is moved on, so a bounded sweep drains what has waited longest and
// comes back for the rest.
func (s *Postgres) Overdue(ctx context.Context, network Network, at time.Time,
	limit int) ([]Due, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	rows, err := s.pool.Query(ctx, `
		select `+dueColumns+`
		  from payments p
		 where p.asset_network = $1 and p.status = $2 and p.expires_at <= $3
		 order by p.expires_at, p.id
		 limit $4`,
		network, AwaitingPayment, at, limit)
	if err != nil {
		return nil, fmt.Errorf("payments on %s: %w", network, err)
	}
	return scanDue(rows, network)
}

// Unsettled are the payments on a network that are waiting for finality, whose
// network has been read past their deadline, and that have no transfer still
// matched against them.
//
// Read past the deadline is a fact about the chain, not the clock: the block
// the network's position sits on is stamped at or after the deadline. A
// transfer the asset would honour is carried in a block stamped before the
// deadline, so once the position is past it every such transfer has been
// read, and nothing arriving later can be one. A position with no time is one
// written before positions carried a time, and says nothing, so nothing on
// its network expires until a round writes one.
//
// A matched transfer is one that may yet pay the payment, whether or not it
// has been seen where the chain keeps it. One short of what was asked never
// pays it, and one that is no longer on the chain no longer says anything, so
// neither holds a payment open.
//
// The oldest deadline first, and at most the number asked for, for the reason
// [Postgres.Overdue] gives.
func (s *Postgres) Unsettled(ctx context.Context, network Network, limit int) ([]Due, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	rows, err := s.pool.Query(ctx, `
		select `+dueColumns+`
		  from payments p
		  join observation_cursors c on c.network = p.asset_network
		 where p.asset_network = $1 and p.status = $2
		   and c.block_time is not null and c.block_time >= p.expires_at
		   and not exists (
		       select 1 from observations o
		        where o.account_id = p.account_id and o.payment_id = p.id
		          and o.reason = $3)
		 order by p.expires_at, p.id
		 limit $4`,
		network, AwaitingFinality, Matched, limit)
	if err != nil {
		return nil, fmt.Errorf("payments on %s: %w", network, err)
	}
	return scanDue(rows, network)
}

// scanDue reads what a sweep of the clock found, and closes the rows.
func scanDue(rows pgx.Rows, network Network) ([]Due, error) {
	defer rows.Close()

	var due []Due
	for rows.Next() {
		var (
			one Due
			row payer
		)
		if err := rows.Scan(&one.Account, &row.stored.ID,
			&row.network, &row.reference, &row.symbol, &row.decimals, &row.amount,
			&row.received, &row.stored.Destination, &row.stored.Status, &row.metadata,
			&row.stored.CreatedAt, &row.stored.ExpiresAt, &row.version); err != nil {
			return nil, fmt.Errorf("payments on %s: %w", network, err)
		}
		var err error
		if one.Payment, one.PaymentAt, err = row.payment(); err != nil {
			return nil, err
		}
		due = append(due, one)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("payments on %s: %w", network, err)
	}
	return due, nil
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
// An attempt confirmed here is announced, as attempt.confirming, with the
// transfer that confirmed it.
//
// The payments move with them too. What a transfer carried is put on the
// payment it was for, and taken back when the transfer is no longer on the
// chain, so a merchant reading one payment sees what came without reading a
// chain.
//
// Every move is conditioned on the revision the caller read, so a round that
// lost a race reports [ErrStale] and writes nothing.
func (s *Postgres) Record(ctx context.Context, tx pgx.Tx, network Network, first, last uint64, final bool, seen []Seen, sent []SeenRefund, now time.Time) error {
	txs := make([]string, 0, len(seen)+len(sent))
	for _, one := range seen {
		if err := recordOne(ctx, tx, network, seenAgainstAttempt(one), final, now); err != nil {
			return err
		}
		txs = append(txs, one.Transfer.Tx)
	}
	// A refund's transfer is a row of the same shape against the refund, and
	// nothing else moves for it here. What a refund reaches is decided by
	// finality, as what a payment reaches is.
	for _, one := range sent {
		if err := recordOne(ctx, tx, network, seenAgainstRefund(one), final, now); err != nil {
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
		if err := arrived(ctx, tx, one); err != nil {
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
		// The one event written outside Save: the payment does not move
		// here, its attempt does, and a merchant waiting for money is told
		// that some was seen. The payment is read again inside the
		// transaction, as arrived reads it, so that the event carries what
		// arrived rather than the payment as it stood before the round.
		p, _, err := find(ctx, tx, one.Account, one.Payment.ID())
		if err != nil {
			return err
		}
		event, err := AnnounceConfirming(p, one.Transfer)
		if err != nil {
			return err
		}
		if err := writeOutbox(ctx, tx, one.Account, p.ID(), event); err != nil {
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
// observation is one row of what a chain was seen to carry, against an
// attempt or against a refund. Exactly one of the two is set, which is what
// the schema requires of the row.
type observation struct {
	account        AccountID
	payment        ID
	attempt        *AttemptID
	refund         *RefundID
	transfer       Transfer
	reason         Reason
	implementation string
}

// seenAgainstAttempt is what a judged transfer of an attempt's key writes.
func seenAgainstAttempt(one Seen) observation {
	id := one.Attempt.ID()
	return observation{
		account: one.Account, payment: one.Payment.ID(), attempt: &id,
		transfer: one.Transfer, reason: one.Reason, implementation: one.Implementation,
	}
}

// seenAgainstRefund is what a judged transfer of a refund's key writes.
func seenAgainstRefund(one SeenRefund) observation {
	id := one.Refund.ID()
	return observation{
		account: one.Account, payment: one.Refund.PaymentID(), refund: &id,
		transfer: one.Transfer, reason: one.Reason, implementation: one.Implementation,
	}
}

func recordOne(ctx context.Context, q queries, network Network, one observation, final bool, now time.Time) error {
	t := one.transfer
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
		        $16, $17, $18, $19, $20)
		on conflict (network, key, tx) do update
		   set position = $7, block_height = $8, block_hash = $9, block_time = $10,
		       asset = $11, authorizer = $12, sender = $13, recipient = $14,
		       value = $15, reason = $16, implementation = $17, seen_at = $18,
		       final_at = coalesce(observations.final_at, $19)`,
		one.account, one.payment, one.attempt, network, t.Key, t.Tx,
		t.Position, height, t.BlockHash, t.BlockTime, t.Asset,
		t.Authorizer, t.From, t.To, t.Value, one.reason,
		one.implementation, now, finalAt, one.refund); err != nil {
		return fmt.Errorf("transfer %s: %w", t.Tx, err)
	}
	return nil
}

// gone is one observation that is no longer on the chain, and what it was for.
type gone struct {
	account AccountID
	payment ID
	attempt AttemptID
}

// vanish marks what was recorded in a span of finalised blocks and is no
// longer there, and takes back what stood on it: the attempts it was matched
// against go to issued, and what it told a payment had arrived is cleared.
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
	var lost []gone
	for rows.Next() {
		var (
			one     gone
			attempt *AttemptID
		)
		if err := rows.Scan(&one.account, &one.payment, &attempt); err != nil {
			rows.Close()
			return fmt.Errorf("observations on %s: %w", network, err)
		}
		// A row against a refund has no attempt, and nothing to take back
		// either: a refund reaches a status when the chain settles its
		// transfer and at no other moment, so a transfer that is no longer
		// there leaves the refund where it was.
		if attempt == nil {
			continue
		}
		one.attempt = *attempt
		lost = append(lost, one)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("observations on %s: %w", network, err)
	}

	for _, one := range lost {
		if err := taken(ctx, q, one); err != nil {
			return err
		}
	}
	return nil
}

// taken takes back what an observation stood for, once it is no longer on the
// chain. The attempt comes first and the payment second, which is the order
// everything that writes both uses.
func taken(ctx context.Context, q queries, one gone) error {
	if err := unconfirm(ctx, q, one); err != nil {
		return err
	}
	return unreceive(ctx, q, one)
}

// unconfirm takes an attempt back to issued, for a transfer that is no longer
// on the chain.
func unconfirm(ctx context.Context, q queries, one gone) error {
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
		return nil
	}
	a, at, err := findAttempt(ctx, q, one.account, one.payment, one.attempt)
	if err != nil {
		return err
	}
	if err := a.Unconfirm(); err != nil {
		// Issued already: nothing was confirmed against this attempt, so there
		// is nothing to take back.
		return nil
	}
	return saveAttempt(ctx, q, one.account, a, at)
}

// unreceive takes back what a payment was told arrived, for a transfer that is
// no longer on the chain. A payment that kept it would say money came that
// nobody sent, and a payment holds one arrival, so the write-once rule would
// keep any later one out.
//
// Taken back only when nothing still stands for it: a range read twice can
// leave one transfer gone and another recorded.
//
// What is counted and what is then written are read by two statements, so a
// round running beside this one could record an arrival between them and have
// it cleared here. One network is read by one round at a time, held by a
// lease, and inside a round what vanished is settled before anything new is
// applied. What runs beside that round is [Postgres.Vanish], which holds every
// attempt of the payment while it calls this, so nothing can record an arrival
// against it in between. The same is true of [unconfirm]. A change to how
// rounds are scheduled is a change to what these two rely on.
func unreceive(ctx context.Context, q queries, one gone) error {
	var standing int
	if err := q.QueryRow(ctx, `
		select count(*)
		  from observations
		 where account_id = $1 and payment_id = $2 and reason = any($3)`,
		one.account, one.payment, []Reason{Matched, Short}).Scan(&standing); err != nil {
		return fmt.Errorf("payment %s: %w", one.payment, err)
	}
	if standing > 0 {
		return nil
	}
	p, at, err := find(ctx, q, one.account, one.payment)
	if err != nil {
		return err
	}
	if err := p.Unreceive(); err != nil {
		// Nothing had arrived, or the payment is past the point where anything
		// can be taken back from it. Either way the row that went is not one
		// this field still answers for.
		return nil
	}
	return savePayment(ctx, q, one.account, p, at)
}

// Vanish marks one recorded transfer as no longer on the chain and takes back
// what stood on it, in a transaction of its own.
//
// A transfer that is named by a network, a key and a transaction is named the
// way a row is keyed, so a caller that read a row is naming that row back. One
// that is not there, or that vanished already, is nothing to do: whoever asked
// has been answered by somebody else, or is asking about a transfer this
// network never recorded.
//
// The row is locked first and the payment's attempts second, so that a round
// recording any transfer against that payment waits rather than landing
// between what [unconfirm] and [unreceive] count and what they then write.
// Every attempt and not only the one the row names: an observation names an
// attempt, so holding them all is what holds back a row written against any of
// them, and [unreceive] counts the rows of the whole payment. A round holding
// the payment and waiting for an attempt while this holds the attempts and
// waits for the payment is a deadlock, which PostgreSQL ends by failing one of
// them; both come round again.
func (s *Postgres) Vanish(ctx context.Context, network Network, key, tx string, now time.Time) (err error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	what := fmt.Sprintf("transfer %s", tx)
	t, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	defer func() { err = unwind(ctx, t, what, err) }()

	var (
		one     gone
		attempt *AttemptID
	)
	if err := t.QueryRow(ctx, `
		select account_id, payment_id, attempt_id
		  from observations
		 where network = $1 and key = $2 and tx = $3 and reason <> $4
		   for update`,
		network, key, tx, Vanished).Scan(&one.account, &one.payment, &attempt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("%s: %w", what, err)
	}
	// A row against a refund is marked and nothing else: there is no attempt
	// to put back, and the refund's own status was never moved by the
	// transfer being seen.
	if attempt != nil {
		one.attempt = *attempt
		if _, err := t.Exec(ctx, `
			select 1 from attempts
			 where account_id = $1 and payment_id = $2
			   for update`, one.account, one.payment); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
	}
	if _, err := t.Exec(ctx, `
		update observations
		   set reason = $4, seen_at = $5
		 where network = $1 and key = $2 and tx = $3`,
		network, key, tx, Vanished, now); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if attempt != nil {
		if err := taken(ctx, t, one); err != nil {
			return err
		}
	}
	if err := t.Commit(ctx); err != nil {
		return fmt.Errorf("%s: %w", what, err)
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
	stored         Stored
	network        string
	reference      string
	symbol         string
	decimals       uint8
	amount         string
	received       *string
	metadata       []byte
	version        int64
	checkoutKeyID  *string
	returnURL      *string
	closedAt       *time.Time
	idempotencyKey *string
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
	if r.checkoutKeyID != nil {
		r.stored.Checkout.KeyID = *r.checkoutKeyID
	}
	if r.idempotencyKey != nil {
		r.stored.Idempotency.Key = *r.idempotencyKey
	}
	if r.returnURL != nil {
		r.stored.ReturnURL = *r.returnURL
	}
	if r.closedAt != nil {
		r.stored.ClosedAt = *r.closedAt
	}
	p, err := Restore(r.stored)
	if err != nil {
		return nil, Revision{}, fmt.Errorf("payment %s: %w", r.stored.ID, err)
	}
	return p, Revision{id: string(r.stored.ID), at: r.version}, nil
}

// refundColumns are what a row holds of a refund, in the order the reads below
// scan them. The asset is not among them: a refund is in the asset of the
// payment it is against, and the reads join it from there rather than keeping
// a second copy that could come to disagree.
const refundColumns = `r.id, r.amount, r.destination, r.scheme, r.network, r.key,
                       r.expires_at, r.status, r.token_hash, r.token_key_id,
                       r.idempotency_key, r.idempotency_body_hash, r.created_at,
                       r.closed_at, r.version,
                       p.asset_network, p.asset_reference, p.asset_symbol, p.asset_decimals`

// CreateRefund stores a refund nothing has stored before.
//
// The payment is read under a lock, and the ceiling is measured inside the
// same transaction as the write. Two requests that arrive together would
// otherwise both read the same remainder and both write, and the merchant
// could sign each of them: the money that went out would be more than the
// money that came in, which is the one thing this check is for.
//
// What the second of them measures against is the first one's row, which it
// reads because every connection opens its transactions at read committed.
// The pool says so rather than this transaction, so that the next check
// written this way inherits it.
func (s *Postgres) CreateRefund(ctx context.Context, account AccountID, r *Refund) (err error) {
	if r == nil {
		return errors.New("payment: nothing to refund")
	}
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("refund %s: %w", r.ID(), err)
	}
	defer func() { err = unwind(ctx, tx, fmt.Sprintf("refund %s", r.ID()), err) }()

	var (
		status   Status
		received *string
	)
	// for update, and the payment before the refunds: whoever takes both
	// takes them in this order.
	err = tx.QueryRow(ctx, `
		select status, received from payments
		 where account_id = $1 and id = $2 for update`, account, r.PaymentID()).
		Scan(&status, &received)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", r.PaymentID(), ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("refund %s: %w", r.ID(), err)
	}
	if status != Succeeded {
		return Problems{{Field: "status", Message: fmt.Sprintf(
			"payment is %s, and only a %s payment can be refunded", status, Succeeded)}}
	}
	if received == nil {
		return Problems{{Field: "status", Message: "nothing arrived for this payment"}}
	}
	held, err := refunded(ctx, tx, account, r.PaymentID())
	if err != nil {
		return fmt.Errorf("refund %s: %w", r.ID(), err)
	}
	left, err := remaining(*received, held)
	if err != nil {
		return fmt.Errorf("refund %s: %w", r.ID(), err)
	}
	if left.Cmp(r.Amount().Amount()) < 0 {
		// Nothing left is nothing left. The subtraction cannot go below zero
		// while this is the only writer of refunds, and the refusal should
		// not depend on that holding to be able to say so.
		if left.Sign() < 0 {
			left = new(big.Int)
		}
		rest, err := NewMoney(r.Amount().Asset(), left)
		if err != nil {
			return fmt.Errorf("refund %s: %w", r.ID(), err)
		}
		return Problems{{Field: "amount", Message: fmt.Sprintf(
			"%s is more than the %s this payment has left to refund",
			r.Amount().Units(), rest.Units())}}
	}
	token, idempotency := r.Token(), r.Idempotency()
	var key *string
	var bodyHash []byte
	if idempotency.IsSet() {
		key, bodyHash = &idempotency.Key, idempotency.BodyHash
	}
	_, err = tx.Exec(ctx, `
		insert into refunds (
			account_id, payment_id, id, amount, destination, scheme, network, key,
			expires_at, status, token_hash, token_key_id, idempotency_key,
			idempotency_body_hash, created_at
		) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		account, r.PaymentID(), r.ID(), digits(r.Amount()), r.Destination(), r.Scheme(),
		r.Network(), r.Key(), r.ExpiresAt(), r.Status(), token.Hash, token.KeyID,
		key, bodyHash, r.CreatedAt())
	if err != nil {
		// Constrained first: what the driver raises holds the values that
		// clashed, and two of them are keys nothing here publishes.
		refused := postgres.Constrained(err)
		var broken postgres.Constraint
		if errors.As(refused, &broken) {
			switch broken.Name {
			case "refunds_by_idempotency_key":
				return fmt.Errorf("refund %s: %w", r.ID(), ErrIdempotencyKeyUsed)
			case "refunds_by_key":
				return fmt.Errorf("refund %s: %w", r.ID(), ErrKeyTaken)
			}
		}
		return fmt.Errorf("refund %s: %w", r.ID(), refused)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("refund %s: %w", r.ID(), err)
	}
	return nil
}

// remaining is what is left of arrived once held is taken off it, both in the
// asset's smallest unit as decimal digits.
func remaining(arrived, held string) (*big.Int, error) {
	in, ok := new(big.Int).SetString(arrived, 10)
	if !ok {
		return nil, fmt.Errorf("what arrived is not a number: %q", arrived)
	}
	out, ok := new(big.Int).SetString(held, 10)
	if !ok {
		return nil, fmt.Errorf("what the refunds hold is not a number: %q", held)
	}
	return in.Sub(in, out), nil
}

// refunded reads what a payment's refunds hold through whatever the caller is
// holding, so that a round already inside a transaction reads what that
// transaction can see.
func refunded(ctx context.Context, q queries, account AccountID, payment ID) (string, error) {
	var held string
	err := q.QueryRow(ctx, `
		select coalesce(sum(amount), 0)::text from refunds
		 where account_id = $1 and payment_id = $2 and status <> $3`,
		account, payment, RefundExpired).Scan(&held)
	if err != nil {
		return "", err
	}
	return held, nil
}

// FindRefund reads one refund of one payment.
func (s *Postgres) FindRefund(ctx context.Context, account AccountID, payment ID, id RefundID) (*Refund, Revision, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	var row refunder
	// Filtered on the account and the payment as well as the identifier: a
	// refund of somebody else's payment is not found rather than found and
	// refused.
	err := s.pool.QueryRow(ctx, `
		select `+refundColumns+`
		  from refunds r
		  join payments p on p.account_id = r.account_id and p.id = r.payment_id
		 where r.account_id = $1 and r.payment_id = $2 and r.id = $3`, account, payment, id).
		Scan(&row.stored.ID, &row.amount, &row.stored.Destination, &row.stored.Scheme,
			&row.stored.Network, &row.stored.Key, &row.stored.ExpiresAt, &row.stored.Status,
			&row.stored.Token.Hash, &row.stored.Token.KeyID, &row.idempotencyKey,
			&row.stored.Idempotency.BodyHash, &row.stored.CreatedAt, &row.closedAt,
			&row.version, &row.network, &row.reference, &row.symbol, &row.decimals)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, Revision{}, fmt.Errorf("%s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, Revision{}, fmt.Errorf("refund %s: %w", id, err)
	}
	row.stored.PaymentID = payment
	return row.refund()
}

// RefundTransfer reads the transfer that matched a refund.
//
// The refund's own rows and not the payment's: both stand against the same
// payment, and a reading that did not tell them apart would answer a refund
// with the transfer that paid the payment it sends back.
func (s *Postgres) RefundTransfer(ctx context.Context, account AccountID, payment ID, id RefundID) (Transfer, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	var (
		t      Transfer
		height int64
	)
	err := s.pool.QueryRow(ctx, `
		select asset, key, authorizer, sender, recipient, value,
		       tx, position, block_height, block_hash, block_time
		  from observations
		 where account_id = $1 and payment_id = $2 and refund_id = $3 and reason = $4
		 order by seen_at desc limit 1`, account, payment, id, Matched).
		Scan(&t.Asset, &t.Key, &t.Authorizer, &t.From, &t.To, &t.Value,
			&t.Tx, &t.Position, &height, &t.BlockHash, &t.BlockTime)
	if errors.Is(err, pgx.ErrNoRows) {
		return Transfer{}, false, nil
	}
	if err != nil {
		return Transfer{}, false, fmt.Errorf("refund %s: %w", id, err)
	}
	if height < 0 {
		return Transfer{}, false, fmt.Errorf("refund %s: block height %d is below zero", id, height)
	}
	t.BlockHeight = uint64(height)
	t.BlockTime = t.BlockTime.UTC()
	return t, true, nil
}

// FindRefundByKey reads the refund that account opened under an idempotency
// key.
func (s *Postgres) FindRefundByKey(ctx context.Context, account AccountID, key string) (*Refund, Revision, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	var payment ID
	var id RefundID
	// The key alone would find another account's refund, and a merchant
	// chooses their own keys.
	err := s.pool.QueryRow(ctx, `
		select payment_id, id from refunds
		 where account_id = $1 and idempotency_key = $2`, account, key).Scan(&payment, &id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, Revision{}, fmt.Errorf("idempotency key: %w", ErrNotFound)
	}
	if err != nil {
		// The message names no key: an error is what a log keeps, and the key
		// is the merchant's.
		return nil, Revision{}, fmt.Errorf("reading a refund by its idempotency key: %w", err)
	}
	return s.FindRefund(ctx, account, payment, id)
}

// Refunded is what the payment's refunds hold against what arrived.
func (s *Postgres) Refunded(ctx context.Context, account AccountID, payment ID) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	held, err := refunded(ctx, s.pool, account, payment)
	if err != nil {
		return "", fmt.Errorf("payment %s: %w", payment, err)
	}
	return held, nil
}

// refunder is a refund's row as the reads above scan it, with the asset of
// the payment it is against.
type refunder struct {
	stored         RefundStored
	amount         string
	network        string
	reference      string
	symbol         string
	decimals       uint8
	idempotencyKey *string
	closedAt       *time.Time
	version        int64
}

// refund rebuilds what the columns hold, and the revision they were read at.
func (r refunder) refund() (*Refund, Revision, error) {
	asset, err := NewAsset(Network(r.network), r.reference, r.symbol, r.decimals)
	if err != nil {
		return nil, Revision{}, fmt.Errorf("refund %s: %w", r.stored.ID, err)
	}
	if r.stored.Amount, err = ParseMoney(asset, r.amount); err != nil {
		return nil, Revision{}, fmt.Errorf("refund %s: %w", r.stored.ID, err)
	}
	if r.idempotencyKey != nil {
		r.stored.Idempotency.Key = *r.idempotencyKey
	}
	if r.closedAt != nil {
		r.stored.ClosedAt = *r.closedAt
	}
	refund, err := RestoreRefund(r.stored)
	if err != nil {
		return nil, Revision{}, fmt.Errorf("refund %s: %w", r.stored.ID, err)
	}
	return refund, Revision{id: string(r.stored.ID), at: r.version}, nil
}

// refundDueColumns are what the sweeps of the clock read of a refund and the
// payment it sends back.
// The asset comes with refundColumns, which joins it from the payment: a
// query using these selects the payment once and reads it once.
const refundDueColumns = `r.account_id, r.payment_id, ` + refundColumns

// RefundDue is a refund a sweep of the clock picked up, with the revision it
// was read at. What [Due] is on the paying side.
type RefundDue struct {
	Account  AccountID
	Refund   *Refund
	RefundAt Revision
}

// RefundCandidate is a recorded transfer that would settle a refund: matched
// against it, and seen where the chain said it would not be replaced.
type RefundCandidate struct {
	Account     AccountID
	Refund      *Refund
	RefundAt    Revision
	Key         string
	Tx          string
	BlockHeight uint64
	BlockHash   string
}

// RefundCandidates reads the transfers recorded against the refunds of a
// network that would settle one, from a place onwards.
//
// What [Postgres.Candidates] is on the paying side, and read the same way: in
// the order the chain carried them, a bounded number at a time.
func (s *Postgres) RefundCandidates(ctx context.Context, network Network, from Place, limit int) ([]RefundCandidate, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	rows, err := s.pool.Query(ctx, `
		select o.key, o.tx, o.block_height, o.block_hash, `+refundDueColumns+`
		  from observations o
		  join refunds r on r.account_id = o.account_id and r.payment_id = o.payment_id
		                and r.id = o.refund_id
		  join payments p on p.account_id = r.account_id and p.id = r.payment_id
		 where o.network = $1 and o.reason = $2 and o.final_at is not null
		   and o.refund_id is not null and r.status = any($3)
		   and (o.block_height, o.tx) > ($4, $5)
		 order by o.block_height, o.tx
		 limit $6`,
		network, Matched, []RefundStatus{RefundCreated, RefundAwaitingFinality},
		from.BlockHeight, from.Tx, limit)
	if err != nil {
		return nil, fmt.Errorf("observations on %s: %w", network, err)
	}
	defer rows.Close()

	var candidates []RefundCandidate
	for rows.Next() {
		var (
			c   RefundCandidate
			row refunder
		)
		if err := rows.Scan(&c.Key, &c.Tx, &c.BlockHeight, &c.BlockHash,
			&c.Account, &row.stored.PaymentID, &row.stored.ID, &row.amount,
			&row.stored.Destination, &row.stored.Scheme, &row.stored.Network,
			&row.stored.Key, &row.stored.ExpiresAt, &row.stored.Status,
			&row.stored.Token.Hash, &row.stored.Token.KeyID, &row.idempotencyKey,
			&row.stored.Idempotency.BodyHash, &row.stored.CreatedAt, &row.closedAt,
			&row.version, &row.network, &row.reference, &row.symbol, &row.decimals); err != nil {
			return nil, fmt.Errorf("observations on %s: %w", network, err)
		}
		if c.Refund, c.RefundAt, err = row.refund(); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("observations on %s: %w", network, err)
	}
	return candidates, nil
}

// OverdueRefunds reads the refunds of a network whose deadline has passed and
// which are still open for a transfer to settle.
func (s *Postgres) OverdueRefunds(ctx context.Context, network Network, at time.Time, limit int) ([]RefundDue, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	rows, err := s.pool.Query(ctx, `
		select `+refundDueColumns+`
		  from refunds r
		  join payments p on p.account_id = r.account_id and p.id = r.payment_id
		 where r.network = $1 and r.status = $2 and r.expires_at <= $3
		 order by r.expires_at, r.id
		 limit $4`,
		network, RefundCreated, at, limit)
	if err != nil {
		return nil, fmt.Errorf("refunds on %s: %w", network, err)
	}
	return scanRefundDue(rows, network)
}

// UnsettledRefunds reads the refunds of a network that are waiting, whose
// deadline the reading of the chain has passed, and whose key nothing was
// seen to have spent out of the wallet the refund would come from.
//
// The position and not the clock, for the reason the paying side reads it
// that way: a refund whose transfer this deployment has not read yet is not
// one nothing settled.
//
// What holds a refund open is a transfer of its key out of the payment's
// destination, whatever the rules made of that transfer. Matched is one such
// transfer; so is one carrying more than was authorised, or going somewhere
// else, or arriving after the deadline. Each of those is the merchant's
// money already gone, and expiring the refund would hand its amount back to
// what the payment can still refund and let the same money go out twice.
//
// The sender is what tells those apart from a transfer somebody else made. A
// key is a nonce, and a nonce is the signer's own, so any wallet can spend
// the same one; a transfer that left another wallet costs the merchant
// nothing and must not hold their money up. A row whose transfer is no
// longer on the chain holds nothing either.
func (s *Postgres) UnsettledRefunds(ctx context.Context, network Network, limit int) ([]RefundDue, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	rows, err := s.pool.Query(ctx, `
		select `+refundDueColumns+`
		  from refunds r
		  join payments p on p.account_id = r.account_id and p.id = r.payment_id
		  join observation_cursors c on c.network = r.network
		 where r.network = $1 and r.status = $2
		   and c.block_time is not null and c.block_time >= r.expires_at
		   and not exists (
		       select 1 from observations o
		        where o.account_id = r.account_id and o.payment_id = r.payment_id
		          and o.refund_id = r.id and o.reason <> $3
		          and o.sender = p.destination)
		 order by r.expires_at, r.id
		 limit $4`,
		network, RefundAwaitingFinality, Vanished, limit)
	if err != nil {
		return nil, fmt.Errorf("refunds on %s: %w", network, err)
	}
	return scanRefundDue(rows, network)
}

// scanRefundDue reads what the two sweeps above select.
func scanRefundDue(rows pgx.Rows, network Network) ([]RefundDue, error) {
	defer rows.Close()

	var due []RefundDue
	for rows.Next() {
		var (
			one RefundDue
			row refunder
		)
		if err := rows.Scan(&one.Account, &row.stored.PaymentID, &row.stored.ID, &row.amount,
			&row.stored.Destination, &row.stored.Scheme, &row.stored.Network,
			&row.stored.Key, &row.stored.ExpiresAt, &row.stored.Status,
			&row.stored.Token.Hash, &row.stored.Token.KeyID, &row.idempotencyKey,
			&row.stored.Idempotency.BodyHash, &row.stored.CreatedAt, &row.closedAt,
			&row.version, &row.network, &row.reference, &row.symbol, &row.decimals); err != nil {
			return nil, fmt.Errorf("refunds on %s: %w", network, err)
		}
		r, at, err := row.refund()
		if err != nil {
			return nil, err
		}
		one.Refund, one.RefundAt = r, at
		due = append(due, one)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("refunds on %s: %w", network, err)
	}
	return due, nil
}

// SaveRefund writes back a refund that was read, with the event its move
// produced, in one transaction. What [Postgres.Save] is on the paying side.
func (s *Postgres) SaveRefund(ctx context.Context, account AccountID, r *Refund, at Revision, e Event) (err error) {
	if r == nil {
		return errors.New("payment: nothing to save")
	}
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("refund %s: %w", r.ID(), err)
	}
	defer func() { err = unwind(ctx, tx, fmt.Sprintf("refund %s", r.ID()), err) }()

	// closed_at is written once, when a final status is first saved: a resave
	// does not move the time the page's life is counted from.
	tag, err := tx.Exec(ctx, `
		update refunds
		   set status = $4, version = version + 1,
		       closed_at = coalesce(closed_at, case when $5 then now() end)
		 where account_id = $1 and id = $2 and version = $3`,
		account, r.ID(), at.at, r.Status(), r.Status().Final())
	if err != nil {
		return fmt.Errorf("refund %s: %w", r.ID(), err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("refund %s: %w", r.ID(), ErrStale)
	}
	if e.Name != "" || len(e.Payload) > 0 {
		if err := writeOutbox(ctx, tx, account, r.PaymentID(), e); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("refund %s: %w", r.ID(), err)
	}
	return nil
}
