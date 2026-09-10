package evm

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/sucopay/sucopay/internal/adapter/chain"
)

// implementationSlot is where EIP-1967 keeps the address of the code a proxy
// runs. It sits one below the hash of the slot's own name, where nothing a
// contract declares for itself can land.
const implementationSlot = "0x360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc"

// Kind is how a chain that speaks Ethereum's JSON-RPC is opened. A network
// declares it, and the endpoint it is reached at.
var Kind = chain.Kind{
	Name:      "evm",
	Normalize: Normalize,
	Open:      open,
}

// network reads one chain through one endpoint.
type network struct {
	client *client
}

var _ chain.Chain = (*network)(nil)

// open makes the adapter for one network, on the endpoint its settings name.
func open(settings chain.Settings) (chain.Chain, error) {
	c, err := newClient(settings.RPC)
	if err != nil {
		return nil, err
	}
	return &network{client: c}, nil
}

// Identity is what the chain calls itself, in decimal, which is how a
// configuration document writes one.
func (n *network) Identity(ctx context.Context) (string, error) {
	var id quantity
	if err := n.client.call(ctx, "eth_chainId", []any{}, &id); err != nil {
		return "", err
	}
	return strconv.FormatUint(uint64(id), 10), nil
}

// Head is the newest block, and the newest one the chain says it will not
// replace.
func (n *network) Head(ctx context.Context) (chain.Head, error) {
	latest, err := n.blockBy(ctx, "latest")
	if err != nil {
		return chain.Head{}, err
	}
	final, err := n.blockBy(ctx, "finalized")
	if err != nil {
		return chain.Head{}, noFinalBlock(err)
	}
	return chain.Head{Latest: latest, Final: final}, nil
}

// noFinalBlock is what a provider that will not answer for the finalized block
// leaves behind.
//
// The words are kept and what they were is dropped. A provider that does not
// know the tag refuses it with the codes another provider refuses a span of
// blocks with, and reading that as a span would have the observer ask again for
// less of a chain it cannot read at all. A refusal for the rate of calls is the
// one that keeps its meaning: it says to come back, not that there is nothing
// here to come back to.
func noFinalBlock(err error) error {
	var limited chain.RateLimited
	if errors.As(err, &limited) {
		return err
	}
	return fmt.Errorf("%s: %w", err, chain.ErrNoFinal)
}

// Block is the block at a height.
//
// A block is asked for by height, and one that came back under another one
// would be written down under the height that was asked for. What the observer
// takes as its next position is the block it read here, and so is what an
// operator moving a cursor by hand writes: a provider answering high would
// move a reader past blocks nobody read, and where it lands outlives
// the provider that gave it.
func (n *network) Block(ctx context.Context, height uint64) (chain.Block, error) {
	block, err := n.blockBy(ctx, quantity(height).String())
	if err != nil {
		return chain.Block{}, err
	}
	if block.Height != height {
		return chain.Block{}, fmt.Errorf("eth_getBlockByNumber: asked for %d and answered for %d",
			height, block.Height)
	}
	return block, nil
}

// Implementation is the address EIP-1967 keeps the code a proxy runs at.
//
// Read at the newest block, and not at the block a transfer was in: public
// endpoints keep no state for a block days old, and refuse to read a slot
// there.
func (n *network) Implementation(ctx context.Context, asset string) (string, error) {
	contract, err := parseAddress(asset)
	if err != nil {
		return "", err
	}
	var slot word
	if err := n.client.call(ctx, "eth_getStorageAt", []any{contract.String(), implementationSlot, "latest"}, &slot); err != nil {
		return "", err
	}
	behind, err := account(slot)
	if err != nil {
		return "", fmt.Errorf("eth_getStorageAt: %w", err)
	}
	return behind.String(), nil
}

// blockBy reads the block a height or a tag names, with the transactions in it
// left out: the observer reads the receipts of the keys it recognised, and a
// block carrying its transactions is that answer several hundred times over.
func (n *network) blockBy(ctx context.Context, which string) (chain.Block, error) {
	// A pointer to a pointer: a provider that has no such block answers with
	// null, which reads into a value as the block at height zero.
	var read *header
	if err := n.client.call(ctx, "eth_getBlockByNumber", []any{which, false}, &read); err != nil {
		return chain.Block{}, err
	}
	if read == nil {
		return chain.Block{}, fmt.Errorf("eth_getBlockByNumber: the provider has no block at %s", which)
	}
	return read.block()
}

// header is a block as a provider answers with one. An answer carries some
// twenty fields, and these four are the ones read.
type header struct {
	Number     quantity `json:"number"`
	Hash       hash     `json:"hash"`
	ParentHash hash     `json:"parentHash"`
	Timestamp  quantity `json:"timestamp"`
}

// block is the header as the boundary carries a block.
func (h *header) block() (chain.Block, error) {
	// The hash is what says the block at a height is still the one that was
	// read there, so a header without one identifies nothing.
	if h.Hash == (hash{}) {
		return chain.Block{}, errors.New("eth_getBlockByNumber: the block is named by no hash")
	}
	at, err := stamp(h.Timestamp)
	if err != nil {
		return chain.Block{}, err
	}
	return chain.Block{
		Height: uint64(h.Number),
		Hash:   h.Hash.String(),
		Parent: h.ParentHash.String(),
		Time:   at,
	}, nil
}

// stamp is when a chain says a block was made. A chain counts whole seconds,
// and a count past what a time holds is not one of them.
func stamp(seconds quantity) (time.Time, error) {
	if seconds > math.MaxInt64 {
		return time.Time{}, fmt.Errorf("eth_getBlockByNumber: the block is stamped %s, which is no time", seconds)
	}
	return time.Unix(int64(seconds), 0).UTC(), nil
}
