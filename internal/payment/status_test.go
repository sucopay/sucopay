package payment_test

import (
	"testing"

	"github.com/sucopay/sucopay/internal/payment"
)

// every status this package defines, so that one added without a rule for it
// shows up here rather than in a deployment.
var every = []payment.Status{
	payment.Created, payment.AwaitingPayment,
	payment.Succeeded, payment.Failed, payment.Expired,
}

func TestStatus_AllowsTheLifecycleAndNothingElse(t *testing.T) {
	// The whole state machine, written out rather than derived, so that this
	// disagrees with the code when the code changes.
	allowed := map[payment.Status][]payment.Status{
		payment.Created:         {payment.AwaitingPayment},
		payment.AwaitingPayment: {payment.Succeeded, payment.Failed, payment.Expired},
	}

	for _, from := range every {
		for _, to := range every {
			want := false
			for _, s := range allowed[from] {
				if s == to {
					want = true
				}
			}
			if got := from.CanBecome(to); got != want {
				t.Errorf("%s -> %s = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestStatus_FinalIsTheThreeThatEndAndNoOthers(t *testing.T) {
	ends := map[payment.Status]bool{
		payment.Succeeded: true, payment.Failed: true, payment.Expired: true,
	}

	for _, s := range every {
		if got := s.Final(); got != ends[s] {
			t.Errorf("%s.Final() = %v, want %v", s, got, ends[s])
		}
	}
	for from := range ends {
		for _, to := range every {
			if from.CanBecome(to) {
				t.Errorf("%s may still become %s", from, to)
			}
		}
	}
}

func TestStatus_ValidAcceptsWhatThisPackageDefinesAndNothingElse(t *testing.T) {
	for _, s := range every {
		if !s.Valid() {
			t.Errorf("%s is defined here but reads as invalid", s)
		}
	}
	// "submitted" and "confirming" describe a transfer, and belong to the
	// attempt that carries it. A stored row holding one as a payment status is
	// a row this build will not act on.
	for _, s := range []payment.Status{"", "paid", "CREATED", "awaiting-payment", "submitted", "confirming"} {
		if payment.Status(s).Valid() {
			t.Errorf("%q reads as a status this package defines", s)
		}
	}
}
