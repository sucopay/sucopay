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

	got, err := finality.Ask(t.Context(), []chain.Chain{c}, r)

	if err != nil {
		t.Fatalf("Ask = %v, want none", err)
	}
	if got != finality.Final {
		t.Errorf("Ask = %q, want %q", got, finality.Final)
	}
}

func TestAsk_AnswersWaitingWhileTheBlockIsNotFinal(t *testing.T) {
	t.Parallel()
	c, r := carrying(t, "tx1")

	got, err := finality.Ask(t.Context(), []chain.Chain{c}, r)

	if err != nil {
		t.Fatalf("Ask = %v, want none", err)
	}
	if got != finality.Waiting {
		t.Errorf("Ask = %q, want %q", got, finality.Waiting)
	}
}

// A block at the recorded height with another hash is another history. The
// height alone would say the transfer is where it was left.
func TestAsk_AnswersElsewhereWhenTheTransferIsInAnotherBlock(t *testing.T) {
	t.Parallel()
	c, r := carrying(t, "tx1")
	c.Reorg(r.Height, "tx1")
	c.Finalize(c.Mine())

	got, err := finality.Ask(t.Context(), []chain.Chain{c}, r)

	if err != nil {
		t.Fatalf("Ask = %v, want none", err)
	}
	if got != finality.Elsewhere {
		t.Errorf("Ask = %q, want %q", got, finality.Elsewhere)
	}
}

// A transaction that is no longer there carries no transfers. A reverted one
// carries none either, which is the same answer for the same reason.
func TestAsk_AnswersGoneWhenTheTransferIsNotThere(t *testing.T) {
	t.Parallel()
	c, r := carrying(t, "tx1")
	c.Reorg(r.Height)
	c.Finalize(c.Mine())

	got, err := finality.Ask(t.Context(), []chain.Chain{c}, r)

	if err != nil {
		t.Fatalf("Ask = %v, want none", err)
	}
	if got != finality.Gone {
		t.Errorf("Ask = %q, want %q", got, finality.Gone)
	}
}

// Two endpoints saying different things settle nothing. Whoever asked is told
// that rather than handed one of the two answers.
func TestAsk_AnswersDisagreedWhenTheEndpointsDoNotSayTheSameThing(t *testing.T) {
	t.Parallel()
	one, r := carrying(t, "tx1")
	one.Finalize(r.Height)
	two, _ := carrying(t, "tx1")

	got, err := finality.Ask(t.Context(), []chain.Chain{one, two}, r)

	if err != nil {
		t.Fatalf("Ask = %v, want none", err)
	}
	if got != finality.Disagreed {
		t.Errorf("Ask = %q, want %q", got, finality.Disagreed)
	}
}

// Every endpoint has to say it. One that says the transfer is final is not
// enough while another has not been asked.
func TestAsk_AnswersFinalOnlyWhenEveryEndpointSaysSo(t *testing.T) {
	t.Parallel()
	one, r := carrying(t, "tx1")
	one.Finalize(r.Height)
	two, _ := carrying(t, "tx1")
	two.Finalize(r.Height)

	got, err := finality.Ask(t.Context(), []chain.Chain{one, two}, r)

	if err != nil {
		t.Fatalf("Ask = %v, want none", err)
	}
	if got != finality.Final {
		t.Errorf("Ask = %q, want %q", got, finality.Final)
	}
}

// An endpoint that does not answer establishes nothing. Reading its silence as
// one of the answers would let a provider that is down settle a payment.
func TestAsk_ReportsAnEndpointThatDoesNotAnswer(t *testing.T) {
	t.Parallel()
	sorry := errors.New("no")
	for what, fail := range map[string]string{"the receipt": "Receipt", "the head": "Head"} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			c, r := carrying(t, "tx1")
			c.Finalize(r.Height)
			c.FailAt(fail, 1, sorry)

			_, err := finality.Ask(t.Context(), []chain.Chain{c}, r)

			if !errors.Is(err, sorry) {
				t.Errorf("Ask = %v, want the endpoint's own error", err)
			}
		})
	}
}

// Nothing to ask is a caller that opened no endpoint, not a payment nothing
// says anything about.
func TestAsk_RefusesToAnswerWithNoEndpoints(t *testing.T) {
	t.Parallel()
	_, r := carrying(t, "tx1")

	_, err := finality.Ask(t.Context(), nil, r)

	if err == nil {
		t.Fatal("an answer was given with nothing asked")
	}
}

// empty is a chain holding a transaction that carried nothing. The chain in
// the process cannot be put in that state: it knows a transaction by the
// transfers in it, and a transaction with none is one it has never seen.
type empty struct{ chain.Chain }

func (empty) Receipt(context.Context, string) ([]chain.Transfer, error) { return nil, nil }

func (empty) Head(context.Context) (chain.Head, error) {
	return chain.Head{Final: chain.Block{Height: 10}}, nil
}

// A transaction that is there and carried nothing is gone as far as a recorded
// transfer is concerned. A reverted one looks like this, and so does one whose
// logs were all of something else.
func TestAsk_AnswersGoneForATransactionThatCarriedNothing(t *testing.T) {
	t.Parallel()

	got, err := finality.Ask(t.Context(), []chain.Chain{empty{}},
		finality.Recorded{Tx: "tx1", Height: 1, Hash: "block1"})

	if err != nil {
		t.Fatalf("Ask = %v, want none", err)
	}
	if got != finality.Gone {
		t.Errorf("Ask = %q, want %q", got, finality.Gone)
	}
}
