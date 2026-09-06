package chain

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Chain is what the observer reads a network through. An adapter implements it
// for one kind of chain.
//
// An adapter classifies what a provider answers into [ErrTooWide], [ErrNoFinal]
// and [RateLimited] where one of them fits, and wraps rather than replaces
// them: the observer tells them apart with errors.Is and errors.As. Any other
// error is the call failing, and the observer tries again next time round.
type Chain interface {
	// Identity is what the chain says it is, written the way a configuration
	// document writes it, so that the two can be compared.
	Identity(ctx context.Context) (string, error)
	// Head is the newest block, and the newest block the chain will not
	// replace.
	Head(ctx context.Context) (Head, error)
	// Block is the block at a height, for checking that a block remembered at
	// that height is the one the chain has there now.
	Block(ctx context.Context, height uint64) (Block, error)
	// Keys are the keys the assets consumed in the blocks from first to last,
	// both included, each with the transaction that consumed it, and the
	// assets whose implementation changed in those blocks. Assets are
	// references, as [Kind.Normalize] writes them.
	Keys(ctx context.Context, first, last uint64, assets []string) (Scan, error)
	// Receipt is every transfer one transaction carried, in the order it
	// carried them. Reading the same transaction twice gives the same
	// transfers in the same order.
	Receipt(ctx context.Context, tx string) ([]Transfer, error)
	// Implementation identifies the code behind an asset, as an opaque string:
	// a different value means a different implementation, and the value is
	// read for nothing else. A chain whose assets cannot change their code
	// returns "".
	Implementation(ctx context.Context, asset string) (string, error)
}

var (
	// ErrTooWide reports that the provider refused the span of blocks asked
	// for, or that its answer hit a limit. The observer asks for a narrower
	// span.
	ErrTooWide = errors.New("chain: span too wide")
	// ErrNoFinal reports that the provider does not say which block is final.
	// The observer does not observe through it.
	ErrNoFinal = errors.New("chain: no final block")
)

// RateLimited reports that the provider refused the call for the rate of
// calls. It is found with errors.As.
type RateLimited struct {
	// RetryAfter is how long the provider asked the caller to wait, or zero
	// when it did not say.
	RetryAfter time.Duration
}

// Error names the refusal, and the wait when there is one.
func (e RateLimited) Error() string {
	if e.RetryAfter == 0 {
		return "chain: rate limited"
	}
	return fmt.Sprintf("chain: rate limited, retry after %s", e.RetryAfter)
}

// Block identifies one block of a chain.
type Block struct {
	// Height is the block's place in the chain, counted from its first block.
	Height uint64
	// Hash identifies the block's content. Two blocks at one height with
	// different hashes are different blocks.
	Hash string
	// Parent is the hash of the block before this one.
	Parent string
	// Time is when the chain says the block was made.
	Time time.Time
}

// Head is where a chain stands.
type Head struct {
	// Latest is the chain's newest block.
	Latest Block
	// Final is the newest block the chain will not replace. What final means
	// is the adapter's to decide, and everything at or below it stays.
	Final Block
}

// Consumed is one key an asset consumed.
type Consumed struct {
	// Key is the key, written as [Kind.Normalize] writes one.
	Key string
	// Tx is the transaction that consumed it.
	Tx string
}

// Scan is what [Chain.Keys] found in a span of blocks.
type Scan struct {
	// Consumed are the keys consumed in the span, each with its transaction.
	Consumed []Consumed
	// Changed are the references of the assets whose implementation changed
	// in the span.
	Changed []string
}

// Transfer is one transfer of an asset as a transaction carried it.
type Transfer struct {
	// Scheme names the way the transfer was authorised, in the word the
	// observer's rules use for it.
	Scheme string
	// Asset is the reference of the asset transferred.
	Asset string
	// Key is what the authorisation consumed, written the way the observer
	// writes the keys it issues, so that the two compare as strings.
	Key string
	// Authorizer is the account that authorised the transfer.
	Authorizer string
	// From is the account the asset left.
	From string
	// To is the account the asset went to.
	To string
	// Value is the amount in the asset's smallest unit, as decimal digits.
	Value string
	// Tx is the transaction that carried the transfer.
	Tx string
	// Position is the transfer's place among the transfers of its
	// transaction, counted from zero.
	Position int
	// Block is the block the transaction is in.
	Block Block
}
