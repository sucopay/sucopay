package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// uniqueViolation is what PostgreSQL calls a row that would break a unique
// constraint or a unique index.
const uniqueViolation = "23505"

// Constraint reports which rule of the schema a write broke.
//
// It carries the name and nothing else. What the driver raises holds the
// values that clashed, and one of those values is the key a payer is about to
// spend: a caller that wraps the driver's error, or logs it, publishes the
// key. The name is what a caller can act on, and is safe to print.
type Constraint struct {
	// Name is the constraint or index the write broke, as the schema names it.
	Name string
}

// Error names the constraint that refused the write.
func (c Constraint) Error() string { return "postgres: " + c.Name }

// Constrained replaces a broken unique constraint with a [Constraint] holding
// its name. Every other error is returned as it was, so that a caller can pass
// anything through this on the way out.
func Constrained(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == uniqueViolation {
		return Constraint{Name: pg.ConstraintName}
	}
	return err
}
