package simulated_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/sucopay/sucopay/internal/adapter/chain"
	"github.com/sucopay/sucopay/internal/adapter/chain/simulated"
)

func TestHead_FollowsWhatWasMinedAndFinalized(t *testing.T) {
	t.Parallel()
	c := simulated.New()
	for range 3 {
		c.Mine()
	}
	head, err := c.Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if head.Latest.Height != 3 {
		t.Errorf("Latest is at %d, want 3", head.Latest.Height)
	}
	if head.Final.Height != 0 {
		t.Errorf("Final is at %d, want 0 before Finalize", head.Final.Height)
	}
	c.Finalize(1)
	head, err = c.Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if head.Final.Height != 1 {
		t.Errorf("Final is at %d, want 1", head.Final.Height)
	}
	if head.Latest.Parent == "" || head.Latest.Parent == head.Latest.Hash {
		t.Errorf("Latest names %q as its parent and %q as itself", head.Latest.Parent, head.Latest.Hash)
	}
}

func TestMine_ReturnsTheHeightItPutTheBlockAt(t *testing.T) {
	t.Parallel()
	c := simulated.New()
	for want := uint64(1); want <= 3; want++ {
		if got := c.Mine(); got != want {
			t.Errorf("Mine() = %d, want %d", got, want)
		}
	}
}

func TestKeys_GiveTheKeyAndTxOfEverythingSent(t *testing.T) {
	t.Parallel()
	c := simulated.New()
	c.Send(chain.Transfer{Scheme: "eip3009", Asset: "jpyc", Key: "nonce-1", From: "payer", To: "shop", Value: "1200"})
	height := c.Mine()

	scan, err := c.Keys(context.Background(), height, height, []string{"jpyc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Consumed) != 1 {
		t.Fatalf("Keys found %d keys, want 1", len(scan.Consumed))
	}
	if scan.Consumed[0].Key != "nonce-1" {
		t.Errorf("key is %q, want %q", scan.Consumed[0].Key, "nonce-1")
	}
	if len(scan.Changed) != 0 {
		t.Errorf("Changed holds %q, and nothing here can be replaced", scan.Changed)
	}

	transfers, err := c.Receipt(context.Background(), scan.Consumed[0].Tx)
	if err != nil {
		t.Fatal(err)
	}
	if len(transfers) != 1 {
		t.Fatalf("Receipt gave %d transfers, want 1", len(transfers))
	}
	got := transfers[0]
	if got.Key != "nonce-1" || got.Value != "1200" || got.To != "shop" {
		t.Errorf("Receipt gave %+v, want the transfer that was sent", got)
	}
	if got.Tx != scan.Consumed[0].Tx {
		t.Errorf("the transfer names tx %q, and Keys named %q", got.Tx, scan.Consumed[0].Tx)
	}
	if got.Block.Height != height {
		t.Errorf("the transfer is in block %d, want %d", got.Block.Height, height)
	}
}

func TestKeys_LeaveOutAnAssetNobodyAskedAbout(t *testing.T) {
	t.Parallel()
	c := simulated.New()
	c.Send(chain.Transfer{Asset: "jpyc", Key: "nonce-1"})
	height := c.Mine()
	cases := []struct {
		name   string
		assets []string
		want   int
	}{
		{"the asset that was sent", []string{"jpyc"}, 1},
		{"another asset", []string{"usdc"}, 0},
		{"no asset at all", nil, 0},
		{"both", []string{"usdc", "jpyc"}, 1},
	}
	for _, c2 := range cases {
		t.Run(c2.name, func(t *testing.T) {
			scan, err := c.Keys(context.Background(), height, height, c2.assets)
			if err != nil {
				t.Fatal(err)
			}
			if len(scan.Consumed) != c2.want {
				t.Errorf("Keys found %d keys, want %d", len(scan.Consumed), c2.want)
			}
		})
	}
}

func TestKeys_ReadOnlyTheBlocksAsked(t *testing.T) {
	t.Parallel()
	c := simulated.New()
	c.Send(chain.Transfer{Asset: "jpyc", Key: "below"})
	c.Mine()
	c.Send(chain.Transfer{Asset: "jpyc", Key: "asked"})
	asked := c.Mine()
	c.Send(chain.Transfer{Asset: "jpyc", Key: "above"})
	c.Mine()

	scan, err := c.Keys(context.Background(), asked, asked, []string{"jpyc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Consumed) != 1 || scan.Consumed[0].Key != "asked" {
		t.Errorf("Keys(%d, %d) found %v, want the key of that block alone", asked, asked, scan.Consumed)
	}
}

func TestReorg_PutsAKeptTransferInABlockOfAnotherHash(t *testing.T) {
	t.Parallel()
	c := simulated.New()
	c.Send(chain.Transfer{Asset: "jpyc", Key: "kept"})
	c.Send(chain.Transfer{Asset: "jpyc", Key: "dropped"})
	height := c.Mine()

	scan, err := c.Keys(context.Background(), height, height, []string{"jpyc"})
	if err != nil {
		t.Fatal(err)
	}
	txOf := map[string]string{}
	for _, consumed := range scan.Consumed {
		txOf[consumed.Key] = consumed.Tx
	}
	before, err := c.Block(context.Background(), height)
	if err != nil {
		t.Fatal(err)
	}

	c.Reorg(height, txOf["kept"])

	after, err := c.Block(context.Background(), height)
	if err != nil {
		t.Fatal(err)
	}
	if after.Hash == before.Hash {
		t.Errorf("block %d still hashes to %q", height, before.Hash)
	}
	scan, err = c.Keys(context.Background(), height, height, []string{"jpyc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Consumed) != 1 || scan.Consumed[0].Key != "kept" {
		t.Fatalf("Keys found %v, want the kept key alone", scan.Consumed)
	}
	transfers, err := c.Receipt(context.Background(), txOf["kept"])
	if err != nil {
		t.Fatal(err)
	}
	if transfers[0].Block.Hash != after.Hash {
		t.Errorf("the kept transfer is in block %q, want %q", transfers[0].Block.Hash, after.Hash)
	}
	if _, err := c.Receipt(context.Background(), txOf["dropped"]); err == nil {
		t.Error("the dropped transaction still has a receipt")
	}
}

func TestFail_IsSpentOnTheNextCall(t *testing.T) {
	t.Parallel()
	c := simulated.New()
	c.Fail(chain.ErrTooWide)
	if _, err := c.Keys(context.Background(), 0, 0, nil); !errors.Is(err, chain.ErrTooWide) {
		t.Errorf("Keys gave %v, want %v", err, chain.ErrTooWide)
	}
	if _, err := c.Keys(context.Background(), 0, 0, nil); err != nil {
		t.Errorf("the call after the failing one gave %v", err)
	}
}

func TestCalls_CountsEveryCallAndHandsBackACopy(t *testing.T) {
	t.Parallel()
	c := simulated.New()
	for range 2 {
		if _, err := c.Block(context.Background(), 0); err != nil {
			t.Fatal(err)
		}
	}
	c.Fail(errors.New("simulated: nothing answers"))
	if _, err := c.Head(context.Background()); err == nil {
		t.Fatal("Head answered a chain that was told to fail")
	}
	calls := c.Calls()
	if calls["Block"] != 2 {
		t.Errorf("Block was called %d times, want 2", calls["Block"])
	}
	if calls["Head"] != 1 {
		t.Errorf("Head was called %d times, want 1: a failed call is a call", calls["Head"])
	}
	calls["Block"] = 99
	if again := c.Calls()["Block"]; again != 2 {
		t.Errorf("writing to the map Calls gave back changed the count to %d", again)
	}
}

func TestIdentity_IsTheNameOfTheKind(t *testing.T) {
	t.Parallel()
	got, err := simulated.New().Identity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "simulated" {
		t.Errorf("Identity() = %q, want %q", got, "simulated")
	}
}

func TestImplementation_IsEmptyBecauseNothingHereIsReplaceable(t *testing.T) {
	t.Parallel()
	got, err := simulated.New().Implementation(context.Background(), "jpyc")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("Implementation() = %q, want the empty string", got)
	}
}

func TestChain_TakesBlocksWhileItIsRead(t *testing.T) {
	t.Parallel()
	c := simulated.New()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 50 {
			c.Send(chain.Transfer{Asset: "jpyc", Key: "nonce"})
			c.Mine()
		}
	}()
	go func() {
		defer wg.Done()
		for range 50 {
			if _, err := c.Head(context.Background()); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	wg.Wait()
}

// The guards below are for a test that asks for a chain that could not
// exist. Each stops where the mistake is, rather than leaving a chain that
// answers questions wrongly from then on.
func TestReorg_RefusesWhatNoChainWouldDo(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		do   func(c *simulated.Chain)
	}{
		{"drop the block below every other", func(c *simulated.Chain) { c.Reorg(0) }},
		{"drop a block that was never made", func(c *simulated.Chain) { c.Reorg(9) }},
		{"drop the final block", func(c *simulated.Chain) { c.Finalize(2); c.Reorg(2) }},
		{"keep a transaction the dropped blocks do not hold", func(c *simulated.Chain) { c.Reorg(2, "tx9") }},
	}
	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			c := simulated.New()
			c.Send(chain.Transfer{Asset: "jpyc", Key: "nonce"})
			c.Mine()
			c.Mine()
			refuses(t, func() { one.do(c) })
		})
	}
}

