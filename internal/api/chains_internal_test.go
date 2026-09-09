package api

import (
	"testing"

	"github.com/sucopay/sucopay/internal/observe"
)

// Every word a network can be in is one this decides about, and each decision
// is written out rather than left to what a word nothing here knows falls to.
//
// The list this reads from is the one that produces the words, so a word
// renamed or added there fails here. Without that, renaming one of the four
// that take a deployment out of service would leave this recognising a state
// nothing reaches, and a deployment reading no network would answer that it
// was well.
func TestServing_DecidesAboutEveryWordANetworkCanBeIn(t *testing.T) {
	t.Parallel()
	// The words nobody here can act on take a deployment out, and the ones
	// somebody has to act on leave it in, because the API is running and the
	// one who acts is the operator or the provider.
	decided := map[string]bool{
		"observing":         true,
		"no-position":       true,
		"finalized-changed": true,
		"finalized-behind":  true,
		"unreachable":       false,
		"stalled":           false,
		"chain-mismatch":    false,
		"no-finalized":      false,
	}

	words := observe.NetworkWords()
	if len(words) != len(decided) {
		t.Fatalf("a network can be in %v, and this decides about %d of them", words, len(decided))
	}
	for _, word := range words {
		in, known := decided[word]
		if !known {
			t.Errorf("nothing here decides about a network that is %q", word)
			continue
		}
		if got := serving(map[string]string{"a": word}); got != in {
			t.Errorf("a deployment whose one network is %q is served %v, want %v", word, got, in)
		}
	}
}
