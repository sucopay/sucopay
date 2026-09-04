package postgres

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Read from inside the package because configure is not something a caller of
// this package asks for, and a way to reach it would exist for these tests and
// nothing else.

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
