package main

import (
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/config"
)

// theChecksummed is an address as an EVM tool writes one, with the letters
// raised where EIP-55 says. Not theAddress, which is nearly all zeros and has
// too few letters to raise. What is compared on a chain is the lower-case
// form, and the two have to end up as one value or a transfer to this address
// reads as a transfer somewhere else.
const theChecksummed = "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed"

func TestNormalize_WritesAReferenceTheOneWayAChainComparesOne(t *testing.T) {
	t.Parallel()
	got, err := normalize(config.Network{Kind: "evm"}, theChecksummed)

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if got != strings.ToLower(theChecksummed) {
		t.Errorf("normalize(%s) = %s, want it in lower case", theChecksummed, got)
	}
}

// A chain with nothing to say about how an account is written leaves the value
// as it came.
func TestNormalize_LeavesAKindThatComparesWhateverItIsGiven(t *testing.T) {
	t.Parallel()
	got, err := normalize(config.Network{Kind: "simulated"}, "Whatever")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if got != "Whatever" {
		t.Errorf("normalize = %q, want it as it came", got)
	}
}

// A digit mistyped in a checksummed address names an account that exists as
// surely as the right one does, and a chain does not give funds back.
func TestNormalize_RefusesWhatIsNotAnAccountOnThatKind(t *testing.T) {
	t.Parallel()
	for what, reference := range map[string]string{
		"a digit that breaks the checksum": "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAeD",
		"too few digits":                   "0x5aaeb6053f3e94c9b9a09f33669435e7ef1bea",
		"nothing at all":                   "",
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			if got, err := normalize(config.Network{Kind: "evm"}, reference); err == nil {
				t.Errorf("normalize(%q) = %q, want a refusal", reference, got)
			}
		})
	}
}

// A kind this build cannot open has nothing to say about how a value is
// written, and guessing would put whatever it guessed into a row.
func TestNormalize_RefusesAKindThisBuildDoesNotCarry(t *testing.T) {
	t.Parallel()
	if _, err := normalize(config.Network{Kind: "ripple"}, "r9cZA1mLbJ"); err == nil {
		t.Error("a value was normalised for a kind nothing can open")
	}
}

// anEVMAssetOn is the sections declaring one network reached over an endpoint
// and one asset on it, whose reference is written the way an EVM tool writes
// one.
func anEVMAssetOn(network string) string {
	return anEVMNetwork(network) +
		"assets:\n  jpyc:\n    network: " + network + "\n" +
		"    reference: \"" + theChecksummed + "\"\n" +
		"    symbol: JPYC\n    decimals: 18\n"
}
