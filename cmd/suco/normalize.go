package main

import (
	"fmt"

	"github.com/sucopay/sucopay/internal/adapter/chain/kinds"
	"github.com/sucopay/sucopay/internal/config"
)

// normalize writes a reference or an account the one way the network's kind
// compares one, and refuses what is not one on that kind.
//
// Comparing a transfer with a payment is comparing strings. An EVM chain
// writes an account in either case, and the same account in two cases is two
// strings, so a payment would never match the transfer that paid it. The
// adapter that knows the chain decides the form; nothing above it does.
//
// It is also where a mistyped account is caught. A wrong digit names an
// account that exists as surely as the right one does, and a chain does not
// give funds back; the mixed case an EVM tool writes carries a checksum, and
// that is the only thing here that could notice.
func normalize(n config.Network, value string) (string, error) {
	kind, known := kinds.Lookup(n.Kind)
	if !known {
		return "", fmt.Errorf("kind %s is one this build cannot open", n.Kind)
	}
	return kind.Normalize(value)
}
