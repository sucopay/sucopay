package webhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"

	"github.com/sucopay/sucopay/internal/payment"
)

// The states a delivery is in. It starts pending, and ends delivered on a
// receiver answering 2xx or failed on the last attempt not getting one.
const (
	Pending   = "pending"
	Delivered = "delivered"
	Failed    = "failed"
)

// Delivery is one event on its way to one endpoint. Its ID is the webhook-id
// the receiver sees, the same on every attempt, so that a receiver can
// refuse a repeat by it.
type Delivery struct {
	ID       ID
	Endpoint ID
	// Account is the payment's, on a delivery to the deployment's endpoint
	// as much as on one to the account's own.
	Account payment.AccountID
	// Payment is empty for an event about no payment, which endpoint.test
	// is.
	Payment    payment.ID
	Type       string
	OccurredAt time.Time
	// Body is what is sent, as [Envelope] wrote it.
	Body     []byte
	State    string
	Attempts int
	NextAt   time.Time
}

// Envelope is the body of every event: what happened, when, whose payment,
// and the thing itself. data is the payment as a read of it answers, or an
// empty object for an event about no payment.
//
// Written without escaping the characters that have a meaning in a page,
// as the payment's own event is: it goes to a merchant's server, not a
// browser, and the escaping would spend six bytes where the value had one.
func Envelope(eventType string, occurred time.Time, account payment.AccountID, data json.RawMessage) ([]byte, error) {
	if !json.Valid(data) {
		return nil, errors.New("webhook envelope: data is not JSON")
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(struct {
		Type      string            `json:"type"`
		Timestamp time.Time         `json:"timestamp"`
		Account   payment.AccountID `json:"account"`
		Data      json.RawMessage   `json:"data"`
	}{eventType, occurred.UTC(), account, data}); err != nil {
		return nil, err
	}
	return bytes.TrimRight(out.Bytes(), "\n"), nil
}

// schedule is how long each attempt waits after the one before it failed:
// the first is made at once, and the rest follow what Standard Webhooks
// recommends. Ten attempts come to a little over three days.
var schedule = []time.Duration{
	0,
	5 * time.Second,
	5 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
	5 * time.Hour,
	10 * time.Hour,
	14 * time.Hour,
	20 * time.Hour,
	24 * time.Hour,
}

// MaxAttempts is how many times a delivery is tried before it is failed.
var MaxAttempts = len(schedule)

// jitterShare is how much of an interval is added at random, so that the
// deliveries a receiver's outage failed together do not come back together.
const jitterShare = 0.1

// NextAttempt is when a delivery is tried again after its attempt number
// failed has failed, counting from one; and false when it has had its last.
// random is a number in [0, 1), the caller's so that a test can say what it
// is.
func NextAttempt(failed int, now time.Time, random func() float64) (time.Time, bool) {
	if failed < 1 || failed >= len(schedule) {
		return time.Time{}, false
	}
	wait := schedule[failed]
	return now.Add(wait + time.Duration(float64(wait)*jitterShare*random())), true
}
