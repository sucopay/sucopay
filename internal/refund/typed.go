package refund

import (
	"strconv"

	"github.com/sucopay/sucopay/internal/payment"
)

// TypedData is what the page hands the merchant's wallet to sign, as EIP-712
// lays it out. The keys under domain and message are the standard's, in
// camelCase.
//
// Unlike what a payer signs, this carries from. A payer may pay from wherever
// they hold the money, and the page fills that in from the wallet; a refund
// can only be signed by the wallet the payment was paid to, so the material
// says which that is and the page checks the wallet against it.
type TypedData struct {
	Domain      Domain  `json:"domain"`
	Message     Message `json:"message"`
	PrimaryType string  `json:"primaryType"`
}

// Domain is the EIP-712 domain of the asset's contract on its chain.
type Domain struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	ChainID           uint64 `json:"chainId"`
	VerifyingContract string `json:"verifyingContract"`
}

// Message is the EIP-3009 authorisation the merchant signs: send value from
// where the payment was paid to, back to where it was paid from, once, before
// the deadline, under the refund's key.
type Message struct {
	From        payment.Address `json:"from"`
	To          payment.Address `json:"to"`
	Value       string          `json:"value"`
	ValidAfter  string          `json:"validAfter"`
	ValidBefore string          `json:"validBefore"`
	Nonce       string          `json:"nonce"`
}

// primaryType names the EIP-3009 function the signature is for.
const primaryType = "TransferWithAuthorization"

// Typed is the signing material of refund r against payment p, with the
// asset's domain under name and version.
func Typed(p *payment.Payment, r *payment.Refund, name, version string, chainID uint64) TypedData {
	return TypedData{
		Domain: Domain{
			Name: name, Version: version, ChainID: chainID,
			VerifyingContract: p.Asset().Reference(),
		},
		Message: Message{
			From:        p.Destination(),
			To:          r.Destination(),
			Value:       r.Amount().Amount().String(),
			ValidAfter:  "0",
			ValidBefore: strconv.FormatInt(r.ExpiresAt().Unix(), 10),
			Nonce:       "0x" + r.Key(),
		},
		PrimaryType: primaryType,
	}
}
