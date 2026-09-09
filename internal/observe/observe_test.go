package observe_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sucopay/sucopay/internal/observe"
	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
)

const network = payment.Network("polygon")

// store returns a fresh database, and the pool behind it for the tests that
// have to look at a row these types would not hand back.
func store(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := postgres.Open(t.Context(), postgrestest.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return pool.Conns()
}

func at(height uint64, hash string) observe.Position {
	return observe.Position{Height: height, Hash: hash}
}

func TestInit_KeepsThePositionItWasFirstGiven(t *testing.T) {
	t.Parallel()
	cursors := observe.NewCursors(store(t))
	now := time.Now()

	if err := cursors.Init(t.Context(), network, at(100, "block100"), now); err != nil {
		t.Fatal(err)
	}
	if err := cursors.Init(t.Context(), network, at(999, "block999"), now); err != nil {
		t.Fatal(err)
	}
	got, ok, err := cursors.Get(t.Context(), network)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != at(100, "block100") {
		t.Errorf("Get = %v, %v; want the position Init was first given", got, ok)
	}
}

func TestGet_SaysWhenNobodyHasSetAPosition(t *testing.T) {
	t.Parallel()
	cursors := observe.NewCursors(store(t))

	got, ok, err := cursors.Get(t.Context(), network)
	if err != nil {
		t.Fatal(err)
	}
	if ok || got != (observe.Position{}) {
		t.Errorf("Get = %v, %v; want the zero position and false", got, ok)
	}
	has, err := cursors.Has(t.Context(), network)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("Has says there is a position on a network nobody has read")
	}
	if err := cursors.Init(t.Context(), network, at(1, "block1"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if has, err = cursors.Has(t.Context(), network); err != nil || !has {
		t.Errorf("Has = %v, %v after Init", has, err)
	}
}

func TestAdvance_MovesOnlyFromThePositionThatWasRead(t *testing.T) {
	t.Parallel()
	pool := store(t)
	cursors := observe.NewCursors(pool)
	now := time.Now()
	if err := cursors.Init(t.Context(), network, at(100, "block100"), now); err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := cursors.Advance(t.Context(), tx, network, at(100, "block100"), at(103, "block103"), now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	stale, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(t, stale)
	err = cursors.Advance(t.Context(), stale, network, at(100, "block100"), at(110, "block110"), now)
	if !errors.Is(err, observe.ErrMoved) {
		t.Fatalf("Advance from a position that moved gave %v, want %v", err, observe.ErrMoved)
	}

	got, _, err := cursors.Get(t.Context(), network)
	if err != nil {
		t.Fatal(err)
	}
	if got != at(103, "block103") {
		t.Errorf("the position is %v, want the one that was committed", got)
	}
}

// Both halves of a position are compared. A height whose block was replaced
// carries the same number, and a hash read at another height is a reader that
// has lost its place, so neither one on its own says the row is where the
// caller left it.
func TestAdvance_RefusesAPositionThatMatchesInOnlyOneHalf(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		from observe.Position
	}{
		{"another hash at the height that was read", at(100, "anotherblock")},
		{"the hash that was read at another height", at(99, "block100")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pool := store(t)
			cursors := observe.NewCursors(pool)
			now := time.Now()
			if err := cursors.Init(t.Context(), network, at(100, "block100"), now); err != nil {
				t.Fatal(err)
			}

			tx, err := pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer rollback(t, tx)
			if err := cursors.Advance(t.Context(), tx, network, c.from, at(101, "block101"), now); !errors.Is(err, observe.ErrMoved) {
				t.Errorf("Advance from %v gave %v, want %v", c.from, err, observe.ErrMoved)
			}
		})
	}
}

func TestAdvance_TouchesTheRowWhenTheChainStoodStill(t *testing.T) {
	t.Parallel()
	pool := store(t)
	cursors := observe.NewCursors(pool)
	if err := cursors.Init(t.Context(), network, at(100, "block100"), time.Now()); err != nil {
		t.Fatal(err)
	}
	before := updatedAt(t, pool)

	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	later := before.Add(time.Minute)
	if err := cursors.Advance(t.Context(), tx, network, at(100, "block100"), at(100, "block100"), later); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if after := updatedAt(t, pool); !after.Equal(later.UTC().Truncate(time.Microsecond)) {
		t.Errorf("updated_at is %s, want %s: a round that read no new block still read", after, later)
	}
}

func TestAdvance_LeavesThePositionWhereItWasWhenTheRoundIsRolledBack(t *testing.T) {
	t.Parallel()
	pool := store(t)
	cursors := observe.NewCursors(pool)
	now := time.Now()
	if err := cursors.Init(t.Context(), network, at(100, "block100"), now); err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := cursors.Advance(t.Context(), tx, network, at(100, "block100"), at(105, "block105"), now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}

	got, _, err := cursors.Get(t.Context(), network)
	if err != nil {
		t.Fatal(err)
	}
	if got != at(100, "block100") {
		t.Errorf("the position is %v after a round that was rolled back, want %v", got, at(100, "block100"))
	}
}

func TestSet_HandsBackWhatItReplaced(t *testing.T) {
	t.Parallel()
	cursors := observe.NewCursors(store(t))
	now := time.Now()

	before, had, err := cursors.Set(t.Context(), network, at(100, "block100"), now)
	if err != nil {
		t.Fatal(err)
	}
	if had || before != (observe.Position{}) {
		t.Errorf("Set on a network with no position gave %v, %v", before, had)
	}
	before, had, err = cursors.Set(t.Context(), network, at(50, "block50"), now)
	if err != nil {
		t.Fatal(err)
	}
	if !had || before != at(100, "block100") {
		t.Errorf("Set gave %v, %v; want the position it replaced", before, had)
	}
	got, _, err := cursors.Get(t.Context(), network)
	if err != nil {
		t.Fatal(err)
	}
	if got != at(50, "block50") {
		t.Errorf("the position is %v, want the one Set was given", got)
	}
}

func TestAcquire_GivesOneNameToOneHolder(t *testing.T) {
	t.Parallel()
	pool := store(t)
	mine, theirs := observe.NewLeases(pool), observe.NewLeases(pool)

	held, err := mine.Acquire(t.Context(), string(network))
	if err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Fatal("nobody held the lease and it was not given")
	}
	held, err = theirs.Acquire(t.Context(), string(network))
	if err != nil {
		t.Fatal(err)
	}
	if held {
		t.Error("two instances hold one lease")
	}

	expire(t, pool, string(network))
	held, err = theirs.Acquire(t.Context(), string(network))
	if err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Error("an expired lease was not taken over")
	}
}

