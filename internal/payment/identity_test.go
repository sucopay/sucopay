package payment_test

import (
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/payment"
)

func TestNewID_MakesSomethingParseIDAccepts(t *testing.T) {
	id, err := payment.NewID()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := payment.ParseID(id.String()); err != nil {
		t.Errorf("ParseID rejected what NewID made (%q): %v", id, err)
	}
}

func TestNewID_DoesNotRepeatItself(t *testing.T) {
	// An identifier that collided would attach one payment's money to
	// another's business.
	seen := make(map[payment.ID]bool, 1000)
	for range 1000 {
		id, err := payment.NewID()
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("%q came back twice", id)
		}
		seen[id] = true
	}
}

func TestParseID_RefusesAnythingThatIsNotOne(t *testing.T) {
	valid := strings.Repeat("ab", 16)

	for _, c := range []struct{ name, id string }{
		{"empty", ""},
		{"too short", valid[:31]},
		{"too long", valid + "a"},
		{"not hexadecimal", strings.Repeat("z", 32)},
		{"uppercase", strings.ToUpper(valid)},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := payment.ParseID(c.id); err == nil {
				t.Errorf("ParseID(%q) returned no error", c.id)
			}
		})
	}
}

func TestParseAddress_KeepsTheAccountAsTheNetworkWroteIt(t *testing.T) {
	// Chains write an account differently, and which spellings mean one
	// account is the chain's business. Changing the case here would break a
	// network whose addresses are case-sensitive, which base58 chains are.
	for _, address := range []string{
		"0x" + strings.Repeat("aB", 20),
		"DRpbCBMxVnDK7maPM5tGv6MvB3v1sRMC86PZ8okm1GwZ",
		"rN7n7otQDd6FczFgLdSqtcsAUxDkw6fzRH",
	} {
		t.Run(address, func(t *testing.T) {
			got, err := payment.ParseAddress(address)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != address {
				t.Errorf("address = %q, want it as written, %q", got, address)
			}
		})
	}
}

func TestParseAddress_RefusesWhatCouldNotBeAnAccount(t *testing.T) {
	for _, c := range []struct{ name, address string }{
		{"empty", ""},
		{"longer than any account", strings.Repeat("a", payment.MaxAddressBytes+1)},
		{"holding a newline", "0xabc\n  destination forged"},
		{"holding a right-to-left override", "0xabc\u202e"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := payment.ParseAddress(c.address); err == nil {
				t.Errorf("ParseAddress(%q) returned no error", c.address)
			}
		})
	}
}
