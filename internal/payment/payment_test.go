package payment_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/payment"
)

var now = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// stored is a row that loads, so that a test changing a single field is
// testing that field.
func stored(t *testing.T, id payment.ID, status payment.Status, createdAt, expiresAt time.Time) payment.Stored {
	t.Helper()
	r := request(t)
	received := payment.Money{}
	if status == payment.Succeeded {
		received = r.Amount
	}
	return payment.Stored{
		ID:          id,
		Amount:      r.Amount,
		Received:    received,
		Destination: r.Destination,
		Metadata:    r.Metadata,
		Status:      status,
		CreatedAt:   createdAt,
		ExpiresAt:   expiresAt,
	}
}

// request is one that opens, so that a test changing a single field is testing
// that field.
func request(t *testing.T) payment.Request {
	t.Helper()
	amount, err := payment.ParseMoney(jpyc(t), one)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := payment.ParseAddress("0x" + strings.Repeat("ab", 20))
	if err != nil {
		t.Fatal(err)
	}
	return payment.Request{
		Amount:      amount,
		Destination: destination,
		Metadata:    map[string]string{"order": "A-1"},
		ExpiresAt:   now.Add(time.Hour),
	}
}

func open(t *testing.T) *payment.Payment {
	t.Helper()
	p, err := payment.New(request(t), now)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// payable returns a payment a customer could pay, which is where most of the
// lifecycle starts.
func payable(t *testing.T) *payment.Payment {
	t.Helper()
	p := open(t)
	if err := p.Await(); err != nil {
		t.Fatal(err)
	}
	return p
}

// settling returns a payment that is no longer payable, which is where expiry
// and a late arrival are decided.
func settling(t *testing.T) *payment.Payment {
	t.Helper()
	p := payable(t)
	if err := p.Settle(p.ExpiresAt()); err != nil {
		t.Fatal(err)
	}
	return p
}

// arrived records the exact billed amount turning up, which is the ordinary
// path and a precondition of succeeding.
func arrived(t *testing.T, p *payment.Payment) {
	t.Helper()
	if err := p.Receive(p.Amount()); err != nil {
		t.Fatal(err)
	}
}

func wantProblems(t *testing.T, err error) payment.Problems {
	t.Helper()
	if err == nil {
		t.Fatal("want problems, got none")
	}
	var ps payment.Problems
	if !errors.As(err, &ps) {
		t.Fatalf("want payment.Problems, got %T: %v", err, err)
	}
	return ps
}

func mustFail(t *testing.T, r payment.Request) error {
	t.Helper()
	if _, err := payment.New(r, now); err != nil {
		return err
	}
	t.Fatal("want an error, got none")
	return nil
}

func TestNew_OpensAPaymentNobodyHasPaidYet(t *testing.T) {
	t.Parallel()
	r := request(t)

	p, err := payment.New(r, now)
	if err != nil {
		t.Fatal(err)
	}

	if p.Status() != payment.Created {
		t.Errorf("status = %s, want %s", p.Status(), payment.Created)
	}
	if _, err := payment.ParseID(p.ID().String()); err != nil {
		t.Errorf("the payment was given an id nothing would accept (%q): %v", p.ID(), err)
	}
	if cmp, err := p.Amount().Cmp(r.Amount); err != nil || cmp != 0 {
		t.Errorf("amount = %s, want %s (err %v)", p.Amount(), r.Amount, err)
	}
	if p.Destination() != r.Destination {
		t.Errorf("destination = %s, want %s", p.Destination(), r.Destination)
	}
	if !p.CreatedAt().Equal(now) {
		t.Errorf("created_at = %s, want %s", p.CreatedAt(), now)
	}
	if !p.ExpiresAt().Equal(r.ExpiresAt) {
		t.Errorf("expires_at = %s, want %s", p.ExpiresAt(), r.ExpiresAt)
	}
}

func TestNew_TakesTheNetworkFromTheAssetSoThereIsOneAnswer(t *testing.T) {
	t.Parallel()
	// A payment settles on the chain its asset lives on. Held as two fields it
	// could hold two answers, and nothing would make them agree.
	r := request(t)
	elsewhere, err := payment.ParseMoney(asset(t, "ethereum", "jpyc-contract", "JPYC", 18), one)
	if err != nil {
		t.Fatal(err)
	}
	r.Amount = elsewhere

	p, err := payment.New(r, now)
	if err != nil {
		t.Fatal(err)
	}

	if got := p.Network(); got != "ethereum" {
		t.Errorf("network = %s, want the one its asset lives on", got)
	}
	if p.Network() != p.Asset().Network() {
		t.Errorf("the payment and its asset disagree: %s and %s", p.Network(), p.Asset().Network())
	}
}

func TestNew_MintsAnIdentifierRatherThanTakingOne(t *testing.T) {
	t.Parallel()
	// An identifier somebody else chose is one they can guess, enumerate, or
	// collide with another payment's.
	first, second := open(t), open(t)

	if first.ID() == second.ID() {
		t.Errorf("two payments opened under one id: %s", first.ID())
	}
}

func TestNew_ReportsEveryProblemAtOnce(t *testing.T) {
	t.Parallel()
	// One round trip should tell a caller everything wrong with what they
	// sent, rather than one thing per attempt.
	_, err := payment.New(payment.Request{}, now)

	fields := map[string]bool{}
	for _, p := range wantProblems(t, err) {
		fields[p.Field] = true
	}
	for _, want := range []string{"amount", "destination", "expires_at"} {
		if !fields[want] {
			t.Errorf("nothing was reported about %s: %v", want, err)
		}
	}
}

func TestNew_RefusesWhatIsNotAPayment(t *testing.T) {
	t.Parallel()
	zero, err := payment.ParseMoney(jpyc(t), "0")
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name   string
		change func(*payment.Request)
		field  string
	}{
		{"nothing is being accepted", func(r *payment.Request) { r.Amount = zero }, "amount"},
		{"no amount at all", func(r *payment.Request) { r.Amount = payment.Money{} }, "amount"},
		{"no destination", func(r *payment.Request) { r.Destination = "" }, "destination"},
		{"no deadline", func(r *payment.Request) { r.ExpiresAt = time.Time{} }, "expires_at"},
		{"a deadline already past", func(r *payment.Request) { r.ExpiresAt = now.Add(-time.Second) }, "expires_at"},
		{"a deadline of this instant", func(r *payment.Request) { r.ExpiresAt = now }, "expires_at"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := request(t)
			c.change(&r)

			found := false
			for _, p := range wantProblems(t, mustFail(t, r)) {
				if p.Field == c.field {
					found = true
				}
			}
			if !found {
				t.Errorf("nothing was reported about %s", c.field)
			}
		})
	}
}

