package payment

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/sucopay/sucopay/internal/invisible"
	"github.com/sucopay/sucopay/internal/problem"
)

// Metadata is bounded because a merchant supplies it and an instance stores
// it, reports it and hands it back. Nothing here is a limit anyone has asked
// to raise; they exist so that the first person to try does so deliberately.
//
// Keys are compared as bytes. Two that differ only by Unicode normalisation,
// such as an accented letter written as one rune or as two, are two keys that
// render alike. Refusing that would mean normalising, which would change what
// a merchant gets back.
const (
	MaxMetadataEntries   = 20
	MaxMetadataKeyBytes  = 64
	MaxMetadataValueSize = 512
	MaxAddressBytes      = 128
)

// Problem is one thing wrong with a request. Field names what it is about, and
// is empty when the problem is not about one field.
type Problem struct {
	Field   string
	Message string
}

func (p Problem) String() string {
	return problem.Line(p.Field, p.Message)
}

// Problems is every problem found in one request. They are reported together
// so that a caller learns everything wrong with what they sent in one round
// trip, and so that whatever renders them can put each beside its own field
// rather than parsing a sentence.
type Problems []Problem

// Error lists every problem, one per line.
func (ps Problems) Error() string {
	lines := make([]string, 0, len(ps))
	for _, p := range ps {
		lines = append(lines, p.String())
	}
	return problem.List("payment", lines)
}

// ID identifies one payment. It is 32 hexadecimal characters: an opaque 128
// bits, chosen so that a merchant's own identifiers never have to be guessed
// at and so that nothing about the payment can be read off it.
type ID string

// NewID returns an identifier no other payment has.
func NewID() (ID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("payment id: %w", err)
	}
	return ID(hex.EncodeToString(b[:])), nil
}

// ParseID reads an identifier, refusing anything that is not the shape
// [NewID] produces.
func ParseID(s string) (ID, error) {
	if err := hexOfLength(s, 32); err != nil {
		return "", fmt.Errorf("payment id: %w", err)
	}
	return ID(s), nil
}

// String returns the identifier as written.
func (id ID) String() string { return string(id) }

