package finality

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sucopay/sucopay/internal/adapter/chain"
	"github.com/sucopay/sucopay/internal/payment"
)

// Network is what deciding one network needs: the endpoints to ask, and how
// long a recorded transfer has to go unfound before it is given up on.
type Network struct {
	// Name is what the configuration document calls the network, which is the
	// name the rows of its transfers carry.
	Name string
	// Endpoints are what is asked. [Ask] holds them to one answer.
	Endpoints []chain.Chain
	// VanishAfter is the rounds in a row the endpoints have to say a transfer
	// is not there before the row is marked as gone. Once is not enough: an
	// endpoint can be reading a state it has not finished replacing, and what
	// is taken back here is a payment's record of money arriving.
	VanishAfter int
	// Wait is how long a payment that has stopped being payable is given to
	// learn whether anything arrives, counted from its deadline. A transfer
	// signed before the deadline can still be carried after it, and how long
	// after is the chain's to decide.
	Wait time.Duration
}

// Worker decides which recorded transfers settled the payments they were
// matched against, one network at a time.
//
// It crosses accounts. What settles a payment is a fact about a chain, and the
// endpoints are asked for the deployment rather than for one merchant; every
// row it writes is written under the account the row came back with.
type Worker struct {
	network Network
	store   *payment.Postgres
	log     *slog.Logger
	now     func() time.Time
	// answers is what the endpoints said about each transfer, and for how many
	// rounds in a row they have said it. A round that ends early leaves the
	// last full round's counts standing, so nothing is given up on because a
	// round was cut short.
	//
	// It belongs to whoever is running the round, and nothing guards it.
	// Reading it from anywhere else, such as something reporting what the
	// worker is making of a network, needs a lock put on it first.
	answers map[recorded]answer
}

// recorded names one transfer the way the row of one is named.
type recorded struct {
	key string
	tx  string
}

// answer is what the endpoints said about a transfer, with the rounds in a row
// they have said it, counting the one it came from.
type answer struct {
	verdict Verdict
	rounds  int
}

// New opens a worker over a network. The clock is the caller's, so that a test
// can say when a round ran.
func New(n Network, store *payment.Postgres, log *slog.Logger, now func() time.Time) *Worker {
	return &Worker{network: n, store: store, log: log, now: now, answers: map[recorded]answer{}}
}

// round asks the endpoints about every recorded transfer that would settle a
// payment, and writes what their answers settle.
//
// An endpoint that does not answer ends the round where it stands. The
// transfer it was asked about is left where it was and so is everything after
// it: a reader that cannot read decides nothing, and the round comes again.
func (w *Worker) round(ctx context.Context) error {
	if w.network.VanishAfter < 1 {
		return fmt.Errorf("%s: a transfer is set to be given up on after %d rounds",
			w.network.Name, w.network.VanishAfter)
	}
	if w.network.Wait < 1 {
		return fmt.Errorf("%s: a payment is set to wait %s for what it is owed",
			w.network.Name, w.network.Wait)
	}
	candidates, err := w.store.Candidates(ctx, payment.Network(w.network.Name))
	if err != nil {
		return err
	}
	said := make(map[recorded]answer, len(candidates))
	for _, c := range candidates {
		verdict, err := Ask(ctx, w.network.Endpoints,
			Recorded{Tx: c.Tx, Height: c.BlockHeight, Hash: c.BlockHash})
		if err != nil {
			return err
		}
		at := recorded{key: c.Key, tx: c.Tx}
		rounds := 1
		if before, told := w.answers[at]; told && before.verdict == verdict {
			rounds = before.rounds + 1
		}
		said[at] = answer{verdict: verdict, rounds: rounds}
		if err := w.decided(ctx, c, said[at]); err != nil {
			return err
		}
	}
	w.answers = said
	return w.swept(ctx)
}

