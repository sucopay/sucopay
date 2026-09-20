package evm

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/adapter/chain"
)

// The EIP-712 example domain, whose separator the standard's own test
// vectors give: Ether Mail, version 1, chain 1, at 0xCcCC…cccC.
func TestDomain_HashesTheStandardsExampleAsTheStandardDoes(t *testing.T) {
	t.Parallel()
	got, err := Domain("Ether Mail", "1", 1, "0xCcCCccccCCCCcCCCCCCcCcCccCcCCCcCcccccccC")

	if err != nil {
		t.Fatal(err)
	}
	if want := "f2cee375fa42b42143804025fc449deafd50cc031ca257e0b194a650a912090f"; hex.EncodeToString(got[:]) != want {
		t.Errorf("Domain = %x, want %s", got, want)
	}
	if other, _ := Domain("Ether Mail", "2", 1, "0xCcCCccccCCCCcCCCCCCcCcCccCcCCCcCcccccccC"); other == got {
		t.Error("another version hashed the same")
	}
	if _, err := Domain("Ether Mail", "1", 1, "not an address"); err == nil {
		t.Error("a contract that is not an address was hashed")
	}
}

// DOMAIN_SEPARATOR() is called on the contract, and the word it answers is
// what the document's domain is compared with.
func TestDomainSeparator_CallsTheContractAndAnswersItsWord(t *testing.T) {
	t.Parallel()
	const separator = "0xf2cee375fa42b42143804025fc449deafd50cc031ca257e0b194a650a912090f"
	n, p := opening(t, map[string]string{"eth_call": answered(`"` + separator + `"`)})

	got, err := n.DomainSeparator(t.Context(), "0xCcCCccccCCCCcCCCCCCcCcCccCcCCCcCcccccccC")

	if err != nil {
		t.Fatal(err)
	}
	if "0x"+hex.EncodeToString(got[:]) != separator {
		t.Errorf("DomainSeparator = %x, want %s", got, separator)
	}
	asked := p.asked
	if len(asked) != 1 || asked[0].method != "eth_call" {
		t.Fatalf("asked %v, want one eth_call", asked)
	}
	call, _ := asked[0].params[0].(map[string]any)
	if call["data"] != domainSeparatorSelector || call["to"] != "0xcccccccccccccccccccccccccccccccccccccccc" {
		t.Errorf("called %v, want DOMAIN_SEPARATOR() on the contract", call)
	}
}

// A contract with no DOMAIN_SEPARATOR() answers an empty word, and that is
// refused as such rather than read as a separator of zeros.
func TestDomainSeparator_RefusesAContractThatAnswersNothing(t *testing.T) {
	t.Parallel()
	n, _ := opening(t, map[string]string{"eth_call": answered(`"0x"`)})

	_, err := n.DomainSeparator(t.Context(), "0xCcCCccccCCCCcCCCCCCcCcCccCcCCCcCcccccccC")

	if err == nil || !strings.Contains(err.Error(), "no DOMAIN_SEPARATOR()") {
		t.Errorf("err = %v, want the contract named as answering no DOMAIN_SEPARATOR()", err)
	}
}

// A contract without DOMAIN_SEPARATOR() reverts with nothing to say, and
// that is told from every other refusal: a revert carrying a reason is a
// function that ran, an empty answer is an address with no code, and a
// provider's own refusal is neither. Only a bare revert is the separator
// being absent, whatever code the provider gives it and whatever status it
// comes under.
func TestDomainSeparator_SaysTheContractHasNoSeparatorOnlyForABareRevert(t *testing.T) {
	t.Parallel()
	const contract = "0xCcCCccccCCCCcCCCCCCcCcCccCcCCCcCcccccccC"
	for name, c := range map[string]struct {
		status int
		body   string
		absent bool
	}{
		"a bare revert":                    {http.StatusOK, failed(3, "execution reverted"), true},
		"a bare revert under another code": {http.StatusOK, failedWith(-32000, "execution reverted", "null"), true},
		"a bare revert with empty data":    {http.StatusOK, failedWith(3, "execution reverted", `"0x"`), true},
		"a bare revert under 400":          {http.StatusBadRequest, failed(3, "execution reverted"), true},
		"a revert with a reason":           {http.StatusOK, failedWith(3, "execution reverted", `"0x08c379a0"`), false},
		"a reason in the message":          {http.StatusOK, failed(3, "execution reverted: Pausable: paused"), false},
		"an empty answer":                  {http.StatusOK, answered(`"0x"`), false},
		"the provider's own refusal":       {http.StatusOK, failed(-32601, "no such method"), false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n := &network{client: answering(t, c.status, c.body, nil)}

			_, err := n.DomainSeparator(t.Context(), contract)

			if err == nil {
				t.Fatal("the call went through")
			}
			if got := errors.Is(err, chain.ErrNoSeparator); got != c.absent {
				t.Errorf("ErrNoSeparator = %v, want %v: %v", got, c.absent, err)
			}
		})
	}
}

