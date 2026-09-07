package payment

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// AttemptID identifies one attempt at paying a payment. It has the shape of an
// [ID]: 32 hexadecimal characters, opaque, and saying nothing about what it
// identifies.
type AttemptID string

// String returns the identifier as written.
func (id AttemptID) String() string { return string(id) }

// Scheme names how a transfer against an attempt is authorised. One scheme is
// defined, and a chain that authorises transfers another way brings its own.
type Scheme string

// EIP3009 is a transfer the payer signs and somebody else submits, spending a
// nonce the payer never chose. The nonce is the attempt's key.
const EIP3009 Scheme = "eip3009"

// AttemptStatus is where an attempt has got to.
//
// An attempt is issued when a payer has been given something to sign, and
// confirming once a transfer spending its key has been seen. What follows
// belongs to submission and to finality, neither of which is here.
type AttemptStatus string

// The states an attempt moves through so far.
const (
	Issued     AttemptStatus = "issued"
	Confirming AttemptStatus = "confirming"
)

// Valid reports whether s is a status this package defines.
func (s AttemptStatus) Valid() bool { return s == Issued || s == Confirming }

// String returns the status as written.
func (s AttemptStatus) String() string { return string(s) }

// Attempt is one payer's go at paying a payment.
//
// A payment can be attempted more than once, and each attempt carries its own
// key: the value the chain will let be spent once. Matching a transfer to a
// payment is matching its key to an attempt, so the key is what the attempt is
// for.
//
// Its fields are unexported, and the zero Attempt is not an attempt: it holds
// no key, so nothing a chain says could be matched to it. [NewAttempt] and
// [RestoreAttempt] are what make one.
type Attempt struct {
	id          AttemptID
	paymentID   ID
	scheme      Scheme
	network     Network
	key         string
	authorizer  Address
	validBefore time.Time
	status      AttemptStatus
	createdAt   time.Time
}

// NewAttempt makes an attempt at paying p, with a key nothing else holds.
//
// The deadline is the payment's, truncated to the second: the chain compares
// it as a whole number of seconds, and a deadline rounded up would be a
// signature valid after the payment stopped being payable.
func NewAttempt(p *Payment, now time.Time) (*Attempt, error) {
	if p == nil {
		return nil, errors.New("attempt: no payment to attempt")
	}
	// The key an attempt holds is something a payer can spend. A payment that
	// is not open for payment is one no transfer should be honoured against,
	// so no key is minted for it.
	if p.Status() != AwaitingPayment {
		return nil, Problems{{Field: "status", Message: "payment is " + p.Status().String() + ", not " + AwaitingPayment.String()}}
	}
	id, err := newAttemptID()
	if err != nil {
		return nil, err
	}
	key, err := newKey()
	if err != nil {
		return nil, err
	}
	return &Attempt{
		id:          id,
		paymentID:   p.ID(),
		scheme:      EIP3009,
		network:     p.Network(),
		key:         key,
		validBefore: p.ExpiresAt().Truncate(time.Second),
		status:      Issued,
		// Truncated to what a timestamptz column keeps, so that an attempt
		// stays equal to itself once it has been stored, the way a payment
		// does.
		createdAt: now.UTC().Truncate(time.Microsecond),
	}, nil
}

// StoredAttempt is an attempt as a row holds one.
type StoredAttempt struct {
	ID          AttemptID
	PaymentID   ID
	Scheme      Scheme
	Network     Network
	Key         string
	Authorizer  Address
	ValidBefore time.Time
	Status      AttemptStatus
	CreatedAt   time.Time
}

