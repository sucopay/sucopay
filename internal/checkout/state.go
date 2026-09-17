package checkout

import (
	"time"

	"github.com/sucopay/sucopay/internal/payment"
)

// PageLife is how long a payment's page can still be read after the
// payment ends. The same as the longest a payment can be open for: the
// token is in the payer's history and screenshots, and a key that read a
// payment's outcome for ever would be a key to keep.
const PageLife = 30 * 24 * time.Hour

// The words a page is given for why the payer cannot be given anything to
// sign. The page shows the payer something for each, and switches on
// nothing else.
const (
	// ReasonExpired is a payment past its deadline.
	ReasonExpired = "expired"
	// ReasonClosing is a payment with less left than a signature takes.
	ReasonClosing = "closing"
	// ReasonPaid is a payment a matched transfer has been seen for, which
	// is waiting to be settled and needs no second one.
	ReasonPaid = "paid"
	// ReasonDone is a payment that has ended.
	ReasonDone = "done"
	// ReasonNotReady is a deployment that has not read the payment's
	// network yet, and could not see a transfer arrive.
	ReasonNotReady = "not_ready"
	// ReasonReissued is a payment whose one reissue was used.
	ReasonReissued = "reissued"
)

// Result is the transfer seen for a payment, as the page shows it: what a
// payer can look up on the chain themselves.
type Result struct {
	Tx          string    `json:"tx"`
	BlockHeight uint64    `json:"block_height"`
	BlockTime   time.Time `json:"block_time"`
	// Value is what the transfer carried, in the asset's smallest unit.
	Value string `json:"value"`
	// Confirming is true while the transfer waits to be settled, and
	// false once the payment succeeded; ReceivedAt is then when it did.
	Confirming bool       `json:"confirming"`
	ReceivedAt *time.Time `json:"received_at"`
}

// Asset is a payment's asset as the page shows it: what a read of the
// payment answers, and the chain id the wallet is switched to.
type Asset struct {
	Symbol   string `json:"symbol"`
	Decimals uint8  `json:"decimals"`
	Network  string `json:"network"`
	ChainID  uint64 `json:"chain_id"`
}

// Merchant is the account as the page names it.
type Merchant struct {
	Name string `json:"name"`
}

// State is what the page shows, in one answer. Nothing of the merchant's
// metadata is in it, and the destination is only in what is signed.
type State struct {
	Status    payment.Status `json:"status"`
	Amount    string         `json:"amount"`
	Asset     Asset          `json:"asset"`
	ExpiresAt time.Time      `json:"expires_at"`
	Merchant  Merchant       `json:"merchant"`
	ReturnURL *string        `json:"return_url"`
	// Attempt is what the payer signs, while there is a live attempt.
	Attempt *TypedData `json:"attempt"`
	Result  *Result    `json:"result"`
	// Reason is why nothing can be signed, and null while something can.
	Reason *string `json:"reason"`
	// Slower is true while the deployment is not reading the payment's
	// network as it usually does.
	Slower bool `json:"slower"`
}

// Facts are what a state is built from besides the payment: what the
// deployment knows of the chain and of the attempts.
type Facts struct {
	Now      time.Time
	ChainID  uint64
	Merchant string
	// NetworkWord is what reading the network has come to, as the probe
	// says it; "observing" is the usual.
	NetworkWord string
	// Watched is whether the network has been read at all.
	Watched bool
	// Matched is the transfer seen for the payment, or nil.
	Matched *Result
	// Reissued is whether the payment's one reissue was used.
	Reissued bool
	// Attempt is the live attempt's signing material, or nil.
	Attempt *TypedData
}

// Build is the state of p under facts.
func Build(p *payment.Payment, f Facts) State {
	asset := p.Asset()
	status := p.Status()
	if status == payment.Created {
		// The page's payer is about to be given something to sign, and
		// created is what a payment is until the first of those.
		status = payment.AwaitingPayment
	}
	s := State{
		Status:    status,
		Amount:    p.Amount().Units(),
		Asset:     Asset{Symbol: asset.Symbol(), Decimals: asset.Decimals(), Network: string(asset.Network()), ChainID: f.ChainID},
		ExpiresAt: p.ExpiresAt(),
		Merchant:  Merchant{Name: f.Merchant},
		Attempt:   f.Attempt,
		Result:    f.Matched,
		Slower:    f.NetworkWord != "observing",
	}
	if u := p.ReturnURL(); u != "" {
		s.ReturnURL = &u
	}
	if reason := Reason(p, f); reason != "" {
		s.Reason = &reason
	}
	return s
}

// Reason is why the payer of p cannot be given anything to sign, and empty
// while they can. The first that applies is the answer, and the order is
// the one a payer would want them in: that the payment ended, then that it
// was paid, then that it expired or is about to, and only then what the
// deployment has not done.
func Reason(p *payment.Payment, f Facts) string {
	switch {
	case p.Status().Final():
		return ReasonDone
	case f.Matched != nil:
		return ReasonPaid
	case !f.Now.Before(p.ExpiresAt()):
		return ReasonExpired
	case p.ExpiresAt().Sub(f.Now) < payment.MinRemaining:
		return ReasonClosing
	case !f.Watched:
		return ReasonNotReady
	case f.Reissued:
		return ReasonReissued
	}
	return ""
}

// Readable says whether a payment's page can still be read at now: for as
// long as the payment is open, and for [PageLife] after it ends.
func Readable(closedAt, now time.Time) bool {
	return closedAt.IsZero() || now.Before(closedAt.Add(PageLife))
}
