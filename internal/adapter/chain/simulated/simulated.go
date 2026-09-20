// Package simulated is a chain inside the process. A test puts blocks and
// transfers into it and reads them back through the boundary every adapter
// implements, so that code above the boundary can be exercised without a
// network.
//
// Nothing here blocks, so no call watches its context. A chain is safe for a
// test to add to while the code under test reads it.
package simulated

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/sucopay/sucopay/internal/adapter/chain"
)

// Kind is how a simulated chain is opened. It needs no settings: there is
// nothing to reach and nothing to identify.
var Kind = chain.Kind{
	Name:      "simulated",
	Normalize: func(reference string) (string, error) { return reference, nil },
	Open:      func(chain.Settings) (chain.Chain, error) { return New(), nil },
}

// origin is where the clock of every simulated chain starts. A block is a
// second after the one made before it, whatever height either sits at.
var origin = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

// Chain is a chain in memory. The zero value is not one; call [New].
type Chain struct {
	mu sync.Mutex
	// blocks are the chain itself, indexed by height.
	blocks []block
	// staged are the transfers waiting for the next block.
	staged []chain.Transfer
	// changing are the assets whose code is replaced in the next block.
	changing []string
	// final is the height Finalize was last given.
	final uint64
	// made counts the blocks ever made, so that two blocks at one height
	// after a reorg have different hashes.
	made int
	// sent counts the transfers ever sent, for their transaction references.
	sent int
	// pending is what a call fails with, and which call it waits for.
	pending failure
	// calls counts the calls to each method of the interface, by name.
	calls map[string]int
	// paused are the assets whose issuer has stopped every transfer, and
	// blocklisted the accounts each asset's issuer refuses transfers of.
	paused      map[string]bool
	blocklisted map[string]map[string]bool
}

// failure is a call made to fail. An empty method is the next call whatever it
// is; a method waits for that method's own count to reach at.
type failure struct {
	method string
	at     int
	err    error
}

// block is one block and what it carries.
type block struct {
	block     chain.Block
	transfers []chain.Transfer
	// changed are the assets whose code was replaced in this block.
	changed []string
}

var _ chain.Chain = (*Chain)(nil)

// New is a chain holding one block, at height zero, which is also its final
// block. A chain never lacks one, so a test that wants [chain.ErrNoFinal]
// hands it to [Chain.Fail].
func New() *Chain {
	c := &Chain{calls: map[string]int{}, paused: map[string]bool{}, blocklisted: map[string]map[string]bool{}}
	c.mine(nil)
	return c
}

// Mine puts the transfers sent since the last block into a new one and
// returns its height.
func (c *Chain) Mine() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	staged := c.staged
	c.staged = nil
	return c.mine(staged)
}

// Send holds a transfer for the next block, under a transaction reference of
// its own. What the caller wrote in Tx, Position and Block is replaced.
func (c *Chain) Send(t chain.Transfer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent++
	t.Tx = fmt.Sprintf("tx%d", c.sent)
	t.Position = 0
	c.staged = append(c.staged, t)
}

// Upgrade replaces the code behind an asset from the next block on, the way an
// upgrade of a proxy does: the block carries the fact, and a scan over it says
// the asset changed. What [Chain.Implementation] answers does not move, because
// nothing here stands in front of anything else to begin with.
func (c *Chain) Upgrade(asset string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.changing = append(c.changing, asset)
}

// Pause stops every transfer of an asset, the way an issuer does with the
// whole token. What [Chain.Paused] answers for it is true until [Chain.Unpause].
func (c *Chain) Pause(asset string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paused[asset] = true
}

// Unpause lets an asset's transfers through again.
func (c *Chain) Unpause(asset string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.paused, asset)
}

// Blocklist refuses transfers of an asset from or to an account, the way an
// issuer does to one account. Nothing here takes an account off it again.
func (c *Chain) Blocklist(asset, account string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.blocklisted[asset] == nil {
		c.blocklisted[asset] = map[string]bool{}
	}
	c.blocklisted[asset][account] = true
}

// Finalize makes the block at a height the final one. Every block at or below
// it stays where it is: [Chain.Reorg] refuses to drop one, and finality only
// moves up.
func (c *Chain) Finalize(height uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if height >= uint64(len(c.blocks)) {
		panic(fmt.Sprintf("simulated: Finalize(%d) on a chain whose latest block is %d", height, len(c.blocks)-1))
	}
	if height < c.final {
		panic(fmt.Sprintf("simulated: Finalize(%d) under the final block at %d", height, c.final))
	}
	c.final = height
}

// Reorg drops the blocks from a height upwards and puts one block in their
// place, holding the transfers of the transactions named, in the order they
// were sent. The new block hashes to something no other block does, so a
// reader that remembered the old one sees it gone. Transactions of the
// dropped blocks that are not named are gone with them.
func (c *Chain) Reorg(from uint64, keep ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if from >= uint64(len(c.blocks)) {
		panic(fmt.Sprintf("simulated: Reorg(%d) on a chain whose latest block is %d", from, len(c.blocks)-1))
	}
	if from <= c.final {
		panic(fmt.Sprintf("simulated: Reorg(%d) would drop the final block at %d", from, c.final))
	}
	var kept []chain.Transfer
	for _, dropped := range c.blocks[from:] {
		for _, t := range dropped.transfers {
			if slices.Contains(keep, t.Tx) {
				kept = append(kept, t)
			}
		}
	}
	if len(kept) != len(keep) {
		panic(fmt.Sprintf("simulated: Reorg(%d) is to keep %q, and the blocks it drops hold %d of them", from, keep, len(kept)))
	}
	c.blocks = c.blocks[:from]
	c.mine(kept)
}

