package payment_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sucopay/sucopay/internal/payment"
)

// theSigner is who signed the transfers these tests judge. The contract they
// come from is the asset's own, which jpyc names, and the network is the one
// that asset is on.
const theSigner = "0xpayer"

var network = jpycNetwork()

// judged is a payment awaiting payment, an attempt against it, and a transfer
// that passes every rule. Each test breaks one thing and reads the reason.
func judged(t *testing.T) (payment.Transfer, *payment.Attempt, *payment.Payment) {
	t.Helper()
	p := awaiting(t, time.Now().Add(time.Hour))
	a, err := payment.NewAttempt(p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return payment.Transfer{
		Scheme:      a.Scheme(),
		Asset:       p.Asset().Reference(),
		Key:         a.Key(),
		Authorizer:  theSigner,
		From:        theSigner,
		To:          string(p.Destination()),
		Value:       p.Amount().Amount().String(),
		Tx:          "tx1",
		BlockHeight: 100,
		BlockHash:   "block100",
		BlockTime:   time.Now(),
	}, a, p
}

// jpycNetwork is the network the tests' asset is on, read once so that a test
// naming a network and the asset naming one cannot drift apart.
func jpycNetwork() payment.Network {
	asset, err := payment.NewAsset("polygon", "jpyc-contract", "JPYC", 18)
	if err != nil {
		panic(err)
	}
	return asset.Network()
}

func TestJudge_ReadsTheRulesInOrderAndSaysWhatFirstFailed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		change func(*testing.T, *payment.Transfer)
		want   payment.Reason
	}{
		{"a transfer that passes every rule", func(*testing.T, *payment.Transfer) {}, payment.Matched},
		{"another asset's contract", func(t *testing.T, transfer *payment.Transfer) {
			transfer.Asset = usdc(t).Reference()
		}, payment.WrongAsset},
		{"another destination", func(_ *testing.T, t *payment.Transfer) { t.To = "0xsomebodyelse" }, payment.WrongTo},
		{"less than the payment asks for", func(_ *testing.T, t *payment.Transfer) { t.Value = "999" }, payment.Short},
		{"a value that is not a number", func(_ *testing.T, t *payment.Transfer) { t.Value = "many" }, payment.Short},
		{"another destination and too little", func(_ *testing.T, t *payment.Transfer) {
			t.To, t.Value = "0xsomebodyelse", "1"
		}, payment.WrongTo},
		{"another asset and another destination", func(t *testing.T, transfer *payment.Transfer) {
			transfer.Asset, transfer.To = usdc(t).Reference(), "0xsomebodyelse"
		}, payment.WrongAsset},
		{"more than the payment asks for", func(_ *testing.T, t *payment.Transfer) { t.Value = "100000" }, payment.Matched},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			transfer, a, p := judged(t)
			c.change(t, &transfer)

			reason, evidence := payment.Judge(transfer, a, p)

			if !evidence {
				t.Fatal("the transfer was not read as this attempt's")
			}
			if reason != c.want {
				t.Errorf("Judge = %q, want %q", reason, c.want)
			}
		})
	}
}

// A payment that has finished waiting takes nothing more, and neither does one
// that was paid. A transfer that arrives for one of those is recorded and left
// against it, and what to do about the money is the merchant's to decide.
func TestJudge_CallsATransferLateWhenNothingCanArriveForThePaymentAnyMore(t *testing.T) {
	t.Parallel()
	for what, end := range map[string]func(*payment.Payment) error{
		"one that finished waiting": func(p *payment.Payment) error {
			if err := p.AwaitFinality(p.ExpiresAt()); err != nil {
				return err
			}
			return p.Expire()
		},
		"one that was paid": func(p *payment.Payment) error {
			if err := p.Receive(p.Amount()); err != nil {
				return err
			}
			return p.Succeed()
		},
		"one that failed": func(p *payment.Payment) error { return p.Fail() },
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			transfer, a, p := judged(t)
			if err := end(p); err != nil {
				t.Fatal(err)
			}

			reason, evidence := payment.Judge(transfer, a, p)

			if !evidence || reason != payment.Late {
				t.Errorf("Judge = %q, %v; want %q on a %s payment", reason, evidence, payment.Late, p.Status())
			}
		})
	}
}