// RestoreAttempt rebuilds an attempt that was stored.
//
// It holds what [NewAttempt] holds. A row that no longer satisfies it is one
// this process refuses to act on: an attempt is what says a transfer belongs
// to a payment, and one nobody can vouch for would say it wrongly.
func RestoreAttempt(s StoredAttempt) (*Attempt, error) {
	if err := hexOfLength(string(s.ID), 32); err != nil {
		return nil, fmt.Errorf("attempt id: %w", err)
	}
	if _, err := ParseID(string(s.PaymentID)); err != nil {
		return nil, err
	}
	if s.Scheme != EIP3009 {
		return nil, fmt.Errorf("attempt: %q is not a scheme", s.Scheme)
	}
	if s.Network == "" {
		return nil, errors.New("attempt: no network")
	}
	if err := hexOfLength(s.Key, 64); err != nil {
		return nil, fmt.Errorf("attempt key: %w", err)
	}
	if !s.Status.Valid() {
		return nil, fmt.Errorf("attempt: %q is not a status", s.Status)
	}
	if s.Authorizer != "" {
		if _, err := ParseAddress(string(s.Authorizer)); err != nil {
			return nil, fmt.Errorf("attempt authorizer: %w", err)
		}
	}
	// Who signed and where the attempt has got to move together, and [Confirm]
	// is what moves them. A row holding one without the other was written by
	// something that is not this package.
	if s.Status == Confirming && s.Authorizer == "" {
		return nil, errors.New("attempt: confirming with nobody who signed it")
	}
	if s.Status == Issued && s.Authorizer != "" {
		return nil, errors.New("attempt: issued with somebody who signed it")
	}
	return &Attempt{
		id:          s.ID,
		paymentID:   s.PaymentID,
		scheme:      s.Scheme,
		network:     s.Network,
		key:         s.Key,
		authorizer:  s.Authorizer,
		validBefore: s.ValidBefore,
		status:      s.Status,
		createdAt:   s.CreatedAt,
	}, nil
}

// Confirm records that a transfer spending this attempt's key has been seen,
// signed by authorizer. Only an issued attempt can be confirmed: a second
// transfer against one key is something the chain does not allow, so seeing
// one means the attempt was read wrongly rather than paid twice.
func (a *Attempt) Confirm(authorizer Address) error {
	if a.status != Issued {
		return Problems{{Field: "status", Message: "attempt is " + a.status.String() + ", not " + Issued.String()}}
	}
	if authorizer == "" {
		return Problems{{Field: "authorizer", Message: "a confirmed transfer has somebody who signed it"}}
	}
	// An authorizer is read off a chain by an adapter. Being typed as an
	// [Address] says where it came from, not that anything checked it.
	if _, err := ParseAddress(string(authorizer)); err != nil {
		return Problems{{Field: "authorizer", Message: err.Error()}}
	}
	a.authorizer = authorizer
	a.status = Confirming
	return nil
}

// ID returns the attempt's identifier.
func (a *Attempt) ID() AttemptID { return a.id }

// PaymentID returns the payment this attempt is against.
func (a *Attempt) PaymentID() ID { return a.paymentID }

// Scheme returns how a transfer against this attempt is authorised.
func (a *Attempt) Scheme() Scheme { return a.scheme }

// Network returns the network the transfer is expected on.
func (a *Attempt) Network() Network { return a.network }

// Key returns what the transfer will spend, as the chain writes one.
func (a *Attempt) Key() string { return a.key }

// Authorizer returns who signed the transfer, or nothing until one is seen.
func (a *Attempt) Authorizer() Address { return a.authorizer }

// ValidBefore returns the moment from which the authorisation is no longer
// good.
func (a *Attempt) ValidBefore() time.Time { return a.validBefore }

// Status returns where the attempt has got to.
func (a *Attempt) Status() AttemptStatus { return a.status }

// CreatedAt returns when the attempt was made.
func (a *Attempt) CreatedAt() time.Time { return a.createdAt }

// newAttemptID returns an identifier no other attempt has.
func newAttemptID() (AttemptID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("attempt id: %w", err)
	}
	return AttemptID(hex.EncodeToString(b[:])), nil
}

// newKey returns a key nothing else holds: 32 bytes, the width the scheme's
// nonce is, from the same source the identifiers come from.
func newKey() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("attempt key: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