func TestNew_RefusesMetadataBeyondWhatItWillStore(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		metadata map[string]string
	}{
		{"too many entries", func() map[string]string {
			m := map[string]string{}
			for i := range payment.MaxMetadataEntries + 1 {
				m[string(rune('a'+i))+"key"] = "v"
			}
			return m
		}()},
		{"an empty key", map[string]string{"": "v"}},
		{"a key too long", map[string]string{strings.Repeat("k", payment.MaxMetadataKeyBytes+1): "v"}},
		{"a value too long", map[string]string{"k": strings.Repeat("v", payment.MaxMetadataValueSize+1)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := request(t)
			r.Metadata = c.metadata

			mustFail(t, r)
		})
	}
}

func TestNew_RefusesMetadataHoldingAnythingThatMovesACursor(t *testing.T) {
	t.Parallel()
	// It is stored, shown in a console, sent in a webhook and read back by
	// whoever supplied it. Refusing it once is the only place that covers all
	// four.
	for _, c := range []struct {
		name  string
		value string
	}{
		{"a newline", "A-1\n  order\tforged"},
		{"an escape sequence", "A-1\x1b[31m"},
		{"a line separator", "A-1\u2028"},
		{"a right-to-left override", "A-1\u202e"},
		{"a zero-width space", "A-1\u200b"},
		{"a left-to-right mark", "A-1\u200e"},
		{"a word joiner", "A-1\u2060"},
		{"a byte order mark", "A-1\ufeff"},
		{"a non-breaking space", "A-1\u00a0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := request(t)
			r.Metadata = map[string]string{"order": c.value}
			mustFail(t, r)

			r = request(t)
			r.Metadata = map[string]string{c.value: "A-1"}
			mustFail(t, r)
		})
	}
}

