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
	// ErrNotFound reports that no payment or attempt was stored under that
	// account and identifier. It is not a problem with what the caller asked
	// for.
	ErrNotFound = errors.New("payment: no such payment")

	// ErrStale reports that the row changed after it was read, so the write
	// was refused rather than laid over what the other writer left. A caller
	// that sees this reads the row again and decides again, rather than
	// reapplying the move it was going to make.
	ErrStale = errors.New("payment: changed since it was read")

	// ErrKeyTaken reports that an attempt on that network already holds the
	// key. A transfer names its key and nothing else, so a key held twice
	// would be a transfer belonging to two payments.
	ErrKeyTaken = errors.New("payment: the key is another attempt's")

	// ErrAttemptLive reports that the payment already has an attempt that
	// could still be paid. A second one would be a second key a payer could
	// spend against one payment.
	ErrAttemptLive = errors.New("payment: the payment has an attempt already")
)

// Revision is a row as it stood when it was read.
//
// It is not a fact about what the row holds, it is a fact about the row, which
// is why it is not on the aggregate: a payment or an attempt that was never
// stored has no revision, and a field holding one would need a value meaning
// "not stored yet" that nothing could tell from a real first revision.
//
// It carries the identifier it was read under. Version numbers are small and
// dense, and every row starts at the same one, so a caller working through
// several rows could hand one row's revision to another's save and have it
// match by coincidence. Saving checks that it did not.
//
// One type for payments and attempts. A revision is a fact about a row, and
// the two rows resolve concurrent updates the same way; the identifier it
// carries is what keeps a payment's revision from saving an attempt.
type Revision struct {
	id string
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
	// anything wrote to it in between. The event the move produced is written
	// with it, and the zero [Event] is a move that produced none.
	Save(ctx context.Context, account AccountID, p *Payment, at Revision, e Event) error
}

// Attempts stores attempts.
//
// An attempt is stored under the account of the payment it is against, and
// every method here takes that account. A transfer seen on a chain names a
// network and a key and nothing about an account, so reading by those belongs
// with the observation of a chain rather than here.
//
// Issue and SaveAttempt each own a transaction, for the reason [Repository]
// gives.
type Attempts interface {
	// Issue stores an attempt nothing has stored before. It reports
	// [ErrKeyTaken] when another attempt on the network holds the same key,
	// and [ErrAttemptLive] when the payment has an attempt that could still be
	// paid.
	Issue(ctx context.Context, account AccountID, a *Attempt) error

	// FindAttempt reads one attempt of a payment, and the revision it was read
	// at. It reports [ErrNotFound] when that account has no such attempt,
	// which includes the case of another account having it.
	FindAttempt(ctx context.Context, account AccountID, payment ID, id AttemptID) (*Attempt, Revision, error)

	// Live reads the attempt of a payment that could still be paid, and
	// reports whether there is one. A payment has at most one.
	Live(ctx context.Context, account AccountID, payment ID) (*Attempt, Revision, bool, error)

	// SaveAttempt writes back an attempt that was read, and reports [ErrStale]
	// if anything wrote to it in between.
	SaveAttempt(ctx context.Context, account AccountID, a *Attempt, at Revision) error
}

// Event is what a change to a payment produced, for whatever delivers events
// to a merchant. Name is the word a merchant matches on, such as
// payment.succeeded, and Payload is the body they are handed.
//
// The zero value is a change that produced none. Not every move is news: a
// payment becoming payable tells a merchant nothing they did not just ask for.
//
// It is written in the transaction that made the change, so that a merchant is
// never told of a change that did not happen and never left unaware of one that
// did. That is the whole of why this is a parameter rather than a second call.
type Event struct {
	Name    string
	Payload []byte
}

// MaxEventBytes bounds a payload. Every other value of no fixed length that
// this package writes has a bound, and one written from inside the process is
// no more trustworthy than one that came over a wire: what puts it there is
// code, and code has bugs. The number is far above anything a payment's fields
// come to and far below what a column would take.
const MaxEventBytes = 64 << 10

// MaxEventNameBytes bounds a name, for the same reason. A name is a few dotted
// words, and the column that holds it takes any length: the bound lives here
// because this is where a caller's mistake can still be named as one.
const MaxEventNameBytes = 64

// Produced reports whether a change produced an event.
func (e Event) Produced() bool { return e.Name != "" }
