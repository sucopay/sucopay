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
	// Unanswered is fewer endpoints answering than agreement takes. Nothing
	// is settled from it either; asking again later is what settles it.
	Unanswered Verdict = "unanswered"
)

// Answers is what the endpoints made of a recorded transfer: the verdict, how
// many gave a well-formed answer, how many of those the verdict rests on, and
// the places in the list of the ones that did not answer. Silent is for a
// caller with more questions to leave those out of the next one.
type Answers struct {
	Verdict  Verdict
	Answered int
	Agreed   int
	Silent   []int
}

// Ask puts a recorded transfer to the endpoints in order until as many as
// agreement takes have said the same thing, and answers that.
//
// How many is the caller's to decide: a node the operator runs answers for
// itself and one is enough, and third parties are held to two because no one
// of them settles anything on its own. The endpoints past the ones that
// decided are not asked.
//
// An endpoint that does not answer is skipped and not counted, whatever kept
// it from answering: silence settles nothing, and it takes nothing away
// either. What ends the asking is the caller's context. One well-formed
// answer that differs from the others is Disagreed, and no further endpoint
// is asked to break the tie: what is settled from that is nothing. Fewer
// well-formed answers than agreement takes is Unanswered, with the count.
//
// Each endpoint bounds its own call and this adds no bound of its own, so a
// caller's deadline has to hold for as many endpoints as it passes.
//
// Whether the endpoints are separate is the caller's to know. Two that are the
// same provider under two names agree with themselves, and nothing here can
// tell: a chain says what it is, not who is answering for it.
func Ask(ctx context.Context, endpoints []chain.Chain, need int, r Recorded) (Answers, error) {
	if need < 1 {
		return Answers{}, fmt.Errorf("finality: %d endpoints have to agree", need)
	}
	if len(endpoints) == 0 {
		return Answers{}, errors.New("finality: no endpoint to ask")
	}
	var a Answers
	for i, endpoint := range endpoints {
		if ctx.Err() != nil {
			return Answers{}, fmt.Errorf("finality: %w", ctx.Err())
		}
		said, err := asked(ctx, endpoint, r)
		if err != nil {
			// Skipped unless it was the caller who stopped waiting, which is
			// the same end as the check above, met part way through a call.
			if ctx.Err() != nil {
				return Answers{}, fmt.Errorf("finality: %w", ctx.Err())
			}
			a.Silent = append(a.Silent, i)
			continue
		}
		a.Answered++
		if a.Answered == 1 {
			a.Verdict = said
		} else if said != a.Verdict {
			return Answers{Verdict: Disagreed, Answered: a.Answered, Silent: a.Silent}, nil
		}
		a.Agreed++
		if a.Agreed == need {
			return a, nil
		}
	}
	a.Verdict = Unanswered
	return a, nil
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
