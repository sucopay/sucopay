package evm

import (
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/invisible"
)

// A word of zero or one is what paused() answers, and nothing else is read as
// a flag: not an empty answer, not a word with anything above its last byte,
// and not a provider's refusal. Every one of those would otherwise be a way
// for a provider to say "not paused" without the contract having said it.
func TestPaused_ReadsOnlyAWordOfZeroOrOneAsAFlag(t *testing.T) {
	t.Parallel()
	const contract = "0xCcCCccccCCCCcCCCCCCcCcCccCcCCCcCcccccccC"
	zero := "0x" + strings.Repeat("00", 32)
	one := "0x" + strings.Repeat("00", 31) + "01"
	for name, c := range map[string]struct {
		body string
		want bool
		err  string
	}{
		"not paused":         {answered(`"` + zero + `"`), false, ""},
		"paused":             {answered(`"` + one + `"`), true, ""},
		"nothing":            {answered(`"0x"`), false, "answered nothing"},
		"two":                {answered(`"0x` + strings.Repeat("00", 31) + `02"`), false, "not a flag"},
		"a high byte set":    {answered(`"0x01` + strings.Repeat("00", 31) + `"`), false, "not a flag"},
		"the contract's own": {failed(3, "execution reverted"), false, "execution reverted"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, p := opening(t, map[string]string{"eth_call " + pausedSelector: c.body})

			got, err := n.Paused(t.Context(), contract)

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
				t.Errorf("Paused = %v, want %v", got, c.want)
			}
			calls := p.calls()
			if len(calls) != 1 || calls[0].method != "eth_call" {
				t.Fatalf("asked %v, want one eth_call", calls)
			}
			call, _ := calls[0].params[0].(map[string]any)
			if call["data"] != pausedSelector || call["to"] != "0xcccccccccccccccccccccccccccccccccccccccc" {
				t.Errorf("called %v, want paused() on the contract", call)
			}
		})
	}
}

// The account goes after the selector as one word, with the address in the
// last twenty bytes: the ABI's layout of an address, and what the contract
// reads its argument from.
func TestBlocklisted_SendsTheAccountAsTheArgumentAndReadsTheFlag(t *testing.T) {
	t.Parallel()
	const contract = "0xCcCCccccCCCCcCCCCCCcCcCccCcCCCcCcccccccC"
	const account = "0xabcdef0123456789abcdef0123456789abcdef01"
	data := blocklistedSelector + strings.Repeat("00", 12) + "abcdef0123456789abcdef0123456789abcdef01"
	n, p := opening(t, map[string]string{"eth_call " + data: answered(`"0x` + strings.Repeat("00", 31) + `01"`)})

	got, err := n.Blocklisted(t.Context(), contract, account)

	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("Blocklisted = false, want the flag the contract answered")
	}
	calls := p.calls()
	if len(calls) != 1 {
		t.Fatalf("asked %v, want one eth_call", calls)
	}
	if call, _ := calls[0].params[0].(map[string]any); call["data"] != data {
		t.Errorf("called with %v, want the selector followed by the account as a word", call["data"])
	}
}

// Neither read reaches the provider with something that is not an address in
// it: the contract's address and the account's are checked here first, as
// [network.Implementation] checks its asset.
func TestPausedAndBlocklisted_RefuseWhatIsNoAddressWithoutAsking(t *testing.T) {
	t.Parallel()
	const contract = "0xCcCCccccCCCCcCCCCCCcCcCccCcCCCcCcccccccC"
	for name, read := range map[string]func(*network) error{
		"an asset that is no address": func(n *network) error {
			_, err := n.Paused(t.Context(), "not a contract")
			return err
		},
		"an account that is no address": func(n *network) error {
			_, err := n.Blocklisted(t.Context(), contract, "not an account")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, p := opening(t, map[string]string{})

			err := read(n)

			if err == nil {
				t.Fatal("what is not an address was read as one")
			}
			if calls := p.calls(); len(calls) != 0 {
				t.Errorf("the provider was asked %v, want nothing", calls)
			}
		})
	}
}

// What a provider writes into an answer that is not a word is repeated in the
// error, and repeated the way every other error repeats a provider: through
// the client, which cuts it short and shows a character that would not show.
// A bidi override in a log line rewrites the line after it.
func TestFlag_RepeatsABrokenAnswerWithNothingInvisibleInIt(t *testing.T) {
	t.Parallel()
	for name, read := range map[string]func(*network) error{
		"paused": func(n *network) error {
			_, err := n.Paused(t.Context(), "0xCcCCccccCCCCcCCCCCCcCcCccCcCCCcCcccccccC")
			return err
		},
		"the domain": func(n *network) error {
			_, err := n.DomainSeparator(t.Context(), "0xCcCCccccCCCCcCCCCCCcCcCccCcCCCcCcccccccC")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// An object where a string should be, with a right-to-left
			// override as its key, written as the character itself: valid
			// JSON, not a word, and not something to print as is.
			n, p := opening(t, map[string]string{"eth_call": answered("{\"\u202e\": 1}")})

			err := read(n)

			if err == nil {
				t.Fatal("an answer that is not a word was read as one")
			}
			if invisible.Has(err.Error()) {
				t.Errorf("the error carries a character that does not show up:\n%q", err)
			}
			if strings.Contains(err.Error(), p.url) {
				t.Errorf("the error carries the endpoint:\n%s", err)
			}
		})
	}
}
