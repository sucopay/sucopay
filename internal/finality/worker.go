package finality

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sucopay/sucopay/internal/adapter/chain"
	"github.com/sucopay/sucopay/internal/invisible"
	"github.com/sucopay/sucopay/internal/payment"
)

// maxErrorBytes bounds what somebody else's failure can put in a line.
const maxErrorBytes = 512

// shown renders an error for a log line. What reaches one of these carries the
// text a provider wrote, nothing bounds how long that is, and slog's JSON
// handler, which is what a deployment writes with, passes a zero-width space
// or an override of the reading order through as the bytes it was given.
func shown(err error) string { return invisible.Shown(err.Error(), maxErrorBytes) }

// The words one round leaves behind for whoever asks whether this deployment
// is settling what it recorded. One network has one of them at a time.
const (
	// deciding is a round that asked the endpoints and wrote what their
	// answers settled. It stands only while rounds keep finishing: one that
	// has not for [staleAfter] is stalled.
	deciding = "deciding"
	// noRound is a network no round has finished on since this instance
	// started. It says nothing is wrong, only that nothing has happened yet.
	noRound = "no-round"
	// waiting is another instance holding the lease on this network. This one
	// is the spare, and what it would have to say about the network is what
	// the instance that is working on it knows.
	waiting = "waiting"
	// unreachable is a round that did not finish, which is almost always a
	// provider that did not answer: everything else a round does is a
	// database this probe reports on separately.
	unreachable = "unreachable"
	// stalled is a network whose rounds have stopped finishing, which is a
	// worker that stopped rather than a chain that is quiet. A round cannot
	// end the loop it is in, so a worker that stops is one stuck inside a
	// round, and the word for that is not the one the last round left.
	stalled = "stalled"
	// tooFew is a round that finished with fewer endpoints answering than
	// agreement takes. What it asked about is left where it was, and stays
	// there until another endpoint answers. It stands as deciding does, and
	// stales the same way.
	tooFew = "too-few"
)

// Agreements is how many third-party endpoints have to say the same thing
// before it is taken as what the chain holds, for a network with no node of
// the operator's own. Two: one endpoint's word alone settles nothing, and
// what a majority would add is for a later phase. A provisional number, not
// a measured one.
const Agreements = 2

// maxSkip bounds how many rounds a candidate the endpoints disagree about is
// left unasked between askings. It is asked less often the longer they
// disagree, and never dropped; an hour of the default recheck is the most it
// waits.
const maxSkip = 60

// staleAfter is how many rounds may be missed before what the last one said
// stops standing. Three, so that one slow round does not take a working
// deployment out of its word.
const staleAfter = 3

// Word is what this network's settling has come to, for whoever is answering
// somebody else's question about this deployment.
func (w *Worker) Word() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if (w.word == deciding || w.word == tooFew) && w.now().Sub(w.finished) > staleAfter*w.network.Recheck {
		return stalled
	}
	return w.word
}

// says what the network has come to.
func (w *Worker) says(word string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.word = word
	if word == deciding || word == tooFew {
		w.finished = w.now()
	}
}

// Workers are the workers of one deployment, one to a network.
type Workers []*Worker

// Words are what every network of the deployment has come to.
func (s Workers) Words() map[string]string {
	words := make(map[string]string, len(s))
	for _, w := range s {
		words[w.network.Name] = w.Word()
	}
	return words
}

// perRound bounds what one round reads and therefore how long it takes. A
// round asks the endpoints about every candidate it reads and writes a row for
// every payment the clock has passed, so this is the most a round can cost
// whatever has piled up behind it. A first number, to be measured again: the
// backlog it is there for is what a deployment has after an outage, and what
// that looks like is not yet known.
const perRound = 100

// leaseTimeout bounds putting the lease down on the way out. A release that
// hangs holds the name it was meant to give up.
const leaseTimeout = 10 * time.Second

// Network is what deciding one network needs: the endpoints to ask, and how
// long a recorded transfer has to go unfound before it is given up on.
type Network struct {
	// Name is what the configuration document calls the network, which is the
	// name the rows of its transfers carry.
	Name string
	// Endpoints are what is asked, in order, and Agreements is how many of
	// them have to say the same thing: one for a node the operator runs,
	// [Agreements] for third parties.
	Endpoints  []chain.Chain
	Agreements int
	// Recheck is how long the worker waits between rounds. Each round asks
	// the endpoints about every transfer that would settle a payment, so this
	// is also how often a payment that has been paid learns that it has.
	Recheck time.Duration
	// Misses is how many times in a row the endpoints have to be asked about a
	// recorded transfer and find nothing before it is given up on. Once is not
	// enough: an endpoint can be reading a state it has not finished
	// replacing, and what is taken back here is a payment's record of money
	// arriving.
	Misses int
}

