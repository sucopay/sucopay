package payment_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/refund"
)

// links derive the tokens a refund's page is reached by, as a deployment
// does.
var links = refund.NewLinks([32]byte{7}, "k1", "https://pay.example")

// opening is a refund of p a merchant asked for: an amount of the payment's
// asset, sent back to where the payment came from.
func opening(t *testing.T, p *payment.Payment, units string) (*payment.Refund, error) {
	t.Helper()
	amount, err := payment.ParseUnits(p.Asset(), units)
	if err != nil {
		t.Fatal(err)
	}
	id, err := payment.NewRefundID()
	if err != nil {
		t.Fatal(err)
	}
	return payment.NewRefund(p, id, payment.RefundRequest{
		Amount:      amount,
		Destination: address(t, "f"),
		Token:       links.Token(id),
	}, now)
}

// paidFor stores a payment the chain settled, which is the only kind a refund
// can be made against.
func paidFor(t *testing.T, s *payment.Postgres, account payment.AccountID) *payment.Payment {
	t.Helper()
	p := payableKept(t, s, account)
	_, at, err := s.Find(t.Context(), account, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Receive(p.Amount()); err != nil {
		t.Fatal(err)
	}
	if err := p.Succeed(); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), account, p, at, payment.Event{}); err != nil {
		t.Fatal(err)
	}
	return p
}