func TestRenew_ExtendsTheLeaseOfWhoeverHoldsIt(t *testing.T) {
	t.Parallel()
	pool := store(t)
	mine, theirs := observe.NewLeases(pool), observe.NewLeases(pool)
	if held, err := mine.Acquire(t.Context(), string(network)); err != nil || !held {
		t.Fatal(err)
	}

	if renewed, err := theirs.Renew(t.Context(), string(network)); err != nil || renewed {
		t.Errorf("Renew by an instance that does not hold the lease = %v, %v", renewed, err)
	}
	expire(t, pool, string(network))
	if renewed, err := mine.Renew(t.Context(), string(network)); err != nil || !renewed {
		t.Errorf("Renew by the holder = %v, %v", renewed, err)
	}
	if held, err := theirs.Acquire(t.Context(), string(network)); err != nil || held {
		t.Errorf("the renewed lease was taken over: %v, %v", held, err)
	}
}

func TestRelease_LetsTheNextInstanceTakeTheName(t *testing.T) {
	t.Parallel()
	pool := store(t)
	mine, theirs := observe.NewLeases(pool), observe.NewLeases(pool)
	if held, err := mine.Acquire(t.Context(), string(network)); err != nil || !held {
		t.Fatal(err)
	}

	if err := theirs.Release(t.Context(), string(network)); err != nil {
		t.Fatal(err)
	}
	if held, err := theirs.Acquire(t.Context(), string(network)); err != nil || held {
		t.Errorf("releasing somebody else's lease freed it: %v, %v", held, err)
	}
	if err := mine.Release(t.Context(), string(network)); err != nil {
		t.Fatal(err)
	}
	if held, err := theirs.Acquire(t.Context(), string(network)); err != nil || !held {
		t.Errorf("the released lease was not free: %v, %v", held, err)
	}
}

