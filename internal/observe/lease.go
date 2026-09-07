package observe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LeaseTTL is how long a lease is held for. An instance renews it while it
// works and lets it lapse when it stops, so this is also how long a network
// waits before another instance takes over from one that died.
const LeaseTTL = 30 * time.Second

// Leases hand each name to one instance at a time.
//
// The deadline is compared with the database's clock rather than any
// instance's, so instances whose clocks disagree still agree on who holds
// what.
type Leases struct {
	pool   *pgxpool.Pool
	holder string
}

// NewLeases returns the leases kept in pool, held under a name no other
// instance has. Two Leases over one pool are two instances, which is what a
// test needs to watch one take over from the other.
func NewLeases(pool *pgxpool.Pool) *Leases {
	var b [16]byte
	// No error to handle: crypto/rand.Read has had none to return since Go
	// 1.24, which is what credential.NewID says of the identifiers it mints.
	_, _ = rand.Read(b[:])
	return &Leases{pool: pool, holder: hex.EncodeToString(b[:])}
}

// Acquire takes the lease on a name, and reports whether this instance now
// holds it. A lease whose deadline has passed is taken over, one another
// instance still holds is left alone, and one this instance already holds is
// put out again, so that a caller which is not sure whether its last call
// landed can ask again.
func (l *Leases) Acquire(ctx context.Context, name string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	tag, err := l.pool.Exec(ctx, `
		insert into leases (name, holder, expires_at)
		values ($1, $2, now() + $3::interval)
		on conflict (name) do update
		   set holder = $2, expires_at = now() + $3::interval
		 where leases.expires_at < now() or leases.holder = $2`, name, l.holder, LeaseTTL.String())
	if err != nil {
		return false, fmt.Errorf("lease %s: %w", name, err)
	}
	return tag.RowsAffected() == 1, nil
}

// Renew puts the deadline out again, and reports whether this instance still
// holds the lease. A false is another instance having taken the name over, and
// the caller stops working on it.
func (l *Leases) Renew(ctx context.Context, name string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	tag, err := l.pool.Exec(ctx, `
		update leases
		   set expires_at = now() + $3::interval
		 where name = $1 and holder = $2`, name, l.holder, LeaseTTL.String())
	if err != nil {
		return false, fmt.Errorf("lease %s: %w", name, err)
	}
	return tag.RowsAffected() == 1, nil
}

// Release gives up the lease, so that the next instance takes the name without
// waiting out the deadline. A lease another instance holds is left alone.
func (l *Leases) Release(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	if _, err := l.pool.Exec(ctx,
		`delete from leases where name = $1 and holder = $2`, name, l.holder); err != nil {
		return fmt.Errorf("lease %s: %w", name, err)
	}
	return nil
}
