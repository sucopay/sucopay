package evm

import (
	"errors"
	"os"
	"testing"

	"github.com/sucopay/sucopay/internal/adapter/chain"
)

// live is an adapter on the endpoint the environment names, or no test at all.
//
// What a public chain answers with is the only thing that says the shapes read
// here are the shapes it writes. An endpoint carries a key more often than
// not, so it is named where a repository cannot hold it.
func live(t *testing.T) *network {
	t.Helper()
	endpoint := os.Getenv("SUCO_TEST_POLYGON_RPC_URL")
	if endpoint == "" {
		t.Skip("SUCO_TEST_POLYGON_RPC_URL names no endpoint to read Polygon at")
	}
	c, err := newClient(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return &network{client: c}
}

func TestIdentity_ReadsThePolygonMainnetAsThe137thChain(t *testing.T) {
	t.Parallel()
	identity, err := live(t).Identity(t.Context())

	if err != nil {
		t.Fatal(err)
	}
	if identity != "137" {
		t.Errorf("the chain calls itself %q, and Polygon is 137", identity)
	}
}

// One payment as it was actually made: a payer's key spent on JPYC, and the
// token moved in the same transaction. Everything the rules are given comes
// out of these two logs.
func TestReceipt_ReadsAPaymentThatWasMadeOnPolygon(t *testing.T) {
	t.Parallel()
	const paid = "0x25a2307e6e108c7aa0e5efefc171c3875cb4bf08007fc1c1bbed14d623e7ea3e"

	transfers, err := live(t).Receipt(t.Context(), paid)

	if err != nil {
		t.Fatal(err)
	}
	if len(transfers) != 1 {
		t.Fatalf("the receipt read as %d transfers, want 1", len(transfers))
	}
	transfer := transfers[0]
	if want := "20000000000000000000000"; transfer.Value != want {
		t.Errorf("the transfer moved %s, want %s", transfer.Value, want)
	}
	// The chain stamped the block, and the contract compared this to the
	// deadline the payer signed.
	if want := int64(1788265269); transfer.Block.Time.Unix() != want {
		t.Errorf("the block is stamped %s, want %d", transfer.Block.Time, want)
	}
	if transfer.Scheme != scheme {
		t.Errorf("the transfer is authorised %q", transfer.Scheme)
	}
	// The reference of the asset, the key and the accounts are written the way
	// the rules compare them: this chain writes an address in mixed case as
	// readily as in lower.
	for what, got := range map[string]string{
		"the asset":      transfer.Asset,
		"the authorizer": transfer.Authorizer,
		"the payer":      transfer.From,
		"the merchant":   transfer.To,
	} {
		if want, err := Normalize(got); err != nil || want != got {
			t.Errorf("%s reads as %q", what, got)
		}
	}
	if len(transfer.Key) != 2*wordBytes {
		t.Errorf("the key reads as %q", transfer.Key)
	}
}

// The two controls an issuer holds, read off the token that is actually paid
// in: both answer, and neither is set. This is what says the selectors are
// the contract's, which no fake provider can.
func TestPausedAndBlocklisted_ReadJPYCOnPolygonAsNeitherPausedNorListing(t *testing.T) {
	t.Parallel()
	const jpyc = "0xE7C3D8C9a439feDe00D2600032D5dB0Be71C3c29"
	n := live(t)

	paused, err := n.Paused(t.Context(), jpyc)
	if err != nil {
		t.Fatal(err)
	}
	if paused {
		t.Error("JPYC reads as paused")
	}
	listed, err := n.Blocklisted(t.Context(), jpyc, "0x0000000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if listed {
		t.Error("an address nobody holds reads as blocklisted")
	}
}

// JPYC has no DOMAIN_SEPARATOR(): the call reverts with nothing to say,
// while name() answers. Read off both JPYC contracts on Polygon on
// 2026-09-20, which is what holds the adapter to this: the separator is
// absent rather than unreadable, and the name is the domain's.
func TestDomainSeparatorAndName_ReadJPYCOnPolygonAsNamedButWithoutASeparator(t *testing.T) {
	t.Parallel()
	n := live(t)
	for _, jpyc := range []string{
		"0xE7C3D8C9a439feDe00D2600032D5dB0Be71C3c29",
		"0x431D5dfF03120AFA4bDf332c61A6e1766eF37BDB",
	} {
		_, err := n.DomainSeparator(t.Context(), jpyc)
		if !errors.Is(err, chain.ErrNoSeparator) {
			t.Errorf("DomainSeparator of %s: %v, want ErrNoSeparator", jpyc, err)
		}
		name, err := n.Name(t.Context(), jpyc)
		if err != nil {
			t.Fatal(err)
		}
		if name != "JPY Coin" {
			t.Errorf("Name of %s = %q, want JPY Coin", jpyc, name)
		}
	}
}
