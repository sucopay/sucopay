package payment_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sucopay/sucopay/internal/payment"
)

// recordingOn is recording for a network the test names, for the claim that a
// reader of one network is handed nothing of another.
func recordingOn(t *testing.T, s *payment.Postgres, pool *pgxpool.Pool, on payment.Network,
	first, last uint64, final bool, seen ...payment.Seen) {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Record(t.Context(), tx, on, first, last, final, seen, now); err != nil {
		if rollback := tx.Rollback(t.Context()); rollback != nil {
			t.Fatal(rollback)
		}
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// spentElsewhere is an attempt on another network, read back the way a round
// reads one, so that recording it moves the rows it stands against.
func spentElsewhere(t *testing.T, s *payment.Postgres, account payment.AccountID,
	on payment.Network) payment.Hit {
	t.Helper()
	issued := spentOn(t, s, account, on)
	hits, err := s.Consumed(t.Context(), on, []string{issued.Attempt.Key()})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("Consumed found %d attempts for one key", len(hits))
	}
	return hits[0]
}

// settled moves a payment to succeeded, for a candidate that is no longer one.
func settled(t *testing.T, s *payment.Postgres, hit payment.Hit) {
	t.Helper()
	p, at, err := s.Find(t.Context(), hit.Account, hit.Payment.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Succeed(); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), hit.Account, p, at, payment.Event{}); err != nil {
		t.Fatal(err)
	}
}

// A worker deciding what is paid runs for the deployment and not for one
// merchant, so it is handed the rows of every account.
func TestCandidates_ReadsTheMatchedFinalRowsOfEveryAccount(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	mine, theirs := spent(t, s, first), spent(t, s, other)
	if err := recording(t, s, pool, 100, 100, true,
		seenAt(mine, "tx1", 100, payment.Matched),
		seenAt(theirs, "tx2", 100, payment.Matched)); err != nil {
		t.Fatal(err)
	}

	candidates, err := s.Candidates(t.Context(), network, payment.Place{}, 10)

	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("Candidates read %d rows, want the two that were recorded", len(candidates))
	}
	byAccount := map[payment.AccountID]payment.Candidate{}
	for _, c := range candidates {
		byAccount[c.Account] = c
	}
	for account, want := range map[payment.AccountID]struct {
		hit payment.Hit
		tx  string
	}{first: {mine, "tx1"}, other: {theirs, "tx2"}} {
		got, ok := byAccount[account]
		if !ok {
			t.Fatalf("Candidates read nothing under %s", account)
		}
		if got.Payment.ID() != want.hit.Payment.ID() {
			t.Errorf("under %s: the payment of %s, want %s", account, got.Payment.ID(), want.hit.Payment.ID())
		}
		if got.Key != want.hit.Attempt.Key() {
			t.Errorf("under %s: the key of another attempt", account)
		}
		if got.Tx != want.tx || got.BlockHeight != 100 || got.BlockHash != "block"+want.tx {
			t.Errorf("under %s: %s in block %d %s, want %s in block 100 block%s",
				account, got.Tx, got.BlockHeight, got.BlockHash, want.tx, want.tx)
		}
		if err := s.Save(t.Context(), account, got.Payment, got.PaymentAt, payment.Event{}); err != nil {
			t.Errorf("the revision Candidates gave did not save: %v", err)
		}
	}
}

// A payment whose deadline passed is still waiting for what was on its way.
func TestCandidates_ReadsAPaymentWaitingForFinality(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)
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
	if err := recording(t, s, pool, 100, 100, true,
		seenAt(hit, "tx1", 100, payment.Matched)); err != nil {
		t.Fatal(err)
	}

	candidates, err := s.Candidates(t.Context(), network, payment.Place{}, 10)

	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Payment.Status() != payment.AwaitingFinality {
		t.Fatalf("Candidates read %d rows of a payment waiting for finality, want one", len(candidates))
	}
}