func TestFinalize_RefusesAHeightNoChainWouldFinalize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		do   func(c *simulated.Chain)
	}{
		{"a height with no block", func(c *simulated.Chain) { c.Finalize(9) }},
		{"a height under the final block", func(c *simulated.Chain) { c.Finalize(2); c.Finalize(1) }},
	}
	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			c := simulated.New()
			c.Mine()
			c.Mine()
			refuses(t, func() { one.do(c) })
		})
	}
}

func TestBlock_HasNothingAboveTheLatest(t *testing.T) {
	t.Parallel()
	c := simulated.New()
	height := c.Mine()
	if _, err := c.Block(context.Background(), height+1); err == nil {
		t.Errorf("Block(%d) answered on a chain whose latest block is %d", height+1, height)
	}
}

// refuses runs a call the chain is to refuse and reads what it refused with.
// A refusal stops the caller where the mistake is, so a test takes it back
// through recover.
func refuses(t *testing.T, call func()) {
	t.Helper()
	defer func() {
		stopped := recover()
		refused, ok := stopped.(string)
		if !ok {
			t.Errorf("it went through, or stopped on something other than a refusal: %v", stopped)
			return
		}
		if !strings.HasPrefix(refused, "simulated: ") {
			t.Errorf("it stopped on %q, which is not this package saying what it refused", refused)
		}
	}()
	call()
}
