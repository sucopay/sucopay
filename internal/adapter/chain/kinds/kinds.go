// Package kinds lists the kinds of chain this build can open. A kind is
// registered here and nowhere else: the configuration names one, and the
// process looks it up here for the adapter to open it with.
package kinds

import (
	"slices"

	"github.com/sucopay/sucopay/internal/adapter/chain"
	"github.com/sucopay/sucopay/internal/adapter/chain/evm"
	"github.com/sucopay/sucopay/internal/adapter/chain/simulated"
)

// all are the kinds this build can open, read through [Lookup] and [Names].
// Every kind the configuration accepts belongs here and nothing else does,
// which a test checks in both directions.
var all = []chain.Kind{simulated.Kind, evm.Kind}

// Lookup finds the kind registered under a name, spelt as the kind spells it.
func Lookup(name string) (chain.Kind, bool) {
	for _, kind := range all {
		if kind.Name == name {
			return kind, true
		}
	}
	return chain.Kind{}, false
}

// Names are the names of the registered kinds, in name order.
func Names() []string {
	names := make([]string, 0, len(all))
	for _, kind := range all {
		names = append(names, kind.Name)
	}
	slices.Sort(names)
	return names
}