// hexOfLength refuses anything that is not n lowercase hexadecimal characters.
// Identifiers and the key an attempt holds are all written this way.
func hexOfLength(s string, n int) error {
	if len(s) != n {
		return fmt.Errorf("want %d characters, got %d", n, len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return errors.New("not hexadecimal")
	}
	if strings.ToLower(s) != s {
		return errors.New("not lowercase")
	}
	return nil
}

// Address is where funds are sent, as the network writes an account.
//
// It is opaque here. Chains write an account differently, and chain-shaped
// values stay behind the adapter that knows the chain: a domain that checked
// for 0x and forty hexadecimal digits would be an EVM domain. What is checked
// here is what holds whatever the chain says.
type Address string

// ParseAddress reads an account as the network writes it.
//
// The shape and the checksum belong to the chain, so the adapter checks them,
// and it does so where a value enters: the kind of the network an asset
// settles on reads the address an operator accepts that asset to, and an
// authorizer read off a chain was written by the adapter that read it. What
// this adds holds on every chain: an address was given at all, it is within
// the bound, and it carries no character that does not show up.
func ParseAddress(s string) (Address, error) {
	switch {
	case s == "":
		return "", errors.New("address: none given")
	case len(s) > MaxAddressBytes:
		return "", fmt.Errorf("address: %d bytes, at most %d", len(s), MaxAddressBytes)
	case invisible.Has(s):
		return "", errors.New("address: holds a character that does not show up")
	}
	return Address(s), nil
}

// String returns the account as written.
func (a Address) String() string { return string(a) }

// Payment records that an amount is being accepted for a piece of business.
//
// Its fields are unexported so that a Payment made by [New] or [Restore] is
// one whose invariants held when it was made and have held through every move
// since. The zero Payment is not a payment: it has no identifier and no
// status, and the methods report that rather than guessing.
//
// One writer at a time. A payment is loaded, moved and saved inside one
// database transaction, which is what decides between two moves racing for the
// same payment; nothing here does, and two goroutines moving one Payment is a
// data race. Awaiting finality lengthens the time in which that matters: a
// late transfer confirming and a sweep giving up on the same payment are two
// writers, and the one that loses has to load again and decide again rather
// than reapply what it was going to do.
type Payment struct {
	id          ID
	amount      Money
	received    Money
	destination Address
	status      Status
	metadata    map[string]string
	createdAt   time.Time
	expiresAt   time.Time
}

// Request is what a caller supplies to open a payment.
//
// It carries no identifier: [New] mints one, so that nothing reaching an
// instance from outside can choose what a payment is called. An identifier
// somebody else chose is one they can guess, enumerate, or collide with
// another payment's.
//
// It carries no network either. The amount names its asset and the asset names
// its network, so a payment cannot hold two answers to which chain it settles
// on.
type Request struct {
	Amount      Money
	Destination Address
	Metadata    map[string]string
	ExpiresAt   time.Time
}

// New opens a payment under an identifier of its own, or reports every reason
// it could not as [Problems].
func New(r Request, now time.Time) (*Payment, error) {
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	return build(id, r, Created, now, now)
}

// Stored is a payment as it was written down. Every field is one the row
// holds, so nothing a caller passes is quietly dropped.
type Stored struct {
	ID          ID
	Amount      Money
	Received    Money
	Destination Address
	Metadata    map[string]string
	Status      Status
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// Restore rebuilds a payment that was stored, under the identifier and the
// deadline it already has.
//
// It holds the same invariants as [New]: a row that no longer satisfies them
// is a row this process refuses to act on rather than one it carries forward.
// The deadline is checked against CreatedAt rather than the present, because a
// payment that has since expired still has to load.
func Restore(s Stored) (*Payment, error) {
	if !s.Status.Valid() {
		return nil, Problems{{Field: "status", Message: fmt.Sprintf("%q is not a status", s.Status)}}
	}
	p, err := build(s.ID, Request{
		Amount:      s.Amount,
		Destination: s.Destination,
		Metadata:    s.Metadata,
		ExpiresAt:   s.ExpiresAt,
	}, s.Status, s.CreatedAt, s.CreatedAt)
	if err != nil {
		return nil, err
	}
	if s.Received.IsSet() {
		if err := p.record(s.Received); err != nil {
			return nil, err
		}
		// Nothing can arrive for a payment nobody could pay yet.
		if s.Status == Created {
			return nil, Problems{{Field: "received", Message: fmt.Sprintf(
				"%s arrived, but the payment is still %s", s.Received, s.Status)}}
		}
	}
	// A succeeded row that is not covered by what arrived is one this build
	// refuses rather than one it reports as paid.
	if s.Status == Succeeded {
		if err := p.covered(); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func build(id ID, r Request, status Status, createdAt, deadlineAfter time.Time) (*Payment, error) {
	var problems Problems
	fail := func(field, format string, args ...any) {
		problems = append(problems, Problem{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	if _, err := ParseID(string(id)); err != nil {
		fail("id", "%v", err)
	}
	switch {
	case !r.Amount.IsSet():
		fail("amount", "none given")
	case r.Amount.Amount().Sign() == 0:
		fail("amount", "zero, which is not a payment")
	}
	if _, err := ParseAddress(string(r.Destination)); err != nil {
		fail("destination", "%v", err)
	}
	switch {
	case r.ExpiresAt.IsZero():
		fail("expires_at", "none given")
	case !r.ExpiresAt.After(deadlineAfter):
		fail("expires_at", "%s is not after %s",
			r.ExpiresAt.UTC().Format(time.RFC3339), deadlineAfter.UTC().Format(time.RFC3339))
	}
	problems = append(problems, metadataProblems(r.Metadata)...)

	if len(problems) > 0 {
		return nil, problems
	}
	return &Payment{
		id:          id,
		amount:      r.Amount,
		destination: r.Destination,
		status:      status,
		metadata:    maps.Clone(r.Metadata),
		// Truncated to what a timestamptz column keeps. Held to the nanosecond
		// here, a payment would stop being equal to itself the moment it was
		// stored and read back.
		createdAt: createdAt.UTC().Truncate(time.Microsecond),
		expiresAt: r.ExpiresAt.UTC().Truncate(time.Microsecond),
	}, nil
}

// metadataProblems reports everything wrong with the metadata rather than the
// first thing, and in a fixed order: ranging over a map would otherwise report
// a different one of them each run.
func metadataProblems(metadata map[string]string) Problems {
	var problems Problems
	if len(metadata) > MaxMetadataEntries {
		problems = append(problems, Problem{"metadata",
			fmt.Sprintf("%d entries, at most %d", len(metadata), MaxMetadataEntries)})
	}
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	for _, key := range keys {
		field := "metadata." + key
		value := metadata[key]
		switch {
		case key == "":
			problems = append(problems, Problem{"metadata", "a key is empty"})
		case len(key) > MaxMetadataKeyBytes:
			problems = append(problems, Problem{field,
				fmt.Sprintf("key is %d bytes, at most %d", len(key), MaxMetadataKeyBytes)})
		case invisible.Has(key):
			problems = append(problems, Problem{field, "key holds a character that does not show up"})
		}
		switch {
		case len(value) > MaxMetadataValueSize:
			problems = append(problems, Problem{field,
				fmt.Sprintf("value is %d bytes, at most %d", len(value), MaxMetadataValueSize)})
		case invisible.Has(value):
			problems = append(problems, Problem{field, "value holds a character that does not show up"})
		}
	}
	return problems
}

// ID returns the payment's identifier.
func (p *Payment) ID() ID { return p.id }

// Amount returns what is being asked for.
func (p *Payment) Amount() Money { return p.amount }

// Received returns what arrived. It is not set until something has.
//
// It is kept apart from [Payment.Amount] rather than replacing it, so that a
// payment that came up short leaves the shortfall visible instead of losing it
// into one field. A failed or expired payment can hold one: money arriving and
// the payment not settling is exactly the case this field is for.
func (p *Payment) Received() Money { return p.received }

// Asset returns which token is being accepted.
func (p *Payment) Asset() Asset { return p.amount.Asset() }

// Network returns the chain the payment settles on, which is the one its asset
// lives on.
func (p *Payment) Network() Network { return p.amount.Asset().Network() }

// Destination returns where the funds go.
func (p *Payment) Destination() Address { return p.destination }

// Status returns where the payment has got to.
func (p *Payment) Status() Status { return p.status }

// CreatedAt returns when the payment was opened, in UTC.
func (p *Payment) CreatedAt() time.Time { return p.createdAt }

// ExpiresAt returns when the payment stops being payable, in UTC.
func (p *Payment) ExpiresAt() time.Time { return p.expiresAt }

// Metadata returns what the merchant attached. The result is a copy.
func (p *Payment) Metadata() map[string]string { return maps.Clone(p.metadata) }

// Await marks the payment as one a customer can now pay.
func (p *Payment) Await() error { return p.moveTo(AwaitingPayment) }

// Receive records what arrived for this payment.
//
// It is not a running total. Making up a shortfall with a second transfer is
// not something this package does, so a second arrival is a payment matched
// twice rather than a payment being topped up.
//
// Money can only land while the payment is payable or awaiting finality.
// Recording an arrival on one that has finished would make what a final payment
// says about itself change afterwards, which is the thing the lifecycle exists
// to prevent.
func (p *Payment) Receive(m Money) error {
	if !p.CanReceive() {
		return Problems{{Field: "received", Message: fmt.Sprintf(
			"payment is %s, which nothing can arrive for", p.status)}}
	}
	return p.record(m)
}

// CanReceive reports whether money can still land on this payment. The list
// lives here rather than at each caller, so that a status added to the
// lifecycle is thought about in one place.
func (p *Payment) CanReceive() bool {
	return p.status == AwaitingPayment || p.status == AwaitingFinality
}

// Unreceive takes back what arrived, for a transfer that is no longer on the
// chain.
//
// Only a payment that could still receive can un-receive. A succeeded payment
// keeps what it was paid: [Restore] refuses a succeeded row that nothing covers,
// so clearing one would leave a row this build cannot read back. Evidence that
// vanished under a payment already called paid is a question about that payment
// rather than about this field.
func (p *Payment) Unreceive() error {
	if !p.CanReceive() {
		return Problems{{Field: "received", Message: fmt.Sprintf(
			"payment is %s, which nothing can be taken back from", p.status)}}
	}
	if !p.received.IsSet() {
		return Problems{{Field: "received", Message: "nothing has arrived"}}
	}
	p.received = Money{}
	return nil
}

// record is Receive without the status check, for [Restore], which has to
// rebuild a row whatever status it was stored in.
func (p *Payment) record(m Money) error {
	switch {
	case !m.IsSet():
		return Problems{{Field: "received", Message: "none given"}}
	case p.received.IsSet():
		return Problems{{Field: "received", Message: fmt.Sprintf(
			"%s already arrived for this payment", p.received)}}
	}
	// Compared rather than only matched by asset, so that two descriptions of
	// one token disagreeing about its decimals are refused here instead of
	// leaving a payment nothing can ever judge covered.
	if _, err := m.Cmp(p.amount); err != nil {
		return Problems{{Field: "received", Message: err.Error()}}
	}
	p.received = m
	return nil
}

// Succeed records that the payment is paid: enough has arrived, and it is final
// by the network's confirmation policy.
func (p *Payment) Succeed() error {
	// The move is checked before the money, so that a payment nobody could have
	// paid yet reads as being in the wrong state rather than as being short.
	if !p.status.CanBecome(Succeeded) {
		return errTransition(p.status, Succeeded)
	}
	if err := p.covered(); err != nil {
		return err
	}
	return p.moveTo(Succeeded)
}

// covered reports whether what arrived is at least what was asked for.
func (p *Payment) covered() error {
	if !p.received.IsSet() {
		return Problems{{Field: "received", Message: "nothing has arrived"}}
	}
	cmp, err := p.received.Cmp(p.amount)
	if err != nil {
		return Problems{{Field: "received", Message: err.Error()}}
	}
	if cmp < 0 {
		return Problems{{Field: "received", Message: fmt.Sprintf(
			"%s arrived, short of %s", p.received, p.amount)}}
	}
	return nil
}

// AwaitFinality records that the payment is no longer payable and is waiting
// to learn whether anything arrives.
//
// It refuses to do so early: a payment made unpayable before its own deadline is
// one a customer was still entitled to pay.
func (p *Payment) AwaitFinality(now time.Time) error {
	if now.Before(p.expiresAt) {
		return Problems{{Field: "expires_at", Message: fmt.Sprintf("%s is after %s",
			p.expiresAt.Format(time.RFC3339), now.UTC().Format(time.RFC3339))}}
	}
	return p.moveTo(AwaitingFinality)
}

// Fail records that the payment will not settle and that waiting longer will
// not change it.
func (p *Payment) Fail() error { return p.moveTo(Failed) }

// Expire records that nothing arrived before the payment stopped being payable.
//
// It takes no deadline. Reaching it means passing through awaiting_finality,
// which is where the deadline is checked, so by now the authorization is dead
// and the question is only whether anything was still in flight. How long to
// wait for that is the network's confirmation policy, which this package does
// not know.
func (p *Payment) Expire() error { return p.moveTo(Expired) }

func (p *Payment) moveTo(want Status) error {
	if !p.status.CanBecome(want) {
		return errTransition(p.status, want)
	}
	p.status = want
	return nil
}
