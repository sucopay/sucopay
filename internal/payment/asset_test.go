package payment_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/payment"
)

// The registry entries these tests use. The two USDC entries are the point:
// one network, one symbol, two tokens.
func jpyc(t *testing.T) payment.Asset {
	t.Helper()
	return asset(t, "polygon", "jpyc-contract", "JPYC", 18)
}

func usdc(t *testing.T) payment.Asset {
	t.Helper()
	return asset(t, "polygon", "usdc-contract", "USDC", 6)
}

func bridgedUSDC(t *testing.T) payment.Asset {
	t.Helper()
	return asset(t, "polygon", "usdc-bridged-contract", "USDC", 6)
}

func asset(t *testing.T, network payment.Network, reference, symbol string, decimals uint8) payment.Asset {
	t.Helper()
	a, err := payment.NewAsset(network, reference, symbol, decimals)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAsset_IsTheTokenNotTheSymbol(t *testing.T) {
	t.Parallel()
	// Two tokens on one network presenting one symbol is the situation USDC
	// is actually in. Deciding by symbol accepts the wrong one.
	native, bridged := usdc(t), bridgedUSDC(t)

	if native.Symbol() != bridged.Symbol() {
		t.Fatal("the two entries no longer share a symbol, so this tests nothing")
	}
	if native.Same(bridged) {
		t.Error("two tokens with one symbol read as the same asset")
	}
	if !native.Same(usdc(t)) {
		t.Error("one token described twice read as two assets")
	}
}

func TestAsset_SameIgnoresWhatOnlyDescribesTheToken(t *testing.T) {
	t.Parallel()
	// A registry that spelled the symbol differently, or got the decimals
	// wrong, still names one token. Which of the two descriptions is right is
	// a question for the registry, not for whether this is the same asset.
	one := asset(t, "polygon", "jpyc-contract", "JPYC", 18)
	other := asset(t, "polygon", "jpyc-contract", "JPY Coin", 6)

	if !one.Same(other) {
		t.Error("one token described two ways read as two assets")
	}
}

func TestAsset_TheSameTokenOnAnotherNetworkIsAnotherAsset(t *testing.T) {
	t.Parallel()
	here := asset(t, "polygon", "jpyc-contract", "JPYC", 18)
	there := asset(t, "ethereum", "jpyc-contract", "JPYC", 18)

	if here.Same(there) {
		t.Error("one reference on two networks read as one asset")
	}
}

func TestNewAsset_RefusesWhatWouldNotIdentifyAToken(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name      string
		network   payment.Network
		reference string
		symbol    string
		decimals  uint8
		want      error
	}{
		{name: "no network", reference: "r", symbol: "S", want: payment.ErrNoNetwork},
		{name: "no reference", network: "polygon", symbol: "S", want: payment.ErrNoReference},
		{
			name: "dividing further than any asset does", network: "polygon", reference: "r",
			symbol: "S", decimals: payment.MaxAssetDecimals + 1, want: payment.ErrTooManyDecimals,
		},
		{name: "a reference that does not show up", network: "polygon", reference: "r\u200bs", symbol: "S"},
		{name: "a symbol that does not show up", network: "polygon", reference: "r", symbol: "S\u202e"},
		{name: "a network that does not show up", network: "poly\ngon", reference: "r", symbol: "S"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := payment.NewAsset(c.network, c.reference, c.symbol, c.decimals)

			if err == nil {
				t.Fatal("want an error, got none")
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func TestAsset_TheZeroValueIsNoToken(t *testing.T) {
	t.Parallel()
	var a payment.Asset

	if a.IsSet() {
		t.Error("the zero Asset says it is set")
	}
	if a.Same(a) {
		t.Error("the zero Asset is the same token as itself")
	}
	if !strings.Contains(a.String(), "no asset") {
		t.Errorf("String() = %q, want it to say there is no asset", a.String())
	}
}

func TestAsset_StringNamesTheTokenAndTheNetwork(t *testing.T) {
	t.Parallel()
	// A symbol alone does not say which token, and this is what a person
	// reads in a report.
	got := usdc(t).String()

	for _, want := range []string{"USDC", "polygon"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, want it to name %s", got, want)
		}
	}
}
