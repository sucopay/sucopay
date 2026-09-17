package payment_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
)

// The account the schema creates, and a second one made here. One account can
// never show that anything is scoped to an account.
const (
	first = payment.AccountID("00000000-0000-0000-0000-000000000001")
	other = payment.AccountID("00000000-0000-0000-0000-000000000002")
)

// store returns a repository over a fresh database, and the pool behind it for
// the tests that have to look at a row this package would not hand back.
func store(t *testing.T) (*payment.Postgres, *pgxpool.Pool) {
	t.Helper()
	pool, err := postgres.Open(t.Context(), postgrestest.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Conns().Exec(t.Context(),
		`insert into accounts (id, name) values ($1, 'second')`, other); err != nil {
		t.Fatal(err)
	}
	return payment.NewPostgres(pool.Conns()), pool.Conns()
}

// kept stores a payment and hands back what a caller would hold after doing so.
func kept(t *testing.T, s *payment.Postgres, account payment.AccountID) *payment.Payment {
	t.Helper()
	p := open(t)
	if err := s.Create(t.Context(), account, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRepository_ReadsBackEverythingItWasGiven(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	p := kept(t, s, first)

	back, _, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}

	if back.ID() != p.ID() {
		t.Errorf("id = %s, want %s", back.ID(), p.ID())
	}
	if cmp, err := back.Amount().Cmp(p.Amount()); err != nil || cmp != 0 {
		t.Errorf("amount = %s, want %s (err %v)", back.Amount(), p.Amount(), err)
	}
	if !back.Asset().Same(p.Asset()) {
		t.Errorf("asset = %s, want %s", back.Asset(), p.Asset())
	}
	if back.Destination() != p.Destination() {
		t.Errorf("destination = %s, want %s", back.Destination(), p.Destination())
	}
	if back.Status() != p.Status() {
		t.Errorf("status = %s, want %s", back.Status(), p.Status())
	}
	if !back.CreatedAt().Equal(p.CreatedAt()) {
		t.Errorf("created_at = %s, want %s", back.CreatedAt(), p.CreatedAt())
	}
	if !back.ExpiresAt().Equal(p.ExpiresAt()) {
		t.Errorf("expires_at = %s, want %s", back.ExpiresAt(), p.ExpiresAt())
	}
	if !reflect.DeepEqual(back.Metadata(), p.Metadata()) {
		t.Errorf("metadata = %v, want %v", back.Metadata(), p.Metadata())
	}
}

func TestRepository_DoesNotFindAPaymentAnotherAccountStored(t *testing.T) {
	t.Parallel()
	// Every row is scoped to an account and every query has to filter on it.
	// Two accounts, one payment, and the one that does not own it must not
	// reach it by knowing its identifier.
	s, _ := store(t)
	p := kept(t, s, first)

	_, _, err := s.Find(t.Context(), other, p.ID())

	if !errors.Is(err, payment.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound: one account read another's payment", err)
	}
}

func TestRepository_DoesNotSaveOverAPaymentAnotherAccountStored(t *testing.T) {
	t.Parallel()
	// Reading is scoped, and so is writing. A save that filtered on the
	// identifier alone would move somebody else's payment.
	s, _ := store(t)
	p := kept(t, s, first)
	_, at, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Await(); err != nil {
		t.Fatal(err)
	}

	if err := s.Save(t.Context(), other, p, at, payment.Event{}); !errors.Is(err, payment.ErrStale) {
		t.Fatalf("err = %v, want ErrStale: one account wrote to another's payment", err)
	}

	back, _, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if back.Status() != payment.Created {
		t.Errorf("status = %s, want it untouched at %s", back.Status(), payment.Created)
	}
}

func TestRepository_ReportsNotFoundForAPaymentNobodyStored(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	id, err := payment.NewID()
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = s.Find(t.Context(), first, id)

	if !errors.Is(err, payment.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestRepository_SavesAMove(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	p := kept(t, s, first)
	loaded, at, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Await(); err != nil {
		t.Fatal(err)
	}
	arrived(t, loaded)
	if err := loaded.Succeed(); err != nil {
		t.Fatal(err)
	}

	if err := s.Save(t.Context(), first, loaded, at, payment.Event{}); err != nil {
		t.Fatal(err)
	}

	back, _, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if back.Status() != payment.Succeeded {
		t.Errorf("status = %s, want %s", back.Status(), payment.Succeeded)
	}
	if cmp, err := back.Received().Cmp(loaded.Amount()); err != nil || cmp != 0 {
		t.Errorf("received = %s, want %s (err %v)", back.Received(), loaded.Amount(), err)
	}
}

func TestRepository_RefusesASaveThatLostTheRace(t *testing.T) {
	t.Parallel()
	// Two readers, one write each. The second holds a revision the first has
	// already moved past, and laying its move over the winner's would lose
	// whatever the winner decided.
	s, _ := store(t)
	p := kept(t, s, first)
	winner, atWinner, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	loser, atLoser, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := winner.Await(); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), first, winner, atWinner, payment.Event{}); err != nil {
		t.Fatal(err)
	}

	if err := loser.Await(); err != nil {
		t.Fatal(err)
	}
	err = s.Save(t.Context(), first, loser, atLoser, payment.Event{})

	if !errors.Is(err, payment.ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
}

func TestRepository_RefusesARevisionReadForAnotherPayment(t *testing.T) {
	t.Parallel()
	// Every payment starts at the same version, so a revision from one payment
	// matches another's row by coincidence. Carrying the identifier is what
	// stops a caller holding several payments from crossing them.
	s, _ := store(t)
	mine := kept(t, s, first)
	theirs := kept(t, s, first)
	_, atTheirs, err := s.Find(t.Context(), first, theirs.ID())
	if err != nil {
		t.Fatal(err)
	}
	loaded, _, err := s.Find(t.Context(), first, mine.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Await(); err != nil {
		t.Fatal(err)
	}

	err = s.Save(t.Context(), first, loaded, atTheirs, payment.Event{})

	if !errors.Is(err, payment.ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
	back, _, err := s.Find(t.Context(), first, mine.ID())
	if err != nil {
		t.Fatal(err)
	}
	if back.Status() != payment.Created {
		t.Errorf("status = %s, want it untouched at %s", back.Status(), payment.Created)
	}
}

func TestRepository_ReadsBackATimeCarryingMorePrecisionThanTheColumnKeeps(t *testing.T) {
	t.Parallel()
	// A timestamptz keeps microseconds. Held to the nanosecond in memory, a
	// payment would stop being equal to itself the moment it was stored, so it
	// is not held that way.
	s, _ := store(t)
	r := request(t)
	r.ExpiresAt = now.Add(time.Hour).Add(987654321 * time.Nanosecond)
	p, err := payment.New(r, now.Add(123456789*time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(t.Context(), first, p); err != nil {
		t.Fatal(err)
	}

	back, _, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}

	if !back.CreatedAt().Equal(p.CreatedAt()) {
		t.Errorf("created_at = %s, want %s", back.CreatedAt(), p.CreatedAt())
	}
	if !back.ExpiresAt().Equal(p.ExpiresAt()) {
		t.Errorf("expires_at = %s, want %s", back.ExpiresAt(), p.ExpiresAt())
	}
}

func TestRepository_EveryFieldOfAPaymentIsAccountedFor(t *testing.T) {
	t.Parallel()
	// Save writes the fields a move changes; Create writes the rest, once. A
	// field added to Payment belongs in one list or the other, and one in
	// neither is a field Save drops without anything noticing.
	//
	// The lists are checked against the update statement as well as against the
	// struct, because classifying a new field and then not writing it is the
	// same silent loss as not classifying it at all.
	moved := map[string]string{"status": "status", "received": "received", "closedAt": "closed_at"}
	fixed := map[string]bool{
		"id": true, "amount": true, "destination": true,
		"metadata": true, "createdAt": true, "expiresAt": true,
		"returnURL": true, "checkout": true,
	}

	fields := reflect.TypeOf(payment.Payment{})
	for i := range fields.NumField() {
		name := fields.Field(i).Name
		if _, ok := moved[name]; !ok && !fixed[name] {
			t.Errorf("Payment.%s is in neither list: does Save have to write it?", name)
		}
	}
	if got, want := fields.NumField(), len(moved)+len(fixed); got != want {
		t.Errorf("Payment has %d fields and the lists name %d", got, want)
	}

	source, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	_, update, ok := strings.Cut(string(source), "update payments")
	if !ok {
		t.Fatal("no update statement in postgres.go: this test no longer checks anything")
	}
	set, _, ok := strings.Cut(update, "where")
	if !ok {
		t.Fatal("the update statement has no where clause")
	}
	for field, column := range moved {
		if !strings.Contains(set, column+" =") {
			t.Errorf("Payment.%s is listed as changing, but Save does not set %s", field, column)
		}
	}
}

// payableKept stores a payment a payer could pay: an attempt is only made
// against one of those.
func payableKept(t *testing.T, s *payment.Postgres, account payment.AccountID) *payment.Payment {
	t.Helper()
	p := kept(t, s, account)
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
	return p
}

// attempted stores an attempt at paying p and hands back what a caller would
// hold after doing so.
func attempted(t *testing.T, s *payment.Postgres, account payment.AccountID, p *payment.Payment) *payment.Attempt {
	t.Helper()
	a, err := payment.NewAttempt(p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Issue(t.Context(), account, a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestIssue_ReadsBackEverythingItWasGiven(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	p := payableKept(t, s, first)
	a := attempted(t, s, first, p)

	read, at, err := s.FindAttempt(t.Context(), first, p.ID(), a.ID())
	if err != nil {
		t.Fatal(err)
	}
	if read.ID() != a.ID() || read.PaymentID() != p.ID() {
		t.Errorf("read back attempt %s of %s, want %s of %s", read.ID(), read.PaymentID(), a.ID(), p.ID())
	}
	if read.Key() != a.Key() || read.Scheme() != a.Scheme() || read.Network() != a.Network() {
		t.Errorf("read back %s %s %q, want %s %s %q",
			read.Scheme(), read.Network(), read.Key(), a.Scheme(), a.Network(), a.Key())
	}
	if !read.ValidBefore().Equal(a.ValidBefore()) {
		t.Errorf("ValidBefore is %s, want %s", read.ValidBefore(), a.ValidBefore())
	}
	if !read.CreatedAt().Equal(a.CreatedAt()) {
		t.Errorf("CreatedAt is %s, want %s", read.CreatedAt(), a.CreatedAt())
	}
	if read.Status() != payment.Issued || read.Authorizer() != "" {
		t.Errorf("read back %s signed by %q, want issued and nobody", read.Status(), read.Authorizer())
	}
	if err := s.SaveAttempt(t.Context(), first, read, at); err != nil {
		t.Errorf("the revision it was read at did not save: %v", err)
	}
}

// Two accounts reaching for one key at the same moment is what the unique
// index over (network, key) is there for. The loser is told the key is taken
// rather than told about a race, and is told it without being shown the key,
// which belongs to the payer the winner is about to give it to.
func TestIssue_LetsExactlyOneOfTwoAccountsTakeAKey(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	// A key nothing holds yet, so that the index is what settles it.
	key := strings.Repeat("ab", 32)

	accounts := []payment.AccountID{first, other}
	made := make([]*payment.Attempt, 0, len(accounts))
	for i, account := range accounts {
		a, err := payment.RestoreAttempt(payment.StoredAttempt{
			ID:          payment.AttemptID(strings.Repeat(strconv.Itoa(i+1), 32)),
			PaymentID:   payableKept(t, s, account).ID(),
			Scheme:      payment.EIP3009,
			Network:     jpyc(t).Network(),
			Key:         key,
			ValidBefore: time.Now().Add(time.Hour).Truncate(time.Second),
			Status:      payment.Issued,
			CreatedAt:   time.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		made = append(made, a)
	}

	begin := make(chan struct{})
	errs := make(chan error, len(made))
	for i, a := range made {
		go func() {
			<-begin
			errs <- s.Issue(t.Context(), accounts[i], a)
		}()
	}
	close(begin)

	won, refused := 0, 0
	for range made {
		switch err := <-errs; {
		case err == nil:
			won++
		case errors.Is(err, payment.ErrKeyTaken):
			refused++
			if strings.Contains(err.Error(), key) {
				t.Errorf("the refusal carries the key: %v", err)
			}
		default:
			t.Errorf("err = %v, want either none or %v", err, payment.ErrKeyTaken)
		}
	}
	if won != 1 || refused != 1 {
		t.Errorf("%d took the key and %d were refused, want one of each", won, refused)
	}
}

func TestIssue_RefusesASecondAttemptHoldingOneKey(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	held := attempted(t, s, first, payableKept(t, s, first))
	elsewhere := payableKept(t, s, other)

	same, err := payment.RestoreAttempt(payment.StoredAttempt{
		ID:          "0123456789abcdef0123456789abcdef",
		PaymentID:   elsewhere.ID(),
		Scheme:      held.Scheme(),
		Network:     held.Network(),
		Key:         held.Key(),
		ValidBefore: held.ValidBefore(),
		Status:      payment.Issued,
		CreatedAt:   time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// The other account's, so that the key is what refuses the write rather
	// than anything the two payments share.
	err = s.Issue(t.Context(), other, same)
	if !errors.Is(err, payment.ErrKeyTaken) {
		t.Fatalf("Issue gave %v, want %v", err, payment.ErrKeyTaken)
	}
	for _, rendered := range []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err)} {
		if strings.Contains(rendered, held.Key()) {
			t.Errorf("the error carries the key another payer is about to spend: %s", rendered)
		}
	}
}

func TestIssue_RefusesASecondAttemptThatCouldStillBePaid(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	p := payableKept(t, s, first)
	attempted(t, s, first, p)

	second, err := payment.NewAttempt(p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Issue(t.Context(), first, second); !errors.Is(err, payment.ErrAttemptLive) {
		t.Fatalf("Issue gave %v, want %v", err, payment.ErrAttemptLive)
	}
}

func TestIssue_RefusesAnAttemptAtAnotherAccountsPayment(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	p := payableKept(t, s, first)

	a, err := payment.NewAttempt(p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Issue(t.Context(), other, a); err == nil {
		t.Error("an attempt was stored against another account's payment")
	}
}

func TestFindAttempt_HidesWhatBelongsToAnotherAccount(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	p := payableKept(t, s, first)
	a := attempted(t, s, first, p)

	if _, _, err := s.FindAttempt(t.Context(), other, p.ID(), a.ID()); !errors.Is(err, payment.ErrNotFound) {
		t.Errorf("FindAttempt gave %v, want %v", err, payment.ErrNotFound)
	}
	if _, _, err := s.FindAttempt(t.Context(), first, p.ID(), "0123456789abcdef0123456789abcdef"); !errors.Is(err, payment.ErrNotFound) {
		t.Errorf("FindAttempt of an attempt nobody stored gave %v, want %v", err, payment.ErrNotFound)
	}
}

func TestLive_FindsTheAttemptThatCouldStillBePaid(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	p := payableKept(t, s, first)

	if _, _, live, err := s.Live(t.Context(), first, p.ID()); err != nil || live {
		t.Fatalf("Live on a payment nobody attempted = %v, %v", live, err)
	}
	a := attempted(t, s, first, p)
	read, _, live, err := s.Live(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !live || read.ID() != a.ID() {
		t.Errorf("Live = %v, %v; want the attempt %s", read, live, a.ID())
	}
	if _, _, live, err := s.Live(t.Context(), other, p.ID()); err != nil || live {
		t.Errorf("Live under another account = %v, %v", live, err)
	}
}

func TestSaveAttempt_WritesTheConfirmationAndMovesTheRevision(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	p := payableKept(t, s, first)
	a := attempted(t, s, first, p)

	read, at, err := s.FindAttempt(t.Context(), first, p.ID(), a.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := read.Confirm("0xpayer"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAttempt(t.Context(), first, read, at); err != nil {
		t.Fatal(err)
	}
	again, next, err := s.FindAttempt(t.Context(), first, p.ID(), a.ID())
	if err != nil {
		t.Fatal(err)
	}
	if again.Status() != payment.Confirming || again.Authorizer() != "0xpayer" {
		t.Errorf("read back %s signed by %q", again.Status(), again.Authorizer())
	}
	if err := s.SaveAttempt(t.Context(), first, again, at); !errors.Is(err, payment.ErrStale) {
		t.Errorf("saving at the revision already used gave %v, want %v", err, payment.ErrStale)
	}
	if err := s.SaveAttempt(t.Context(), first, again, next); err != nil {
		t.Errorf("saving at the revision just read gave %v", err)
	}
}

func TestSaveAttempt_MovesTheRowsRevisionOnEverySave(t *testing.T) {
	t.Parallel()
	s, conns := store(t)
	p := payableKept(t, s, first)
	a := attempted(t, s, first, p)

	revision := func(t *testing.T) int64 {
		t.Helper()
		var at int64
		if err := conns.QueryRow(t.Context(),
			`select revision from attempts where id = $1`, a.ID()).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	before := revision(t)
	read, at, err := s.FindAttempt(t.Context(), first, p.ID(), a.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := read.Confirm("0xpayer"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAttempt(t.Context(), first, read, at); err != nil {
		t.Fatal(err)
	}
	if after := revision(t); after != before+1 {
		t.Errorf("the row is at revision %d, want %d", after, before+1)
	}
}

// A revision is a fact about a row, and one type carries both a payment's and
// an attempt's. What keeps the two apart is the identifier it was read under.
func TestSaveAttempt_RefusesARevisionReadForAPayment(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	p := payableKept(t, s, first)
	a := attempted(t, s, first, p)

	// Both rows are moved to the same number first, so that what refuses the
	// save is the identifier the revision carries rather than a version that
	// happens not to match.
	read, at, err := s.FindAttempt(t.Context(), first, p.ID(), a.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := read.Confirm("0xpayer"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAttempt(t.Context(), first, read, at); err != nil {
		t.Fatal(err)
	}
	_, ofPayment, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	read, ofAttempt, err := s.FindAttempt(t.Context(), first, p.ID(), a.ID())
	if err != nil {
		t.Fatal(err)
	}
	if ofPayment == ofAttempt {
		t.Fatal("the two revisions are the same value, so this test proves nothing")
	}
	if err := s.SaveAttempt(t.Context(), first, read, ofPayment); !errors.Is(err, payment.ErrStale) {
		t.Errorf("saving an attempt at its payment's revision gave %v, want %v", err, payment.ErrStale)
	}
	if err := s.Save(t.Context(), first, p, ofAttempt, payment.Event{}); !errors.Is(err, payment.ErrStale) {
		t.Errorf("saving a payment at its attempt's revision gave %v, want %v", err, payment.ErrStale)
	}
}

func TestSaveAttempt_RefusesARevisionReadForAnotherAttempt(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	one := payableKept(t, s, first)
	two := payableKept(t, s, first)
	first_, second := attempted(t, s, first, one), attempted(t, s, first, two)

	_, at, err := s.FindAttempt(t.Context(), first, one.ID(), first_.ID())
	if err != nil {
		t.Fatal(err)
	}
	read, _, err := s.FindAttempt(t.Context(), first, two.ID(), second.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAttempt(t.Context(), first, read, at); !errors.Is(err, payment.ErrStale) {
		t.Errorf("saving one attempt at another's revision gave %v, want %v", err, payment.ErrStale)
	}
}

func TestSaveAttempt_DoesNotSaveOverAnAttemptAnotherAccountStored(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	p := payableKept(t, s, first)
	a := attempted(t, s, first, p)

	read, at, err := s.FindAttempt(t.Context(), first, p.ID(), a.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := read.Confirm("0xpayer"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAttempt(t.Context(), other, read, at); !errors.Is(err, payment.ErrStale) {
		t.Fatalf("SaveAttempt under another account gave %v, want %v", err, payment.ErrStale)
	}
	again, _, err := s.FindAttempt(t.Context(), first, p.ID(), a.ID())
	if err != nil {
		t.Fatal(err)
	}
	if again.Status() != payment.Issued || again.Authorizer() != "" {
		t.Errorf("the row is %s signed by %q after a save from another account", again.Status(), again.Authorizer())
	}
}

func TestSaveAttempt_EveryFieldOfAnAttemptIsAccountedFor(t *testing.T) {
	t.Parallel()
	// The same division as a payment's: what a move changes, and what Issue
	// writes once. A field in neither list is one SaveAttempt drops.
	moved := map[string]string{"status": "status", "authorizer": "authorizer"}
	fixed := map[string]bool{
		"id": true, "paymentID": true, "scheme": true, "network": true,
		"key": true, "validBefore": true, "createdAt": true,
	}

	fields := reflect.TypeOf(payment.Attempt{})
	for i := range fields.NumField() {
		name := fields.Field(i).Name
		if _, ok := moved[name]; !ok && !fixed[name] {
			t.Errorf("Attempt.%s is in neither list: does SaveAttempt have to write it?", name)
		}
	}
	if got, want := fields.NumField(), len(moved)+len(fixed); got != want {
		t.Errorf("Attempt has %d fields and the lists name %d", got, want)
	}

	source, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	_, update, ok := strings.Cut(string(source), "update attempts")
	if !ok {
		t.Fatal("no update statement in postgres.go: this test no longer checks anything")
	}
	set, _, ok := strings.Cut(update, "where")
	if !ok {
		t.Fatal("the update statement has no where clause")
	}
	for field, column := range moved {
		if !strings.Contains(set, column+" =") {
			t.Errorf("Attempt.%s is listed as changing, but SaveAttempt does not set %s", field, column)
		}
	}
}

// eventsFor reads the outbox rows a payment produced, oldest first. The
// repository hands none back, so a test that cares looks at the rows.
func eventsFor(t *testing.T, conns *pgxpool.Pool, account payment.AccountID, id payment.ID) []string {
	t.Helper()
	rows, err := conns.Query(t.Context(),
		`select event from outbox where account_id = $1 and payment_id = $2 order by id`,
		account, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var event string
		if err := rows.Scan(&event); err != nil {
			t.Fatal(err)
		}
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// A change and the event it produced are one write. Whatever delivers events
// is told about a payment that moved, and about no payment that did not.
func TestSave_WritesTheEventWithTheChange(t *testing.T) {
	t.Parallel()
	s, conns := store(t)
	p := kept(t, s, first)
	_, at, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Await(); err != nil {
		t.Fatal(err)
	}

	err = s.Save(t.Context(), first, p, at, payment.Event{
		Name: "payment.awaiting_payment", Payload: []byte(`{"id":"x"}`),
	})

	if err != nil {
		t.Fatalf("Save = %v, want none", err)
	}
	if got := eventsFor(t, conns, first, p.ID()); !slices.Equal(got, []string{"payment.awaiting_payment"}) {
		t.Errorf("outbox holds %v, want the one event the change produced", got)
	}
}

// A change that produces no event leaves the outbox alone. A payment becoming
// payable tells a merchant nothing they did not just ask for.
func TestSave_WritesNoEventWhenTheChangeProducedNone(t *testing.T) {
	t.Parallel()
	s, conns := store(t)
	p := kept(t, s, first)
	_, at, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Await(); err != nil {
		t.Fatal(err)
	}

	if err := s.Save(t.Context(), first, p, at, payment.Event{}); err != nil {
		t.Fatal(err)
	}

	if got := eventsFor(t, conns, first, p.ID()); len(got) != 0 {
		t.Errorf("outbox holds %v, want nothing", got)
	}
}

// The write that lost the race leaves nothing behind. An event for a change
// that did not happen would tell a merchant about a payment that never moved.
func TestSave_WritesNoEventWhenTheChangeWasRefused(t *testing.T) {
	t.Parallel()
	s, conns := store(t)
	p := kept(t, s, first)
	winner, atWinner, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	loser, atLoser, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := winner.Await(); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), first, winner, atWinner, payment.Event{}); err != nil {
		t.Fatal(err)
	}
	if err := loser.Await(); err != nil {
		t.Fatal(err)
	}

	err = s.Save(t.Context(), first, loser, atLoser, payment.Event{
		Name: "payment.awaiting_payment", Payload: []byte(`{"id":"x"}`),
	})

	if !errors.Is(err, payment.ErrStale) {
		t.Fatalf("Save = %v, want ErrStale", err)
	}
	if got := eventsFor(t, conns, first, p.ID()); len(got) != 0 {
		t.Errorf("outbox holds %v, want nothing: the change it describes was refused", got)
	}
}

// A body the column cannot take is named as the caller's mistake, before the
// transaction that would fail on it. The insert shares a transaction with the
// change it describes, so a body that is not JSON would take a change that was
// fine down with it.
func TestSave_RefusesAnEventBodyThatIsNotJSON(t *testing.T) {
	t.Parallel()
	s, conns := store(t)
	p := kept(t, s, first)
	_, at, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Await(); err != nil {
		t.Fatal(err)
	}

	err = s.Save(t.Context(), first, p, at, payment.Event{
		Name: "payment.succeeded", Payload: []byte("not json"),
	})

	if err == nil {
		t.Fatal("a body that is not JSON was written")
	}
	if !strings.Contains(err.Error(), "JSON") {
		t.Errorf("error %v does not say what is wrong with the body", err)
	}
	if got := eventsFor(t, conns, first, p.ID()); len(got) != 0 {
		t.Errorf("outbox holds %v, want nothing", got)
	}
	back, _, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if back.Status() != payment.Created {
		t.Errorf("payment is %s, want the change refused with the event", back.Status())
	}
}

// A name is bounded the way a body is. The column takes any length, so this is
// the only place a caller's mistake is still a caller's mistake.
func TestSave_RefusesAnEventNameOverTheBound(t *testing.T) {
	t.Parallel()
	s, conns := store(t)
	p := kept(t, s, first)
	_, at, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Await(); err != nil {
		t.Fatal(err)
	}

	err = s.Save(t.Context(), first, p, at, payment.Event{
		Name:    strings.Repeat("a", payment.MaxEventNameBytes+1),
		Payload: []byte(`{"id":"x"}`),
	})

	if err == nil {
		t.Fatal("an event name over the bound was written")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(payment.MaxEventNameBytes)) {
		t.Errorf("error %v does not say what the bound is", err)
	}
	if got := eventsFor(t, conns, first, p.ID()); len(got) != 0 {
		t.Errorf("outbox holds %v, want nothing", got)
	}
	back, _, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if back.Status() != payment.Created {
		t.Errorf("payment is %s, want the change refused with the event", back.Status())
	}
}

// The bound is where it says it is. A test that only refuses what is over it
// passes just as well against a bound one short.
func TestSave_WritesAnEventAtTheBound(t *testing.T) {
	t.Parallel()
	s, conns := store(t)
	p := kept(t, s, first)
	_, at, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Await(); err != nil {
		t.Fatal(err)
	}
	// `{"x":"` and `"}` around the padding come to eight bytes.
	body := []byte(`{"x":"` + strings.Repeat("a", payment.MaxEventBytes-8) + `"}`)
	name := strings.Repeat("a", payment.MaxEventNameBytes)

	err = s.Save(t.Context(), first, p, at, payment.Event{Name: name, Payload: body})

	if err != nil {
		t.Fatalf("Save = %v, want an event at the bound to be written", err)
	}
	if got := eventsFor(t, conns, first, p.ID()); !slices.Equal(got, []string{name}) {
		t.Errorf("outbox holds %d events, want the one at the bound", len(got))
	}
}

// A body of no fixed length is bounded, the way every other one this package
// writes is. What puts it there is code rather than a stranger, and code has
// bugs.
func TestSave_RefusesAnEventBodyOverTheBound(t *testing.T) {
	t.Parallel()
	s, conns := store(t)
	p := kept(t, s, first)
	_, at, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Await(); err != nil {
		t.Fatal(err)
	}
	body := append([]byte(`{"x":"`), bytes.Repeat([]byte("a"), payment.MaxEventBytes)...)

	err = s.Save(t.Context(), first, p, at, payment.Event{Name: "payment.succeeded", Payload: body})

	if err == nil {
		t.Fatal("an event body over the bound was written")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(payment.MaxEventBytes)) {
		t.Errorf("error %v does not say what the bound is", err)
	}
	if got := eventsFor(t, conns, first, p.ID()); len(got) != 0 {
		t.Errorf("outbox holds %v, want nothing", got)
	}
}

// Half an event is a caller that meant to give one. Either half is refused
// by name, and the change the event was to describe is refused with it: a
// caller told which half it left out has less to look for than one told what
// the driver made of an empty value.
func TestSave_RefusesHalfOfAnEvent(t *testing.T) {
	t.Parallel()
	for what, e := range map[string]payment.Event{
		"a body and no name": {Payload: []byte(`{"id":"x"}`)},
		"a name and no body": {Name: "payment.succeeded"},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			s, conns := store(t)
			p := kept(t, s, first)
			_, at, err := s.Find(t.Context(), first, p.ID())
			if err != nil {
				t.Fatal(err)
			}
			if err := p.Await(); err != nil {
				t.Fatal(err)
			}

			err = s.Save(t.Context(), first, p, at, e)

			if err == nil {
				t.Fatalf("an event with %s was written", what)
			}
			if !strings.Contains(err.Error(), what) {
				t.Errorf("error %v does not say which half was left out", err)
			}
			if got := eventsFor(t, conns, first, p.ID()); len(got) != 0 {
				t.Errorf("outbox holds %v, want nothing", got)
			}
			back, _, err := s.Find(t.Context(), first, p.ID())
			if err != nil {
				t.Fatal(err)
			}
			if back.Status() != payment.Created {
				t.Errorf("payment is %s, want the change refused with the event", back.Status())
			}
		})
	}
}

// When a payment ends is written by the store, once, when the first final
// status is saved: the checkout page's token lives for a while after it.
func TestRepository_WritesWhenAPaymentEndedOnceAndLeavesItAlone(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	p := payableKept(t, s, first)
	loaded, at, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.ClosedAt().IsZero() {
		t.Fatalf("a payable payment has closed_at %s", loaded.ClosedAt())
	}
	// Past its deadline and past the wait for finality: the way a payment
	// nobody paid ends.
	if err := loaded.AwaitFinality(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Expire(); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), first, loaded, at, payment.Event{}); err != nil {
		t.Fatal(err)
	}

	ended, at, err := s.Find(t.Context(), first, p.ID())
	if err != nil {
		t.Fatal(err)
	}

	if ended.ClosedAt().IsZero() || time.Since(ended.ClosedAt()) > time.Minute {
		t.Errorf("closed_at = %s, want about now", ended.ClosedAt())
	}
	// Saved again as it is: the time stands.
	if err := s.Save(t.Context(), first, ended, at, payment.Event{}); err != nil {
		t.Fatal(err)
	}
	var closedAt time.Time
	if err := pool.QueryRow(t.Context(), `select closed_at from payments where id = $1`, p.ID()).Scan(&closedAt); err != nil {
		t.Fatal(err)
	}
	if !closedAt.Equal(ended.ClosedAt()) {
		t.Errorf("closed_at moved to %s on a resave, want %s kept", closedAt, ended.ClosedAt())
	}
}