// Everything a deciding round has no business asking about: what did not match,
// what was read ahead of finality, what is already settled, what is no longer
// on the chain, and what is on another network.
func TestCandidates_LeavesOutWhatIsNotAMatchedFinalRowOfAnOpenPayment(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	wanted := spent(t, s, first)
	if err := recording(t, s, pool, 100, 100, true,
		seenAt(wanted, "tx1", 100, payment.Matched)); err != nil {
		t.Fatal(err)
	}

	short := spent(t, s, first)
	if err := recording(t, s, pool, 101, 101, true,
		worth(seenAt(short, "tx2", 101, payment.Short), "1")); err != nil {
		t.Fatal(err)
	}

	ahead := spent(t, s, first)
	if err := recording(t, s, pool, 102, 102, false,
		seenAt(ahead, "tx3", 102, payment.Matched)); err != nil {
		t.Fatal(err)
	}

	paid := spent(t, s, first)
	if err := recording(t, s, pool, 103, 103, true,
		seenAt(paid, "tx4", 103, payment.Matched)); err != nil {
		t.Fatal(err)
	}
	settled(t, s, paid)

	lost := spent(t, s, first)
	if err := recording(t, s, pool, 104, 104, true,
		seenAt(lost, "tx5", 104, payment.Matched)); err != nil {
		t.Fatal(err)
	}
	if err := recording(t, s, pool, 104, 104, true); err != nil {
		t.Fatal(err)
	}

	elsewhere := spentElsewhere(t, s, first, "ethereum")
	recordingOn(t, s, pool, "ethereum", 105, 105, true,
		seenAt(elsewhere, "tx6", 105, payment.Matched))

	candidates, err := s.Candidates(t.Context(), network, payment.Place{}, 10)

	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("Candidates read %d rows, want only the matched final row of the open payment", len(candidates))
	}
	if candidates[0].Tx != "tx1" {
		t.Errorf("Candidates read %s, want tx1", candidates[0].Tx)
	}
}

// What a round decides is gone is taken back the same way the observer takes
// back what it can no longer see: the row says so, the attempt is free to be
// spent again, and the payment no longer says money arrived.
func TestVanish_MarksTheRowAndTakesBackTheAttemptAndWhatArrived(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)
	if err := recording(t, s, pool, 100, 100, true,
		seenAt(hit, "tx1", 100, payment.Matched)); err != nil {
		t.Fatal(err)
	}
	at := now.Add(time.Hour)

	if err := s.Vanish(t.Context(), network, hit.Attempt.Key(), "tx1", at); err != nil {
		t.Fatal(err)
	}

	var reason string
	var lastSeen time.Time
	if err := pool.QueryRow(t.Context(), `
		select reason, seen_at from observations where network = $1 and tx = $2`,
		network, "tx1").Scan(&reason, &lastSeen); err != nil {
		t.Fatal(err)
	}
	if reason != string(payment.Vanished) {
		t.Errorf("the row reads %s, want %s", reason, payment.Vanished)
	}
	if !lastSeen.Equal(at) {
		t.Errorf("the row was last seen at %s, want %s", lastSeen, at)
	}
	if got := attemptNow(t, s, hit).Status(); got != payment.Issued {
		t.Errorf("the attempt is %s, want issued", got)
	}
	p, _, err := s.Find(t.Context(), first, hit.Payment.ID())
	if err != nil {
		t.Fatal(err)
	}
	if p.Received().IsSet() {
		t.Errorf("the payment says %s arrived, want nothing", p.Received().Amount())
	}
}

// finalAt is when the observer stamped the row as seen inside the finalised
// range, which is the mark a round reads to pick what to ask about.
func finalAt(t *testing.T, pool *pgxpool.Pool, tx string) time.Time {
	t.Helper()
	var stamped *time.Time
	if err := pool.QueryRow(t.Context(), `
		select final_at from observations where network = $1 and tx = $2`,
		network, tx).Scan(&stamped); err != nil {
		t.Fatal(err)
	}
	if stamped == nil {
		t.Fatalf("the row for %s says nothing about being seen inside the finalised range", tx)
	}
	return *stamped
}

// The mark belongs to the reading that wrote it. A round reads it to pick what
// to ask the endpoints about and writes nothing over it, so a transfer that was
// given up on still says when the chain had settled the block it was seen in.
func TestVanish_KeepsTheMarkSayingWhenTheRowWasSeenFinal(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)
	if err := recording(t, s, pool, 100, 100, true,
		seenAt(hit, "tx1", 100, payment.Matched)); err != nil {
		t.Fatal(err)
	}
	stamped := finalAt(t, pool, "tx1")

	if err := s.Vanish(t.Context(), network, hit.Attempt.Key(), "tx1", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	if got := finalAt(t, pool, "tx1"); !got.Equal(stamped) {
		t.Errorf("the row reads %s, want the %s the observer wrote", got, stamped)
	}
}

