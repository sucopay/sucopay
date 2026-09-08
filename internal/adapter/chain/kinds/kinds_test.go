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

// The two lists are one list written twice. A kind registered here and unknown
// to the configuration could never be opened, because no document could
// declare it; a kind the configuration accepts and nobody registered would be
// declared and then not found.
func TestNames_AreEveryKindTheConfigurationKnowsAndNoOther(t *testing.T) {
	t.Parallel()
	if names, known := kinds.Names(), config.NetworkKinds(); !slices.Equal(names, known) {
		t.Errorf("Names() = %q, and the configuration accepts %q", names, known)
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

// The adapter of a chain that is reached over a network needs an endpoint to
// reach it at, and a reference on it is an address.
func TestLookup_FindsEVMWithBothOfItsFunctions(t *testing.T) {
	t.Parallel()
	kind, ok := kinds.Lookup("evm")
	if !ok {
		t.Fatal(`Lookup("evm") found nothing`)
	}
	if kind.Normalize == nil || kind.Open == nil {
		t.Fatal("the kind is missing one of its functions")
	}
	reference, err := kind.Normalize("0xE7C3D8C9a439feDe00D2600032D5dB0Be71C3c29")
	if err != nil {
		t.Fatal(err)
	}
	if want := "0xe7c3d8c9a439fede00d2600032d5db0be71c3c29"; reference != want {
		t.Errorf("Normalize gave %q, want %q", reference, want)
	}
	if _, err := kind.Open(chain.Settings{Name: "polygon", ChainID: "137"}); err == nil {
		t.Error("Open made a chain out of settings naming no endpoint")
	}
}