// The key an attempt holds dies at the payment's deadline, so a transfer the
// chain carried at all was authorised while the payment was open. A payment
// that stopped being payable is waiting for that transfer, and a round that
// reads it while the payment waits reads the payment's own.
func TestJudge_MatchesATransferAgainstAPaymentWaitingForFinality(t *testing.T) {
	t.Parallel()
	transfer, a, p := judged(t)
	if err := p.AwaitFinality(p.ExpiresAt()); err != nil {
		t.Fatal(err)
	}

	reason, evidence := payment.Judge(transfer, a, p)

	if !evidence || reason != payment.Matched {
		t.Errorf("Judge = %q, %v; want %q on a %s payment", reason, evidence, payment.Matched, p.Status())
	}
}

// A transfer is paired with the attempt whose key it spent. Judging one
// against another attempt would say something about a payment that has nothing
// to do with it.
func TestJudge_RefusesToReadATransferAsAnotherAttemptsEvidence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		change func(*payment.Transfer)
	}{
		{"another key", func(t *payment.Transfer) { t.Key = "00" }},
		{"another scheme", func(t *payment.Transfer) { t.Scheme = "permit" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			transfer, a, p := judged(t)
			c.change(&transfer)

			if reason, evidence := payment.Judge(transfer, a, p); evidence {
				t.Errorf("Judge = %q, true; want it read as another attempt's", reason)
			}
		})
	}
	transfer, a, _ := judged(t)
	if _, evidence := payment.Judge(transfer, a, nil); evidence {
		t.Error("a transfer was judged against no payment")
	}
	if _, evidence := payment.Judge(transfer, nil, nil); evidence {
		t.Error("a transfer was judged against no attempt")
	}
}

// spent is a payment made payable, an attempt against it, and the hit a
// reader gets back for the attempt's key.
func spent(t *testing.T, s *payment.Postgres, account payment.AccountID) payment.Hit {
	t.Helper()
	p := payableKept(t, s, account)
	a, err := payment.NewAttempt(p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Issue(t.Context(), account, a); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Consumed(t.Context(), p.Network(), []string{a.Key()})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("Consumed found %d attempts for one key", len(hits))
	}
	return hits[0]
}

// seenAt is what a round hands Record for a hit: the transfer the chain
// carried, at a height, judged.
func seenAt(hit payment.Hit, tx string, height uint64, reason payment.Reason) payment.Seen {
	return payment.Seen{
		Hit: hit,
		Transfer: payment.Transfer{
			Scheme:      hit.Attempt.Scheme(),
			Asset:       hit.Payment.Asset().Reference(),
			Key:         hit.Attempt.Key(),
			Authorizer:  theSigner,
			From:        theSigner,
			To:          string(hit.Payment.Destination()),
			Value:       hit.Payment.Amount().Amount().String(),
			Tx:          tx,
			BlockHeight: height,
			BlockHash:   "block" + tx,
			BlockTime:   now,
		},
		Reason:         reason,
		Implementation: "implementation",
	}
}

// recording runs Record in a transaction of its own and commits it, the way a
// round does around the rest of what it writes.
func recording(t *testing.T, s *payment.Postgres, pool *pgxpool.Pool, first, last uint64, final bool, seen ...payment.Seen) error {
	t.Helper()
	return recordingAt(t, s, pool, now, first, last, final, seen...)
}

