package payment

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"time"
)

// RefundID identifies one refund of one payment. It has the shape of an [ID]:
// 32 hexadecimal characters, opaque, and saying nothing about what it
// identifies.
type RefundID string

// String returns the identifier as written.
func (id RefundID) String() string { return string(id) }

// RefundStatus is where a refund has got to.
//
// A refund is created with the key its authorisation spends already minted,
// so there is no state for waiting to become payable: what a payment calls
// awaiting_payment, a refund is from the moment it exists. A transfer seen
// for it moves nothing either; only the chain settling one does.
type RefundStatus string

// The states a refund moves through.
const (
	// RefundCreated is a refund whose key is good and whose transfer has not
	// settled.
	RefundCreated RefundStatus = "created"
	// RefundAwaitingFinality is a refund past its deadline, where whether a
	// transfer already in flight settles is not decided.
	RefundAwaitingFinality RefundStatus = "awaiting_finality"
	// RefundSucceeded is a refund whose transfer the chain settled.
	RefundSucceeded RefundStatus = "succeeded"
	// RefundExpired is a refund past its deadline that nothing settled.
	RefundExpired RefundStatus = "expired"
)

// refundMoves is what each status may become. A refund never goes back: a
// transfer that vanishes leaves the status where it was, because no status
// was reached by the transfer being seen.
var refundMoves = map[RefundStatus][]RefundStatus{
	RefundCreated:          {RefundSucceeded, RefundAwaitingFinality},
	RefundAwaitingFinality: {RefundSucceeded, RefundExpired},
	RefundSucceeded:        nil,
	RefundExpired:          nil,
}

// Valid says whether the status is one this build knows.
func (s RefundStatus) Valid() bool {
	_, known := refundMoves[s]
	return known
}

// Final says whether nothing follows the status.
func (s RefundStatus) Final() bool { return s == RefundSucceeded || s == RefundExpired }

// String returns the status as written.
func (s RefundStatus) String() string { return string(s) }

// RefundLife is how long a refund's authorisation is good for: the time a
// merchant has to open the page, reach their wallet, sign and send. A first
// number, not a measured one.
//
// It is also how long the amount is held against the payment's ceiling, so a
// refund nobody signs holds the rest of the money up for this long.
const RefundLife = 30 * time.Minute

// PageToken is what a row keeps of the token a page is reached by: a hash of
// the token, and which key derived it. The token itself is derived again from
// the key and the identifier whenever the URL is answered, and never stored.
type PageToken struct {
	Hash  []byte
	KeyID string
}

// IsSet says whether a row was given a token.
func (t PageToken) IsSet() bool { return len(t.Hash) > 0 && t.KeyID != "" }

// RefundRequest is what a caller supplies to open a refund. The identifier,
// the key and the deadline are minted here: none of them is anybody else's to
// choose.
type RefundRequest struct {
	// Amount is what to send back, in the payment's asset.
	Amount Money
	// Destination is where it goes: the address the payment was paid from.
	// The caller reads it off the transfer that paid the payment rather than
	// choosing it.
	Destination Address
	// Token is what the row keeps of the merchant's signing page.
	Token PageToken
	// Idempotency is the key the request carried, and empty when it carried
	// none.
	Idempotency Idempotency
}

// Refund is money going back to whoever paid a payment.
//
// It is the payment's shape reversed, with the merchant as the signer: what
// moves the money is an authorisation only the payment's destination can
// sign, spending a key this issued, and the same reading of the chain that
// settles a payment settles this.
type Refund struct {
	id          RefundID
	paymentID   ID
	amount      Money
	destination Address
	scheme      Scheme
	network     Network
	key         string
	expiresAt   time.Time
	status      RefundStatus
	token       PageToken
	idempotency Idempotency
	createdAt   time.Time
	closedAt    time.Time
}