func TestNew_ReportsEveryBadMetadataEntryInAFixedOrder(t *testing.T) {
	t.Parallel()
	// Ranging over a map reports a different one of them each run, which is a
	// caller fixing one problem at a time and a test that flakes.
	r := request(t)
	r.Metadata = map[string]string{
		"a": strings.Repeat("v", payment.MaxMetadataValueSize+1),
		"b": "A-1\u200b",
		"c": "A-1\n",
	}

	first := wantProblems(t, mustFail(t, r))
	if len(first) < 3 {
		t.Fatalf("reported %d problems, want one per bad entry: %v", len(first), first)
	}
	for range 20 {
		again := wantProblems(t, mustFail(t, r))
		if len(again) != len(first) {
			t.Fatalf("reported %d problems, then %d", len(first), len(again))
		}
		for i := range first {
			if again[i] != first[i] {
				t.Fatalf("problem %d was %v, then %v", i, first[i], again[i])
			}
		}
	}
}

func TestNew_AcceptsMetadataHoldingAnOrdinarySpace(t *testing.T) {
	t.Parallel()
	r := request(t)
	r.Metadata = map[string]string{"customer name": "Ada Lovelace"}

	if _, err := payment.New(r, now); err != nil {
		t.Errorf("refused ordinary text: %v", err)
	}
}

func TestNew_CopiesMetadataSoACallerCannotChangeIt(t *testing.T) {
	t.Parallel()
	r := request(t)
	p, err := payment.New(r, now)
	if err != nil {
		t.Fatal(err)
	}

	r.Metadata["order"] = "changed"

	if got := p.Metadata()["order"]; got != "A-1" {
		t.Errorf("metadata order = %q, want A-1: the caller's map reached inside", got)
	}
}

func TestPayment_MetadataReturnsACopy(t *testing.T) {
	t.Parallel()
	p := open(t)

	p.Metadata()["order"] = "changed"

	if got := p.Metadata()["order"]; got != "A-1" {
		t.Errorf("metadata order = %q, want A-1: what Metadata returned was the map itself", got)
	}
}

func TestPayment_MovesFromOpenedToPaid(t *testing.T) {
	t.Parallel()
	p := open(t)

	if err := p.Await(); err != nil {
		t.Fatalf("becoming payable: %v", err)
	}
	if p.Status() != payment.AwaitingPayment {
		t.Fatalf("status = %s, want %s", p.Status(), payment.AwaitingPayment)
	}
	arrived(t, p)
	if err := p.Succeed(); err != nil {
		t.Fatalf("settling: %v", err)
	}
	if p.Status() != payment.Succeeded {
		t.Errorf("status = %s, want %s", p.Status(), payment.Succeeded)
	}
}

func TestPayment_APayableOneCanEndInFailureAsWellAsSuccess(t *testing.T) {
	t.Parallel()
	// Both are reachable from the one state, and a walk down the happy path
	// alone would not tell Fail from Succeed.
	for _, c := range []struct {
		name string
		move func(*testing.T, *payment.Payment) error
		want payment.Status
	}{
		{"paid", func(t *testing.T, p *payment.Payment) error { arrived(t, p); return p.Succeed() }, payment.Succeeded},
		{"will not settle", func(_ *testing.T, p *payment.Payment) error { return p.Fail() }, payment.Failed},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := payable(t)

			if err := c.move(t, p); err != nil {
				t.Fatalf("err = %v, want none", err)
			}
			if p.Status() != c.want {
				t.Errorf("status = %s, want %s", p.Status(), c.want)
			}
		})
	}
}

