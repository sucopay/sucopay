// Package postgrestest gives tests one database and one way to ask the server
// what it sees.
//
// Tests that need PostgreSQL do not skip when it is missing. A skipped test
// reports success without having run, so the absence of a database is a
// failure that says how to start one.
package postgrestest

import (
	"context"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Var is the environment variable naming the database tests run against. The
// Makefile sets it to what `make dev` starts, and CI to its own service.
const Var = "SUCO_TEST_DATABASE_URL"

// URL is the database to run against.
func URL(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(Var)
	if dsn == "" {
		t.Fatalf("%s is not set. Run `make dev` to start a database, then `make test`.", Var)
	}
	return dsn
}

// WithName returns the URL with an application name attached, which is how a
// test tells its own connections apart from every other connection to the
// server.
func WithName(t *testing.T, dsn, name string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("application_name", name)
	u.RawQuery = q.Encode()
	return u.String()
}

// Backends counts the server's connections carrying an application name. It
// asks over a connection of its own, so what it counts is what the server sees
// rather than what the code under test believes.
func Backends(t *testing.T, name string) int {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := conn.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("closing the connection this count was taken over: %v", err)
		}
	}()

	var n int
	err = conn.QueryRow(t.Context(),
		"select count(*) from pg_stat_activity where application_name = $1", name).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
