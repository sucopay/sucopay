package checkout_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/checkout"
	"github.com/sucopay/sucopay/internal/payment"
)

func opening(t *testing.T, status payment.Status, expiresAt time.Time) *payment.Payment {
	t.Helper()
	amount, _ := payment.ParseMoney(jpyc(t), "1000"+strings.Repeat("0", 18))
	stored := payment.Stored{
		ID: payment.ID("0123456789abcdef0123456789abcdef"), Amount: amount,
		Destination: payment.Address("0xabababababababababababababababababababab"),
		Status:      status, CreatedAt: now, ExpiresAt: expiresAt,
	}
	if status == payment.Succeeded {
		// A succeeded payment is one something arrived for.
		stored.Received = amount
	}
	p, err := payment.Restore(stored)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The reason a payer is given for not being able to sign, and the one that
// wins when more than one applies.
func TestReason_SaysTheFirstThingThatStopsAPayerSigning(t *testing.T) {
	t.Parallel()
	in15 := now.Add(15 * time.Minute)
	matched := &checkout.Result{Tx: "0xtx"}
	for what, c := range map[string]struct {
		status  payment.Status
		expires time.Time
		facts   checkout.Facts
		want    string
	}{
		"payable":             {payment.AwaitingPayment, in15, checkout.Facts{Now: now, Watched: true}, ""},
		"created is payable":  {payment.Created, in15, checkout.Facts{Now: now, Watched: true}, ""},
		"past the deadline":   {payment.AwaitingPayment, in15, checkout.Facts{Now: in15, Watched: true}, checkout.ReasonExpired},
		"under two minutes":   {payment.AwaitingPayment, in15, checkout.Facts{Now: in15.Add(-payment.MinRemaining + time.Second), Watched: true}, checkout.ReasonClosing},
		"two minutes exactly": {payment.AwaitingPayment, in15, checkout.Facts{Now: in15.Add(-payment.MinRemaining), Watched: true}, ""},
		"unwatched":           {payment.AwaitingPayment, in15, checkout.Facts{Now: now, Watched: false}, checkout.ReasonNotReady},
		"reissued":            {payment.AwaitingPayment, in15, checkout.Facts{Now: now, Watched: true, Reissued: true}, checkout.ReasonReissued},
		"paid, and settling":  {payment.AwaitingFinality, in15, checkout.Facts{Now: now, Watched: true, Matched: matched}, checkout.ReasonPaid},
		"paid before expired": {payment.AwaitingPayment, in15, checkout.Facts{Now: in15, Watched: true, Matched: matched}, checkout.ReasonPaid},
		"succeeded":           {payment.Succeeded, in15, checkout.Facts{Now: in15.Add(time.Hour), Watched: true, Matched: matched}, checkout.ReasonDone},
		"expired":             {payment.Expired, in15, checkout.Facts{Now: in15.Add(time.Hour), Watched: true}, checkout.ReasonDone},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			if got := checkout.Reason(opening(t, c.status, c.expires), c.facts); got != c.want {
				t.Errorf("Reason = %q, want %q", got, c.want)
			}
		})
	}
}

func TestBuild_ShowsCreatedAsAwaitingPaymentAndSlowerWhileTheNetworkIsNotObserving(t *testing.T) {
	t.Parallel()
	p := opening(t, payment.Created, now.Add(15*time.Minute))

	s := checkout.Build(p, checkout.Facts{Now: now, Watched: true, ChainID: 137, Merchant: "Shop", NetworkWord: "observing"})

	if s.Status != payment.AwaitingPayment || s.Asset.ChainID != 137 || s.Merchant.Name != "Shop" || s.Slower || s.Reason != nil || s.ReturnURL != nil {
		t.Errorf("state = %+v, want awaiting_payment, chain 137, not slower, no reason", s)
	}
	if slow := checkout.Build(p, checkout.Facts{Now: now, Watched: true, NetworkWord: "stalled"}); !slow.Slower {
		t.Error("a stalled network does not show as slower")
	}
}

func TestReadable_HoldsWhileOpenAndForThirtyDaysAfterTheEnd(t *testing.T) {
	t.Parallel()
	if !checkout.Readable(time.Time{}, now.Add(365*24*time.Hour)) {
		t.Error("an open payment's page stopped being readable")
	}
	if !checkout.Readable(now, now.Add(checkout.PageLife-time.Second)) || checkout.Readable(now, now.Add(checkout.PageLife)) {
		t.Error("the page's life after the end is not thirty days")
	}
}
