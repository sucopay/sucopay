package kinds_test

import (
	"slices"
	"strconv"
	"testing"

	"github.com/sucopay/sucopay/internal/adapter/chain/kinds"
	"github.com/sucopay/sucopay/internal/config"
)

func TestLookup_RefusesANameNobodyRegistered(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "polygon", "EVM", "Simulated"} {
		t.Run(strconv.Quote(name), func(t *testing.T) {
			if kind, ok := kinds.Lookup(name); ok {
				t.Errorf("Lookup(%q) = %q, true; want false", name, kind.Name)
			}
		})
	}
}

// A kind registered here and unknown to the configuration could never be
// opened, because no document could declare it. The other direction, a kind
// the configuration accepts and nobody registered, holds once every adapter
// is in.
func TestNames_AreKindsTheConfigurationKnows(t *testing.T) {
	t.Parallel()
	known := config.NetworkKinds()
	names := kinds.Names()
	if !slices.IsSorted(names) {
		t.Errorf("Names() = %q, want name order", names)
	}
	for _, name := range names {
		if !slices.Contains(known, name) {
			t.Errorf("%q is registered, and the configuration knows no such kind", name)
		}
	}
}