// Fail makes the next call fail with an error. The call is counted, and the
// call after it answers as usual.
func (c *Chain) Fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = failure{err: err}
}

// FailAt makes one method's nth call fail, counting that method's calls from
// now. A caller that reads a chain calls some methods more than once in a row,
// and which of those fails is what the reading is being tested on.
func (c *Chain) FailAt(method string, nth int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = failure{method: method, at: c.calls[method] + nth, err: err}
}

// Calls are how many times each method of the interface has been called, by
// method name. The map is the caller's to keep.
func (c *Chain) Calls() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	calls := make(map[string]int, len(c.calls))
	for name, count := range c.calls {
		calls[name] = count
	}
	return calls
}

// Identity is the name of the kind. A simulated chain has nothing else to
// call itself.
func (c *Chain) Identity(_ context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.enter("Identity"); err != nil {
		return "", err
	}
	return Kind.Name, nil
}

// Head is the newest block and the one Finalize last named.
func (c *Chain) Head(_ context.Context) (chain.Head, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.enter("Head"); err != nil {
		return chain.Head{}, err
	}
	return chain.Head{Latest: c.blocks[len(c.blocks)-1].block, Final: c.blocks[c.final].block}, nil
}

// Block is the block at a height.
func (c *Chain) Block(_ context.Context, height uint64) (chain.Block, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.enter("Block"); err != nil {
		return chain.Block{}, err
	}
	if height >= uint64(len(c.blocks)) {
		return chain.Block{}, fmt.Errorf("simulated: no block at height %d", height)
	}
	return c.blocks[height].block, nil
}

// Keys are the keys the assets asked about consumed in a span of blocks. A
// simulated asset cannot be replaced, so nothing is ever reported as changed.
func (c *Chain) Keys(_ context.Context, first, last uint64, assets []string) (chain.Scan, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.enter("Keys"); err != nil {
		return chain.Scan{}, err
	}
	var scan chain.Scan
	for height := first; height <= last && height < uint64(len(c.blocks)); height++ {
		for _, t := range c.blocks[height].transfers {
			if slices.Contains(assets, t.Asset) {
				scan.Consumed = append(scan.Consumed, chain.Consumed{Key: t.Key, Tx: t.Tx})
			}
		}
		for _, asset := range c.blocks[height].changed {
			if slices.Contains(assets, asset) && !slices.Contains(scan.Changed, asset) {
				scan.Changed = append(scan.Changed, asset)
			}
		}
	}
	return scan, nil
}

// Receipt is the transfers one transaction carried. A transaction dropped by
// a reorg has none, the way a chain has no receipt for one it does not hold.
func (c *Chain) Receipt(_ context.Context, tx string) ([]chain.Transfer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.enter("Receipt"); err != nil {
		return nil, err
	}
	for _, b := range c.blocks {
		var transfers []chain.Transfer
		for _, t := range b.transfers {
			if t.Tx == tx {
				transfers = append(transfers, t)
			}
		}
		if len(transfers) > 0 {
			return transfers, nil
		}
	}
	return nil, fmt.Errorf("simulated: no transaction %s: %w", tx, chain.ErrNoTransaction)
}

// Name is the reference itself. A simulated asset has no contract to call
// itself anything, so it goes by what the test called it.
func (c *Chain) Name(_ context.Context, asset string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.enter("Name"); err != nil {
		return "", err
	}
	return asset, nil
}

// DomainSeparator is nothing: a simulated asset is signed under no
// domain, and a document's domain for one has nothing to be checked
// against.
func (c *Chain) DomainSeparator(_ context.Context, _ string) ([32]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.enter("DomainSeparator"); err != nil {
		return [32]byte{}, err
	}
	return [32]byte{}, chain.ErrNoDomain
}

// Paused is whether [Chain.Pause] was called for the asset. False until it
// is: a simulated issuer has stopped nothing unless a test says so.
func (c *Chain) Paused(_ context.Context, asset string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.enter("Paused"); err != nil {
		return false, err
	}
	return c.paused[asset], nil
}

// Blocklisted is whether [Chain.Blocklist] was called for the asset and the
// account.
func (c *Chain) Blocklisted(_ context.Context, asset, account string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.enter("Blocklisted"); err != nil {
		return false, err
	}
	return c.blocklisted[asset][account], nil
}

// Implementation is empty. Nothing here stands in front of anything else.
func (c *Chain) Implementation(_ context.Context, _ string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.enter("Implementation"); err != nil {
		return "", err
	}
	return "", nil
}

// enter counts a call and takes the error left for it by [Chain.Fail]. The
// caller holds the lock.
func (c *Chain) enter(method string) error {
	c.calls[method]++
	waiting := c.pending
	if waiting.err == nil {
		return nil
	}
	if waiting.method != "" && (waiting.method != method || c.calls[method] != waiting.at) {
		return nil
	}
	c.pending = failure{}
	return waiting.err
}

// mine adds a block holding the transfers, and returns its height. The caller
// holds the lock.
func (c *Chain) mine(transfers []chain.Transfer) uint64 {
	c.made++
	height := uint64(len(c.blocks))
	var parent string
	if height > 0 {
		parent = c.blocks[height-1].block.Hash
	}
	made := chain.Block{
		Height: height,
		Hash:   fmt.Sprintf("block%d", c.made),
		Parent: parent,
		Time:   origin.Add(time.Duration(c.made) * time.Second),
	}
	for i := range transfers {
		transfers[i].Block = made
	}
	changed := c.changing
	c.changing = nil
	c.blocks = append(c.blocks, block{block: made, transfers: transfers, changed: changed})
	return height
}
