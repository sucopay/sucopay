package chain_test

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/adapter/chain"
)

// A chain is read in words no chain owns. The two below are words
// of one family of chains, and either one in this package would mean the
// boundary had learnt the chain behind it.
func TestSource_HoldsNoWordOfAChain(t *testing.T) {
	t.Parallel()
	for _, name := range sourceFiles(t) {
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			text := strings.ToLower(string(src))
			for _, word := range []string{"0x", "topic"} {
				if strings.Contains(text, word) {
					t.Errorf("holds %q, a word of one kind of chain", word)
				}
			}
		})
	}
}

func TestRateLimited_KeepsTheWaitThroughWrapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		wait time.Duration
		msg  string
	}{
		{"no wait given", 0, "chain: rate limited"},
		{"seven seconds", 7 * time.Second, "chain: rate limited, retry after 7s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := fmt.Errorf("scanning: %w", chain.RateLimited{RetryAfter: c.wait})
			var got chain.RateLimited
			if !errors.As(err, &got) {
				t.Fatalf("errors.As found no RateLimited in %v", err)
			}
			if got.RetryAfter != c.wait {
				t.Errorf("RetryAfter = %s, want %s", got.RetryAfter, c.wait)
			}
			if err.Error() != "scanning: "+c.msg {
				t.Errorf("Error() = %q, want %q", err.Error(), "scanning: "+c.msg)
			}
		})
	}
}