// NewRefund opens a refund of p under an identifier the caller minted with
// [NewRefundID], and a freshly minted key and deadline.
//
// The identifier comes in because the page's token is derived from it, and
// the row keeps what that derivation produced: whoever opens a refund has to
// know what it is called before it exists.
//
// Only a payment the chain settled can be refunded. What arrived is taken
// back when a transfer vanishes, and the one status that keeps it is
// succeeded, so a refund of anything earlier could send money back that never
// finally arrived. The ceiling the amount is measured against is the store's:
// it needs every other refund of the payment, which this does not have.
func NewRefund(p *Payment, id RefundID, r RefundRequest, now time.Time) (*Refund, error) {
	if p == nil {
		return nil, Problems{{Message: "no payment to refund"}}
	}
	var problems Problems
	fail := func(field, format string, args ...any) {
		problems = append(problems, Problem{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	if p.Status() != Succeeded {
		fail("status", "payment is %s, and only a %s payment can be refunded", p.Status(), Succeeded)
	}
	switch {
	case !r.Amount.IsSet():
		fail("amount", "none given")
	case r.Amount.Amount().Sign() == 0:
		fail("amount", "zero, which is not a refund")
	case !r.Amount.Asset().Same(p.Asset()):
		fail("amount", "%s is not the payment's asset", r.Amount.Asset().Symbol())
	}
	if _, err := ParseAddress(string(r.Destination)); err != nil {
		fail("destination", "%v", err)
	}
	if !r.Token.IsSet() {
		fail("token", "none given")
	}
	if _, err := ParseRefundID(string(id)); err != nil {
		fail("id", "%v", err)
	}
	if len(problems) > 0 {
		return nil, problems
	}
	key, err := newKey()
	if err != nil {
		return nil, err
	}
	return &Refund{
		id:          id,
		paymentID:   p.ID(),
		amount:      r.Amount,
		destination: r.Destination,
		scheme:      EIP3009,
		network:     p.Network(),
		key:         key,
		// Truncated to what a timestamptz column keeps, as a payment's are.
		expiresAt:   now.Add(RefundLife).UTC().Truncate(time.Microsecond),
		status:      RefundCreated,
		token:       PageToken{Hash: slices.Clone(r.Token.Hash), KeyID: r.Token.KeyID},
		idempotency: Idempotency{Key: r.Idempotency.Key, BodyHash: slices.Clone(r.Idempotency.BodyHash)},
		createdAt:   now.UTC().Truncate(time.Microsecond),
	}, nil
}

// RefundStored is a refund as a row holds it, for rebuilding one that was
// stored.
type RefundStored struct {
	ID          RefundID
	PaymentID   ID
	Amount      Money
	Destination Address
	Scheme      Scheme
	Network     Network
	Key         string
	ExpiresAt   time.Time
	Status      RefundStatus
	Token       PageToken
	Idempotency Idempotency
	CreatedAt   time.Time
	// ClosedAt is when the refund reached a final status, and zero while it
	// has not.
	ClosedAt time.Time
}

// RestoreRefund rebuilds a refund that was stored. It holds the same
// invariants as [NewRefund] bar the payment's status, which belongs to the
// payment and is read there.
func RestoreRefund(s RefundStored) (*Refund, error) {
	var problems Problems
	fail := func(field, format string, args ...any) {
		problems = append(problems, Problem{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	if !s.Status.Valid() {
		fail("status", "%q is not a status", s.Status)
	}
	if !s.Amount.IsSet() || s.Amount.Amount().Sign() == 0 {
		fail("amount", "none given")
	}
	if _, err := ParseAddress(string(s.Destination)); err != nil {
		fail("destination", "%v", err)
	}
	if len(problems) > 0 {
		return nil, problems
	}
	return &Refund{
		id: s.ID, paymentID: s.PaymentID, amount: s.Amount, destination: s.Destination,
		scheme: s.Scheme, network: s.Network, key: s.Key,
		expiresAt: s.ExpiresAt.UTC(), status: s.Status,
		token:       PageToken{Hash: slices.Clone(s.Token.Hash), KeyID: s.Token.KeyID},
		idempotency: Idempotency{Key: s.Idempotency.Key, BodyHash: slices.Clone(s.Idempotency.BodyHash)},
		createdAt:   s.CreatedAt.UTC(), closedAt: s.ClosedAt.UTC(),
	}, nil
}

// ID is what the refund is called.
func (r *Refund) ID() RefundID { return r.id }

// PaymentID is the payment the refund is against.
func (r *Refund) PaymentID() ID { return r.paymentID }

// Amount is what goes back.
func (r *Refund) Amount() Money { return r.amount }

// Destination is where it goes.
func (r *Refund) Destination() Address { return r.destination }

// Scheme is how the transfer is authorised.
func (r *Refund) Scheme() Scheme { return r.scheme }

// Network is the chain the transfer is on.
func (r *Refund) Network() Network { return r.network }

// Key is what the authorisation spends, and what a transfer is recognised by.
func (r *Refund) Key() string { return r.key }

// ExpiresAt is when the authorisation stops being good, in UTC.
func (r *Refund) ExpiresAt() time.Time { return r.expiresAt }

// Status is where the refund has got to.
func (r *Refund) Status() RefundStatus { return r.status }

// Token is what the row keeps of the merchant's signing page.
func (r *Refund) Token() PageToken {
	return PageToken{Hash: slices.Clone(r.token.Hash), KeyID: r.token.KeyID}
}

// Idempotency is the key the request that opened the refund carried.
func (r *Refund) Idempotency() Idempotency {
	return Idempotency{Key: r.idempotency.Key, BodyHash: slices.Clone(r.idempotency.BodyHash)}
}

// CreatedAt is when the refund was opened, in UTC.
func (r *Refund) CreatedAt() time.Time { return r.createdAt }

// ClosedAt is when the refund reached a final status, in UTC, and zero while
// it has not.
func (r *Refund) ClosedAt() time.Time { return r.closedAt }

// Settle records that the chain settled the refund's transfer.
func (r *Refund) Settle() error { return r.moveTo(RefundSucceeded) }

// AwaitFinality moves a refund past its deadline to waiting, where whether a
// transfer already in flight settles is what is left to decide.
func (r *Refund) AwaitFinality() error { return r.moveTo(RefundAwaitingFinality) }

// Expire ends a refund nothing settled.
func (r *Refund) Expire() error { return r.moveTo(RefundExpired) }

// Holds says whether the refund still holds its amount against the payment's
// ceiling. Everything but an expired one does: a refund nobody has signed yet
// is money the merchant may still send.
func (r *Refund) Holds() bool { return r.status != RefundExpired }

// moveTo takes the refund to a status, or reports why it cannot.
func (r *Refund) moveTo(s RefundStatus) error {
	if !slices.Contains(refundMoves[r.status], s) {
		return Problems{{Field: "status", Message: fmt.Sprintf(
			"refund is %s, which does not become %s", r.status, s)}}
	}
	r.status = s
	return nil
}

// NewRefundID returns an identifier nothing else holds.
func NewRefundID() (RefundID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("refund id: %w", err)
	}
	return RefundID(hex.EncodeToString(b[:])), nil
}

// ParseRefundID reads an identifier, refusing anything that is not the shape
// [NewRefundID] produces.
func ParseRefundID(s string) (RefundID, error) {
	if err := hexOfLength(s, 32); err != nil {
		return "", fmt.Errorf("refund id: %w", err)
	}
	return RefundID(s), nil
}
