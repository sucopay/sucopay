package checkout

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sucopay/sucopay/internal/payment"
)

// storeTimeout bounds one call to the database.
const storeTimeout = 5 * time.Second

// ErrNotFound is a token no page can be read for: none the deployment
// derived, one derived under a key since swapped, or one whose payment
// ended more than [PageLife] ago. The three are one answer, so that the
// answer says nothing about what exists.
var ErrNotFound = errors.New("checkout: not found")

// Postgres reads what a page needs that the payment's own store does not
// answer: which payment a token is for, and what was seen for it.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres reads through pool.
func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

// Owner is what a token was found to be for: the payment, its account, and
// the account's name, which is what the page calls the merchant.
type Owner struct {
	Account payment.AccountID
	Name    string
	Payment payment.ID
}

// Lookup is the payment a token's hash is kept on, if the token was derived
// under keyID and the payment's page can still be read at now.
func (s *Postgres) Lookup(ctx context.Context, hash []byte, keyID string, now time.Time) (Owner, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	var (
		o        Owner
		closedAt *time.Time
	)
	err := s.pool.QueryRow(ctx, `
		select p.account_id, a.name, p.id, p.closed_at
		  from payments p
		  join accounts a on a.id = p.account_id
		 where p.checkout_hash = $1 and p.checkout_key_id = $2`, hash, keyID).
		Scan(&o.Account, &o.Name, &o.Payment, &closedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Owner{}, ErrNotFound
	}
	if err != nil {
		return Owner{}, fmt.Errorf("checkout: %w", err)
	}
	var closed time.Time
	if closedAt != nil {
		closed = *closedAt
	}
	if !Readable(closed, now) {
		return Owner{}, ErrNotFound
	}
	return o, nil
}

// Matched is the transfer seen for a payment that matched it, or nil where
// none has: what a page shows as the result. The first in the order the chain
// carried them, should more than one have been seen, which is the one the
// payment's arrival was credited from; confirming until the payment
// succeeded.
//
// The same order as [payment.Postgres.MatchedTransfer], and it has to be: one
// payment answering a merchant with one transfer and showing its payer
// another would leave the two of them reading different chains.
func (s *Postgres) Matched(ctx context.Context, account payment.AccountID, id payment.ID, status payment.Status) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	var (
		r       Result
		height  int64
		finalAt *time.Time
	)
	err := s.pool.QueryRow(ctx, `
		select tx, block_height, block_time, value, final_at
		  from observations
		 where account_id = $1 and payment_id = $2 and reason = $3
		   and attempt_id is not null
		 order by block_height, tx, position limit 1`, account, id, payment.Matched).
		Scan(&r.Tx, &height, &r.BlockTime, &r.Value, &finalAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("payment %s: %w", id, err)
	}
	if height < 0 {
		return nil, fmt.Errorf("payment %s: block height %d is below zero", id, height)
	}
	r.BlockHeight = uint64(height)
	r.BlockTime = r.BlockTime.UTC()
	r.Confirming = status != payment.Succeeded
	if status == payment.Succeeded && finalAt != nil {
		at := finalAt.UTC()
		r.ReceivedAt = &at
	}
	return &r, nil
}
