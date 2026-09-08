package payment

import (
	"fmt"
	"time"

	"github.com/sucopay/sucopay/internal/invisible"
)

// Transfer is one transfer of an asset, as a chain wrote it and an adapter
// handed it back. Every field is what the chain says, and nothing here has
// been compared with anything yet.
type Transfer struct {
	// Scheme names how the transfer was authorised.
	Scheme Scheme
	// Asset is the reference of the asset transferred, as the chain writes
	// one.
	Asset string
	// Key is what the authorisation consumed: the attempt's key, if the
	// transfer is against one.
	Key string
	// Authorizer is the account that signed the authorisation.
	Authorizer string
	// From is the account the asset left, and To the account it went to.
	From, To string
	// Value is the amount in the asset's smallest unit, as decimal digits.
	Value string
	// Tx is the transaction that carried the transfer.
	Tx string
	// Position is the transfer's place among the transfers of its
	// transaction, counted from zero.
	Position int
	// BlockHeight, BlockHash and BlockTime are the block the transaction is
	// in, as it stood when the transfer was seen.
	BlockHeight uint64
	BlockHash   string
	BlockTime   time.Time
}

// Reason is what the rules made of a transfer, in one word. It is what a
// merchant is shown when a payer says they paid and the payment says
// otherwise.
type Reason string

// The words a judged transfer carries, and the one the observer writes when a
// transfer it recorded is no longer on the chain.
const (
	// Matched is a transfer that passed every rule.
	Matched Reason = "matched"
	// WrongAsset is a transfer of another asset than the payment's.
	WrongAsset Reason = "wrong_asset"
	// WrongTo is a transfer to somewhere other than the payment's
	// destination.
	WrongTo Reason = "wrong_to"
	// Short is a transfer of less than the payment asks for.
	Short Reason = "short"
	// Late is a transfer that passed every rule against a payment that was
	// not open for payment.
	Late Reason = "late"
	// Vanished is a transfer that was recorded and is no longer on the chain
	// where it was seen.
	Vanished Reason = "vanished"
)

// String returns the reason as written.
func (r Reason) String() string { return string(r) }

// Judge says what a transfer is worth against one attempt at one payment, and
// whether the transfer is that attempt's at all.
//
// The rules are read in order, and the first one a transfer fails is what it
// is worth: another asset's contract, another destination, too little. A
// transfer that passes them all is matched, unless the payment was not open
// for payment when it arrived, which is late.
//
// The false is a transfer of a key this attempt does not hold. Whoever reads a
// chain pairs a transfer with the attempt whose key it consumed, and a pair
// that does not hold is a mistake there rather than evidence about a payment.
func Judge(t Transfer, a *Attempt, p *Payment) (Reason, bool) {
	if a == nil || p == nil {
		return "", false
	}
	// Rule 2. The key is one this payment was issued, which is what the pair
	// coming in says; a pair that does not hold is a mistake in the pairing.
	if t.Key != a.Key() || t.Scheme != a.Scheme() || a.Network() != p.Network() {
		return "", false
	}
	// Rule 1. Another contract can carry the same event under the same name,
	// so what a transfer says about itself counts only on the asset's own
	// contract.
	if t.Asset != p.Asset().Reference() {
		return WrongAsset, true
	}
	// Rules 3 and 4. The payer signs what they choose to sign, so a signature
	// that spends the right key can still name another destination or another
	// amount.
	if t.To != string(p.Destination()) {
		return WrongTo, true
	}
	paid, err := ParseMoney(p.Asset(), t.Value)
	if err != nil {
		return Short, true
	}
	if short, err := paid.Cmp(p.Amount()); err != nil || short < 0 {
		return Short, true
	}
	if p.Status() != AwaitingPayment {
		return Late, true
	}
	return Matched, true
}

// Hit is an attempt a transfer's key belongs to, with the payment it is
// against, each with the revision it was read at.
//
// It carries the account because whoever reads a chain does not know one: a
// transfer names a network and a key, and the account is what the lookup hands
// back for every read and write after it.
type Hit struct {
	// Account owns the payment and the attempt.
	Account AccountID
	// Attempt is the attempt whose key was consumed, at AttemptAt.
	Attempt   *Attempt
	AttemptAt Revision
	// Payment is what the attempt is against, at PaymentAt.
	Payment   *Payment
	PaymentAt Revision
}

// Seen is one transfer, judged.
type Seen struct {
	Hit
	// Transfer is what the chain said.
	Transfer Transfer
	// Reason is what [Judge] made of it.
	Reason Reason
	// Implementation identifies the code behind the asset when the transfer
	// was seen. It is empty on a chain whose assets cannot be replaced.
	Implementation string
}

// MaxTransferField is the longest a chain's own writing may be in a transfer:
// a reference, an account, a transaction, a hash. An EVM chain writes 66
// characters at most, and the bound is wide enough for a chain that writes
// more without letting a provider fill a row with whatever it likes.
const MaxTransferField = 256

// screen refuses what a row should not carry, whatever a provider answered.
// The adapter checks the shapes of its own chain; this is the last gate before
// the value is stored and, later, shown to whoever asks what arrived.
func screen(t Transfer) error {
	for name, value := range map[string]string{
		"asset": t.Asset, "key": t.Key, "authorizer": t.Authorizer,
		"from": t.From, "to": t.To, "value": t.Value, "tx": t.Tx,
		"block hash": t.BlockHash, "scheme": string(t.Scheme),
	} {
		switch {
		case len(value) > MaxTransferField:
			return fmt.Errorf("transfer %s: %s is %d bytes, at most %d",
				t.Tx, name, len(value), MaxTransferField)
		case invisible.Has(value):
			return fmt.Errorf("transfer %s: %s holds a character that does not show up", t.Tx, name)
		}
	}
	return nil
}