// recordingAt is the same, at a moment the test chooses.
func recordingAt(t *testing.T, s *payment.Postgres, pool *pgxpool.Pool, at time.Time, first, last uint64, final bool, seen ...payment.Seen) error {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Record(t.Context(), tx, network, first, last, final, seen, at); err != nil {
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

// spentOn is the same, for a payment in an asset on another network. A key is
// unique on its network, so a reader of one network must not be handed the
// attempts of another.
func spentOn(t *testing.T, s *payment.Postgres, account payment.AccountID, elsewhere payment.Network) payment.Hit {
	t.Helper()
	amount, err := payment.ParseMoney(asset(t, elsewhere, "jpyc-contract", "JPYC", 18), one)
	if err != nil {
		t.Fatal(err)
	}
	r := request(t)
	r.Amount = amount
	p, err := payment.New(r, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(t.Context(), account, p); err != nil {
		t.Fatal(err)
	}
	_, at, err := s.Find(t.Context(), account, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Await(); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), account, p, at, payment.Event{}); err != nil {
		t.Fatal(err)
	}
	a, err := payment.NewAttempt(p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Issue(t.Context(), account, a); err != nil {
		t.Fatal(err)
	}
	return payment.Hit{Account: account, Attempt: a, Payment: p}
}

func TestConsumed_ReadsTheAttemptsOfEveryAccountAndNothingElse(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	mine, theirs := spent(t, s, first), spent(t, s, other)

	// A key spent on another network, which a reader of this one must not be
	// handed even when it asks for that key by name.
	elsewhere := spentOn(t, s, first, "ethereum")

	hits, err := s.Consumed(t.Context(), network, []string{
		mine.Attempt.Key(), theirs.Attempt.Key(), elsewhere.Attempt.Key(),
		"a key nobody was issued"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("Consumed found %d attempts, want the two that were issued", len(hits))
	}
	byAccount := map[payment.AccountID]payment.Hit{}
	for _, hit := range hits {
		byAccount[hit.Account] = hit
	}
	for account, want := range map[payment.AccountID]payment.Hit{first: mine, other: theirs} {
		got, ok := byAccount[account]
		if !ok {
			t.Fatalf("Consumed found nothing under %s", account)
		}
		if got.Attempt.Key() != want.Attempt.Key() || got.Payment.ID() != want.Payment.ID() {
			t.Errorf("under %s: attempt of %s, want %s", account, got.Payment.ID(), want.Payment.ID())
		}
		if err := s.SaveAttempt(t.Context(), account, got.Attempt, got.AttemptAt); err != nil {
			t.Errorf("the revision Consumed gave did not save: %v", err)
		}
	}
}

// observed reads the row a transfer left, which no method hands back.
func observed(t *testing.T, pool *pgxpool.Pool, tx string) (height int64, hash, reason, implementation string, final *time.Time) {
	t.Helper()
	if err := pool.QueryRow(t.Context(), `
		select block_height, block_hash, reason, implementation, final_at
		  from observations where network = $1 and tx = $2`, network, tx).
		Scan(&height, &hash, &reason, &implementation, &final); err != nil {
		t.Fatal(err)
	}
	return height, hash, reason, implementation, final
}

// rows counts the observations, for the claims about a row being rewritten
// rather than added to.
func rows(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), `select count(*) from observations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// attemptNow reads an attempt back, for the claims about where a round left
// it.
func attemptNow(t *testing.T, s *payment.Postgres, hit payment.Hit) *payment.Attempt {
	t.Helper()
	a, _, err := s.FindAttempt(t.Context(), hit.Account, hit.Payment.ID(), hit.Attempt.ID())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRecord_KeepsOneRowForATransferThatComesBackInAnotherBlock(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)

	if err := recording(t, s, pool, 100, 100, false, seenAt(hit, "tx1", 100, payment.Matched)); err != nil {
		t.Fatal(err)
	}
	if err := recording(t, s, pool, 101, 101, false, seenAt(hit, "tx1", 101, payment.Matched)); err != nil {
		t.Fatal(err)
	}

	if n := rows(t, pool); n != 1 {
		t.Errorf("%d rows for one transfer, want one", n)
	}
	height, hash, reason, _, _ := observed(t, pool, "tx1")
	if height != 101 || hash != "blocktx1" {
		t.Errorf("the row is at %d %q, want the block it came back in", height, hash)
	}
	if reason != string(payment.Matched) {
		t.Errorf("the row reads %q", reason)
	}
}

func TestRecord_StampsWhatItSawInTheFinalisedRangeOnce(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)

	if err := recording(t, s, pool, 100, 100, false, seenAt(hit, "tx1", 100, payment.Matched)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, final := observed(t, pool, "tx1"); final != nil {
		t.Errorf("a transfer seen ahead of finality is stamped final at %s", final)
	}

	if err := recording(t, s, pool, 100, 100, true, seenAt(hit, "tx1", 100, payment.Matched)); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, first_ := observed(t, pool, "tx1")
	if first_ == nil {
		t.Fatal("a transfer seen in the finalised range is not stamped")
	}
	later := now.Add(time.Minute)
	if err := recordingAt(t, s, pool, later, 100, 100, true, seenAt(hit, "tx1", 100, payment.Matched)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, again := observed(t, pool, "tx1"); !again.Equal(*first_) {
		t.Errorf("the stamp moved from %s to %s, and a block at or below the final one does not change", first_, again)
	}
}

func TestRecord_ConfirmsTheAttemptOfAMatchedTransfer(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)

	if err := recording(t, s, pool, 100, 100, true, seenAt(hit, "tx1", 100, payment.Matched)); err != nil {
		t.Fatal(err)
	}

	a := attemptNow(t, s, hit)
	if a.Status() != payment.Confirming || a.Authorizer() != theSigner {
		t.Errorf("the attempt is %s signed by %q", a.Status(), a.Authorizer())
	}
}

func TestRecord_LeavesTheAttemptAloneForATransferThatFailedARule(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)

	if err := recording(t, s, pool, 100, 100, true, seenAt(hit, "tx1", 100, payment.WrongTo)); err != nil {
		t.Fatal(err)
	}

	if a := attemptNow(t, s, hit); a.Status() != payment.Issued {
		t.Errorf("the attempt is %s after a transfer that failed a rule", a.Status())
	}
	if _, _, reason, _, _ := observed(t, pool, "tx1"); reason != string(payment.WrongTo) {
		t.Errorf("the row reads %q", reason)
	}
}

func TestRecord_MarksWhatIsNoLongerThereAndTakesTheAttemptBack(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)
	if err := recording(t, s, pool, 100, 100, true, seenAt(hit, "tx1", 100, payment.Matched)); err != nil {
		t.Fatal(err)
	}

	// The same span, read again with nothing in it.
	back := attemptNow(t, s, hit)
	hit.Attempt, _, _ = s.FindAttempt(t.Context(), hit.Account, hit.Payment.ID(), hit.Attempt.ID())
	if err := recording(t, s, pool, 100, 100, true); err != nil {
		t.Fatal(err)
	}

	if _, _, reason, _, _ := observed(t, pool, "tx1"); reason != string(payment.Vanished) {
		t.Errorf("the row of a transfer nobody can see reads %q", reason)
	}
	if a := attemptNow(t, s, hit); a.Status() != payment.Issued || a.Authorizer() != "" {
		t.Errorf("the attempt is %s signed by %q, want issued and nobody", a.Status(), a.Authorizer())
	}
	if back.Status() != payment.Confirming {
		t.Fatalf("the attempt was %s before the transfer vanished", back.Status())
	}

	// And back again, in a later block.
	hit.Attempt, hit.AttemptAt, _ = s.FindAttempt(t.Context(), hit.Account, hit.Payment.ID(), hit.Attempt.ID())
	if err := recording(t, s, pool, 101, 101, true, seenAt(hit, "tx1", 101, payment.Matched)); err != nil {
		t.Fatal(err)
	}
	if _, _, reason, _, _ := observed(t, pool, "tx1"); reason != string(payment.Matched) {
		t.Errorf("the row of a transfer that came back reads %q", reason)
	}
	if a := attemptNow(t, s, hit); a.Status() != payment.Confirming {
		t.Errorf("the attempt is %s after the transfer came back", a.Status())
	}
	if n := rows(t, pool); n != 1 {
		t.Errorf("%d rows after a transfer went and came back, want one", n)
	}
}

func TestRecord_LeavesAnIssuedAttemptWhereItIsWhenARowVanishes(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)
	if err := recording(t, s, pool, 100, 100, true, seenAt(hit, "tx1", 100, payment.WrongTo)); err != nil {
		t.Fatal(err)
	}

	if err := recording(t, s, pool, 100, 100, true); err != nil {
		t.Fatal(err)
	}

	if _, _, reason, _, _ := observed(t, pool, "tx1"); reason != string(payment.Vanished) {
		t.Errorf("the row reads %q", reason)
	}
	if a := attemptNow(t, s, hit); a.Status() != payment.Issued {
		t.Errorf("the attempt is %s", a.Status())
	}
}

func TestRecord_TouchesNothingAheadOfFinality(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)
	if err := recording(t, s, pool, 100, 100, true, seenAt(hit, "tx1", 100, payment.Matched)); err != nil {
		t.Fatal(err)
	}

	// A round that read ahead of finality and saw nothing where the transfer
	// is. What it cannot see may only not have arrived yet.
	if err := recording(t, s, pool, 100, 100, false); err != nil {
		t.Fatal(err)
	}

	if _, _, reason, _, _ := observed(t, pool, "tx1"); reason != string(payment.Matched) {
		t.Errorf("a round ahead of finality left the row reading %q", reason)
	}
	if a := attemptNow(t, s, hit); a.Status() != payment.Confirming {
		t.Errorf("a round ahead of finality left the attempt %s", a.Status())
	}
}

func TestRecord_RefusesARevisionThatMoved(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)
	// Somebody else moved the attempt after this round read it.
	moved := attemptNow(t, s, hit)
	if err := moved.Confirm(theSigner); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAttempt(t.Context(), hit.Account, moved, hit.AttemptAt); err != nil {
		t.Fatal(err)
	}

	err := recording(t, s, pool, 100, 100, true, seenAt(hit, "tx1", 100, payment.Matched))

	if !errors.Is(err, payment.ErrStale) {
		t.Fatalf("Record gave %v, want %v", err, payment.ErrStale)
	}
	if n := rows(t, pool); n != 0 {
		t.Errorf("%d rows after a round that was rolled back, want none", n)
	}
}

func TestRecord_WritesTheImplementationItWasGiven(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)
	none := seenAt(hit, "tx1", 100, payment.Matched)
	none.Implementation = ""

	if err := recording(t, s, pool, 100, 100, true, none); err != nil {
		t.Fatal(err)
	}

	if _, _, _, implementation, _ := observed(t, pool, "tx1"); implementation != "" {
		t.Errorf("the row holds %q, and the chain said nothing", implementation)
	}
}

// A key is spent once, so two matched rows for one attempt mean the same
// authorisation came back in another transaction. The row that went is marked,
// and the attempt stays where the row that arrived put it.
func TestRecord_KeepsTheAttemptConfirmingWhileAnotherRowStillMatches(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)
	if err := recording(t, s, pool, 100, 100, true, seenAt(hit, "tx1", 100, payment.Matched)); err != nil {
		t.Fatal(err)
	}

	hit.Attempt, hit.AttemptAt, _ = s.FindAttempt(t.Context(), hit.Account, hit.Payment.ID(), hit.Attempt.ID())
	if err := recording(t, s, pool, 100, 100, true, seenAt(hit, "tx2", 100, payment.Matched)); err != nil {
		t.Fatal(err)
	}

	if _, _, reason, _, _ := observed(t, pool, "tx1"); reason != string(payment.Vanished) {
		t.Errorf("the transaction that went reads %q", reason)
	}
	if _, _, reason, _, _ := observed(t, pool, "tx2"); reason != string(payment.Matched) {
		t.Errorf("the transaction that arrived reads %q", reason)
	}
	if a := attemptNow(t, s, hit); a.Status() != payment.Confirming {
		t.Errorf("the attempt is %s while a transfer still matches it", a.Status())
	}
}

func TestRecord_RefusesAValueTheColumnCouldNotHold(t *testing.T) {
	t.Parallel()
	// What an adapter hands over is a string, and the column holds the digits
	// of an asset's smallest unit. A round that wrote one of these would have
	// stored something other than what the chain said.
	for what, value := range map[string]string{
		"one that is not a number":                             "many",
		"one digit more than the widest amount there could be": strings.Repeat("9", 79),
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			s, pool := store(t)
			hit := spent(t, s, first)
			nonsense := seenAt(hit, "tx1", 100, payment.WrongAsset)
			nonsense.Transfer.Value = value

			if err := recording(t, s, pool, 100, 100, true, nonsense); err == nil {
				t.Fatal("a value the column could not hold was written")
			}
			if n := rows(t, pool); n != 0 {
				t.Errorf("%d rows after a round that was rolled back, want none", n)
			}
		})
	}
}

// What a chain says goes into a row a merchant is shown, so the last gate
// before it is stored refuses what nobody could read and what nobody should
// have to store.
func TestRecord_RefusesWhatAProviderShouldNotBeAbleToWrite(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		change func(*payment.Transfer)
	}{
		{"a hash longer than any chain writes", func(t *payment.Transfer) {
			t.BlockHash = strings.Repeat("a", payment.MaxTransferField+1)
		}},
		{"an account holding a character that does not show up", func(t *payment.Transfer) {
			t.From = "0xpayer\u200b"
		}},
		{"a reference holding one", func(t *payment.Transfer) {
			t.Asset = "jpyc-contract\u200b"
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, pool := store(t)
			hit := spent(t, s, first)
			one := seenAt(hit, "tx1", 100, payment.WrongAsset)
			c.change(&one.Transfer)

			if err := recording(t, s, pool, 100, 100, true, one); err == nil {
				t.Fatal("it was written")
			}
			if n := rows(t, pool); n != 0 {
				t.Errorf("%d rows after a round that was rolled back, want none", n)
			}
		})
	}
}

// worth is a transfer of a value the payment did not ask for, so that what
// arrived and what was asked for can be told apart.
func worth(s payment.Seen, value string) payment.Seen {
	s.Transfer.Value = value
	return s
}

// What arrived sits on the payment, so a merchant reading one payment sees it
// without reading the chain. It is put there when the transfer is recorded
// rather than when the payment settles: a transfer short of the amount never
// settles anything, and the difference is what makes the shortfall readable.
func TestRecord_PutsWhatArrivedOnThePayment(t *testing.T) {
	t.Parallel()
	for what, reason := range map[string]payment.Reason{
		"matched": payment.Matched,
		"short":   payment.Short,
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			s, pool := store(t)
			hit := spent(t, s, first)

			if err := recording(t, s, pool, 100, 100, false,
				worth(seenAt(hit, "tx1", 100, reason), "7")); err != nil {
				t.Fatal(err)
			}

			back, _, err := s.Find(t.Context(), first, hit.Payment.ID())
			if err != nil {
				t.Fatal(err)
			}
			if got := back.Received(); !got.IsSet() || got.Amount().String() != "7" {
				t.Errorf("received = %v, want the 7 that arrived", got)
			}
			if got := back.Amount().Amount().String(); got == "7" {
				t.Errorf("the payment asks for %s, which the test cannot tell from what arrived", got)
			}
		})
	}
}

// A transfer that went somewhere else, or arrived for a payment nothing could
// be paid for, is not an arrival for this payment. The row records it and the
// payment says nothing about it.
func TestRecord_LeavesThePaymentWithNothingArrivedForATransferThatIsNotOne(t *testing.T) {
	t.Parallel()
	for what, reason := range map[string]payment.Reason{
		"wrong_to":    payment.WrongTo,
		"wrong_asset": payment.WrongAsset,
		"late":        payment.Late,
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			s, pool := store(t)
			hit := spent(t, s, first)

			if err := recording(t, s, pool, 100, 100, false,
				worth(seenAt(hit, "tx1", 100, reason), "7")); err != nil {
				t.Fatal(err)
			}

			back, _, err := s.Find(t.Context(), first, hit.Payment.ID())
			if err != nil {
				t.Fatal(err)
			}
			if got := back.Received(); got.IsSet() {
				t.Errorf("received = %v, want nothing to have arrived", got)
			}
		})
	}
}

// The same transfer seen again is the same arrival. A round that reads a
// range twice, or reads one that was put back, leaves the payment where the
// first reading left it rather than failing on a second arrival.
func TestRecord_LeavesWhatArrivedAloneWhenTheSameTransferIsSeenAgain(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)
	seen := worth(seenAt(hit, "tx1", 100, payment.Matched), "7")

	if err := recording(t, s, pool, 100, 100, false, seen); err != nil {
		t.Fatal(err)
	}
	// Read again the way a round does, so the second recording is held against
	// a payment parsed from the row rather than the one the first left behind.
	p, at, err := s.Find(t.Context(), first, hit.Payment.ID())
	if err != nil {
		t.Fatal(err)
	}
	hit.Payment, hit.PaymentAt = p, at
	if err := recording(t, s, pool, 100, 100, false,
		worth(seenAt(hit, "tx1", 100, payment.Matched), "7")); err != nil {
		t.Fatalf("the same transfer seen again: %v", err)
	}

	back, _, err := s.Find(t.Context(), first, hit.Payment.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Received(); !got.IsSet() || got.Amount().String() != "7" {
		t.Errorf("received = %v, want the 7 the first reading put there", got)
	}
}

// What arrived goes back when the transfer it came from is no longer on the
// chain. A payment that kept it would say money came that nobody sent, and the
// write-once rule would keep any later arrival out.
func TestRecord_TakesBackWhatArrivedWhenTheTransferIsGone(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)

	if err := recording(t, s, pool, 100, 100, true,
		worth(seenAt(hit, "tx1", 100, payment.Matched), "7")); err != nil {
		t.Fatal(err)
	}
	if err := recording(t, s, pool, 100, 100, true); err != nil {
		t.Fatal(err)
	}

	back, _, err := s.Find(t.Context(), first, hit.Payment.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Received(); got.IsSet() {
		t.Errorf("received = %v, want nothing: the transfer it came from is gone", got)
	}
	if a := attemptNow(t, s, hit); a.Status() != payment.Issued {
		t.Errorf("the attempt is %s, want it back where it was", a.Status())
	}
}

// One row vanishing while another still stands leaves what arrived where it
// is. A range read twice can leave a transfer recorded under one transaction
// and gone from another.
func TestRecord_KeepsWhatArrivedWhileAnotherRowStillStands(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)

	if err := recording(t, s, pool, 100, 101, true,
		worth(seenAt(hit, "tx1", 100, payment.Matched), "7"),
		worth(seenAt(hit, "tx2", 101, payment.Matched), "7")); err != nil {
		t.Fatal(err)
	}
	if err := recording(t, s, pool, 100, 101, true,
		worth(seenAt(hit, "tx2", 101, payment.Matched), "7")); err != nil {
		t.Fatal(err)
	}

	back, _, err := s.Find(t.Context(), first, hit.Payment.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Received(); !got.IsSet() || got.Amount().String() != "7" {
		t.Errorf("received = %v, want the 7 the standing row still says arrived", got)
	}
}

// A payment nothing can arrive for is left alone rather than failing the
// round. Judge answers short before it looks at the status, so a transfer
// short of the amount is judged short whatever the payment has become; a
// round that failed on one would never move its cursor past it.
func TestRecord_LeavesAPaymentNothingCanArriveForAlone(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)
	if err := hit.Payment.Fail(); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), first, hit.Payment, hit.PaymentAt, payment.Event{}); err != nil {
		t.Fatal(err)
	}
	again, at, err := s.Find(t.Context(), first, hit.Payment.ID())
	if err != nil {
		t.Fatal(err)
	}
	hit.Payment, hit.PaymentAt = again, at

	err = recording(t, s, pool, 100, 100, false, worth(seenAt(hit, "tx1", 100, payment.Short), "1"))

	if err != nil {
		t.Fatalf("the round failed on a payment nothing can arrive for: %v", err)
	}
	if _, _, reason, _, _ := observed(t, pool, "tx1"); reason != string(payment.Short) {
		t.Errorf("the row reads %q, want the transfer recorded all the same", reason)
	}
}

// A hit is read before a round begins, so the payment on it can be older than
// the row by the time the round writes. What arrived is decided from the row,
// not from the copy: deciding from the copy would write the payment twice from
// two readings that each still said nothing arrived, and the second would lose
// on the revision and take the whole round with it.
func TestRecord_DecidesWhatArrivedFromTheRowRatherThanTheHit(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)
	// A second reading of the same attempt, so the round holds two payments
	// that each say nothing has arrived. Reading the attempt again is what a
	// round does for every key it finds consumed.
	hits, err := s.Consumed(t.Context(), hit.Payment.Network(), []string{hit.Attempt.Key()})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("Consumed found %d attempts for one key", len(hits))
	}
	again := hits[0]

	// The second is short, so only the first moves the attempt: what an
	// attempt does with two readings of itself is its own question, and this
	// one is about the payment. A chain cannot spend one key twice, so the
	// pairing is made here rather than found; the path is guarded for the
	// attempt statuses that submission and finality add, which will let one
	// payment hold more than one.
	err = recording(t, s, pool, 100, 101, false,
		worth(seenAt(hit, "tx1", 100, payment.Matched), "7"),
		worth(seenAt(again, "tx2", 101, payment.Short), "7"))

	if err != nil {
		t.Fatalf("a round holding two payments read before it began: %v", err)
	}
	back, _, err := s.Find(t.Context(), first, hit.Payment.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Received(); !got.IsSet() || got.Amount().String() != "7" {
		t.Errorf("received = %v, want the 7 the first reading put there", got)
	}
}

// A value the rules could not read is already evidence: Judge calls it short
// and the row holds it as the chain wrote it. Reading it again here and
// failing would abort a round over something already recorded.
func TestRecord_LeavesThePaymentAloneForAValueItCannotRead(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)

	err := recording(t, s, pool, 100, 100, false, worth(seenAt(hit, "tx1", 100, payment.Short), "1.5"))

	if err != nil {
		t.Fatalf("the round failed on a value the rules had already judged: %v", err)
	}
	back, _, err := s.Find(t.Context(), first, hit.Payment.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Received(); got.IsSet() {
		t.Errorf("received = %v, want nothing: the value could not be read", got)
	}
}

// A payment that has been paid keeps what paid it. Restore refuses a succeeded
// row that nothing covers, so clearing one would leave a row nothing can read
// back. Evidence that vanished under a payment already called paid is a
// question about that payment.
func TestRecord_KeepsWhatPaidAPaymentThatIsAlreadyPaid(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	hit := spent(t, s, first)
	if err := recording(t, s, pool, 100, 100, true, seenAt(hit, "tx1", 100, payment.Matched)); err != nil {
		t.Fatal(err)
	}
	paid, at, err := s.Find(t.Context(), first, hit.Payment.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := paid.Succeed(); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), first, paid, at, payment.Event{}); err != nil {
		t.Fatal(err)
	}

	if err := recording(t, s, pool, 100, 100, true); err != nil {
		t.Fatalf("a transfer vanishing under a paid payment: %v", err)
	}

	back, _, err := s.Find(t.Context(), first, hit.Payment.ID())
	if err != nil {
		t.Fatalf("the paid payment cannot be read back: %v", err)
	}
	if got := back.Received(); !got.IsSet() {
		t.Errorf("received = %v, want what paid it to still be there", got)
	}
}
