package checkout

import (
	"strconv"

	"github.com/sucopay/sucopay/internal/payment"
)

// TypedData is what the page hands the wallet to sign, as EIP-712 lays it
// out, less the payer's own address, which the page fills in. The keys
// under domain and message are the standard's, in camelCase.
type TypedData struct {
	// ID is the attempt's, for showing and for asking for a reissue; it is
	// not signed.
	ID          payment.AttemptID `json:"id"`
	Domain      Domain            `json:"domain"`
	Message     Message           `json:"message"`
	PrimaryType string            `json:"primaryType"`
}

// Domain is the EIP-712 domain of the asset's contract on its chain.
type Domain struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	ChainID           uint64 `json:"chainId"`
	VerifyingContract string `json:"verifyingContract"`
}

// Message is the EIP-3009 authorisation the payer signs: pay value to the
// destination, once, before the deadline, under the attempt's key.
type Message struct {
	To          payment.Address `json:"to"`
	Value       string          `json:"value"`
	ValidAfter  string          `json:"validAfter"`
	ValidBefore string          `json:"validBefore"`
	Nonce       string          `json:"nonce"`
}

// primaryType names the EIP-3009 function the signature is for.
const primaryType = "TransferWithAuthorization"

// Typed is the signing material of attempt a against payment p, with the
// asset's domain under name and version.
func Typed(p *payment.Payment, a *payment.Attempt, name, version string, chainID uint64) TypedData {
	return TypedData{
		ID: a.ID(),
		Domain: Domain{
			Name: name, Version: version, ChainID: chainID,
			VerifyingContract: p.Asset().Reference(),
		},
		Message: Message{
			To:          p.Destination(),
			Value:       p.Amount().Amount().String(),
			ValidAfter:  "0",
			ValidBefore: strconv.FormatInt(a.ValidBefore().Unix(), 10),
			Nonce:       "0x" + a.Key(),
		},
		PrimaryType: primaryType,
	}
}