// Two transfers can pay one payment, and one of them going leaves the other
// standing. What it says arrived stays on the payment.
func TestVanish_KeepsWhatAnotherStandingRowSaysArrived(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)
	if err := recording(t, s, pool, 100, 100, true,
		seenAt(hit, "tx1", 100, payment.Matched),
		seenAt(hit, "tx2", 100, payment.Matched)); err != nil {
		t.Fatal(err)
	}

	if err := s.Vanish(t.Context(), network, hit.Attempt.Key(), "tx1", now); err != nil {
		t.Fatal(err)
	}

	p, _, err := s.Find(t.Context(), first, hit.Payment.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !p.Received().IsSet() {
		t.Error("the payment says nothing arrived, want what the standing transfer carried")
	}
	if got := attemptNow(t, s, hit).Status(); got != payment.Confirming {
		t.Errorf("the attempt is %s, want it left confirming", got)
	}
}

// A round that decided twice about the same transfer, or that was handed a
// transaction the network has no row for, writes nothing and says nothing is
// wrong.
func TestVanish_DoesNothingForARowThatIsNotStanding(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)
	if err := recording(t, s, pool, 100, 100, true,
		seenAt(hit, "tx1", 100, payment.Matched)); err != nil {
		t.Fatal(err)
	}
	if err := s.Vanish(t.Context(), network, hit.Attempt.Key(), "tx1", now); err != nil {
		t.Fatal(err)
	}

	for what, call := range map[string]func() error{
		"a row that vanished already": func() error {
			return s.Vanish(t.Context(), network, hit.Attempt.Key(), "tx1", now.Add(time.Hour))
		},
		"a transaction nothing was recorded for": func() error {
			return s.Vanish(t.Context(), network, hit.Attempt.Key(), "tx9", now)
		},
		"a network nothing was recorded on": func() error {
			return s.Vanish(t.Context(), "ethereum", hit.Attempt.Key(), "tx1", now)
		},
	} {
		t.Run(what, func(t *testing.T) {
			if err := call(); err != nil {
				t.Fatalf("Vanish = %v, want none", err)
			}
		})
	}

	var seen time.Time
	if err := pool.QueryRow(t.Context(), `
		select seen_at from observations where network = $1 and tx = $2`,
		network, "tx1").Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if !seen.Equal(now) {
		t.Errorf("the row was last seen at %s, want the moment it vanished at %s", seen, now)
	}
}

