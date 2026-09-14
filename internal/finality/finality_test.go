package finality_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sucopay/sucopay/internal/adapter/chain"
	"github.com/sucopay/sucopay/internal/adapter/chain/simulated"
	"github.com/sucopay/sucopay/internal/finality"
)

// carrying is a chain holding one transfer, with the block it went into
// recorded the way an observation holds it.
func carrying(t *testing.T, tx string) (*simulated.Chain, finality.Recorded) {
	t.Helper()
	c := simulated.New()
	c.Send(chain.Transfer{Scheme: "eip3009", Asset: "0xasset", Key: "key",
		Authorizer: "0xsigner", From: "0xsigner", To: "0xmerchant", Value: "1", Tx: tx})
	height := c.Mine()
	block, err := c.Block(t.Context(), height)
	if err != nil {
		t.Fatal(err)
	}
	return c, finality.Recorded{Tx: tx, Height: block.Height, Hash: block.Hash}
}

func TestAsk_AnswersFinalWhenTheTransferIsWhereItWasRecordedAndTheBlockIsFinal(t *testing.T) {
	t.Parallel()
	c, r := carrying(t, "tx1")
	c.Finalize(r.Height)

	got, err := finality.Ask(t.Context(), []chain.Chain{c}, 1, r)

	if err != nil {
		t.Fatalf("Ask = %v, want none", err)
	}
	if got.Verdict != finality.Final {
		t.Errorf("Ask = %q, want %q", got.Verdict, finality.Final)
	}
}

func TestAsk_AnswersWaitingWhileTheBlockIsNotFinal(t *testing.T) {
	t.Parallel()
	c, r := carrying(t, "tx1")

	got, err := finality.Ask(t.Context(), []chain.Chain{c}, 1, r)

	if err != nil {
		t.Fatalf("Ask = %v, want none", err)
	}
	if got.Verdict != finality.Waiting {
		t.Errorf("Ask = %q, want %q", got.Verdict, finality.Waiting)
	}
}

// A block at the recorded height with another hash is another history. The
// height alone would say the transfer is where it was left.
func TestAsk_AnswersElsewhereWhenTheTransferIsInAnotherBlock(t *testing.T) {
	t.Parallel()
	c, r := carrying(t, "tx1")
	c.Reorg(r.Height, "tx1")
	c.Finalize(c.Mine())

	got, err := finality.Ask(t.Context(), []chain.Chain{c}, 1, r)

	if err != nil {
		t.Fatalf("Ask = %v, want none", err)
	}
	if got.Verdict != finality.Elsewhere {
		t.Errorf("Ask = %q, want %q", got.Verdict, finality.Elsewhere)
	}
}

// A transaction that is no longer there carries no transfers. A reverted one
// carries none either, which is the same answer for the same reason.
func TestAsk_AnswersGoneWhenTheTransferIsNotThere(t *testing.T) {
	t.Parallel()
	c, r := carrying(t, "tx1")
	c.Reorg(r.Height)
	c.Finalize(c.Mine())

	got, err := finality.Ask(t.Context(), []chain.Chain{c}, 1, r)

	if err != nil {
		t.Fatalf("Ask = %v, want none", err)
	}
	if got.Verdict != finality.Gone {
		t.Errorf("Ask = %q, want %q", got.Verdict, finality.Gone)
	}
}

// An endpoint whose final block is below the recorded one has not settled that
// height: the block it holds there may still be replaced by one that carries
// the transfer. Gone is for an endpoint that has settled the height and holds
// nothing there.
func TestAsk_AnswersWaitingWhenTheTransferIsNotThereAndTheHeightIsNotFinal(t *testing.T) {
	t.Parallel()
	c, r := carrying(t, "tx1")
	c.Reorg(r.Height)

	got, err := finality.Ask(t.Context(), []chain.Chain{c}, 1, r)

	if err != nil {
		t.Fatalf("Ask = %v, want none", err)
	}
	if got.Verdict != finality.Waiting {
		t.Errorf("Ask = %q, want %q", got.Verdict, finality.Waiting)
	}
}

// Two endpoints saying different things settle nothing. Whoever asked is told
// that rather than handed one of the two answers.
func TestAsk_AnswersDisagreedWhenTheEndpointsDoNotSayTheSameThing(t *testing.T) {
	t.Parallel()
	one, r := carrying(t, "tx1")
	one.Finalize(r.Height)
	two, _ := carrying(t, "tx1")

	got, err := finality.Ask(t.Context(), []chain.Chain{one, two}, 2, r)

	if err != nil {
		t.Fatalf("Ask = %v, want none", err)
	}
	if got.Verdict != finality.Disagreed {
		t.Errorf("Ask = %q, want %q", got.Verdict, finality.Disagreed)
	}
}

// As many as agreement takes have to say it. One that says the transfer is
// final is not enough while a second has not been asked.
func TestAsk_AnswersUnansweredWhenFewerAnswerThanAgreementTakes(t *testing.T) {
	t.Parallel()
	one, r := carrying(t, "tx1")
	one.Finalize(r.Height)

	got, err := finality.Ask(t.Context(), []chain.Chain{one}, 2, r)

	if err != nil {
		t.Fatalf("Ask = %v, want none", err)
	}
	if got.Verdict != finality.Unanswered || got.Answered != 1 {
		t.Errorf("Ask = %+v, want unanswered with the one answer counted", got)
	}
}

