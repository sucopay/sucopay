package refund

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

// PageLife is how long a refund's page can still be read after the refund
// ends. The same as suco Checkout's, and for the same reason: the token is in
// the merchant's history, and a key that read an outcome for ever would be a
// key to keep.
const PageLife = 30 * 24 * time.Hour

// ErrNotFound is a token no page can be read for: none the deployment
// derived, one derived under a key since swapped, or one whose refund ended
// more than [PageLife] ago. The three are one answer, so that the answer says
// nothing about what exists.
var ErrNotFound = errors.New("refund: not found")

// Postgres reads which refund a token is for.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres reads through pool.
func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

// Owner is what a token was found to be for.
type Owner struct {
	Account payment.AccountID
	Payment payment.ID
	Refund  payment.RefundID
}

// Lookup is the refund a token's hash is kept on, if the token was derived
// under keyID and the refund's page can still be read at now.
func (s *Postgres) Lookup(ctx context.Context, hash []byte, keyID string, now time.Time) (Owner, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	var (
		o        Owner
		closedAt *time.Time
	)
	err := s.pool.QueryRow(ctx, `
		select account_id, payment_id, id, closed_at
		  from refunds
		 where token_hash = $1 and token_key_id = $2`, hash, keyID).
		Scan(&o.Account, &o.Payment, &o.Refund, &closedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Owner{}, ErrNotFound
	}
	if err != nil {
		return Owner{}, fmt.Errorf("refund: %w", err)
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

// Readable says whether a refund's page can still be read at now: while the
// refund is open, and for [PageLife] after it ends.
func Readable(closedAt, now time.Time) bool {
	return closedAt.IsZero() || now.Before(closedAt.Add(PageLife))
}