func TestPayment_RefusesAMoveTheLifecycleDoesNotAllow(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		setUp func(*testing.T) *payment.Payment
		move  func(*payment.Payment) error
		want  payment.Status
	}{
		{"paid before anyone could pay it", open, (*payment.Payment).Succeed, payment.Created},
		{"failed before anyone could pay it", open, (*payment.Payment).Fail, payment.Created},
		{"made payable twice", payable, (*payment.Payment).Await, payment.AwaitingPayment},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := c.setUp(t)

			if err := c.move(p); err == nil {
				t.Fatal("want an error, got none")
			}
			if p.Status() != c.want {
				t.Errorf("status = %s, want it unchanged at %s", p.Status(), c.want)
			}
		})
	}
}

func TestPayment_RefusesToMoveOnFromAFinalStatus(t *testing.T) {
	t.Parallel()
	p := payable(t)
	arrived(t, p)
	if err := p.Succeed(); err != nil {
		t.Fatal(err)
	}

	err := p.Fail()

	if err == nil {
		t.Fatal("a payment that succeeded then failed")
	}
	if !strings.Contains(err.Error(), "final") {
		t.Errorf("error does not say why: %v", err)
	}
}

func TestSettle_RefusesToStopAcceptingPaymentEarly(t *testing.T) {
	t.Parallel()
	p := payable(t)

	if err := p.Settle(now); err == nil {
		t.Fatal("stopped accepting payment while a customer was still entitled to pay")
	}
	if p.Status() != payment.AwaitingPayment {
		t.Errorf("status = %s, want it unchanged", p.Status())
	}

	if err := p.Settle(p.ExpiresAt()); err != nil {
		t.Fatalf("refused to stop accepting payment at the deadline: %v", err)
	}
	if p.Status() != payment.Settling {
		t.Errorf("status = %s, want %s", p.Status(), payment.Settling)
	}
}

func TestExpire_OnlyOnceThePaymentStoppedBeingPayable(t *testing.T) {
	t.Parallel()
	p := payable(t)

	if err := p.Expire(); err == nil {
		t.Fatal("expired a payment a customer could still pay")
	}
	if p.Status() != payment.AwaitingPayment {
		t.Errorf("status = %s, want it unchanged", p.Status())
	}

	closed := settling(t)
	if err := closed.Expire(); err != nil {
		t.Fatalf("refused to expire a payment nobody could pay any more: %v", err)
	}
	if closed.Status() != payment.Expired {
		t.Errorf("status = %s, want %s", closed.Status(), payment.Expired)
	}
}

func TestSucceed_AcceptsATransferThatLandedAfterTheDeadline(t *testing.T) {
	t.Parallel()
	p := settling(t)
	arrived(t, p)

	if err := p.Succeed(); err != nil {
		t.Fatalf("a transfer landed after the deadline and had nowhere to go: %v", err)
	}
	if p.Status() != payment.Succeeded {
		t.Errorf("status = %s, want %s", p.Status(), payment.Succeeded)
	}
}

func TestReceive_KeepsWhatArrivedApartFromWhatWasBilled(t *testing.T) {
	t.Parallel()
	// An underpayment that overwrote the billed amount would read as a payment
	// for less, and nothing downstream could tell the two apart.
	p := payable(t)
	short, err := payment.ParseMoney(p.Asset(), "1")
	if err != nil {
		t.Fatal(err)
	}

	if err := p.Receive(short); err != nil {
		t.Fatal(err)
	}

	if cmp, err := p.Amount().Cmp(short); err != nil || cmp <= 0 {
		t.Errorf("amount = %s, want it still the billed amount (cmp %d, err %v)", p.Amount(), cmp, err)
	}
	if cmp, err := p.Received().Cmp(short); err != nil || cmp != 0 {
		t.Errorf("received = %s, want %s (err %v)", p.Received(), short, err)
	}
}

func TestReceive_RefusesWhatThisPaymentCannotBeIn(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		money func(*testing.T, *payment.Payment) payment.Money
	}{
		{"nothing at all", func(*testing.T, *payment.Payment) payment.Money { return payment.Money{} }},
		{"another asset", func(t *testing.T, _ *payment.Payment) payment.Money {
			m, err := payment.ParseMoney(asset(t, "ethereum", "usdc-contract", "USDC", 6), one)
			if err != nil {
				t.Fatal(err)
			}
			return m
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := payable(t)

			if err := p.Receive(c.money(t, p)); err == nil {
				t.Fatal("want an error, got none")
			}
			if p.Received().IsSet() {
				t.Errorf("received = %s, want nothing recorded", p.Received())
			}
		})
	}
}

