package payment

import (
	"errors"
	"fmt"

	"github.com/sucopay/sucopay/internal/invisible"
)

// MaxAssetDecimals bounds how far an asset may divide. JPYC has 18 and USDC
// has 6; nothing needs more than this, and a number beyond it is a registry
// that has been filled in wrong rather than an asset nobody has met.
const MaxAssetDecimals = 36

// Network names the chain an asset lives on, matching a key under networks in
// the configuration document.
type Network string

func (n Network) String() string { return string(n) }

// Asset is one token on one network.
//
// What identifies it is Network and Reference, never Symbol. A symbol is a
// label the token itself supplies: two tokens on one network can present the
// same one, which has happened to USDC on more than one chain, and a second
// yen-pegged coin would not be JPYC however it spelled itself. Comparing
// amounts by symbol accepts the wrong token.
//
// Reference is opaque here. What identifies a token on a chain is the chain's
// business: an address on one, a mint on another, an issuer and a currency on
// a third. [ADR 0002] keeps chain-shaped values behind the adapter, so this
// package holds the string and the adapter decides whether it means anything.
//
// [ADR 0002]: chain-specific code stays behind an adapter.
type Asset struct {
	network   Network
	reference string
	symbol    string
	decimals  uint8
}

var (
	// ErrNoNetwork reports an asset that names no network.
	ErrNoNetwork = errors.New("asset has no network")
	// ErrNoReference reports an asset the network could not identify.
	ErrNoReference = errors.New("asset has no reference")
	// ErrTooManyDecimals reports an asset dividing further than any does.
	ErrTooManyDecimals = errors.New("asset has too many decimals")
)

// NewAsset describes one token on one network. Reference is whatever that
// network identifies the token by; symbol is for showing a person, and takes
// no part in deciding whether two assets are the same.
func NewAsset(network Network, reference, symbol string, decimals uint8) (Asset, error) {
	switch {
	case network == "":
		return Asset{}, ErrNoNetwork
	case reference == "":
		return Asset{}, ErrNoReference
	case decimals > MaxAssetDecimals:
		return Asset{}, fmt.Errorf("%w: %d, at most %d", ErrTooManyDecimals, decimals, MaxAssetDecimals)
	}
	for name, value := range map[string]string{
		"network": string(network), "reference": reference, "symbol": symbol,
	} {
		if invisible.Has(value) {
			return Asset{}, fmt.Errorf("asset %s holds a character that does not show up", name)
		}
	}
	return Asset{network: network, reference: reference, symbol: symbol, decimals: decimals}, nil
}

// Network returns the chain the asset lives on.
func (a Asset) Network() Network { return a.network }

// Reference returns what the network identifies the token by.
func (a Asset) Reference() string { return a.reference }

// Symbol returns what to show a person. It does not identify the asset.
func (a Asset) Symbol() string { return a.symbol }

// Decimals returns how many places divide one unit into the smallest unit.
func (a Asset) Decimals() uint8 { return a.decimals }

// IsSet reports whether a came from [NewAsset] rather than being the zero
// value.
func (a Asset) IsSet() bool { return a.network != "" && a.reference != "" }

// Same reports whether a and other are the same token. Symbol and decimals
// take no part: they describe the token, and two descriptions of one token
// that disagree are a registry to fix rather than two tokens.
func (a Asset) Same(other Asset) bool {
	return a.IsSet() && other.IsSet() &&
		a.network == other.network && a.reference == other.reference
}

// String names the asset for a person, by symbol and network. It is not an
// identifier: see [Asset.Reference].
func (a Asset) String() string {
	if !a.IsSet() {
		return "no asset"
	}
	symbol := a.symbol
	if symbol == "" {
		symbol = a.reference
	}
	return symbol + " on " + string(a.network)
}