// name() answers an ABI string, and only the one layout is read as one: the
// offset of the bytes as the first word, their length as the second, and the
// bytes padded with zeros to a whole word, each number small enough to be
// what it says. What comes out is held to what a reader can see whole.
func TestName_ReadsTheOneLayoutOfAnABIStringAndRefusesEveryOther(t *testing.T) {
	t.Parallel()
	const contract = "0xCcCCccccCCCCcCCCCCCcCcCccCcCCCcCcccccccC"
	word := func(n int) string { return fmt.Sprintf("%064x", n) }
	padded := func(s string) string {
		h := hex.EncodeToString([]byte(s))
		return h + strings.Repeat("0", (64-len(h)%64)%64)
	}
	str := func(s string) string { return `"0x` + word(32) + word(len(s)) + padded(s) + `"` }
	for name, c := range map[string]struct {
		body string
		want string
		err  string
	}{
		"a name":                          {answered(str("JPY Coin")), "JPY Coin", ""},
		"a name filling its word":         {answered(str(strings.Repeat("a", 32))), strings.Repeat("a", 32), ""},
		"an empty answer":                 {answered(`"0x"`), "", "no name()"},
		"one word":                        {answered(`"0x` + word(32) + `"`), "", "two words"},
		"an offset elsewhere":             {answered(`"0x` + word(64) + word(8) + padded("JPY Coin") + `"`), "", "first word"},
		"an offset with a high byte set":  {answered(`"0x01` + word(32)[2:] + word(8) + padded("JPY Coin") + `"`), "", "first word"},
		"a length with a high byte set":   {answered(`"0x` + word(32) + "01" + word(8)[2:] + padded("JPY Coin") + `"`), "", "256 bytes"},
		"a length past the bytes":         {answered(`"0x` + word(32) + word(40) + padded("JPY Coin") + `"`), "", "after the length"},
		"a word too many":                 {answered(`"0x` + word(32) + word(8) + padded("JPY Coin") + word(0) + `"`), "", "after the length"},
		"a length past the most":          {answered(`"0x` + word(32) + word(257) + padded("JPY Coin") + `"`), "", "256 bytes"},
		"an answer longer than one takes": {answered(`"0x` + word(32) + word(257) + strings.Repeat("61", 288) + `"`), "", "bytes takes"},
		"padding that is not zeros":       {answered(`"0x` + word(32) + word(8) + hex.EncodeToString([]byte("JPY Coin")) + strings.Repeat("00", 23) + "01" + `"`), "", "padding"},
		"bytes that are not UTF-8":        {answered(`"0x` + word(32) + word(1) + "ff" + strings.Repeat("00", 31) + `"`), "", "not UTF-8"},
		"a character that does not show":  {answered(str("JPY\u202eCoin")), "", "do not show"},
		"the contract's own":              {failed(3, "execution reverted"), "", "execution reverted"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, p := opening(t, map[string]string{"eth_call " + nameSelector: c.body})

			got, err := n.Name(t.Context(), contract)

			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want one saying %q", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("Name = %q, want %q", got, c.want)
			}
			calls := p.calls()
			if len(calls) != 1 || calls[0].method != "eth_call" {
				t.Fatalf("asked %v, want one eth_call", calls)
			}
			call, _ := calls[0].params[0].(map[string]any)
			if call["data"] != nameSelector || call["to"] != "0xcccccccccccccccccccccccccccccccccccccccc" {
				t.Errorf("called %v, want name() on the contract", call)
			}
		})
	}
}

func TestName_RefusesAReferenceThatIsNotAnAddressWithoutAsking(t *testing.T) {
	t.Parallel()
	n, p := opening(t, nil)

	_, err := n.Name(t.Context(), "JPYC")

	if err == nil {
		t.Fatal("a reference that is not an address was read")
	}
	if calls := p.calls(); len(calls) != 0 {
		t.Errorf("asked %v, want nothing asked", calls)
	}
}