// The endpoints past the ones that decided are not asked. A round asks about
// every transfer it recorded, and a third question that changes nothing is a
// third of the cost for nothing.
func TestAsk_DecidesOnTheFirstTwoAnswersThatAgree(t *testing.T) {
	t.Parallel()
	one, r := carrying(t, "tx1")
	one.Finalize(r.Height)
	two, _ := carrying(t, "tx1")
	two.Finalize(r.Height)
	three, _ := carrying(t, "tx1")

	got, err := finality.Ask(t.Context(), []chain.Chain{one, two, three}, 2, r)

	if err != nil {
		t.Fatalf("Ask = %v, want none", err)
	}
	if got.Verdict != finality.Final || got.Agreed != 2 {
		t.Errorf("Ask = %+v, want final on two agreeing", got)
	}
	if calls := three.Calls(); calls["Receipt"] != 0 || calls["Head"] != 0 {
		t.Errorf("the third endpoint was asked %v, want it left alone", calls)
	}
}

// An endpoint that does not answer establishes nothing, and takes nothing
// away: it is skipped, and the next one is asked in its place. Reading its
// silence as one of the answers would let a provider that is down settle a
// payment.
func TestAsk_SkipsAnEndpointThatDoesNotAnswer(t *testing.T) {
	t.Parallel()
	sorry := errors.New("no")
	for what, fail := range map[string]string{"the receipt": "Receipt", "the head": "Head"} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			down, r := carrying(t, "tx1")
			down.Finalize(r.Height)
			down.FailAt(fail, 1, sorry)
			one, _ := carrying(t, "tx1")
			one.Finalize(r.Height)
			two, _ := carrying(t, "tx1")
			two.Finalize(r.Height)

			got, err := finality.Ask(t.Context(), []chain.Chain{down, one, two}, 2, r)

			if err != nil {
				t.Fatalf("Ask = %v, want none", err)
			}
			if got.Verdict != finality.Final || got.Answered != 2 {
				t.Errorf("Ask = %+v, want final on the two that answered", got)
			}
		})
	}
}

// One well-formed answer that differs settles nothing, and no third endpoint
// is asked to break the tie.
func TestAsk_AnswersDisagreedOnOneWellFormedAnswerThatDiffers(t *testing.T) {
	t.Parallel()
	one, r := carrying(t, "tx1")
	one.Finalize(r.Height)
	two, _ := carrying(t, "tx1")
	three, _ := carrying(t, "tx1")
	three.Finalize(r.Height)

	got, err := finality.Ask(t.Context(), []chain.Chain{one, two, three}, 2, r)

	if err != nil {
		t.Fatalf("Ask = %v, want none", err)
	}
	if got.Verdict != finality.Disagreed {
		t.Errorf("Ask = %+v, want disagreed", got)
	}
	if calls := three.Calls(); calls["Receipt"] != 0 {
		t.Errorf("the third endpoint was asked to break the tie: %v", calls)
	}
}

// A context that ended is the one thing that ends the asking with an error:
// an endpoint that did not answer because the caller stopped waiting is not
// one to skip and carry on past.
func TestAsk_StopsWhenTheContextEnds(t *testing.T) {
	t.Parallel()
	c, r := carrying(t, "tx1")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := finality.Ask(ctx, []chain.Chain{c}, 1, r)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Ask = %v, want the context's own error", err)
	}
}

// Nothing to ask is a caller that opened no endpoint, not a payment nothing
// says anything about.
func TestAsk_RefusesToAnswerWithNoEndpoints(t *testing.T) {
	t.Parallel()
	_, r := carrying(t, "tx1")

	_, err := finality.Ask(t.Context(), nil, 1, r)

	if err == nil {
		t.Fatal("an answer was given with nothing asked")
	}
}

// A call the context ends in the middle of is the same end as one it ended
// before, and not a silence to skip past to the next endpoint.
func TestAsk_StopsWhenTheContextEndsDuringACall(t *testing.T) {
	t.Parallel()
	_, r := carrying(t, "tx1")
	ctx, cancel := context.WithCancel(t.Context())
	quitting := ending{cancel: cancel}

	got, err := finality.Ask(ctx, []chain.Chain{quitting}, 1, r)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Ask = %+v, %v; want the context's own error", got, err)
	}
}

// ending is an endpoint whose call ends the caller's context and then fails,
// the way a call that was cut short by its context does.
type ending struct {
	chain.Chain
	cancel context.CancelFunc
}

func (e ending) Receipt(context.Context, string) ([]chain.Transfer, error) {
	e.cancel()
	return nil, context.Canceled
}

// empty is a chain holding a transaction that carried nothing, with its final
// block at a height of the test's choosing. The chain in the process cannot be
// put in that state: it knows a transaction by the transfers in it, and a
// transaction with none is one it has never seen.
type empty struct {
	chain.Chain
	final uint64
}

func (empty) Receipt(context.Context, string) ([]chain.Transfer, error) { return nil, nil }

func (e empty) Head(context.Context) (chain.Head, error) {
	return chain.Head{Final: chain.Block{Height: e.final}}, nil
}

// A transaction that is there and carried nothing is gone as far as a recorded
// transfer is concerned, once the endpoint has settled the height. A reverted
// one looks like this, and so does one whose logs were all of something else.
func TestAsk_AnswersGoneForATransactionThatCarriedNothing(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct {
		final uint64
		want  finality.Verdict
	}{
		"at the height":    {1, finality.Gone},
		"past the height":  {10, finality.Gone},
		"below the height": {0, finality.Waiting},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := finality.Ask(t.Context(), []chain.Chain{empty{final: tt.final}}, 1,
				finality.Recorded{Tx: "tx1", Height: 1, Hash: "block1"})

			if err != nil {
				t.Fatalf("Ask = %v, want none", err)
			}
			if got.Verdict != tt.want {
				t.Errorf("Ask = %q, want %q", got.Verdict, tt.want)
			}
		})
	}
}
