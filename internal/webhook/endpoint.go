// Package webhook tells a merchant's server that a payment changed: where to
// tell it, how the telling is signed, and the sending and resending until it
// is heard.
package webhook

import (
	"errors"
	"slices"
	"time"

	"github.com/sucopay/sucopay/internal/payment"
)

// Problem is one thing wrong with a destination or a registration. Field
// names the key it is about.
type Problem struct {
	Field   string
	Message string
}

// Scope says whose an endpoint is: one account's, or the deployment's.
type Scope string

const (
	// ScopeAccount is an endpoint of one account, which receives that
	// account's events and no other's.
	ScopeAccount Scope = "account"
	// ScopeDeployment is an endpoint of the whole deployment, which receives
	// every account's events. It is the operator's, for a provider who wants
	// every merchant's payments in one place. Nothing makes one yet: the
	// route that would issue it comes with the credentials of the deployment.
	ScopeDeployment Scope = "deployment"
)

// Events are the types an endpoint may receive. A registration that names
// none receives all of them, these and any added later.
var Events = []string{
	"payment.awaiting_payment",
	"attempt.confirming",
	"payment.succeeded",
	"payment.expired",
	"payment.failed",
	"refund.succeeded",
	"refund.expired",
	"endpoint.test",
}

// MaxEndpoints is how many endpoints one account, or the deployment, may
// hold. Enough for production, a trial and a migration to overlap; a first
// number, not a measured one.
const MaxEndpoints = 8

// MaxDescriptionBytes bounds the note a merchant keeps on an endpoint.
const MaxDescriptionBytes = 200

// RotationGrace is how long the secret an endpoint had before stays in
// force beside the new one, so that a receiver can move its verification
// over without a gap. Stripe's ceiling for the same thing.
const RotationGrace = 24 * time.Hour

// Endpoint is one place events are sent, as a reader of it sees it: with no
// secret, which is shown once and never read back.
type Endpoint struct {
	ID          ID
	Scope       Scope
	Account     payment.AccountID
	URL         string
	Description string
	// Events is what the endpoint receives, and nil for everything.
	Events    []string
	Enabled   bool
	CreatedAt time.Time
}

// Receives says whether an event of the type reaches this endpoint.
func (e Endpoint) Receives(eventType string) bool {
	return e.Enabled && (e.Events == nil || slices.Contains(e.Events, eventType))
}

// ID names an endpoint or a delivery: 32 lowercase hexadecimal characters
// from crypto/rand, the same shape as a payment's, so that nothing can be
// read from the order they were made in.
type ID string

// NewID mints an identifier.
func NewID() (ID, error) {
	id, err := payment.NewID()
	if err != nil {
		return "", err
	}
	return ID(id), nil
}

// ParseID reads an identifier as a path carries it.
func ParseID(s string) (ID, error) {
	id, err := payment.ParseID(s)
	if err != nil {
		return "", errors.New("webhook id: not an id")
	}
	return ID(id), nil
}

func (id ID) String() string { return string(id) }

// ErrNotFound is an endpoint or a delivery the caller cannot see: none with
// the id, one of another account, or one that was deleted. The three are one
// answer, so that the answer says nothing about what exists.
var ErrNotFound = errors.New("webhook: not found")

// ErrTooMany is a registration that would take an account past
// [MaxEndpoints].
var ErrTooMany = errors.New("webhook: too many endpoints")

// ErrDisabled is a delivery asked of an endpoint that is disabled, which
// receives nothing until it is enabled again.
var ErrDisabled = errors.New("webhook: endpoint is disabled")

// ErrPending is a delivery asked for while one like it is still on its
// way: a test while the last test is pending, or a resend of a delivery
// that has not been delivered or failed yet.
var ErrPending = errors.New("webhook: delivery is pending")
