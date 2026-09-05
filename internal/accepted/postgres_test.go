package accepted_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sucopay/sucopay/internal/accepted"
	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
)

// The account the schema creates, and a second one made here. One account can
// never show that anything is scoped to an account.
const (
	first = payment.AccountID("00000000-0000-0000-0000-000000000001")
	other = payment.AccountID("00000000-0000-0000-0000-000000000002")
	// nobody is an account no row was ever written for.
	nobody = payment.AccountID("00000000-0000-0000-0000-000000000009")
)

var now = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// store returns a store over a fresh database, and the pool behind it for the
// tests that have to look at a column this package does not hand back.
func store(t *testing.T) (*accepted.Postgres, *pgxpool.Pool) {
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
	return accepted.NewPostgres(pool.Conns()), pool.Conns()
}

func asset(t *testing.T, network, reference string) payment.Asset {
	t.Helper()
	a, err := payment.NewAsset(payment.Network(network), reference, "JPYC", 18)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func address(t *testing.T, digit string) payment.Address {
	t.Helper()
	a, err := payment.ParseAddress("0x" + strings.Repeat(digit, 40))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestPostgres_ReadsBackTheDestinationItWasGiven(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	jpyc := asset(t, "polygon", "0x1")
	if err := s.Accept(t.Context(), first, jpyc, address(t, "a"), now); err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.Destination(t.Context(), first, jpyc)
	if err != nil {
		t.Fatal(err)
	}

	if !ok || got != address(t, "a") {
		t.Errorf("destination = %q, %v; want %q, true", got, ok, address(t, "a"))
	}
}

func TestPostgres_TellsAnAssetFromAnotherOnTheSameNetwork(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	if err := s.Accept(t.Context(), first, asset(t, "polygon", "0x1"), address(t, "a"), now); err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.Destination(t.Context(), first, asset(t, "polygon", "0x2"))
	if err != nil {
		t.Fatal(err)
	}

	if ok || got != "" {
		t.Errorf("destination = %q, %v; want none", got, ok)
	}
}

func TestPostgres_KeepsOneAccountsAssetsFromAnother(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	jpyc := asset(t, "polygon", "0x1")
	if err := s.Accept(t.Context(), first, jpyc, address(t, "a"), now); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := s.Destination(t.Context(), other, jpyc); err != nil || ok {
		t.Errorf("the other account reads a destination: %v, %v", ok, err)
	}
	if listed, err := s.List(t.Context(), other); err != nil || len(listed) != 0 {
		t.Errorf("the other account lists %v, %v; want nothing", listed, err)
	}
	if err := s.Accept(t.Context(), other, jpyc, address(t, "b"), now); err != nil {
		t.Fatal(err)
	}
	if got, _, err := s.Destination(t.Context(), first, jpyc); err != nil || got != address(t, "a") {
		t.Errorf("after the other account accepted the same asset, destination = %q, %v; want %q",
			got, err, address(t, "a"))
	}
}

func TestPostgres_AcceptingAnAssetAgainReplacesTheDestination(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	jpyc := asset(t, "polygon", "0x1")
	later := now.Add(time.Hour)
	if err := s.Accept(t.Context(), first, jpyc, address(t, "a"), now); err != nil {
		t.Fatal(err)
	}

	if err := s.Accept(t.Context(), first, jpyc, address(t, "b"), later); err != nil {
		t.Fatal(err)
	}

	listed, err := s.List(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Destination != address(t, "b") || !listed[0].UpdatedAt.Equal(later) {
		t.Errorf("listed %+v, want one asset paid to %q as of %s", listed, address(t, "b"), later)
	}
	var createdAt time.Time
	if err := pool.QueryRow(t.Context(), `select created_at from accepted_assets`).Scan(&createdAt); err != nil {
		t.Fatal(err)
	}
	if !createdAt.Equal(now) {
		t.Errorf("created_at = %s, want %s, when the asset was first accepted", createdAt, now)
	}
}

func TestPostgres_ListsByNetworkAndThenByReference(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	for _, a := range []payment.Asset{
		asset(t, "polygon", "0x2"), asset(t, "ethereum", "0x9"), asset(t, "polygon", "0x1"),
	} {
		if err := s.Accept(t.Context(), first, a, address(t, "a"), now); err != nil {
			t.Fatal(err)
		}
	}

	listed, err := s.List(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, a := range listed {
		got = append(got, string(a.Network)+" "+a.Reference)
	}
	if want := "ethereum 0x9 polygon 0x1 polygon 0x2"; strings.Join(got, " ") != want {
		t.Errorf("listed %q, want %q", got, want)
	}
}

func TestPostgres_RefusesAnAccountThatDoesNotExist(t *testing.T) {
	t.Parallel()
	s, _ := store(t)

	err := s.Accept(t.Context(), nobody, asset(t, "polygon", "0x1"), address(t, "a"), now)

	if err == nil {
		t.Fatal("accepted an asset for an account no row exists for")
	}
}

func TestPostgres_RefusesTheZeroAssetAndTheEmptyAddress(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	for name, tc := range map[string]struct {
		asset       payment.Asset
		destination payment.Address
	}{
		"the zero asset":    {payment.Asset{}, address(t, "a")},
		"the empty address": {asset(t, "polygon", "0x1"), ""},
	} {
		t.Run(name, func(t *testing.T) {
			if err := s.Accept(t.Context(), first, tc.asset, tc.destination, now); err == nil {
				t.Error("accepted it")
			}
		})
	}
	var rows int
	if err := pool.QueryRow(t.Context(), `select count(*) from accepted_assets`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("%d rows written, want none", rows)
	}
}
