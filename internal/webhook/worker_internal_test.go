package webhook

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
)

// held is a Leases that answers as told, and needs no database.
type held bool

func (h held) Acquire(context.Context, string) (bool, error) { return bool(h), nil }
func (held) Release(context.Context, string) error           { return nil }

// The word a worker says follows what its rounds come to: nothing at first,
// delivering after a round, stalled once rounds stop, waiting behind another
// instance, and unreachable when the database is not.
func TestWorker_WordFollowsWhatItsRoundsComeTo(t *testing.T) {
	t.Parallel()
	pool, err := postgres.Open(t.Context(), postgrestest.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	cipher, _ := NewCipher([32]byte{1})
	clock := time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWorker(NewPostgres(pool.Conns(), cipher, "k1"), NewSender(loopbackResolver{}, nil, now), held(true), quiet, now)

	if got := w.Word(); got != noRound {
		t.Errorf("before any round: %q, want %s", got, noRound)
	}
	// One round of a run, which is over before the next: the round is
	// milliseconds and the wait between rounds is five seconds.
	run := func(w *Worker) {
		t.Helper()
		ctx, stop := context.WithCancel(t.Context())
		go func() {
			time.Sleep(500 * time.Millisecond)
			stop()
		}()
		if err := w.Run(ctx); err != nil {
			t.Fatal(err)
		}
	}
	run(w)
	if got := w.Word(); got != delivering {
		t.Errorf("after a round: %q, want %s", got, delivering)
	}
	clock = clock.Add(staleAfter*period + time.Second)
	if got := w.Word(); got != stalled {
		t.Errorf("with no round for %v: %q, want %s", staleAfter*period, got, stalled)
	}

	spare := NewWorker(NewPostgres(pool.Conns(), cipher, "k1"), NewSender(loopbackResolver{}, nil, now), held(false), quiet, now)
	run(spare)
	if got := spare.Word(); got != waiting {
		t.Errorf("behind another instance: %q, want %s", got, waiting)
	}

	pool.Close()
	run(w)
	if got := w.Word(); got != notFinished {
		t.Errorf("with the database closed: %q, want %s", got, notFinished)
	}
}