// refunding opens a refund of p and stores it.
func refunding(t *testing.T, s *payment.Postgres, account payment.AccountID, p *payment.Payment, units string) *payment.Refund {
	t.Helper()
	r, err := opening(t, p, units)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRefund(t.Context(), account, r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRefund_IsOnlyOpenedAgainstAPaymentTheChainSettled(t *testing.T) {
	t.Parallel()
	s, _ := store(t)

	for name, p := range map[string]*payment.Payment{
		"created": kept(t, s, first),
		"payable": payableKept(t, s, first),
		"settled": paidFor(t, s, first),
	} {
		t.Run(name, func(t *testing.T) {
			r, err := opening(t, p, "1")
			if name != "settled" {
				var problems payment.Problems
				if !errors.As(err, &problems) {
					t.Fatalf("NewRefund gave %v, want problems", err)
				}
				if !strings.Contains(problems.Error(), "succeeded") {
					t.Errorf("problems = %v, want the status named", problems)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewRefund = %v, want none", err)
			}
			if r.Status() != payment.RefundCreated {
				t.Errorf("status = %s, want created", r.Status())
			}
			if !r.ExpiresAt().Equal(now.Add(payment.RefundLife).UTC()) {
				t.Errorf("expires_at = %s, want %s after now", r.ExpiresAt(), payment.RefundLife)
			}
		})
	}
}

func TestRefund_TheStoreRefusesAPaymentThatIsNotSettled(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	settledPayment := paidFor(t, s, first)
	unsettled := payableKept(t, s, first)
	r, err := opening(t, settledPayment, "1")
	if err != nil {
		t.Fatal(err)
	}
	// The same refund against the payment nothing settled: the aggregate saw a
	// settled one, and the row is what the store reads.
	moved, err := opening(t, settledPayment, "1")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.CreateRefund(t.Context(), first, r); err != nil {
		t.Fatalf("CreateRefund = %v, want none", err)
	}
	err = s.CreateRefund(t.Context(), first, rebound(t, moved, unsettled.ID()))

	var problems payment.Problems
	if !errors.As(err, &problems) || !strings.Contains(problems.Error(), "succeeded") {
		t.Errorf("CreateRefund gave %v, want the payment's status", err)
	}
}

func TestRefund_HoldsWhatIsLeftOfWhatArrived(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	p := paidFor(t, s, first)

	refunding(t, s, first, p, "0.4")
	held, err := s.Refunded(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}

	if want := "400000000000000000"; held != want {
		t.Errorf("Refunded = %s, want %s", held, want)
	}
	over, err := opening(t, p, "0.7")
	if err != nil {
		t.Fatal(err)
	}
	err = s.CreateRefund(t.Context(), first, over)
	var problems payment.Problems
	if !errors.As(err, &problems) {
		t.Fatalf("CreateRefund gave %v, want problems", err)
	}
	if got := problems.Error(); !strings.Contains(got, "0.6") || !strings.Contains(got, "0.7") {
		t.Errorf("problems = %v, want what was asked for and what is left", got)
	}
	// What is left is refundable to the last unit.
	refunding(t, s, first, p, "0.6")
	if held, err = s.Refunded(t.Context(), first, p.ID()); err != nil || held != "1000000000000000000" {
		t.Errorf("Refunded = %s, %v, want all of what arrived", held, err)
	}
}

func TestRefund_ReadsBackWhatItWasOpenedWith(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	p := paidFor(t, s, first)
	r := refunding(t, s, first, p, "0.25")

	back, _, err := s.FindRefund(t.Context(), first, p.ID(), r.ID())

	if err != nil {
		t.Fatal(err)
	}
	if back.Amount().Units() != "0.25" || back.Destination() != r.Destination() {
		t.Errorf("read back %s to %s, want 0.25 to %s", back.Amount().Units(), back.Destination(), r.Destination())
	}
	if back.Key() != r.Key() || back.Network() != p.Network() || back.Scheme() != payment.EIP3009 {
		t.Errorf("read back key %q on %s as %s", back.Key(), back.Network(), back.Scheme())
	}
	if !back.ExpiresAt().Equal(r.ExpiresAt()) || back.Status() != payment.RefundCreated {
		t.Errorf("read back %s expiring %s", back.Status(), back.ExpiresAt())
	}
	if _, _, err := s.FindRefund(t.Context(), other, p.ID(), r.ID()); !errors.Is(err, payment.ErrNotFound) {
		t.Errorf("another account read the refund: %v", err)
	}
}

func TestRefund_KeepsTheAmountOffTheCeilingUntilItExpires(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	p := paidFor(t, s, first)
	r := refunding(t, s, first, p, "1")

	// What the sweep of the clock will do, before there is one to do it.
	if _, err := pool.Exec(t.Context(),
		`update refunds set status = $1 where id = $2`, payment.RefundExpired, r.ID()); err != nil {
		t.Fatal(err)
	}

	held, err := s.Refunded(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if held != "0" {
		t.Errorf("Refunded = %s, want an expired refund to hold nothing", held)
	}
	refunding(t, s, first, p, "1")
}

func TestRefund_IsNotOpenedAgainstAnotherAccountsPayment(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	p := paidFor(t, s, first)
	r, err := opening(t, p, "1")
	if err != nil {
		t.Fatal(err)
	}

	err = s.CreateRefund(t.Context(), other, r)

	if !errors.Is(err, payment.ErrNotFound) {
		t.Errorf("CreateRefund gave %v, want %v", err, payment.ErrNotFound)
	}
}

// rebound is a refund against another payment, for the tests that need one
// the aggregate would not have made.
func rebound(t *testing.T, r *payment.Refund, id payment.ID) *payment.Refund {
	t.Helper()
	back, err := payment.RestoreRefund(payment.RefundStored{
		ID: r.ID(), PaymentID: id, Amount: r.Amount(), Destination: r.Destination(),
		Scheme: r.Scheme(), Network: r.Network(), Key: r.Key(), ExpiresAt: r.ExpiresAt(),
		Status: r.Status(), Token: r.Token(), CreatedAt: r.CreatedAt(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return back
}

func TestRefund_LetsNoTwoRequestsMeasureAgainstTheSameRemainder(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	p := paidFor(t, s, first)
	const racing = 3

	// The payment is held here so that every request reaches the ceiling
	// before any of them writes. A store that reads the remainder without
	// taking the row walks straight past this and writes three times.
	held, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := held.Exec(t.Context(),
		`select id from payments where account_id = $1 and id = $2 for update`, first, p.ID()); err != nil {
		t.Fatal(err)
	}

	// Each asks for everything that arrived. One of them can have it.
	answers := make(chan error, racing)
	for range racing {
		r, err := opening(t, p, "1")
		if err != nil {
			t.Fatal(err)
		}
		go func() { answers <- s.CreateRefund(t.Context(), first, r) }()
	}
	blocking(t, pool, racing)
	if err := held.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	opened, refused := 0, 0
	for range racing {
		switch err := <-answers; {
		case err == nil:
			opened++
		case errors.As(err, &payment.Problems{}):
			refused++
		default:
			t.Errorf("CreateRefund gave %v, want either the row or the ceiling", err)
		}
	}
	if opened != 1 || refused != racing-1 {
		t.Errorf("%d refunds were opened and %d refused, want 1 and %d", opened, refused, racing-1)
	}
	total, err := s.Refunded(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if want := p.Amount().Amount().String(); total != want {
		t.Errorf("the refunds hold %s, want %s, which is what arrived", total, want)
	}
}

// blocking waits until n backends of this database are waiting on a lock, or
// until it has waited long enough that they are not going to. Either way the
// requests have reached the store; whether they are held there is what the
// test is about.
func blocking(t *testing.T, pool *pgxpool.Pool, n int) {
	t.Helper()
	for until := time.Now().Add(2 * time.Second); time.Now().Before(until); {
		var blocked int
		if err := pool.QueryRow(t.Context(), `
			select count(*) from pg_stat_activity
			 where datname = current_database() and wait_event_type = 'Lock'`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