func TestSucceed_ReportsTheMoveBeforeTheMoney(t *testing.T) {
	t.Parallel()
	// Both are wrong for a payment nobody could pay yet. Reporting it as short
	// would send a reader looking for money that was never due.
	p := open(t)

	problems := wantProblems(t, p.Succeed())

	for _, problem := range problems {
		if problem.Field == "status" {
			return
		}
	}
	t.Errorf("nothing was reported about the status: %v", problems)
}

func TestExpire_StillExpiresAnUnderpaymentNobodyToppedUp(t *testing.T) {
	t.Parallel()
	// Underpayment is recorded, not handled: the shortfall stays visible and
	// the payment still ends. A coverage check here, added by analogy with
	// Succeed, would leave short payments open forever.
	p := settling(t)
	short, err := payment.ParseMoney(p.Asset(), "1")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Receive(short); err != nil {
		t.Fatal(err)
	}

	if err := p.Expire(); err != nil {
		t.Fatalf("an underpaid payment could not be expired: %v", err)
	}
	if cmp, err := p.Received().Cmp(short); err != nil || cmp != 0 {
		t.Errorf("received = %s, want the shortfall still visible at %s (err %v)", p.Received(), short, err)
	}
}

func TestReceive_RefusesOnAPaymentNothingCanArriveFor(t *testing.T) {
	t.Parallel()
	// What a finished payment says about itself does not change afterwards,
	// and nothing has been opened for payment yet before it is payable.
	for _, c := range []struct {
		name  string
		setUp func(*testing.T) *payment.Payment
	}{
		{"not payable yet", open},
		{"already paid", func(t *testing.T) *payment.Payment {
			p := payable(t)
			arrived(t, p)
			if err := p.Succeed(); err != nil {
				t.Fatal(err)
			}
			return p
		}},
		{"already expired", func(t *testing.T) *payment.Payment {
			p := settling(t)
			if err := p.Expire(); err != nil {
				t.Fatal(err)
			}
			return p
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := c.setUp(t)
			before := p.Received()

			if err := p.Receive(p.Amount()); err == nil {
				t.Fatal("want an error, got none")
			}
			if p.Received().IsSet() != before.IsSet() {
				t.Errorf("received changed to %s", p.Received())
			}
		})
	}
}

func TestRestore_RebuildsAPaymentStillSettlingOnAnUnderpayment(t *testing.T) {
	t.Parallel()
	// A row can hold less than it billed for as long as it is not succeeded,
	// which is where a recorded underpayment lives until somebody decides.
	id, err := payment.NewID()
	if err != nil {
		t.Fatal(err)
	}
	row := stored(t, id, payment.Settling, now, now.Add(time.Hour))
	short, err := payment.ParseMoney(row.Amount.Asset(), "1")
	if err != nil {
		t.Fatal(err)
	}
	row.Received = short

	back, err := payment.Restore(row)

	if err != nil {
		t.Fatalf("a settling payment holding an underpayment would not load: %v", err)
	}
	if cmp, err := back.Received().Cmp(short); err != nil || cmp != 0 {
		t.Errorf("received = %s, want %s (err %v)", back.Received(), short, err)
	}
}

func TestRestore_RefusesARowWhereMoneyArrivedBeforeAnyoneCouldPay(t *testing.T) {
	t.Parallel()
	id, err := payment.NewID()
	if err != nil {
		t.Fatal(err)
	}
	row := stored(t, id, payment.Created, now, now.Add(time.Hour))
	row.Received = row.Amount

	if _, err := payment.Restore(row); err == nil {
		t.Fatal("loaded a payment that was paid before it was payable")
	}
}

func TestReceive_RefusesASecondArrival(t *testing.T) {
	t.Parallel()
	p := payable(t)
	arrived(t, p)

	err := p.Receive(p.Amount())

	if err == nil {
		t.Fatal("recorded a second arrival on one payment")
	}
	if cmp, cmpErr := p.Received().Cmp(p.Amount()); cmpErr != nil || cmp != 0 {
		t.Errorf("received = %s, want it unchanged (cmp %d, err %v)", p.Received(), cmp, cmpErr)
	}
}

