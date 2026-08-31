// Package postgrestest gives tests one database and one way to ask the server
// what it sees.
//
// Tests that need PostgreSQL do not skip when it is missing. A skipped test
// reports success without having run, so the absence of a database is a
// failure that says how to start one.
package postgrestest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// ddlTimeout bounds making and dropping a database. Neither waits on anything
// a test controls, so one that does not answer is a server to look at rather
// than a run to keep waiting on.
const ddlTimeout = 30 * time.Second

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
	conn := connect(t, URL(t))
	defer closing(t, conn)

	var n int
	err := conn.QueryRow(t.Context(),
		"select count(*) from pg_stat_activity where application_name = $1", name).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Fresh returns the URL of a database of this test's own, dropped when the
// test ends. Migrations and rows are what these tests are about, so sharing
// one database between them would make the order they ran in part of the
// result.
func Fresh(t *testing.T) string {
	t.Helper()

	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	// The name goes into a statement that cannot take a parameter, so it is
	// made here rather than taken from anywhere: hexadecimal and a fixed
	// prefix are what it can be.
	name := "suco_test_" + hex.EncodeToString(suffix[:])

	admin := connect(t, URL(t))
	making, stopMaking := context.WithTimeout(t.Context(), ddlTimeout)
	defer stopMaking()
	if _, err := admin.Exec(making, `create database `+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, stop := context.WithTimeout(context.WithoutCancel(t.Context()), ddlTimeout)
		defer stop()
		drop := connect(t, URL(t))
		defer closing(t, drop)
		if _, err := drop.Exec(ctx, `drop database if exists `+name+` with (force)`); err != nil {
			t.Errorf("dropping %s: %v", name, err)
		}
	})
	closing(t, admin)

	u, err := url.Parse(URL(t))
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

func connect(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.WithoutCancel(t.Context()), dsn)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func closing(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	if err := conn.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Errorf("closing a connection: %v", err)
	}
}
