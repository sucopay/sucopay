package payment

import (
	"context"
	"errors"
)

// AccountID names the account a payment belongs to.
//
// It is opaque here. Whether an account exists is something the database knows
// and this package does not, so nothing is checked about the value beyond it
// being present.
type AccountID string

// String returns the account as written.
func (a AccountID) String() string { return string(a) }

var (
	// ErrNotFound reports that no payment was stored under that account and
	// identifier. It is not a problem with what the caller asked for.
	ErrNotFound = errors.New("payment: no such payment")

	// ErrStale reports that the payment changed after it was read, so the
	// write was refused rather than laid over what the other writer left. A
	// caller that sees this reads the payment again and decides again, rather
	// than reapplying the move it was going to make.
	ErrStale = errors.New("payment: changed since it was read")
)

// Revision is a payment as its row stood when it was read.
//
// It is not a fact about the payment, it is a fact about the row, which is why
// it is not on the aggregate: a Payment that was never stored has no revision,
// and a field holding one would need a value meaning "not stored yet" that
// nothing could tell from a real first revision.
//
// It carries the identifier it was read under. Version numbers are small and
// dense, and every payment starts at the same one, so a caller working through
// several payments could hand one payment's revision to another's save and have
// it match by coincidence. Saving checks that it did not.
type Revision struct {
	id ID
	at int64
}

// Repository stores payments.
//
// Save owns its transaction rather than joining one the caller opened. Whatever
// has to be written atomically with a payment is written inside it, so that no
// caller can leave half of it done: an event that reports a payment nobody
// stored is worse than an event nobody sent.
type Repository interface {
	// Create stores a payment nothing has stored before.
	Create(ctx context.Context, account AccountID, p *Payment) error

	// Find reads a payment, and the revision it was read at. It reports
	// [ErrNotFound] when that account has no payment under that identifier,
	// which includes the case of another account having one.
	Find(ctx context.Context, account AccountID, id ID) (*Payment, Revision, error)

	// Save writes back a payment that was read, and reports [ErrStale] if
	// anything wrote to it in between.
	Save(ctx context.Context, account AccountID, p *Payment, at Revision) error
}
