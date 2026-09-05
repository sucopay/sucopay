package payment

// Status is where a payment has got to.
//
// These are the payment's own states, not a transfer's. Whether a transfer has
// been seen, and how many confirmations it has, belongs to the attempt that
// carries it: one payment can be attempted more than once, and an attempt that
// fails leaves the payment payable until its deadline. A payment-level
// "confirming" would have to move back and forth as transfers arrived, and
// would say nothing a merchant asked.
type Status string

// The states a payment moves through. It is created, then it can be paid, then
// it stops being payable, and then it has been paid or it has not.
//
// AwaitingFinality is where a payment waits once it stops being payable. A
// transfer authorised a moment before the deadline can still be arriving after
// it, and without this state the only place to put one is a payment already
// called expired, which is final.
const (
	Created          Status = "created"
	AwaitingPayment  Status = "awaiting_payment"
	AwaitingFinality Status = "awaiting_finality"
	Succeeded        Status = "succeeded"
	Failed           Status = "failed"
	Expired          Status = "expired"
)

// next is every move a payment may make. A status absent from this map is
// final: nothing follows succeeded, failed or expired, and money that has
// settled does not become money that has not.
//
// Expired follows awaiting_finality rather than awaiting_payment, so that
// calling a payment expired cannot take away a chance to pay that somebody
// still had.
var next = map[Status][]Status{
	Created:          {AwaitingPayment},
	AwaitingPayment:  {Succeeded, Failed, AwaitingFinality},
	AwaitingFinality: {Succeeded, Expired},
}

// Valid reports whether s is a status this package defines.
func (s Status) Valid() bool {
	if _, ok := next[s]; ok {
		return true
	}
	return s == Succeeded || s == Failed || s == Expired
}

// Final reports whether nothing follows s.
func (s Status) Final() bool {
	_, ok := next[s]
	return !ok && s.Valid()
}

// CanBecome reports whether a payment in status s may move to want.
func (s Status) CanBecome(want Status) bool {
	for _, allowed := range next[s] {
		if allowed == want {
			return true
		}
	}
	return false
}

// String returns the status as written.
func (s Status) String() string { return string(s) }

// errTransition reports a move a payment may not make.
func errTransition(from, to Status) error {
	if from.Final() {
		return Problems{{Field: "status", Message: "payment is " + from.String() + ", which is final"}}
	}
	return Problems{{Field: "status", Message: "payment cannot go from " + from.String() + " to " + to.String()}}
}
