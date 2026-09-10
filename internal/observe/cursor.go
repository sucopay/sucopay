package observe

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sucopay/sucopay/internal/payment"
)

// storeTimeout bounds one statement. A call that hangs holds a connection and
// the lease that brought it here, so it fails instead.
const storeTimeout = 10 * time.Second

// ErrMoved reports that the position is no longer the one the caller read, so
// the write was refused. Either another instance advanced it, or an operator
// put it somewhere else. The caller reads the position again and starts its
// round from there.
var ErrMoved = errors.New("observe: the position moved since it was read")

// Position is a block a network has been read up to.
//
// A cursor is what moves and a position is where it is. [Cursors] holds one
// cursor for each network, and this is the value it holds; what an operator
// moves, and what a word like finalized-changed is about, is the cursor.
//
// The hash is held with the height because a height alone does not say which
// chain it was on: the block at a height can be replaced, and a reader that
// carried on from the height would skip whatever the new block holds.
type Position struct {
	// Height is the block's place in the chain.
	Height uint64
	// Hash identifies the block that was at that height when it was read.
	Hash string
}

// Cursors store how far each network has been read.
type Cursors struct {
	pool *pgxpool.Pool
}

// NewCursors returns the positions kept in pool.
func NewCursors(pool *pgxpool.Pool) *Cursors { return &Cursors{pool: pool} }

// Get is the position a network has been read to, and whether it has one.
func (c *Cursors) Get(ctx context.Context, network payment.Network) (Position, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	var (
		height int64
		hash   string
	)
	err := c.pool.QueryRow(ctx, `
		select height, hash
		  from observation_cursors
		 where network = $1`, network).Scan(&height, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return Position{}, false, nil
	}
	if err != nil {
		return Position{}, false, fmt.Errorf("position of %s: %w", network, err)
	}
	if height < 0 {
		return Position{}, false, fmt.Errorf("position of %s: height %d", network, height)
	}
	return Position{Height: uint64(height), Hash: hash}, true, nil
}

// Has reports whether a network has a position. It is what a service asks
// before issuing an attempt: a network nothing has ever read would take the
// payment and never see anything.
//
// Whether a round has finished on it lately is [Cursors.Touched], and is not
// asked here. A reader that stopped reads forward from where it left off.
func (c *Cursors) Has(ctx context.Context, network payment.Network) (bool, error) {
	_, ok, err := c.Get(ctx, network)
	return ok, err
}

// Touched is when the position of a network was last written, and whether the
// network has one at all.
//
// A round writes the position every time it goes round, whether or not the
// position moved, so this is when the network was last read rather than when
// it last moved. An instance that cannot take the lease reads it to say
// whether whoever holds the lease is still getting anywhere.
func (c *Cursors) Touched(ctx context.Context, network payment.Network) (time.Time, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	var at time.Time
	err := c.pool.QueryRow(ctx,
		`select updated_at from observation_cursors where network = $1`, network).Scan(&at)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return time.Time{}, false, nil
	case err != nil:
		return time.Time{}, false, fmt.Errorf("position of %s: %w", network, err)
	}
	return at, true, nil
}

// Init gives a network its first position. A network that already has one
// keeps it, so an instance starting up does not pull the position back to
// wherever it would have begun.
func (c *Cursors) Init(ctx context.Context, network payment.Network, at Position, now time.Time) error {
	height, err := bigint(at)
	if err != nil {
		return fmt.Errorf("position of %s: %w", network, err)
	}
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	if _, err := c.pool.Exec(ctx, `
		insert into observation_cursors (network, height, hash, updated_at)
		values ($1, $2, $3, $4)
		on conflict (network) do nothing`, network, height, at.Hash, now); err != nil {
		return fmt.Errorf("position of %s: %w", network, err)
	}
	return nil
}

// Advance moves a network's position, inside the transaction that writes what
// was found on the way. It reports [ErrMoved] when the row no longer holds
// from, which is what keeps two instances reading one network from writing
// over each other.
//
// The round that read no new block still advances: to and from are equal, and
// the moment is written, which is how a reader that is keeping up is told
// apart from one that has stopped.
func (c *Cursors) Advance(ctx context.Context, tx pgx.Tx, network payment.Network, from, to Position, now time.Time) error {
	was, err := bigint(from)
	if err != nil {
		return fmt.Errorf("position of %s: %w", network, err)
	}
	next, err := bigint(to)
	if err != nil {
		return fmt.Errorf("position of %s: %w", network, err)
	}
	// The caller's context and the caller's transaction. A deadline of its own
	// here would cut a round that the caller is still bounding.
	tag, err := tx.Exec(ctx, `
		update observation_cursors
		   set height = $4, hash = $5, updated_at = $6
		 where network = $1 and height = $2 and hash = $3`,
		network, was, from.Hash, next, to.Hash, now)
	if err != nil {
		return fmt.Errorf("position of %s: %w", network, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("position of %s: %w", network, ErrMoved)
	}
	return nil
}

// Set puts a network's position where an operator says, and hands back what
// was there. It is not part of a round: it is how somebody puts a reader back
// on a chain that moved under it, and what it hands back is what an operator
// is shown as the position they replaced.
func (c *Cursors) Set(ctx context.Context, network payment.Network, at Position, now time.Time) (before Position, had bool, err error) {
	next, err := bigint(at)
	if err != nil {
		return Position{}, false, fmt.Errorf("position of %s: %w", network, err)
	}
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return Position{}, false, fmt.Errorf("position of %s: %w", network, err)
	}
	defer func() {
		// The subject is put on here rather than by unwind, which serves the
		// rounds as well, and a round already names the network it is of.
		was := err
		if err = unwind(ctx, tx, err); err != nil && was == nil {
			err = fmt.Errorf("position of %s: %w", network, err)
		}
	}()

	// The row is held while it is read, so that what comes back is what this
	// call replaced. A round advancing the position at the same moment either
	// finishes before the read or waits behind it.
	var (
		height int64
		hash   string
	)
	found := true
	switch scan := tx.QueryRow(ctx, `
		select height, hash
		  from observation_cursors
		 where network = $1
		   for update`, network).Scan(&height, &hash); {
	case errors.Is(scan, pgx.ErrNoRows):
		found = false
	case scan != nil:
		return Position{}, false, fmt.Errorf("position of %s: %w", network, scan)
	}

	if _, err := tx.Exec(ctx, `
		insert into observation_cursors (network, height, hash, updated_at)
		values ($1, $2, $3, $4)
		on conflict (network) do update
		   set height = $2, hash = $3, updated_at = $4`,
		network, next, at.Hash, now); err != nil {
		return Position{}, false, fmt.Errorf("position of %s: %w", network, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Position{}, false, fmt.Errorf("position of %s: %w", network, err)
	}
	if !found {
		return Position{}, false, nil
	}
	if height < 0 {
		return Position{}, false, fmt.Errorf("position of %s: height %d", network, height)
	}
	return Position{Height: uint64(height), Hash: hash}, true, nil
}

// bigint is the position's height as the column holds one. A chain that had
// produced more blocks than this would have outlived several of everything
// else here, but a value the column cannot hold is refused rather than stored
// as a negative one.
func bigint(at Position) (int64, error) {
	if at.Height > math.MaxInt64 {
		return 0, fmt.Errorf("height %d is more than a bigint holds", at.Height)
	}
	return int64(at.Height), nil
}