// swept moves the payments the clock has passed by: those that have reached
// their deadline stop being payable, and those that waited out the wait with
// nothing that could pay them are given up on.
//
// It runs after the deciding, so a transfer that settles a payment in this
// round settles it before the clock is read. Reaching the end of the wait is
// not what ends a payment; having nothing left that could pay it is.
func (w *Worker) swept(ctx context.Context) error {
	network := payment.Network(w.network.Name)
	now := w.now()

	reached, err := w.store.Overdue(ctx, network, now)
	if err != nil {
		return err
	}
	for _, one := range reached {
		if err := one.Payment.AwaitFinality(now); err != nil {
			return err
		}
		if err := w.told(ctx, one.Account, one.Payment, one.PaymentAt,
			"payment awaiting finality"); err != nil {
			return err
		}
	}

	over, err := w.store.Unsettled(ctx, network, now.Add(-w.network.Wait))
	if err != nil {
		return err
	}
	for _, one := range over {
		if err := one.Payment.Expire(); err != nil {
			return err
		}
		if err := w.told(ctx, one.Account, one.Payment, one.PaymentAt,
			"payment expired"); err != nil {
			return err
		}
	}
	return nil
}

// told writes a payment's new state and the event that says so together, so a
// merchant is told exactly what was kept, and writes a line saying what moved.
// The network and the payment are on every line; more is the caller's to add.
//
// A payment that moved between being read and this is left where it is. The
// row is read again next round, and by then it says what the writer that won
// made of it.
func (w *Worker) told(ctx context.Context, account payment.AccountID, p *payment.Payment,
	at payment.Revision, said string, fields ...any) error {
	event, err := payment.Announce(p)
	if err != nil {
		return err
	}
	if err := w.store.Save(ctx, account, p, at, event); err != nil {
		if errors.Is(err, payment.ErrStale) {
			return nil
		}
		return err
	}
	w.log.Info(said, append([]any{"network", w.network.Name, "payment", p.ID()}, fields...)...)
	return nil
}

// decided writes what one answer means for the transfer it was about.
func (w *Worker) decided(ctx context.Context, c payment.Candidate, said answer) error {
	switch said.verdict {
	case Final:
		return w.pay(ctx, c, said)
	case Gone:
		if said.rounds < w.network.VanishAfter {
			return nil
		}
		if err := w.store.Vanish(ctx, payment.Network(w.network.Name),
			c.Key, c.Tx, w.now()); err != nil {
			return err
		}
		w.log.Warn("transfer vanished", "network", w.network.Name, "payment", c.Payment.ID(),
			"tx", c.Tx, "height", c.BlockHeight, "rounds", said.rounds)
	case Elsewhere:
		// Said once, on the way into the state. A transfer the chain holds
		// somewhere other than where it was recorded stays that way until the
		// observer reads the range again, and a line each round would bury
		// whatever else the log has to say.
		if said.rounds == 1 {
			w.log.Warn("transfer recorded in another block", "network", w.network.Name,
				"payment", c.Payment.ID(), "tx", c.Tx, "height", c.BlockHeight)
		}
	case Disagreed:
		if said.rounds == 1 {
			w.log.Warn("the endpoints disagree about a transfer", "network", w.network.Name,
				"payment", c.Payment.ID(), "tx", c.Tx, "height", c.BlockHeight)
		}
	}
	// Waiting, and anything that is not one of these: the payment stays where
	// it is. Settling is the move that cannot be taken back.
	return nil
}

// pay settles a payment on a transfer the endpoints call final. The payment's
// new state and the event that says so are written together, so a merchant is
// told exactly what was kept.
func (w *Worker) pay(ctx context.Context, c payment.Candidate, said answer) error {
	if err := c.Payment.Succeed(); err != nil {
		// What the payment says arrived is less than it asked for. The row
		// matched, so another transfer put a shorter amount there first, and
		// a payment holds one arrival.
		if said.rounds == 1 {
			w.log.Warn("payment not settled by a final transfer", "network", w.network.Name,
				"payment", c.Payment.ID(), "tx", c.Tx, "error", err)
		}
		return nil
	}
	// Two transfers can pay one payment. The first of them settles it, and the
	// second finds it moved, which [Worker.told] passes over.
	return w.told(ctx, c.Account, c.Payment, c.PaymentAt, "payment succeeded",
		"tx", c.Tx, "height", c.BlockHeight)
}
