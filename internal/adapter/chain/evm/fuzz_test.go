package evm

import (
	"encoding/json"
	"strings"
	"testing"
)

// FuzzNormalize reads what a document says an account or an asset is. What
// reaches it is whatever somebody typed there.
func FuzzNormalize(f *testing.F) {
	for _, seed := range []string{
		"", "0x", checksummed[0], strings.ToLower(checksummed[0]),
		"0X" + strings.ToUpper(checksummed[0][2:]), strings.Repeat("a", 200),
		"0x" + strings.Repeat("ab", addressBytes), "a\nb", "a\u202eb", "\xff",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, reference string) {
		got, err := Normalize(reference)
		if err != nil {
			return
		}

		if want := len("0x") + 2*addressBytes; len(got) != want {
			t.Fatalf("Normalize(%q) = %q, which is %d characters and not %d", reference, got, len(got), want)
		}
		if !strings.HasPrefix(got, "0x") || got != strings.ToLower(got) {
			t.Fatalf("Normalize(%q) = %q, which is not the one form this compares", reference, got)
		}
		// What it writes, it reads back as itself: a reference is written to a
		// document, read out of one, and put through this again.
		again, err := Normalize(got)
		if err != nil {
			t.Fatalf("Normalize(%q) = %q, and then refuses it: %v", reference, got, err)
		}
		if again != got {
			t.Errorf("Normalize(%q) = %q, and reading that gives %q", reference, got, again)
		}
	})
}

// FuzzReadReceipt reads a receipt the way a provider answers with one. Every
// value in it was written by somebody else, and what comes out of it is what
// the rules are handed.
func FuzzReadReceipt(f *testing.F) {
	one := receiptOf(tx, 0x89,
		used(asset, authorizer, spent, tx),
		sent(asset, authorizer, merchant, "0x"+strings.Repeat("00", wordBytes-1)+"07", tx),
	)
	for _, seed := range []string{"", "null", "[]", "{}", `{"logs":[]}`, one, one[:len(one)/2]} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, body string) {
		var read *receipt
		if err := json.Unmarshal([]byte(body), &read); err != nil || read == nil {
			return
		}
		transfers, err := read.transfers(read.TransactionHash.String())
		if err != nil {
			return
		}

		for _, transfer := range transfers {
			// Whatever a provider wrote, what the rules are given is in the
			// one form they compare in.
			for what, reference := range map[string]string{
				"asset": transfer.Asset, "authorizer": transfer.Authorizer,
				"payer": transfer.From, "merchant": transfer.To,
			} {
				if written, err := Normalize(reference); err != nil || written != reference {
					t.Errorf("the %s reads as %q", what, reference)
				}
			}
			if len(transfer.Key) != 2*wordBytes {
				t.Errorf("the key reads as %q", transfer.Key)
			}
			if transfer.Value == "" || strings.TrimLeft(transfer.Value, "0123456789") != "" {
				t.Errorf("the value reads as %q, which is not an amount in digits", transfer.Value)
			}
		}
	})
}
