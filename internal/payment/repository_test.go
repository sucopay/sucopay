package payment_test

import (
	"errors"
	"os"
	"reflect"
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

	if err := s.Save(t.Context(), other, p, at); !errors.Is(err, payment.ErrStale) {
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

	if err := s.Save(t.Context(), first, loaded, at); err != nil {
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
	if err := s.Save(t.Context(), first, winner, atWinner); err != nil {
		t.Fatal(err)
	}

	if err := loser.Await(); err != nil {
		t.Fatal(err)
	}
	err = s.Save(t.Context(), first, loser, atLoser)

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

	err = s.Save(t.Context(), first, loaded, atTheirs)

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
	moved := map[string]string{"status": "status", "received": "received"}
	fixed := map[string]bool{
		"id": true, "amount": true, "destination": true,
		"metadata": true, "createdAt": true, "expiresAt": true,
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
