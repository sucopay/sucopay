package refund

import (
	"time"

	"github.com/sucopay/sucopay/internal/payment"
)

// The words a page is given for why nothing can be signed. The page shows the
// merchant something for each, and switches on nothing else.
const (
	// ReasonExpired is a refund past its deadline: the key is no longer good,
	// and the merchant opens another refund.
	ReasonExpired = "expired"
	// ReasonSent is a refund a transfer has been seen for, which is waiting
	// to settle and needs no second one.
	ReasonSent = "sent"
	// ReasonDone is a refund that has ended.
	ReasonDone = "done"
)

// Asset is the refund's asset as the page shows it, with the chain id the
// wallet is switched to.
type Asset struct {
	Symbol   string `json:"symbol"`
	Decimals uint8  `json:"decimals"`
	Network  string `json:"network"`
	ChainID  uint64 `json:"chain_id"`
}

// Result is the transfer seen for the refund, as the page shows it.
type Result struct {
	Tx          string    `json:"tx"`
	BlockHeight uint64    `json:"block_height"`
	BlockTime   time.Time `json:"block_time"`
	// Value is what the transfer carried, in the asset's smallest unit.
	Value string `json:"value"`
	// Settling is true while the transfer waits to be settled, and false once
	// the refund succeeded.
	Settling bool `json:"settling"`
}

// State is what the merchant's page shows, in one answer.
type State struct {
	Status payment.RefundStatus `json:"status"`
	// Payment is what the refund is against, so that a merchant opening the
	// page can tell which one it is.
	Payment payment.ID `json:"payment"`
	Amount  string     `json:"amount"`
	Asset   Asset      `json:"asset"`
	// From is the wallet that has to sign, which is where the payment was
	// paid, and To where the money goes back to.
	From      payment.Address `json:"from"`
	To        payment.Address `json:"to"`
	ExpiresAt time.Time       `json:"expires_at"`
	// Authorization is what the merchant signs, while there is anything to
	// sign.
	Authorization *TypedData `json:"authorization"`
	Result        *Result    `json:"result"`
	// Reason is why nothing can be signed, and null while something can.
	Reason *string `json:"reason"`
}

// Facts are what a state is built from besides the refund and its payment.
type Facts struct {
	Now     time.Time
	ChainID uint64
	// Domain is the EIP-712 domain of the asset's contract.
	Name, Version string
	// Seen is the transfer seen for the refund, or nil.
	Seen *payment.Transfer
}

// Build is the state of r against p under facts.
func Build(p *payment.Payment, r *payment.Refund, f Facts) State {
	asset := p.Asset()
	s := State{
		Status:  r.Status(),
		Payment: r.PaymentID(),
		Amount:  r.Amount().Units(),
		Asset: Asset{
			Symbol: asset.Symbol(), Decimals: asset.Decimals(),
			Network: string(asset.Network()), ChainID: f.ChainID,
		},
		From:      p.Destination(),
		To:        r.Destination(),
		ExpiresAt: r.ExpiresAt(),
	}
	if f.Seen != nil {
		s.Result = &Result{
			Tx: f.Seen.Tx, BlockHeight: f.Seen.BlockHeight, BlockTime: f.Seen.BlockTime.UTC(),
			Value: f.Seen.Value, Settling: r.Status() != payment.RefundSucceeded,
		}
	}
	if reason := Reason(r, f); reason != "" {
		s.Reason = &reason
		return s
	}
	typed := Typed(p, r, f.Name, f.Version, f.ChainID)
	s.Authorization = &typed
	return s
}

// Reason is why nothing can be signed for r, and empty while something can.
// The first that applies is the answer: that the refund ended, then that a
// transfer is on its way, then that the key has died.
func Reason(r *payment.Refund, f Facts) string {
	switch {
	case r.Status().Final():
		return ReasonDone
	case f.Seen != nil:
		return ReasonSent
	case !f.Now.Before(r.ExpiresAt()):
		return ReasonExpired
	}
	return ""
}
