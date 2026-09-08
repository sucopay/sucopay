package evm

import (
	"encoding/hex"
	"strings"
	"testing"
)

// checksummed are addresses as EIP-55 writes one: the digits in lower case,
// with a letter raised wherever the hash of those digits says to raise it.
// The first four are the standard's own examples, and the last is what a
// network's document holds an asset as.
var checksummed = []string{
	"0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed",
	"0xfB6916095ca1df60bB79Ce92cE3Ea74c37c5d359",
	"0xdbF03B407c01E7cD3CBea99509d93f8DDDC8C6FB",
	"0xD1220A0cf47c7B9Be7A2E6BA89F429762e7b9aDb",
	"0xE7C3D8C9a439feDe00D2600032D5dB0Be71C3c29",
}

func TestNormalize_WritesEveryCasingOfOneAddressTheSameWay(t *testing.T) {
	t.Parallel()
	for _, written := range checksummed {
		want := strings.ToLower(written)
		for name, reference := range map[string]string{
			"a checksummed address":    written,
			"an address in lower case": want,
			"an address in upper case": "0x" + strings.ToUpper(written[2:]),
		} {
			t.Run(name+" "+written, func(t *testing.T) {
				got, err := Normalize(reference)

				if err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Errorf("Normalize(%q) = %q, want %q", reference, got, want)
				}
			})
		}
	}
}

func TestNormalize_RefusesWhatIsNotAnAccountOnThisChain(t *testing.T) {
	t.Parallel()
	written := checksummed[0]
	digits := written[2:]
	for name, reference := range map[string]string{
		"nothing":                              "",
		"no prefix":                            digits,
		"a raised prefix":                      "0X" + digits,
		"19 bytes":                             written[:len(written)-2],
		"21 bytes":                             written + "ab",
		"digits that are not hexadecimal":      "0x" + strings.Repeat("zz", 20),
		"a letter the checksum does not raise": "0x5AAeb6053F3E94C9b9A09f33669435E7Ef1BeAed",
		"a letter the checksum raises":         "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1Beaed",
		"a name instead":                       "jpyc",
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := Normalize(reference); err == nil {
				t.Errorf("Normalize(%q) = %q, and that is not an address", reference, got)
			}
		})
	}
}

// A raised letter is what tells a mistyped address from a good one, and the
// only thing that does. Nothing else here would notice a wrong digit.
func TestNormalize_RefusesEverySingleLetterMistypedInAChecksummedAddress(t *testing.T) {
	t.Parallel()
	for _, written := range checksummed {
		for i := 2; i < len(written); i++ {
			flipped := []byte(written)
			switch c := flipped[i]; {
			case c >= 'a' && c <= 'f':
				flipped[i] = c - 'a' + 'A'
			case c >= 'A' && c <= 'F':
				flipped[i] = c - 'A' + 'a'
			default:
				continue
			}
			if _, err := Normalize(string(flipped)); err == nil {
				t.Errorf("Normalize(%q) took it, and it is %q with one letter recased", flipped, written)
			}
		}
	}
}

// The addresses this reads are the ones the chain writes into a log, where an
// account sits in the low bytes of a word. A word carrying anything above
// them is not an account, and reading one as an account would name whoever
// the low bytes happen to be.
func TestAccount_ReadsOnlyAWordWithNothingAboveTheAddress(t *testing.T) {
	t.Parallel()
	var w word
	for i := range w {
		w[i] = byte(i)
	}
	if _, err := account(w); err == nil {
		t.Error("a word with bytes above the address read as an account")
	}

	copy(w[:12], make([]byte, 12))
	got, err := account(w)
	if err != nil {
		t.Fatal(err)
	}
	if want := "0x" + hex.EncodeToString(w[12:]); got.String() != want {
		t.Errorf("the account read as %s, want %s", got, want)
	}
}