// Leases hand a name to one instance at a time. Asking for one this instance
// already holds puts its deadline out again, which is how a round that is
// still going keeps it.
type Leases interface {
	Acquire(ctx context.Context, name string) (bool, error)
	Release(ctx context.Context, name string) error
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
	leases  Leases
	log     *slog.Logger
	now     func() time.Time
	// perRound is the most one round reads of each of the three things it
	// reads. Taken from the constant, and its own field so that a test can
	// put a backlog in front of a round without making one.
	perRound int
	// answers is what the endpoints said about each transfer the last time
	// they were asked, and how many times in a row they have said it. pass is
	// what the pass going on has heard so far, and takes its place when the
	// pass reaches the end.
	//
	// A pass is every candidate asked about once, which is one round while
	// there are no more candidates than a round reads and several rounds when
	// there are. Dropping what a pass did not hear is what keeps the map to
	// the transfers that are still candidates.
	//
	// from is where the next round goes on from.
	//
	// All three belong to whoever is running the round, and nothing guards
	// them. Reading them from anywhere else, such as something reporting what
	// the worker is making of a network, needs a lock put on them first.
	answers map[recorded]answer
	pass    map[recorded]answer
	from    payment.Place

	// What a round leaves behind is read by whoever is answering somebody
	// else's question about this deployment, which is another goroutine.
	mu       sync.Mutex
	word     string
	finished time.Time
}

// recorded names one transfer the way the row of one is named.
type recorded struct {
	key string
	tx  string
}

// answer is what the endpoints said about a transfer, with the rounds in a row
// they have said it, counting the one it came from, and how many rounds the
// transfer is left unasked before it is put to them again.
type answer struct {
	verdict Verdict
	rounds  int
	skip    int
}

// New opens a worker over a network. The clock is the caller's, so that a test
// can say when a round ran.
func New(n Network, store *payment.Postgres, leases Leases, log *slog.Logger,
	now func() time.Time) *Worker {
	return &Worker{network: n, store: store, leases: leases, log: log, now: now,
		answers: map[recorded]answer{}, pass: map[recorded]answer{}, perRound: perRound,
		// Nothing has been decided for this network yet, and a network
		// nothing has been decided for is not one to say anything else about.
		word: noRound}
}

// lease is the name this worker holds a network under. Not the network's own
// name: whoever reads the chain holds that one, and the two run beside each
// other.
//
// Separated by a dot, which is the one character a network's name cannot hold:
// a document is refused for a name with one in it, because a name is a path
// segment in every setting under it. So no name this builds is a name a
// network could have, and the two never take each other's lease.
func (w *Worker) lease() string { return "finality." + w.network.Name }

