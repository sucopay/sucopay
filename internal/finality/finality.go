// Package finality decides whether a transfer a round recorded is one the
// chain will keep.
//
// Being in a block and being safe to treat as paid are not the same thing. A
// round writes what it saw as soon as it sees it; this asks again, later, and
// answers what the endpoints make of the same transfer now.
package finality

import (
	"context"
	"errors"
	"fmt"

	"github.com/sucopay/sucopay/internal/adapter/chain"
)

// Recorded is a transfer as an observation holds it: the transaction that
// carried it, and the block it was in when it was seen.
type Recorded struct {
	// Tx is the transaction that carried the transfer.
	Tx string
	// Height and Hash are the block the transaction was in when it was seen.
	// Hash is the block's, not the transaction's.
	Height uint64
	Hash   string
}

// Verdict is what the endpoints make of a recorded transfer.
type Verdict string

const (
	// Final is the transfer where it was recorded, in a block the chain will
	// not replace.
	Final Verdict = "final"
	// Waiting is the transfer where it was recorded, in a block the chain may
	// still replace. Asking again later is what settles it.
	Waiting Verdict = "waiting"
	// Elsewhere is the transfer in a block other than the one it was recorded
	// in. What was recorded is not what the chain holds, and the round that
	// reads the range again is what puts the record right.
	Elsewhere Verdict = "elsewhere"
	// Gone is the transfer nowhere on the chain, at a height the chain will
	// not replace. A transaction that was dropped carries no transfers, and so
	// does one that reverted. Not being there at a height the chain may still
	// replace is Waiting: the block there now can give way to one that
	// carries the transfer.
	Gone Verdict = "gone"
	// Disagreed is the endpoints not saying the same thing. Nothing is settled
	// from it: whoever asked is told that rather than handed one of the
	// answers.
	Disagreed Verdict = "disagreed"
)

// Ask puts a recorded transfer to every endpoint and answers what they all
// say.
//
// Every endpoint has to say the same thing. How many are asked is the caller's
// to decide: a node the operator runs answers for itself and is asked alone,
// and third parties are all asked because no one of them settles anything on
// its own.
//
// They are asked one after another, and the first that differs ends it. A round
// asks about the transfers it recorded, which are few. Each endpoint bounds its
// own call and this adds no bound of its own, so a caller's deadline has to
// hold for as many endpoints as it passes.
//
// Whether the endpoints are separate is the caller's to know. Two that are the
// same provider under two names agree with themselves, and nothing here can
// tell: a chain says what it is, not who is answering for it.
//
// An endpoint that does not answer is an error rather than a verdict. Reading
// silence as one of the answers would let a provider that is down settle a
// payment, or take one away.
func Ask(ctx context.Context, endpoints []chain.Chain, r Recorded) (Verdict, error) {
	if len(endpoints) == 0 {
		return "", errors.New("finality: no endpoint to ask")
	}
	var agreed Verdict
	for i, endpoint := range endpoints {
		said, err := asked(ctx, endpoint, r)
		if err != nil {
			// By its place rather than by where it is: an endpoint carries a
			// credential, and nothing that reads one writes it anywhere.
			return "", fmt.Errorf("finality: endpoint %d: %w", i, err)
		}
		if i > 0 && said != agreed {
			return Disagreed, nil
		}
		agreed = said
	}
	return agreed, nil
}

// asked is what one endpoint makes of a recorded transfer.
//
// The three things the answer turns on are whether the transaction still
// carries the transfer, whether the block it carries it in is the one that was
// recorded, and whether that block is one the chain will not replace. The hash
// is compared as well as the height because another block can take a height:
// the same number with another hash is another history.
func asked(ctx context.Context, endpoint chain.Chain, r Recorded) (Verdict, error) {
	carried, err := endpoint.Receipt(ctx, r.Tx)
	if err != nil && !errors.Is(err, chain.ErrNoTransaction) {
		return "", err
	}
	// A transaction the chain does not hold, or one that is there and carried
	// nothing. One that reverted looks like the second, and so does one whose
	// logs were all of something else; either way the transfer that was
	// recorded is not on this chain. Whether that is settled turns on the
	// height: an endpoint whose final block is below the recorded one may
	// still be handed the block that carries the transfer, and one that is
	// only behind must not be read as one that has dropped it.
	if len(carried) == 0 {
		return settled(ctx, endpoint, r.Height, Gone)
	}
	// One transaction is in one block, so the first transfer says where the
	// transaction is.
	at := carried[0].Block
	if at.Height != r.Height || at.Hash != r.Hash {
		return Elsewhere, nil
	}
	return settled(ctx, endpoint, at.Height, Final)
}

// settled is the verdict once the endpoint's final block has reached the
// height, and Waiting until it has.
func settled(ctx context.Context, endpoint chain.Chain, height uint64, then Verdict) (Verdict, error) {
	head, err := endpoint.Head(ctx)
	if err != nil {
		return "", err
	}
	if head.Final.Height < height {
		return Waiting, nil
	}
	return then, nil
}
