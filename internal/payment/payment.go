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
	if p.Field == "" {
		return p.Message
	}
	return invisible.Quote(p.Field) + ": " + p.Message
}

// Problems is every problem found in one request. They are reported together
// so that a caller learns everything wrong with what they sent in one round
// trip, and so that whatever renders them can put each beside its own field
// rather than parsing a sentence.
type Problems []Problem

// Error lists every problem, one per line.
func (ps Problems) Error() string {
	if len(ps) == 1 {
		return "payment: " + ps[0].String()
	}
	lines := make([]string, 0, len(ps)+1)
	lines = append(lines, fmt.Sprintf("payment: %d problems", len(ps)))
	for _, p := range ps {
		lines = append(lines, "  "+p.String())
	}
	return strings.Join(lines, "\n")
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
	if len(s) != 32 {
		return "", fmt.Errorf("payment id: want 32 characters, got %d", len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", errors.New("payment id: not hexadecimal")
	}
	if strings.ToLower(s) != s {
		return "", errors.New("payment id: not lowercase")
	}
	return ID(s), nil
}

// String returns the identifier as written.
func (id ID) String() string { return string(id) }

// Address is where funds are sent, as the network writes an account.
//
// It is opaque here. Chains write an account differently, and [ADR 0002] keeps
// chain-shaped values behind the adapter that knows the chain; a domain that
// checked for 0x and forty hexadecimal digits would be an EVM domain. What is
// checked here is what holds whatever the chain says.
//
// [ADR 0002]: chain-specific code stays behind an adapter.
type Address string

// ParseAddress reads an account as the network writes it.
//
// TODO(1101hirokin): the adapter has to check the shape and the checksum
// before anything sends funds to an address this returned. A chain does not
// give them back, and a mistyped destination is accepted here.
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
// Its fields are unexported so that the only Payment a caller can hold is one
// whose invariants held when it was made and have held through every move
// since. [New] is the way to make one.
//
// One writer at a time. A payment is loaded, moved and saved inside one
// database transaction, which is what decides between two moves racing for the
// same payment; nothing here does, and two goroutines moving one Payment is a
// data race.
type Payment struct {
	id          ID
	amount      Money
	destination Address
	status      Status
	metadata    map[string]string
	createdAt   time.Time
	expiresAt   time.Time
}

// Request is what a caller supplies to open a payment.
//
// It carries no identifier: [New] mints one, so that nothing reaching an
// instance from outside can choose what a payment is called. The identifier
// becomes the nonce the transfer is authorised under, and one an outsider
// picked is one they can front-run or collide.
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

// Restore rebuilds a payment that was stored, under the identifier it already
// has and with the deadline it was stored with. Any ExpiresAt on r is ignored.
//
// It holds the same invariants as [New]: a row that no longer satisfies them
// is a row this process refuses to act on rather than one it carries forward.
// The deadline is checked against createdAt rather than the present, because a
// payment that has since expired still has to load.
func Restore(id ID, r Request, status Status, createdAt, expiresAt time.Time) (*Payment, error) {
	if !status.Valid() {
		return nil, Problems{{Field: "status", Message: fmt.Sprintf("%q is not a status", status)}}
	}
	r.ExpiresAt = expiresAt
	return build(id, r, status, createdAt, createdAt)
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
		createdAt:   createdAt.UTC(),
		expiresAt:   r.ExpiresAt.UTC(),
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

// Amount returns what is being accepted.
func (p *Payment) Amount() Money { return p.amount }

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

// Succeed records that the payment has been settled: enough has arrived, and
// it is final by the network's confirmation policy.
func (p *Payment) Succeed() error { return p.moveTo(Succeeded) }

// Fail records that the payment will not settle and that waiting longer will
// not change it.
func (p *Payment) Fail() error { return p.moveTo(Failed) }

// Expire records that the payment stopped being payable before anyone paid
// it. It refuses to do so early: a payment that expires before its own
// deadline is one a customer was still entitled to pay.
func (p *Payment) Expire(now time.Time) error {
	if now.Before(p.expiresAt) {
		return Problems{{Field: "expires_at", Message: fmt.Sprintf("%s is after %s",
			p.expiresAt.Format(time.RFC3339), now.UTC().Format(time.RFC3339))}}
	}
	return p.moveTo(Expired)
}

func (p *Payment) moveTo(want Status) error {
	if !p.status.CanBecome(want) {
		return errTransition(p.status, want)
	}
	p.status = want
	return nil
}
