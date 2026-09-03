package payment_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sucopay/sucopay/internal/payment"
)

// serving returns a service over a fresh database, reading a clock the test
// controls.
func serving(t *testing.T) (*payment.Service, *payment.Postgres, *pgxpool.Pool) {
	t.Helper()
	s, pool := store(t)
	return payment.NewService(s, func() time.Time { return now }), s, pool
}

func TestService_OpensAPaymentNothingCanPayYet(t *testing.T) {
	t.Parallel()
	// Opened and payable are two steps, so that a payment cannot be paid
	// between being created and whoever asked for it finishing with it.
	svc, s, _ := serving(t)

	p, err := svc.Open(t.Context(), first, request(t))
	if err != nil {
		t.Fatal(err)
	}

	if p.Status() != payment.Created {
		t.Errorf("status = %s, want %s", p.Status(), payment.Created)
	}
	back, _, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatalf("the payment it returned was not stored: %v", err)
	}
	if back.Status() != payment.Created {
		t.Errorf("stored status = %s, want %s", back.Status(), payment.Created)
	}
}

func TestService_ReadsTheClockItWasGiven(t *testing.T) {
	t.Parallel()
	at := now.Add(72 * time.Hour)
	s, _ := store(t)
	svc := payment.NewService(s, func() time.Time { return at })
	r := request(t)
	r.ExpiresAt = at.Add(time.Hour)

	p, err := svc.Open(t.Context(), first, r)
	if err != nil {
		t.Fatal(err)
	}

	if !p.CreatedAt().Equal(at) {
		t.Errorf("created_at = %s, want %s", p.CreatedAt(), at)
	}
}

func TestService_StoresNothingForAPaymentItRefused(t *testing.T) {
	t.Parallel()
	// Refused before it is written, not written and then judged: a row nobody
	// can act on is worse than no row.
	svc, _, pool := serving(t)
	r := request(t)
	r.Destination = ""

	p, err := svc.Open(t.Context(), first, r)

	if err == nil {
		t.Fatal("opened a payment with no destination")
	}
	if p != nil {
		t.Errorf("returned a payment as well as an error: %v", p)
	}
	var rows int
	if err := pool.QueryRow(t.Context(), `select count(*) from payments`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("%d payments stored, want none", rows)
	}
}

func TestService_MakesAPaymentPayable(t *testing.T) {
	t.Parallel()
	svc, s, _ := serving(t)
	opened, err := svc.Open(t.Context(), first, request(t))
	if err != nil {
		t.Fatal(err)
	}

	p, err := svc.Await(t.Context(), first, opened.ID())
	if err != nil {
		t.Fatal(err)
	}

	if p.Status() != payment.AwaitingPayment {
		t.Errorf("status = %s, want %s", p.Status(), payment.AwaitingPayment)
	}
	back, _, err := s.Find(t.Context(), first, opened.ID())
	if err != nil {
		t.Fatal(err)
	}
	if back.Status() != payment.AwaitingPayment {
		t.Errorf("stored status = %s, want it saved as %s", back.Status(), payment.AwaitingPayment)
	}
}

func TestService_RefusesToMakeAPaymentPayableTwice(t *testing.T) {
	t.Parallel()
	// The move the aggregate refuses is refused here too, rather than being
	// written and refused by the row.
	svc, _, _ := serving(t)
	opened, err := svc.Open(t.Context(), first, request(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Await(t.Context(), first, opened.ID()); err != nil {
		t.Fatal(err)
	}

	_, err = svc.Await(t.Context(), first, opened.ID())

	if err == nil {
		t.Fatal("made a payment payable twice")
	}
}

// staleOnSave answers every save with ErrStale, so that what the service does
// with that answer can be read without racing a real repository for it.
type staleOnSave struct{ *payment.Postgres }

func (staleOnSave) Save(context.Context, payment.AccountID, *payment.Payment, payment.Revision) error {
	return payment.ErrStale
}

func TestService_PassesOnAPaymentThatMovedUnderneathIt(t *testing.T) {
	t.Parallel()
	// Reported in a form errors.Is reads, so that a caller can tell this from a
	// move the aggregate refused and from a payment that was never there.
	s, _ := store(t)
	svc := payment.NewService(s, func() time.Time { return now })
	opened, err := svc.Open(t.Context(), first, request(t))
	if err != nil {
		t.Fatal(err)
	}
	losing := payment.NewService(staleOnSave{s}, func() time.Time { return now })

	_, err = losing.Await(t.Context(), first, opened.ID())

	if !errors.Is(err, payment.ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
}

func TestService_LetsExactlyOneOfTwoCallersMakeAPaymentPayable(t *testing.T) {
	t.Parallel()
	// The service holds nothing between calls, so two of them racing is decided
	// by the database and by nothing in this process. Run under -race, this also
	// fails if somebody gives Service a cache later.
	//
	// Which way the loser is refused depends on when it read: before the winner
	// saved, and the version check catches it; after, and it reads a payment
	// that is already payable and the aggregate refuses the move. Both are the
	// same answer to the caller, so the test asks only that exactly one won.
	svc, _, _ := serving(t)
	opened, err := svc.Open(t.Context(), first, request(t))
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := svc.Await(t.Context(), first, opened.ID())
			errs <- err
		}()
	}
	close(start)

	won, refused := 0, 0
	for range 2 {
		var problems payment.Problems
		switch err := <-errs; {
		case err == nil:
			won++
		case errors.Is(err, payment.ErrStale), errors.As(err, &problems):
			refused++
		default:
			t.Errorf("err = %v, want either none, ErrStale, or a refused move", err)
		}
	}
	if won != 1 || refused != 1 {
		t.Errorf("%d succeeded and %d were refused, want one of each", won, refused)
	}
}

func TestService_ReportsAnUnknownPaymentAsNotFound(t *testing.T) {
	t.Parallel()
	svc, _, _ := serving(t)
	id, err := payment.NewID()
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.Await(t.Context(), first, id)

	if !errors.Is(err, payment.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestService_DoesNotReachAPaymentAnotherAccountOpened(t *testing.T) {
	t.Parallel()
	svc, _, _ := serving(t)
	opened, err := svc.Open(t.Context(), first, request(t))
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.Await(t.Context(), other, opened.ID())

	if !errors.Is(err, payment.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