// Run decides and sweeps until the context ends.
//
// The lease is asked for at the top of every round rather than renewed part
// way through one. Asking for a lease this instance holds puts its deadline
// out again, which is what keeps it while rounds keep coming.
//
// What a round reads is bounded, so how long one takes is bounded by how slow
// the endpoints are rather than by how much has piled up. A round that
// outlasts the term even so is one another instance can start beside: what
// both of them write is refused by the revision it was read at, and giving up
// on a transfer is refused by the row already saying so, so the cost is asking
// the endpoints twice rather than deciding twice.
//
// A round that does not finish is said and left. What it did not get to is
// still there next time, and the alternative is an instance that stops working
// on a network because a provider was down for a minute.
func (w *Worker) Run(ctx context.Context) error {
	defer func() {
		// Put down on the way out so that another instance does not wait out
		// the term for a network nobody is deciding for.
		release, stop := context.WithTimeout(context.WithoutCancel(ctx), leaseTimeout)
		defer stop()
		if err := w.leases.Release(release, w.lease()); err != nil {
			w.log.Warn("the lease was not put down", "network", w.network.Name, "error", shown(err))
		}
	}()

	for {
		held, err := w.leases.Acquire(ctx, w.lease())
		switch {
		case err != nil:
			w.log.Warn("the lease could not be asked for", "network", w.network.Name, "error", shown(err))
		case !held:
			w.says(waiting)
		default:
			switch short, err := w.round(ctx); {
			case err != nil:
				w.says(unreachable)
				w.log.Warn("the round did not finish", "network", w.network.Name, "error", shown(err))
			case short:
				w.says(tooFew)
			default:
				w.says(deciding)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(w.network.Recheck):
		}
	}
}

// round asks the endpoints about the recorded transfers that would settle a
// payment, writes what their answers settle, and then reads the clock.
//
// As many transfers as one round reads, going on from where the last round
// stopped. A deployment with more of them than that gets through them over
// several rounds, and comes round to the beginning when it reaches the end.
//
// An endpoint that does not answer is skipped, and a transfer fewer endpoints
// answered about than agreement takes is left where it was. The round goes on
// to the next transfer and says, in short, that it was short of endpoints: a
// reader that cannot read decides nothing, and the round comes again.
//
// A transfer the endpoints disagree about is put to them again less often the
// longer they disagree, and never dropped: disagreement does not resolve
// itself, and asking every round would be asking for the same answer.
func (w *Worker) round(ctx context.Context) (short bool, err error) {
	if w.network.Misses < 1 {
		return false, fmt.Errorf("%s: a transfer is set to be given up on after %d rounds",
			w.network.Name, w.network.Misses)
	}
	if w.network.Agreements < 1 {
		return false, fmt.Errorf("%s: %d endpoints are set to have to agree",
			w.network.Name, w.network.Agreements)
	}
	network := payment.Network(w.network.Name)
	candidates, err := w.store.Candidates(ctx, network, w.from, w.perRound)
	if err != nil {
		return false, err
	}
	for _, c := range candidates {
		at := recorded{key: c.Key, tx: c.Tx}
		before, told := w.answers[at]
		if told && before.verdict == Disagreed && before.skip > 0 {
			before.skip--
			w.pass[at] = before
			continue
		}
		said, err := Ask(ctx, w.network.Endpoints, w.network.Agreements,
			Recorded{Tx: c.Tx, Height: c.BlockHeight, Hash: c.BlockHash})
		if err != nil {
			return false, err
		}
		if said.Verdict == Unanswered {
			// Nothing was said, so nothing is remembered as said: what the
			// endpoints have been saying stands until they say otherwise.
			short = true
			if told {
				w.pass[at] = before
			}
			continue
		}
		now := answer{verdict: said.Verdict, rounds: 1}
		if told && before.verdict == said.Verdict {
			now.rounds = before.rounds + 1
		}
		if said.Verdict == Disagreed {
			now.skip = min(now.rounds, maxSkip)
			if now.rounds == 1 {
				if err := w.store.Disagree(ctx, network, c.Key, c.Tx, w.now()); err != nil {
					return false, err
				}
			}
		} else if told && before.verdict == Disagreed {
			if err := w.store.Agree(ctx, network, c.Key, c.Tx); err != nil {
				return false, err
			}
		}
		w.pass[at] = now
		if err := w.decided(ctx, c, now); err != nil {
			return false, err
		}
	}
	if len(candidates) < w.perRound {
		w.answers, w.pass, w.from = w.pass, map[recorded]answer{}, payment.Place{}
	} else {
		last := candidates[len(candidates)-1]
		w.from = payment.Place{BlockHeight: last.BlockHeight, Tx: last.Tx}
	}
	if short {
		w.log.Warn("fewer endpoints answered than agreement takes", "network", w.network.Name,
			"agreements", w.network.Agreements)
	}
	return short, w.swept(ctx)
}

// swept moves the payments the clock has passed by: those that have reached
// their deadline stop being payable, and those whose network has been read
// past the deadline with nothing that could pay them expire.
//
// It runs after the deciding, so a transfer that settles a payment in this
// round settles it before the clock is read. The second sweep is not on the
// clock at all. A deployment that has stopped reading expires nothing, however
// long it has been stopped: the transfer it has not read may be one carried
// before the deadline, and expiring the payment would make that money late.
func (w *Worker) swept(ctx context.Context) error {
	network := payment.Network(w.network.Name)
	now := w.now()

	reached, err := w.store.Overdue(ctx, network, now, w.perRound)
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

	over, err := w.store.Unsettled(ctx, network, w.perRound)
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
		if said.rounds < w.network.Misses {
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
				"payment", c.Payment.ID(), "tx", c.Tx, "error", shown(err))
		}
		return nil
	}
	// Two transfers can pay one payment. The first of them settles it, and the
	// second finds it moved, which [Worker.told] passes over.
	return w.told(ctx, c.Account, c.Payment, c.PaymentAt, "payment succeeded",
		"tx", c.Tx, "height", c.BlockHeight)
}