// A payment's identifier is that account's, so one account's row and another
// account's payment can carry the same one. Reading them as the same payment
// would put a transfer against a record it has nothing to do with.
func TestCandidates_ReadsTheRowAgainstThePaymentOfItsOwnAccount(t *testing.T) {
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
		       amount, destination, status, metadata, created_at, expires_at
		  from payments where account_id = $2 and id = $3`,
		other, first, hit.Payment.ID()); err != nil {
		t.Fatal(err)
	}

	candidates, err := s.Candidates(t.Context(), network, payment.Place{}, 10)

	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("Candidates read %d rows, want the one of the account the row is under", len(candidates))
	}
	if candidates[0].Account != first {
		t.Errorf("Candidates read the row under %s, want %s", candidates[0].Account, first)
	}
}

// recordMatched records one matched final transfer for a fresh payment, at a
// height of the test's choosing, and hands back the transaction.
func recordMatched(t *testing.T, s *payment.Postgres, pool *pgxpool.Pool, height uint64) string {
	t.Helper()
	hit := spent(t, s, first)
	tx := fmt.Sprintf("tx%d", height)
	if err := recording(t, s, pool, height, height, true,
		seenAt(hit, tx, height, payment.Matched)); err != nil {
		t.Fatal(err)
	}
	return tx
}

// A round asks the endpoints about every candidate it reads, so what it reads
// is what it costs. The bound is the caller's.
func TestCandidates_ReadsAtMostWhatItWasAskedFor(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	for height := uint64(100); height < 103; height++ {
		recordMatched(t, s, pool, height)
	}

	candidates, err := s.Candidates(t.Context(), network, payment.Place{}, 2)

	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("Candidates read %d of three rows, want the two it was asked for", len(candidates))
	}
}

// The block and the transaction put the rows in an order, and a caller that
// hands back where it stopped is given what comes after. A round that read the
// same rows every time would never reach the ones behind them.
func TestCandidates_GoesOnFromWhereACallerStopped(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	var txs []string
	for height := uint64(100); height < 103; height++ {
		txs = append(txs, recordMatched(t, s, pool, height))
	}

	first, err := s.Candidates(t.Context(), network, payment.Place{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].Tx != txs[0] || first[1].Tx != txs[1] {
		t.Fatalf("Candidates read %v, want %v in the order of their blocks", shown(first), txs[:2])
	}

	rest, err := s.Candidates(t.Context(), network,
		payment.Place{BlockHeight: first[1].BlockHeight, Tx: first[1].Tx}, 2)

	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || rest[0].Tx != txs[2] {
		t.Errorf("Candidates read %v after the second, want only %s", shown(rest), txs[2])
	}
}

// shown is the transactions a read came back with, for a failure to name.
func shown(candidates []payment.Candidate) []string {
	txs := make([]string, 0, len(candidates))
	for _, c := range candidates {
		txs = append(txs, c.Tx)
	}
	return txs
}

// The same rows a round would read, counted. Whoever is looking at a
// deployment from outside the process is told how much is waiting, not which.
func TestUndecided_CountsTheRowsWaitingForAnAnswer(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	if waiting, err := s.Undecided(t.Context(), network); err != nil || waiting != 0 {
		t.Fatalf("Undecided = %d, %v; want none on an empty network", waiting, err)
	}
	for height := uint64(100); height < 102; height++ {
		recordMatched(t, s, pool, height)
	}
	short := spent(t, s, first)
	if err := recording(t, s, pool, 102, 102, true,
		worth(seenAt(short, "tx102short", 102, payment.Short), "1")); err != nil {
		t.Fatal(err)
	}

	waiting, err := s.Undecided(t.Context(), network)

	if err != nil {
		t.Fatal(err)
	}
	if waiting != 2 {
		t.Errorf("Undecided = %d, want the two that would be read", waiting)
	}
}

// Disagreement is written the first time and kept from then, so that the
// time doctor sees is when it started; agreement takes it away.
func TestDisagreeing_CountsTheRowsTheEndpointsDisagreeAboutUntilTheyAgree(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)
	if err := recording(t, s, pool, 100, 100, true, seenAt(hit, "tx100", 100, payment.Matched)); err != nil {
		t.Fatal(err)
	}
	recordMatched(t, s, pool, 101)
	first := now.Add(-time.Hour)
	if err := s.Disagree(t.Context(), network, hit.Attempt.Key(), "tx100", first); err != nil {
		t.Fatal(err)
	}
	if err := s.Disagree(t.Context(), network, hit.Attempt.Key(), "tx100", now); err != nil {
		t.Fatal(err)
	}

	disagreeing, err := s.Disagreeing(t.Context(), network)

	if err != nil {
		t.Fatal(err)
	}
	if disagreeing != 1 {
		t.Errorf("Disagreeing = %d, want the one row the endpoints disagree about", disagreeing)
	}
	var since time.Time
	if err := pool.QueryRow(t.Context(), `select disagreed_at from observations where tx = 'tx100'`).Scan(&since); err != nil {
		t.Fatal(err)
	}
	if !since.Equal(first) {
		t.Errorf("disagreed_at = %v, want the first time, %v", since, first)
	}
	if err := s.Agree(t.Context(), network, hit.Attempt.Key(), "tx100"); err != nil {
		t.Fatal(err)
	}
	if disagreeing, err := s.Disagreeing(t.Context(), network); err != nil || disagreeing != 0 {
		t.Errorf("Disagreeing = %d, %v; want none once they agree", disagreeing, err)
	}
}

// Counted across accounts, like the rows it counts. A number that left one
// merchant's transfers out would say a deployment was getting somewhere while
// theirs sat still.
func TestUndecided_CountsTheRowsOfEveryAccount(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	mine, theirs := spent(t, s, first), spent(t, s, other)
	// Both in one round: a round that read the finalised range marks what it
	// recorded there before and can no longer see as gone.
	if err := recording(t, s, pool, 100, 100, true,
		seenAt(mine, "tx1", 100, payment.Matched),
		seenAt(theirs, "tx2", 100, payment.Matched)); err != nil {
		t.Fatal(err)
	}

	waiting, err := s.Undecided(t.Context(), network)

	if err != nil {
		t.Fatal(err)
	}
	if waiting != 2 {
		t.Errorf("Undecided = %d, want one for each account", waiting)
	}
}
