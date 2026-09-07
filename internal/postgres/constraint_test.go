package postgres_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
)

// The driver's own error carries the row that clashed: the values, the
// detail, and the schema around them. A caller wants to know which rule was
// broken, and the value that broke it is a payment key nobody may log.
func TestConstrained_KeepsTheNameAndNothingOfTheRow(t *testing.T) {
	t.Parallel()
	pool, err := postgres.Open(t.Context(), postgrestest.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const secret = "6ea1e04c5f2c4d8e9f0a1b2c3d4e5f60"
	if _, err := pool.Conns().Exec(t.Context(),
		`create table nonces (key text, constraint nonces_key_key unique (key))`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Conns().Exec(t.Context(), `insert into nonces (key) values ($1)`, secret); err != nil {
		t.Fatal(err)
	}
	_, clash := pool.Conns().Exec(t.Context(), `insert into nonces (key) values ($1)`, secret)
	if clash == nil {
		t.Fatal("the second row went in")
	}
	var raised *pgconn.PgError
	if !errors.As(clash, &raised) {
		t.Fatalf("the driver raised %v, which is not its own error type", clash)
	}
	if !strings.Contains(raised.Detail, secret) {
		t.Fatalf("the driver's error does not carry the value, so this test proves nothing: %+v", raised)
	}

	got := postgres.Constrained(fmt.Errorf("issuing: %w", clash))
	var broken postgres.Constraint
	if !errors.As(got, &broken) {
		t.Fatalf("Constrained gave %v, and errors.As found no Constraint", got)
	}
	if broken.Name != "nonces_key_key" {
		t.Errorf("the constraint is %q, want %q", broken.Name, "nonces_key_key")
	}
	for _, rendered := range []string{got.Error(), fmt.Sprintf("%v", got), fmt.Sprintf("%+v", got)} {
		if strings.Contains(rendered, secret) {
			t.Errorf("the error still carries the value that clashed: %s", rendered)
		}
	}
}

func TestConstrained_LeavesEveryOtherFailureAsItWas(t *testing.T) {
	t.Parallel()
	pool, err := postgres.Open(t.Context(), postgrestest.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	cases := []struct {
		name string
		make func(t *testing.T) error
	}{
		{"one the driver never raised", func(*testing.T) error { return errors.New("no connection was free") }},
		{"nothing at all", func(*testing.T) error { return nil }},
		{"a table that is not there", func(t *testing.T) error {
			_, err := pool.Conns().Exec(t.Context(), `select 1 from nothing_of_the_sort`)
			if err == nil {
				t.Fatal("the query went through")
			}
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			was := c.make(t)
			if got := postgres.Constrained(was); !errors.Is(got, was) {
				t.Errorf("Constrained gave %v, want the error it was given", got)
			}
		})
	}
}
