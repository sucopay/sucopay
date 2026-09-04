package postgres

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
)

// Read from inside the package because configure, budget and acquireTimeout
// are not things a caller of this package asks for, and a way to reach them
// would exist for these tests and nothing else.

// The connection count belongs to the operator: their server has a
// max_connections this process cannot see. These two say that whatever the
// driver decided from the url is what comes out.
func TestConfigure_TakesTheConnectionCountTheURLAsksFor(t *testing.T) {
	t.Parallel()
	cfg, err := configure("postgres://u:p@127.0.0.1:5432/db?pool_max_conns=7")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.MaxConns != 7 {
		t.Errorf("MaxConns = %d, want the 7 the url asked for", cfg.MaxConns)
	}
}

// Asked of the driver rather than worked out here. Repeating its rule would
// only agree with itself: max(4, NumCPU) and max(6, NumCPU) are the same
// number on any machine with more than six cores, so a changed default would
// go unnoticed on most of them.
func TestConfigure_LeavesTheCountWhereTheDriverPutIt(t *testing.T) {
	t.Parallel()
	const dsn = "postgres://u:p@127.0.0.1:5432/db"
	driver, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := configure(dsn)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.MaxConns != driver.MaxConns {
		t.Errorf("MaxConns = %d, want the driver's own %d", cfg.MaxConns, driver.MaxConns)
	}
}

func TestBudget_BoundsAWaitThatCarriesNoBoundOfItsOwn(t *testing.T) {
	t.Parallel()
	ctx, cancel := budget(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("no deadline, so a caller with none waits as long as the pool takes")
	}
	if left := time.Until(deadline); left > acquireTimeout || left < acquireTimeout-time.Second {
		t.Errorf("deadline in %s, want about %s", left, acquireTimeout)
	}
}

func TestBudget_LeavesABoundShorterThanItAlone(t *testing.T) {
	t.Parallel()
	short := 50 * time.Millisecond
	caller, stop := context.WithTimeout(context.Background(), short)
	defer stop()

	ctx, cancel := budget(caller)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("no deadline")
	}
	if left := time.Until(deadline); left > short {
		t.Errorf("deadline in %s, want no more than the caller's %s", left, short)
	}
}

// Inside the package so that the bound is acquireTimeout itself. Written
// against a literal, this would keep passing if Acquire were wired to either
// of the ten-second constants beside it.
func TestAcquire_GivesUpWithinTheBudgetWhenTheCallerSetNoDeadline(t *testing.T) {
	t.Parallel()
	pool := lending(t)
	held, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	began := time.Now()
	_, err = pool.Acquire(context.WithoutCancel(t.Context()))
	waited := time.Since(began)

	if err == nil {
		t.Fatal("a second connection came out of a pool holding one")
	}
	if waited > 2*acquireTimeout {
		t.Errorf("waited %s, want no more than the budget of %s", waited, acquireTimeout)
	}
}

// lending opens a pool the database will lend exactly one connection from, so
// that a second caller waits on something a test controls.
//
// postgres_test.go has one of these too. Acquire's own behaviour is tested
// through the exported surface and belongs there; only the test that has to
// name acquireTimeout is in here, and reaching across the two packages for a
// fixture would need a surface that exists for nothing else.
func lending(t *testing.T) *Pool {
	t.Helper()
	u, err := url.Parse(postgrestest.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("pool_max_conns", "1")
	u.RawQuery = q.Encode()

	pool, err := Open(t.Context(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
