package payment_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
)

// The schema constrains a payment's status to the same list this package
// defines. Two places hold that list, so this is what keeps them together: a
// status added here without a migration fails, and so does one dropped from
// the schema while the domain still has it.
func TestSchema_AcceptsEveryStatusTheDomainDefinesAndNoOther(t *testing.T) {
	t.Parallel()
	dsn := postgrestest.Fresh(t)
	pool, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}

	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := conn.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("closing: %v", err)
		}
	}()

	insert := func(t *testing.T, id, status string) error {
		t.Helper()
		_, err := conn.Exec(t.Context(), `
			insert into payments (
				id, account_id, asset_network, asset_reference, asset_symbol,
				asset_decimals, amount, destination, status, created_at, expires_at
			) values ($1, '00000000-0000-0000-0000-000000000001', 'polygon', 'r', 'JPYC',
				18, 1, '0xabc', $2, now(), now() + interval '1 hour')`, id, status)
		return err
	}

	for _, status := range every {
		t.Run(status.String(), func(t *testing.T) {
			if err := insert(t, strings.Repeat("a", 31)+status.String()[:1], status.String()); err != nil {
				t.Errorf("the schema refused %s, which the domain defines: %v", status, err)
			}
		})
	}
	t.Run("one the domain removed", func(t *testing.T) {
		if err := insert(t, strings.Repeat("b", 32), "confirming"); err == nil {
			t.Error("the schema accepted a status the domain does not define")
		}
	})
}
