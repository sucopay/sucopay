package postgres_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
)

func TestOpen_UsesTheDatabaseTheURLNames(t *testing.T) {
	dsn := postgrestest.URL(t)
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	absent := *u
	absent.Path = "/no_such_database"

	t.Run("the one that exists", func(t *testing.T) {
		pool, err := postgres.Open(t.Context(), dsn)
		if err != nil {
			t.Fatalf("open: %v\n\nRun `make dev` to start the database these tests need.", err)
		}
		defer pool.Close()

		version, err := pool.ServerVersion(t.Context())
		if err != nil {
			t.Fatalf("server version: %v", err)
		}
		if version == "" {
			t.Error("the server reported no version, so nothing confirms what was reached")
		}
	})

	t.Run("one that does not", func(t *testing.T) {
		pool, err := postgres.Open(t.Context(), absent.String())

		if err == nil {
			pool.Close()
			t.Fatal("want an error, got none: the name in the URL was not the database that was opened")
		}
		if !strings.Contains(err.Error(), "no_such_database") {
			t.Errorf("error does not name the database it looked for: %v", err)
		}
	})
}

func TestOpen_ReportsAFailureWithoutThePasswordFromTheURL(t *testing.T) {
	const password = "hunter2"

	wrongPassword, err := url.Parse(postgrestest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	wrongPassword.User = url.UserPassword(wrongPassword.User.Username(), password)

	for _, c := range []struct {
		name string
		url  string
	}{
		{"not a URL", "postgres://admin:" + password + "@%%%bad host/db"},
		// The driver redacts a password written as userinfo and repeats one
		// given as a setting. Both forms connect, so both are tried here.
		{"a setting beside a bad one", "postgres://admin@127.0.0.1:5432/db?password=" + password + "&connect_timeout=abc"},
		{"a setting beside a bad sslmode", "postgres://admin@127.0.0.1:5432/db?password=" + password + "&sslmode=bogus"},
		// Redaction cuts at the first colon, so half of this one used to be
		// printed.
		{"holding a colon", "postgres://admin:hun:" + password + "@127.0.0.1:5432/d%zz"},
		{"wrong password", wrongPassword.String()},
		{"nothing listening", "postgres://admin:" + password + "@127.0.0.1:1/db"},
	} {
		t.Run(c.name, func(t *testing.T) {
			pool, err := postgres.Open(t.Context(), c.url)

			if err == nil {
				pool.Close()
				t.Fatal("want an error, got none")
			}
			for _, part := range []string{password, "hun:"} {
				if strings.Contains(err.Error(), part) {
					t.Errorf("the error carries %q from the url: %v", part, err)
				}
			}
		})
	}
}

func TestOpen_RefusesAConnectionToAnotherMachineThatNothingAuthenticates(t *testing.T) {
	// The check runs before any connection, so none of these reach a network.
	pool, err := postgres.Open(t.Context(), "postgres://admin:pw@db.example.com:5432/db")

	if err == nil {
		pool.Close()
		t.Fatal("want an error, got none: saying nothing about sslmode connects in the clear")
	}
	if !strings.Contains(err.Error(), "sslmode") {
		t.Errorf("error does not say what the url has to ask for: %v", err)
	}
	if strings.Contains(err.Error(), "pw") {
		t.Errorf("the error carries the password: %v", err)
	}
}

func TestOpen_AcceptsADatabaseOnThisMachineWithoutTLS(t *testing.T) {
	// make dev and CI both connect this way, and nothing on a wire can stand
	// between the two ends.
	pool, err := postgres.Open(t.Context(), postgrestest.URL(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	pool.Close()
}

func TestOpen_RefusesAnEmptyURL(t *testing.T) {
	// The driver would read this as "use the defaults" and reach a local
	// socket, so the refusal has to be ours and has to say so.
	pool, err := postgres.Open(t.Context(), "")

	if err == nil {
		pool.Close()
		t.Fatal("want an error, got none")
	}
	if !strings.Contains(err.Error(), "no url") {
		t.Errorf("error does not say what was missing: %v", err)
	}
}

func TestClose_ReleasesTheConnections(t *testing.T) {
	const name = "suco-close-test"
	dsn := postgrestest.WithName(t, postgrestest.URL(t), name)

	pool, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ServerVersion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if open := postgrestest.Backends(t, name); open == 0 {
		t.Fatal("the server saw no connection, so this cannot tell whether one was released")
	}

	pool.Close()

	if open := postgrestest.Backends(t, name); open != 0 {
		t.Errorf("the server still has %d connection(s) after Close", open)
	}
}
