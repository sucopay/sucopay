package payment_test

import (
	"encoding/json"
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

// paidFor stores a payment a transfer paid and the chain settled, which is the
// only kind a refund can be made against.
func paidFor(t *testing.T, s *payment.Postgres, pool *pgxpool.Pool, account payment.AccountID) *payment.Payment {
	t.Helper()
	hit := spent(t, s, account)
	if err := recordingAt(t, s, pool, now, 1, 20, false,
		seenAt(hit, "0xpaid", 10, payment.Matched)); err != nil {
		t.Fatal(err)
	}
	// What arrived is on the payment; what is left is the chain settling it.
	p, at, err := s.Find(t.Context(), account, hit.Payment.ID())
	if err != nil {
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
	s, pool := store(t)

	for name, p := range map[string]*payment.Payment{
		"created": kept(t, s, first),
		"payable": payableKept(t, s, first),
		"settled": paidFor(t, s, pool, first),
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
	s, pool := store(t)
	settledPayment := paidFor(t, s, pool, first)
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
	s, pool := store(t)
	p := paidFor(t, s, pool, first)

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
	s, pool := store(t)
	p := paidFor(t, s, pool, first)
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
	p := paidFor(t, s, pool, first)
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
	s, pool := store(t)
	p := paidFor(t, s, pool, first)
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
	p := paidFor(t, s, pool, first)
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

// sending is a refund of p with a transfer of it seen on the chain, judged.
func sending(t *testing.T, s *payment.Postgres, account payment.AccountID, p *payment.Payment,
	r *payment.Refund, tx string, value string, to, from payment.Address, at time.Time) payment.SeenRefund {
	t.Helper()
	hits, err := s.RefundsConsumed(t.Context(), p.Network(), []string{r.Key()})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("RefundsConsumed found %d refunds for one key", len(hits))
	}
	transfer := payment.Transfer{
		Scheme: r.Scheme(), Asset: p.Asset().Reference(), Key: r.Key(),
		Authorizer: string(from), From: string(from), To: string(to), Value: value,
		Tx: tx, BlockHeight: 10, BlockHash: "block" + tx, BlockTime: at,
	}
	reason, judged := payment.JudgeRefund(transfer, hits[0].Refund, hits[0].Payment)
	if !judged {
		t.Fatalf("the transfer was not judged against the refund")
	}
	return payment.SeenRefund{
		RefundHit: hits[0], Transfer: transfer, Reason: reason, Implementation: "implementation",
	}
}

func TestRefund_JudgesATransferAgainstTheRefundItSpends(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	p := paidFor(t, s, pool, first)
	r := refunding(t, s, first, p, "0.5")
	half := r.Amount().Amount().String()
	to, from := r.Destination(), p.Destination()

	for name, c := range map[string]struct {
		value    string
		to, from payment.Address
		at       time.Time
		want     payment.Reason
	}{
		"what the refund says":  {half, to, from, now, payment.Matched},
		"somewhere else":        {half, address(t, "d"), from, now, payment.WrongTo},
		"out of another wallet": {half, to, address(t, "e"), now, payment.WrongFrom},
		"less than it says":     {"1", to, from, now, payment.Short},
		"more than it says":     {half + "0", to, from, now, payment.Over},
		"after the deadline":    {half, to, from, r.ExpiresAt(), payment.Late},
	} {
		t.Run(name, func(t *testing.T) {
			seen := sending(t, s, first, p, r, "0x"+name, c.value, c.to, c.from, c.at)
			if seen.Reason != c.want {
				t.Errorf("the transfer is %s, want %s", seen.Reason, c.want)
			}
		})
	}
}

// recordingRefund hands Record what a round found for a refund, and rolls the
// transaction back rather than leaving it open when the write is refused.
func recordingRefund(t *testing.T, s *payment.Postgres, pool *pgxpool.Pool, at time.Time,
	first, last uint64, final bool, sent ...payment.SeenRefund) error {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Record(t.Context(), tx, network, first, last, final, nil, sent, at); err != nil {
		if rollback := tx.Rollback(t.Context()); rollback != nil {
			t.Fatal(rollback)
		}
		return err
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return nil
}

func TestRefund_RecordsATransferAgainstTheRefundAndNotThePayment(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	p := paidFor(t, s, pool, first)
	r := refunding(t, s, first, p, "0.5")
	seen := sending(t, s, first, p, r, "0xback", r.Amount().Amount().String(),
		r.Destination(), p.Destination(), now)

	if err := recordingRefund(t, s, pool, now, 1, 20, false, seen); err != nil {
		t.Fatal(err)
	}

	// The row stands against the refund, and against no attempt.
	var attempt, refund *string
	if err := pool.QueryRow(t.Context(),
		`select attempt_id, refund_id from observations where tx = $1`, "0xback").
		Scan(&attempt, &refund); err != nil {
		t.Fatal(err)
	}
	if attempt != nil || refund == nil || *refund != r.ID().String() {
		t.Errorf("the row stands against attempt %v and refund %v", attempt, refund)
	}
	// What the payment was paid is what the payment says it was paid: the
	// refund's transfer is not an arrival, and does not become the payment's.
	back, _, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Received(); got.Units() != p.Amount().Units() {
		t.Errorf("the payment received %s, want %s", got.Units(), p.Amount().Units())
	}
	paid, found, err := s.MatchedTransfer(t.Context(), first, p.ID())
	if err != nil || !found {
		t.Fatalf("MatchedTransfer found %v, %v", found, err)
	}
	if paid.Tx == "0xback" {
		t.Error("the payment's transfer is the refund's")
	}
	// And the refund's own reading finds it.
	sent, found, err := s.RefundTransfer(t.Context(), first, p.ID(), r.ID())
	if err != nil || !found || sent.Tx != "0xback" {
		t.Errorf("RefundTransfer = %v, %v, %v", sent.Tx, found, err)
	}
}

func TestRefund_LeavesARefundWhereItIsWhenItsTransferVanishes(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	p := paidFor(t, s, pool, first)
	r := refunding(t, s, first, p, "0.5")
	seen := sending(t, s, first, p, r, "0xback", r.Amount().Amount().String(),
		r.Destination(), p.Destination(), now)
	if err := recordingRefund(t, s, pool, now, 1, 20, true, seen); err != nil {
		t.Fatal(err)
	}

	// A later round over the same finalised blocks no longer carries it.
	if err := recordingRefund(t, s, pool, now.Add(time.Minute), 1, 20, true); err != nil {
		t.Fatal(err)
	}

	back, _, err := s.FindRefund(t.Context(), first, p.ID(), r.ID())
	if err != nil {
		t.Fatal(err)
	}
	if back.Status() != payment.RefundCreated {
		t.Errorf("the refund is %s, want it left where it was", back.Status())
	}
	if _, found, err := s.RefundTransfer(t.Context(), first, p.ID(), r.ID()); err != nil || found {
		t.Errorf("the transfer that vanished is still answered: %v, %v", found, err)
	}
}

func TestRefund_SettlesOnATransferTheChainKeptAndSaysSo(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	p := paidFor(t, s, pool, first)
	r := refunding(t, s, first, p, "0.5")
	seen := sending(t, s, first, p, r, "0xback", r.Amount().Amount().String(),
		r.Destination(), p.Destination(), now)
	if err := recordingRefund(t, s, pool, now, 1, 20, true, seen); err != nil {
		t.Fatal(err)
	}

	candidates, err := s.RefundCandidates(t.Context(), p.Network(), payment.Place{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Tx != "0xback" {
		t.Fatalf("RefundCandidates = %d rows, want the refund's transfer", len(candidates))
	}
	c := candidates[0]
	if err := c.Refund.Settle(); err != nil {
		t.Fatal(err)
	}
	event, err := payment.AnnounceRefund(c.Refund, &seen.Transfer)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRefund(t.Context(), first, c.Refund, c.RefundAt, event); err != nil {
		t.Fatal(err)
	}

	back, _, err := s.FindRefund(t.Context(), first, p.ID(), r.ID())
	if err != nil {
		t.Fatal(err)
	}
	if back.Status() != payment.RefundSucceeded || back.ClosedAt().IsZero() {
		t.Errorf("the refund is %s, closed at %v", back.Status(), back.ClosedAt())
	}
	// What the merchant is told is what a read of the refund answers, less
	// the URL they signed on.
	var told map[string]any
	if err := json.Unmarshal(event.Payload, &told); err != nil {
		t.Fatal(err)
	}
	if event.Name != "refund.succeeded" {
		t.Errorf("event = %q, want refund.succeeded", event.Name)
	}
	if told["id"] != r.ID().String() || told["amount"] != "0.5" || told["status"] != "succeeded" {
		t.Errorf("payload = %v", told)
	}
	if _, has := told["refund_url"]; has {
		t.Errorf("the payload carries the page's URL: %v", told)
	}
	transfer, _ := told["transfer"].(map[string]any)
	if transfer["tx"] != "0xback" {
		t.Errorf("payload transfer = %v, want the transfer that settled it", told["transfer"])
	}
}

func TestRefund_ExpiresOnceTheChainIsReadPastItsDeadlineWithNothingSent(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	p := paidFor(t, s, pool, first)
	r := refunding(t, s, first, p, "0.5")

	// At the deadline the key dies, and the refund waits to see whether
	// anything signed before it arrives.
	due, err := s.OverdueRefunds(t.Context(), p.Network(), r.ExpiresAt(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("OverdueRefunds = %d rows, want the one past its deadline", len(due))
	}
	if err := due[0].Refund.AwaitFinality(); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRefund(t.Context(), first, due[0].Refund, due[0].RefundAt, payment.Event{}); err != nil {
		t.Fatal(err)
	}

	// Nothing expires while the chain has been read only to a moment before
	// the deadline: a transfer signed before it may still be in a block this
	// deployment has not read.
	readPast(t, pool, p.Network(), r.ExpiresAt().Add(-time.Minute))
	over, err := s.UnsettledRefunds(t.Context(), p.Network(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(over) != 0 {
		t.Errorf("UnsettledRefunds = %d rows before the chain was read that far", len(over))
	}
	readPast(t, pool, p.Network(), r.ExpiresAt().Add(time.Minute))

	over, err = s.UnsettledRefunds(t.Context(), p.Network(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(over) != 1 {
		t.Fatalf("UnsettledRefunds = %d rows, want the one nothing settled", len(over))
	}
	if err := over[0].Refund.Expire(); err != nil {
		t.Fatal(err)
	}
	event, err := payment.AnnounceRefund(over[0].Refund, nil)
	if err != nil {
		t.Fatal(err)
	}
	if event.Name != "refund.expired" {
		t.Errorf("event = %q, want refund.expired", event.Name)
	}
	if err := s.SaveRefund(t.Context(), first, over[0].Refund, over[0].RefundAt, event); err != nil {
		t.Fatal(err)
	}
	// And the amount is the payment's to refund again.
	held, err := s.Refunded(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if held != "0" {
		t.Errorf("the refunds hold %s, want an expired one to hold nothing", held)
	}
}

// readPast puts the network's reading position past a moment, the way a round
// of the observer would.
func readPast(t *testing.T, pool *pgxpool.Pool, network payment.Network, at time.Time) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `
		insert into observation_cursors (network, height, hash, block_time, updated_at)
		values ($1, 100, 'block', $2, $2)
		on conflict (network) do update set block_time = $2`, network, at); err != nil {
		t.Fatal(err)
	}
}

func TestRefund_AnnouncesNothingWhileItIsStillOpen(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	p := paidFor(t, s, pool, first)
	r := refunding(t, s, first, p, "0.5")

	event, err := payment.AnnounceRefund(r, nil)

	if err != nil {
		t.Fatal(err)
	}
	if event.Name != "" || len(event.Payload) > 0 {
		t.Errorf("a refund nobody has signed announced %q", event.Name)
	}
	if err := r.AwaitFinality(); err != nil {
		t.Fatal(err)
	}
	if event, err = payment.AnnounceRefund(r, nil); err != nil || event.Name != "" {
		t.Errorf("a refund waiting for finality announced %q, %v", event.Name, err)
	}
}
