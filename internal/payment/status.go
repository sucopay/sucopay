package payment

// Status is where a payment has got to.
//
// These are the payment's own states, not a transfer's. Whether a transfer has
// been seen, and how many confirmations it has, belongs to the attempt that
// carries it: one payment can be attempted more than once, an attempt that
// fails leaves the payment payable until its deadline, and an underpayment is
// answered by a second transfer rather than by a new payment. A payment-level
// "confirming" would have to move back and forth as transfers arrived, and
// would say nothing a merchant asked.
type Status string

// The states a payment moves through. It is created, then it can be paid, and
// then it has been paid, will not be, or stopped being payable.
const (
	Created         Status = "created"
	AwaitingPayment Status = "awaiting_payment"
	Succeeded       Status = "succeeded"
	Failed          Status = "failed"
	Expired         Status = "expired"
)

// next is every move a payment may make. A status absent from this map is
// final: nothing follows succeeded, failed or expired, and money that has
// settled does not become money that has not.
var next = map[Status][]Status{
	Created:         {AwaitingPayment},
	AwaitingPayment: {Succeeded, Failed, Expired},
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
