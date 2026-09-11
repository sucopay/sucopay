package payment_test

import (
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/payment"
)

// deadline is when the payments these tests make stop being payable.
var deadline = now.Add(time.Hour)

// waiting stores a payment that has passed its deadline and is waiting to
// learn whether anything arrives.
func waiting(t *testing.T, s *payment.Postgres, account payment.AccountID) *payment.Payment {
	t.Helper()
	p := payableKept(t, s, account)
	back, at, err := s.Find(t.Context(), account, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := back.AwaitFinality(back.ExpiresAt()); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), account, back, at, payment.Event{}); err != nil {
		t.Fatal(err)
	}
	return back
}

// paid moves a payment to succeeded, for a row no sweep of the clock owns.
func paid(t *testing.T, s *payment.Postgres, account payment.AccountID, id payment.ID) {
	t.Helper()
	p, at, err := s.Find(t.Context(), account, id)
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
}

// ids are the payments a sweep read, by the account they belong to.
func ids(due []payment.Due) map[payment.AccountID]payment.ID {
	by := map[payment.AccountID]payment.ID{}
	for _, one := range due {
		by[one.Account] = one.Payment.ID()
	}
	return by
}

// The deadline is the moment a payment stops being payable, so reaching it is
// passing it. A sweep runs for the deployment and reads every account's.
func TestOverdue_ReadsThePaymentsOfEveryAccountThatReachedTheirDeadline(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	mine, theirs := payableKept(t, s, first), payableKept(t, s, other)

	early, err := s.Overdue(t.Context(), network, deadline.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(early) != 0 {
		t.Fatalf("Overdue read %d payments a second before the deadline, want none", len(early))
	}

	over, err := s.Overdue(t.Context(), network, deadline)

	if err != nil {
		t.Fatal(err)
	}
	if len(over) != 2 {
		t.Fatalf("Overdue read %d payments at the deadline, want the two that reached it", len(over))
	}
	by := ids(over)
	for account, want := range map[payment.AccountID]payment.ID{first: mine.ID(), other: theirs.ID()} {
		if by[account] != want {
			t.Errorf("under %s: %s, want %s", account, by[account], want)
		}
	}
	for _, one := range over {
		if err := s.Save(t.Context(), one.Account, one.Payment, one.PaymentAt, payment.Event{}); err != nil {
			t.Errorf("the revision Overdue gave did not save: %v", err)
		}
	}
}

// A payment that is not open for payment has nothing to stop being open for,
// and one on another network belongs to another worker.
func TestOverdue_LeavesOutWhatIsNotOpenForPaymentOnThisNetwork(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	open := payableKept(t, s, first)
	kept(t, s, first)
	waiting(t, s, first)
	settled := payableKept(t, s, first)
	paid(t, s, first, settled.ID())
	spentOn(t, s, first, "ethereum")

	over, err := s.Overdue(t.Context(), network, deadline)

	if err != nil {
		t.Fatal(err)
	}
	if len(over) != 1 {
		t.Fatalf("Overdue read %d payments, want only the one open for payment", len(over))
	}
	if over[0].Payment.ID() != open.ID() {
		t.Errorf("Overdue read %s, want %s", over[0].Payment.ID(), open.ID())
	}
}

// The wait is over and nothing is standing that could still pay it.
func TestUnsettled_ReadsThePaymentsOfEveryAccountWhoseWaitIsOver(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	mine, theirs := waiting(t, s, first), waiting(t, s, other)

	early, err := s.Unsettled(t.Context(), network, deadline.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(early) != 0 {
		t.Fatalf("Unsettled read %d payments a second before the wait was over, want none", len(early))
	}

	done, err := s.Unsettled(t.Context(), network, deadline)

	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 2 {
		t.Fatalf("Unsettled read %d payments, want the two whose wait is over", len(done))
	}
	by := ids(done)
	for account, want := range map[payment.AccountID]payment.ID{first: mine.ID(), other: theirs.ID()} {
		if by[account] != want {
			t.Errorf("under %s: %s, want %s", account, by[account], want)
		}
	}
}

// A transfer that matched is one that may still pay the payment, whether or
// not it has been seen where the chain keeps it. Giving up on a payment while
// one stands would end a payment that money is on its way to.
func TestUnsettled_LeavesOutAPaymentWithATransferStillMatchedAgainstIt(t *testing.T) {
	t.Parallel()
	for what, final := range map[string]bool{"seen where the chain keeps it": true, "seen ahead of that": false} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			s, pool := store(t)
			hit := spent(t, s, first)
			if err := recording(t, s, pool, 100, 100, final,
				seenAt(hit, "tx1", 100, payment.Matched)); err != nil {
				t.Fatal(err)
			}
			p, at, err := s.Find(t.Context(), first, hit.Payment.ID())
			if err != nil {
				t.Fatal(err)
			}
			if err := p.AwaitFinality(p.ExpiresAt()); err != nil {
				t.Fatal(err)
			}
			if err := s.Save(t.Context(), first, p, at, payment.Event{}); err != nil {
				t.Fatal(err)
			}

			done, err := s.Unsettled(t.Context(), network, deadline)

			if err != nil {
				t.Fatal(err)
			}
			if len(done) != 0 {
				t.Errorf("Unsettled read %d payments with a transfer still matched, want none", len(done))
			}
		})
	}
}