func TestSucceed_RefusesUntilEnoughHasArrived(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		arrival func(*testing.T, *payment.Payment)
	}{
		{"nothing arrived", func(*testing.T, *payment.Payment) {}},
		{"less than billed", func(t *testing.T, p *payment.Payment) {
			short, err := payment.ParseMoney(p.Asset(), "1")
			if err != nil {
				t.Fatal(err)
			}
			if err := p.Receive(short); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := payable(t)
			c.arrival(t, p)

			if err := p.Succeed(); err == nil {
				t.Fatal("a merchant was told to fulfil an order nobody paid for")
			}
			if p.Status() == payment.Succeeded {
				t.Error("status = succeeded")
			}
		})
	}
}

func TestSucceed_AcceptsMoreThanWasBilled(t *testing.T) {
	t.Parallel()
	// Overpayment settles the order. What to do about the excess is the
	// merchant's, and refusing it would leave them paid and the payment open.
	p := payable(t)
	over, err := payment.ParseMoney(p.Asset(), one+"0")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Receive(over); err != nil {
		t.Fatal(err)
	}

	if err := p.Succeed(); err != nil {
		t.Fatalf("refused an overpayment: %v", err)
	}
}

func TestRestore_RefusesARowThatSaysPaidWithoutTheMoney(t *testing.T) {
	t.Parallel()
	id, err := payment.NewID()
	if err != nil {
		t.Fatal(err)
	}
	row := stored(t, id, payment.Succeeded, now, now.Add(time.Hour))
	row.Received = payment.Money{}

	if _, err := payment.Restore(row); err == nil {
		t.Fatal("loaded a succeeded payment with nothing recorded as arrived")
	}
}

func TestRestore_RebuildsAPaymentThatHasAlreadyExpired(t *testing.T) {
	t.Parallel()
	// A row whose deadline has passed still has to load, or nothing could read
	// back what happened.
	created, expires := now.Add(-48*time.Hour), now.Add(-47*time.Hour)
	id, err := payment.NewID()
	if err != nil {
		t.Fatal(err)
	}

	back, err := payment.Restore(stored(t, id, payment.Expired, created, expires))

	if err != nil {
		t.Fatalf("a stored payment would not load: %v", err)
	}
	if back.ID() != id {
		t.Errorf("id = %s, want the one it was stored under, %s", back.ID(), id)
	}
	if back.Status() != payment.Expired {
		t.Errorf("status = %s, want %s", back.Status(), payment.Expired)
	}
	if !back.CreatedAt().Equal(created) {
		t.Errorf("created_at = %s, want %s", back.CreatedAt(), created)
	}
	if !back.ExpiresAt().Equal(expires) {
		t.Errorf("expires_at = %s, want %s", back.ExpiresAt(), expires)
	}
}

func TestRestore_RefusesARowThatIsNoLongerAPayment(t *testing.T) {
	t.Parallel()
	id, err := payment.NewID()
	if err != nil {
		t.Fatal(err)
	}
	created := now.Add(-time.Hour)

	for _, c := range []struct {
		name   string
		id     payment.ID
		status payment.Status
		change func(*payment.Stored)
	}{
		{name: "a status nothing defines", id: id, status: "paid"},
		{name: "a status that was removed", id: id, status: "confirming"},
		{name: "an identifier of another shape", id: "1", status: payment.Created},
		{
			name: "no destination", id: id, status: payment.Created,
			change: func(s *payment.Stored) { s.Destination = "" },
		},
		{
			name: "a deadline before it was created", id: id, status: payment.Created,
			change: func(s *payment.Stored) { s.ExpiresAt = s.CreatedAt.Add(-time.Hour) },
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			row := stored(t, c.id, c.status, created, now)
			if c.change != nil {
				c.change(&row)
			}

			if _, err := payment.Restore(row); err == nil {
				t.Fatal("want an error, got none")
			}
		})
	}
}