// rollback ends a transaction a test is done with.
func rollback(t *testing.T, tx pgx.Tx) {
	t.Helper()
	if err := tx.Rollback(context.WithoutCancel(t.Context())); err != nil {
		t.Error(err)
	}
}

// The statements name their network, and with one row in the table a
// statement that dropped it would still pass every other test here. The two
// networks below stand at the same position, so a write that reached both
// could not be told apart from one that reached the right one.
func TestAdvance_TouchesOnlyTheNetworkItWasGiven(t *testing.T) {
	t.Parallel()
	pool := store(t)
	cursors := observe.NewCursors(pool)
	const other = payment.Network("ethereum")
	now := time.Now()
	for _, n := range []payment.Network{network, other} {
		if err := cursors.Init(t.Context(), n, at(100, "block100"), now); err != nil {
			t.Fatal(err)
		}
	}

	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := cursors.Advance(t.Context(), tx, network, at(100, "block100"), at(101, "block101"), now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got := positionOf(t, pool, other); got != at(100, "block100") {
		t.Errorf("%s is at %v, and the round advanced %s", other, got, network)
	}
	if got := positionOf(t, pool, network); got != at(101, "block101") {
		t.Errorf("%s is at %v, want the position the round advanced it to", network, got)
	}
}

// Reading and replacing a position also name their network, and here the two
// stand apart so that a read of the wrong row comes back with the wrong
// numbers.
func TestGet_AndSetReadTheNetworkTheyWereAsked(t *testing.T) {
	t.Parallel()
	pool := store(t)
	cursors := observe.NewCursors(pool)
	const other = payment.Network("ethereum")
	now := time.Now()
	if err := cursors.Init(t.Context(), network, at(100, "block100"), now); err != nil {
		t.Fatal(err)
	}
	if err := cursors.Init(t.Context(), other, at(500, "block500"), now); err != nil {
		t.Fatal(err)
	}

	got, _, err := cursors.Get(t.Context(), other)
	if err != nil {
		t.Fatal(err)
	}
	if got != at(500, "block500") {
		t.Errorf("Get(%s) = %v, want %v", other, got, at(500, "block500"))
	}
	before, had, err := cursors.Set(t.Context(), other, at(600, "block600"), now)
	if err != nil {
		t.Fatal(err)
	}
	if !had || before != at(500, "block500") {
		t.Errorf("Set(%s) replaced %v, %v; want the position that network stood at", other, before, had)
	}
	if got := positionOf(t, pool, network); got != at(100, "block100") {
		t.Errorf("%s is at %v after another network was set", network, got)
	}
}

func TestAcquire_HoldsOneNameWithoutHoldingAnother(t *testing.T) {
	t.Parallel()
	pool := store(t)
	mine, theirs := observe.NewLeases(pool), observe.NewLeases(pool)
	if held, err := mine.Acquire(t.Context(), "polygon"); err != nil || !held {
		t.Fatal(err)
	}

	if held, err := theirs.Acquire(t.Context(), "ethereum"); err != nil || !held {
		t.Errorf("another name was not free: %v, %v", held, err)
	}
	if renewed, err := mine.Renew(t.Context(), "ethereum"); err != nil || renewed {
		t.Errorf("Renew reached a name this instance does not hold: %v, %v", renewed, err)
	}
	if err := theirs.Release(t.Context(), "ethereum"); err != nil {
		t.Fatal(err)
	}
	if renewed, err := mine.Renew(t.Context(), "polygon"); err != nil || !renewed {
		t.Errorf("releasing another name took this one away: %v, %v", renewed, err)
	}
}

func TestAcquire_GivesTheHolderItsOwnLeaseBack(t *testing.T) {
	t.Parallel()
	leases := observe.NewLeases(store(t))
	for range 2 {
		held, err := leases.Acquire(t.Context(), string(network))
		if err != nil {
			t.Fatal(err)
		}
		if !held {
			t.Error("an instance asking again for a lease it holds was told it does not")
		}
	}
}

// Set is what an operator is shown as the position they replaced, so what it
// hands back has to be the row it wrote over, not one that was already stale
// when the call started.
func TestSet_HandsBackTheRowItWroteOverWhenARoundWasWriting(t *testing.T) {
	t.Parallel()
	pool := store(t)
	cursors := observe.NewCursors(pool)
	now := time.Now()
	if err := cursors.Init(t.Context(), network, at(100, "block100"), now); err != nil {
		t.Fatal(err)
	}

	round, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := cursors.Advance(t.Context(), round, network, at(100, "block100"), at(200, "block200"), now); err != nil {
		t.Fatal(err)
	}

	type answer struct {
		before observe.Position
		had    bool
		err    error
	}
	done := make(chan answer, 1)
	go func() {
		before, had, err := cursors.Set(t.Context(), network, at(50, "block50"), now)
		done <- answer{before, had, err}
	}()
	// The round holds the row, so Set is waiting on it. Nothing here can see
	// that it is waiting; what the test asserts is what Set says once the
	// round it waited for has landed.
	time.Sleep(200 * time.Millisecond)
	if err := round.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if !got.had || got.before != at(200, "block200") {
		t.Errorf("Set gave %v, %v; want the position the round had just committed", got.before, got.had)
	}
}

func TestInit_RefusesAHeightThatNoRowCouldHold(t *testing.T) {
	t.Parallel()
	cursors := observe.NewCursors(store(t))
	tallest := observe.Position{Height: math.MaxUint64, Hash: "block"}

	if err := cursors.Init(t.Context(), network, tallest, time.Now()); err == nil {
		t.Error("a height a bigint cannot hold was stored")
	}
	if _, _, err := cursors.Set(t.Context(), network, tallest, time.Now()); err == nil {
		t.Error("Set stored a height a bigint cannot hold")
	}
	if has, err := cursors.Has(t.Context(), network); err != nil || has {
		t.Errorf("the refused height left a row: %v, %v", has, err)
	}
}

// positionOf reads a row without going through the type under test.
func positionOf(t *testing.T, pool *pgxpool.Pool, network payment.Network) observe.Position {
	t.Helper()
	var (
		height int64
		hash   string
	)
	if err := pool.QueryRow(t.Context(),
		`select height, hash from observation_cursors where network = $1`, network).Scan(&height, &hash); err != nil {
		t.Fatal(err)
	}
	return observe.Position{Height: uint64(height), Hash: hash}
}

// updatedAt reads the column no method hands back.
func updatedAt(t *testing.T, pool *pgxpool.Pool) time.Time {
	t.Helper()
	var when time.Time
	if err := pool.QueryRow(t.Context(),
		`select updated_at from observation_cursors where network = $1`, network).Scan(&when); err != nil {
		t.Fatal(err)
	}
	return when
}

// expire puts a lease's deadline in the past, which is the only way a test
// can watch a takeover without waiting out the term.
func expire(t *testing.T, pool *pgxpool.Pool, name string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(),
		`update leases set expires_at = now() - interval '1 second' where name = $1`, name); err != nil {
		t.Fatal(err)
	}
}

// Whoever is not reading a network reads when its position was last written to
// tell whether anybody is.
func TestTouched_IsWhenThePositionWasLastWritten(t *testing.T) {
	t.Parallel()
	pool := store(t)
	cursors := observe.NewCursors(pool)

	if _, found, err := cursors.Touched(t.Context(), "polygon"); err != nil || found {
		t.Fatalf("a network nobody has read reads as written at: %v, %v", found, err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := cursors.Init(t.Context(), "polygon", at(7, "0xabc"), now); err != nil {
		t.Fatal(err)
	}

	touched, found, err := cursors.Touched(t.Context(), "polygon")

	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("the network reads as one nobody has read")
	}
	if !touched.Equal(now) {
		t.Errorf("the position was last written at %s, want %s", touched, now)
	}
}
