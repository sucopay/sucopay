package evm

import (
	"encoding/hex"
	"strings"
	"testing"
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
