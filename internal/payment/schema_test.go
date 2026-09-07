package payment_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
)

// migrated opens a database with the schema on it and hands back a connection
// to write rows through.
func migrated(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := postgrestest.Fresh(t)
	pool, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("closing: %v", err)
		}
	})
	return conn
}

// The schema constrains a payment's status to the same list this package
// defines. Two places hold that list, so this is what keeps them together: a
// status added here without a migration fails, and so does one dropped from
// the schema while the domain still has it.
func TestSchema_AcceptsEveryStatusTheDomainDefinesAndNoOther(t *testing.T) {
	t.Parallel()
	conn := migrated(t)

	insert := func(t *testing.T, id, status string) error {
		t.Helper()
		// received covers the amount, because a succeeded row without it is
		// refused for a different reason than the one under test.
		_, err := conn.Exec(t.Context(), `
			insert into payments (
				id, account_id, asset_network, asset_reference, asset_symbol,
				asset_decimals, amount, received, destination, status, created_at, expires_at
			) values ($1, '00000000-0000-0000-0000-000000000001', 'polygon', 'r', 'JPYC',
				18, 1, 1, '0xabc', $2, now(), now() + interval '1 hour')`, id, status)
		return err
	}

	// Numbered rather than derived from the status name, which is not distinct
	// in its first letters.
	for i, status := range every {
		t.Run(status.String(), func(t *testing.T) {
			if err := insert(t, fmt.Sprintf("%032d", i), status.String()); err != nil {
				t.Errorf("the schema refused %s, which the domain defines: %v", status, err)
			}
		})
	}
	t.Run("one the domain removed", func(t *testing.T) {
		if err := insert(t, fmt.Sprintf("%032d", len(every)), "confirming"); err == nil {
			t.Error("the schema accepted a status the domain does not define")
		}
	})
}

// The aggregate refuses to succeed a payment on less than it billed. The row
// refuses it too, because the aggregate is not the only thing that can write
// one.
func TestSchema_RefusesASucceededRowOnLessThanItBilled(t *testing.T) {
	t.Parallel()
	conn := migrated(t)

	insert := func(t *testing.T, id, status string, received any) error {
		t.Helper()
		_, err := conn.Exec(t.Context(), `
			insert into payments (
				id, account_id, asset_network, asset_reference, asset_symbol,
				asset_decimals, amount, received, destination, status, created_at, expires_at
			) values ($1, '00000000-0000-0000-0000-000000000001', 'polygon', 'r', 'JPYC',
				18, 100, $3, '0xabc', $2, now(), now() + interval '1 hour')`, id, status, received)
		return err
	}

	for i, c := range []struct {
		name     string
		received any
		want     bool
	}{
		{"nothing arrived", nil, false},
		{"less than billed", 99, false},
		{"exactly what was billed", 100, true},
		{"more than billed", 101, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := insert(t, fmt.Sprintf("%032d", i), "succeeded", c.received)
			if c.want && err != nil {
				t.Errorf("the schema refused a covered payment: %v", err)
			}
			if !c.want && err == nil {
				t.Error("the schema accepted a succeeded payment nobody covered")
			}
		})
	}

	// The rule is about succeeded rows only. A payment still waiting is allowed
	// to have less than it billed, which is how an underpayment is recorded.
	t.Run("an open payment may hold less than it billed", func(t *testing.T) {
		if err := insert(t, fmt.Sprintf("%032d", 99), "awaiting_payment", 1); err != nil {
			t.Errorf("the schema refused to record an underpayment: %v", err)
		}
	})
}

// everyAttemptStatus is the list the domain defines. The schema constrains the
// column to the same one, and this is what keeps the two together.
var everyAttemptStatus = []payment.AttemptStatus{payment.Issued, payment.Confirming}

func TestSchema_AcceptsEveryAttemptStatusTheDomainDefinesAndNoOther(t *testing.T) {
	t.Parallel()
	conn := migrated(t)

	if _, err := conn.Exec(t.Context(), `
		insert into payments (
			id, account_id, asset_network, asset_reference, asset_symbol,
			asset_decimals, amount, destination, status, created_at, expires_at
		) values ('00000000000000000000000000000001',
			'00000000-0000-0000-0000-000000000001', 'polygon', 'r', 'JPYC',
			18, 1, '0xabc', 'awaiting_payment', now(), now() + interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
	insert := func(t *testing.T, id, status string) error {
		t.Helper()
		_, err := conn.Exec(t.Context(), `
			insert into attempts (
				account_id, payment_id, id, scheme, network, key, valid_before,
				status, created_at
			) values ('00000000-0000-0000-0000-000000000001',
				'00000000000000000000000000000001', $1, 'eip3009', 'polygon', $1,
				now() + interval '1 hour', $2, now())`, id, status)
		return err
	}

	// One row per status, and a live attempt is unique per payment, so the
	// rows that are not under test are moved out of the way by leaving only
	// the one being inserted live: each subtest deletes what came before it.
	for i, status := range everyAttemptStatus {
		t.Run(status.String(), func(t *testing.T) {
			if _, err := conn.Exec(t.Context(), `delete from attempts`); err != nil {
				t.Fatal(err)
			}
			if err := insert(t, fmt.Sprintf("%032d", i), status.String()); err != nil {
				t.Errorf("the schema refused %s, which the domain defines: %v", status, err)
			}
		})
	}
	t.Run("submitted, which only submission will add", func(t *testing.T) {
		if _, err := conn.Exec(t.Context(), `delete from attempts`); err != nil {
			t.Fatal(err)
		}
		if err := insert(t, fmt.Sprintf("%032d", len(everyAttemptStatus)), "submitted"); err == nil {
			t.Error("the schema accepted a status the domain does not define")
		}
	})
}
