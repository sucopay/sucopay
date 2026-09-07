package kinds_test

import (
	"context"
	"slices"
	"strconv"
	"testing"

	"github.com/sucopay/sucopay/internal/adapter/chain"
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

// A kind is of no use to the process without both of its functions: one to
// write a reference the way the kind writes one, and one to open a chain.
func TestLookup_FindsSimulatedWithBothOfItsFunctions(t *testing.T) {
	t.Parallel()
	kind, ok := kinds.Lookup("simulated")
	if !ok {
		t.Fatal(`Lookup("simulated") found nothing`)
	}
	if kind.Normalize == nil {
		t.Fatal("the kind has no Normalize")
	}
	if kind.Open == nil {
		t.Fatal("the kind has no Open")
	}
	reference, err := kind.Normalize("Whatever It Says")
	if err != nil {
		t.Fatal(err)
	}
	if reference != "Whatever It Says" {
		t.Errorf("Normalize gave %q, and a simulated chain writes a reference as it is given", reference)
	}
	opened, err := kind.Open(chain.Settings{Name: "local"})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := opened.Identity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if identity != kind.Name {
		t.Errorf("the chain calls itself %q, and the kind is %q", identity, kind.Name)
	}
}
