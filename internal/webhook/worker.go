package webhook

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/sucopay/sucopay/internal/invisible"
)

// The words one round leaves behind for whoever asks whether this
// deployment is delivering what its payments produced. The same shape as a
// network's settling has.
const (
	// delivering is a round that expanded the outbox and sent what was due.
	// It stands only while rounds keep finishing.
	delivering = "delivering"
	// noRound is a deployment no round has finished on since this instance
	// started.
	noRound = "no-round"
	// waiting is another instance holding the lease. This one is the spare.
	waiting = "waiting"
	// notFinished is a round that did not finish, which is the database not
	// answering: a receiver not answering is an attempt, not a failed round.
	// The word is the one a network's settling uses for the same thing.
	notFinished = "unreachable"
	// stalled is a worker whose rounds have stopped finishing.
	stalled = "stalled"
)

// The bounds a round keeps. period is between rounds, and the shortest
// interval a retry waits. expandPerRound is how many outbox rows one round
// turns into deliveries, perRound how many deliveries it sends, and
// perEndpoint how many of those go to any one endpoint, so that one slow
// receiver does not take the round and the connections open do not grow
// with the endpoints registered. staleAfter is how many periods may pass
// without a round before the last word stops standing.
const (
	period         = 5 * time.Second
	expandPerRound = 100
	perRound       = 32
	perEndpoint    = 4
	staleAfter     = 3
)

// leaseName is what this worker holds the deployment's deliveries under.
// One worker for the deployment rather than one per endpoint: a delivery is
// sent once, and the lease is what makes that so across instances.
const leaseName = "webhooks"

// leaseTimeout bounds putting the lease down on the way out.
const leaseTimeout = 10 * time.Second

// maxErrorBytes bounds what somebody else's failure can put in a line.
const maxErrorBytes = 512

// Leases hand a name to one instance at a time. Asking for one this
// instance already holds puts its deadline out again.
type Leases interface {
	Acquire(ctx context.Context, name string) (bool, error)
	Release(ctx context.Context, name string) error
}

// Worker sends what the payments produced to the endpoints that receive
// it: every round, it turns the outbox into deliveries, sends the
// deliveries whose time has come, and writes down what each came to.
//
// It crosses accounts, as settling does. Every delivery it sends is sent
// under the account the row came back with, to that account's endpoint or
// the deployment's.
type Worker struct {
	store  *Postgres
	sender *Sender
	leases Leases
	log    *slog.Logger
	now    func() time.Time
	// random is for the jitter on a retry's interval: a number in [0, 1).
	random func() float64

	// What a round leaves behind is read by whoever is answering somebody
	// else's question about this deployment, which is another goroutine.
	mu       sync.Mutex
	word     string
	finished time.Time
}

// NewWorker opens a worker over store, sending through sender. The clock is
// the caller's, so that a test can say when a round ran.
func NewWorker(store *Postgres, sender *Sender, leases Leases, log *slog.Logger, now func() time.Time) *Worker {
	return &Worker{store: store, sender: sender, leases: leases, log: log, now: now,
		random: rand.Float64, word: noRound}
}

// Word is what this deployment's delivering has come to, for whoever is
// answering somebody else's question about it.
func (w *Worker) Word() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.word == delivering && w.now().Sub(w.finished) > staleAfter*period {
		return stalled
	}
	return w.word
}

func (w *Worker) says(word string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.word, w.finished = word, w.now()
}

// Run makes a round every period until ctx ends, holding the lease while it
// does. A round that does not finish is said and left: what it did not get
// to is still there next time.
func (w *Worker) Run(ctx context.Context) error {
	defer func() {
		release, stop := context.WithTimeout(context.WithoutCancel(ctx), leaseTimeout)
		defer stop()
		if err := w.leases.Release(release, leaseName); err != nil {
			w.log.Warn("the lease was not put down", "lease", leaseName, "error", shown(err))
		}
	}()

	for {
		held, err := w.leases.Acquire(ctx, leaseName)
		switch {
		case err != nil:
			w.log.Warn("the lease could not be asked for", "lease", leaseName, "error", shown(err))
		case !held:
			w.says(waiting)
		default:
			if err := w.Round(ctx); err != nil {
				w.says(notFinished)
				w.log.Warn("the round did not finish", "error", shown(err))
			} else {
				w.says(delivering)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(period):
		}
	}
}

// Round expands the outbox, sends what is due, and writes down what each
// send came to. Run makes one every period, and a test makes one itself.
// The sends go out together, as many as are due; what is due is bounded by
// [perRound] and [perEndpoint], so that is how many connections a round
// holds at most.
//
// A send that gets no answer is an attempt written down, not an error. An
// error is the database: a round that cannot read what is due or write
// what happened has nothing to go on with.
func (w *Worker) Round(ctx context.Context) error {
	now := w.now()
	if _, err := w.store.Expand(ctx, now, expandPerRound); err != nil {
		return err
	}
	due, err := w.store.Due(ctx, now, perRound, perEndpoint)
	if err != nil {
		return err
	}
	var (
		sends sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	for _, d := range due {
		sends.Add(1)
		go func() {
			defer sends.Done()
			if err := w.send(ctx, d); err != nil {
				mu.Lock()
				first = errors.Join(first, err)
				mu.Unlock()
			}
		}()
	}
	sends.Wait()
	return first
}

// send makes one attempt at d and writes down what it came to.
func (w *Worker) send(ctx context.Context, d Due) error {
	started := w.now()
	var out Outcome
	secrets, err := w.store.Secrets(ctx, d.Delivery.Endpoint, started)
	switch {
	case errors.Is(err, ErrSealed):
		// Nothing to sign under. The attempt is written down as one, so that
		// the delivery's list says why nothing arrived, and it comes round
		// again: a rotation seals a new secret under the key in force.
		out = Outcome{Reason: ReasonSecret, Took: w.now().Sub(started)}
	case err != nil:
		return err
	default:
		out = w.sender.Send(ctx, Endpoint{ID: d.Delivery.Endpoint, URL: d.URL}, d.Allowed, secrets, d.Delivery)
	}
	return w.store.Attempted(ctx, d.Delivery, out, w.now(), w.random)
}

func shown(err error) string { return invisible.Shown(err.Error(), maxErrorBytes) }