// A transfer short of what was asked never pays the payment, so it is no
// reason to keep waiting. One that vanished is no longer on the chain.
func TestUnsettled_ReadsAPaymentWhoseOnlyTransfersCannotPayIt(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)
	if err := recording(t, s, pool, 100, 100, true,
		worth(seenAt(hit, "tx1", 100, payment.Short), "1")); err != nil {
		t.Fatal(err)
	}
	gone := spent(t, s, first)
	if err := recording(t, s, pool, 101, 101, true,
		seenAt(gone, "tx2", 101, payment.Matched)); err != nil {
		t.Fatal(err)
	}
	if err := s.Vanish(t.Context(), network, gone.Attempt.Key(), "tx2", now); err != nil {
		t.Fatal(err)
	}
	for _, hit := range []payment.Hit{hit, gone} {
		p, at, err := s.Find(t.Context(), first, hit.Payment.ID())
		if err != nil {
			t.Fatal(err)
		}
		if err := p.AwaitFinality(p.ExpiresAt()); err != nil {
			t.Fatal(err)
		}
		if err := s.Save(t.Context(), first, p, at, payment.Event{}); err != nil {
			t.Fatal(err)
		}
	}

	done, err := s.Unsettled(t.Context(), network, deadline)

	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 2 {
		t.Errorf("Unsettled read %d payments, want both the short one and the one that vanished", len(done))
	}
}

func TestUnsettled_LeavesOutWhatIsNotWaitingForFinalityOnThisNetwork(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	held := waiting(t, s, first)
	payableKept(t, s, first)
	kept(t, s, first)
	settled := waiting(t, s, first)
	paid(t, s, first, settled.ID())
	elsewhere := spentOn(t, s, first, "ethereum")
	p, at, err := s.Find(t.Context(), first, elsewhere.Payment.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.AwaitFinality(p.ExpiresAt()); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), first, p, at, payment.Event{}); err != nil {
		t.Fatal(err)
	}

	done, err := s.Unsettled(t.Context(), network, deadline)

	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 {
		t.Fatalf("Unsettled read %d payments, want only the one waiting on this network", len(done))
	}
	if done[0].Payment.ID() != held.ID() {
		t.Errorf("Unsettled read %s, want %s", done[0].Payment.ID(), held.ID())
	}
}

// A payment's identifier is that account's. Another account's transfer must
// not be read as a reason to keep this payment waiting.
func TestUnsettled_AsksOnlyAboutTheTransfersOfItsOwnAccount(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)
	if err := recording(t, s, pool, 100, 100, true,
		seenAt(hit, "tx1", 100, payment.Matched)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		insert into payments (id, account_id, asset_network, asset_reference, asset_symbol,
		                      asset_decimals, amount, destination, status, metadata,
		                      created_at, expires_at)
		select id, $1, asset_network, asset_reference, asset_symbol, asset_decimals,
		       amount, destination, $4, metadata, created_at, expires_at
		  from payments where account_id = $2 and id = $3`,
		other, first, hit.Payment.ID(), payment.AwaitingFinality); err != nil {
		t.Fatal(err)
	}

	done, err := s.Unsettled(t.Context(), network, deadline)

	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 {
		t.Fatalf("Unsettled read %d payments, want the one whose own account recorded nothing", len(done))
	}
	if done[0].Account != other {
		t.Errorf("Unsettled read the payment under %s, want %s", done[0].Account, other)
	}
}
