package checkout_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sucopay/sucopay/internal/checkout"
	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
)

const (
	first = payment.AccountID("00000000-0000-0000-0000-000000000001")
	other = payment.AccountID("00000000-0000-0000-0000-000000000002")
)

var (
	now   = time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	key   = [32]byte{5}
	links = checkout.NewLinks(key, "k1", "https://pay.example")
)

// opened is a migrated database with a second account, and the pool over
// it.
func opened(t *testing.T) *pgxpool.Pool {
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
		`insert into accounts (id, name) values ($1, 'Other Shop')`, other); err != nil {
		t.Fatal(err)
	}
	return pool.Conns()
}

// jpyc is an asset on a network the tests name polygon.
func jpyc(t *testing.T) payment.Asset {
	t.Helper()
	a, err := payment.NewAsset("polygon", "0x431d5dff03120afa4bdf332c61a6e1766ef37bdb", "JPYC", 18)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// paid makes a payment of account with a checkout page, as the API makes
// one, and returns it.
func paid(t *testing.T, pool *pgxpool.Pool, account payment.AccountID, returnURL string) *payment.Payment {
	t.Helper()
	// 1000 JPYC, in the asset's smallest unit.
	amount, err := payment.ParseMoney(jpyc(t), "1000"+strings.Repeat("0", 18))
	if err != nil {
		t.Fatal(err)
	}
	destination, err := payment.ParseAddress("0xabababababababababababababababababababab")
	if err != nil {
		t.Fatal(err)
	}
	id, err := payment.NewID()
	if err != nil {
		t.Fatal(err)
	}
	svc := payment.NewService(payment.NewPostgres(pool), payment.NewPostgres(pool), watching{}, func() time.Time { return now })
	p, err := svc.OpenAs(t.Context(), account, id, payment.Request{
		Amount: amount, Destination: destination, Metadata: map[string]string{"order": "A-1"},
		ExpiresAt: now.Add(15 * time.Minute), ReturnURL: returnURL, Checkout: links.Checkout(id),
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// watching is a cursor store that has read every network.
type watching struct{}

func (watching) Has(context.Context, payment.Network) (bool, error) { return true, nil }

func TestLookup_FindsThePaymentOfATokenDerivedUnderTheKeyInForce(t *testing.T) {
	t.Parallel()
	pool := opened(t)
	s := checkout.NewPostgres(pool)
	p := paid(t, pool, other, "")
	token := checkout.Derive(key, p.ID())

	owner, err := s.Lookup(t.Context(), checkout.Hash(token), "k1", now)

	if err != nil || owner.Payment != p.ID() || owner.Account != other || owner.Name != "Other Shop" {
		t.Errorf("Lookup = %+v, %v; want the payment, its account and the account's name", owner, err)
	}
	if _, err := s.Lookup(t.Context(), checkout.Hash(token), "k2", now); !errors.Is(err, checkout.ErrNotFound) {
		t.Errorf("under another key id = %v, want ErrNotFound", err)
	}
	if _, err := s.Lookup(t.Context(), checkout.Hash(checkout.Derive([32]byte{6}, p.ID())), "k1", now); !errors.Is(err, checkout.ErrNotFound) {
		t.Errorf("a token under another key = %v, want ErrNotFound", err)
	}
}

func TestLookup_StopsFindingAPaymentThirtyDaysAfterItEnded(t *testing.T) {
	t.Parallel()
	pool := opened(t)
	s := checkout.NewPostgres(pool)
	p := paid(t, pool, first, "")
	hash := checkout.Hash(checkout.Derive(key, p.ID()))
	ended := now.Add(20 * time.Minute)
	if _, err := pool.Exec(t.Context(), `update payments set status = 'expired', closed_at = $2 where id = $1`, p.ID(), ended); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Lookup(t.Context(), hash, "k1", ended.Add(checkout.PageLife-time.Second)); err != nil {
		t.Errorf("just inside the page's life = %v, want found", err)
	}
	if _, err := s.Lookup(t.Context(), hash, "k1", ended.Add(checkout.PageLife)); !errors.Is(err, checkout.ErrNotFound) {
		t.Errorf("at the end of the page's life = %v, want ErrNotFound", err)
	}
}

func TestMatched_IsTheTransferSeenForThePaymentConfirmingUntilItSucceeded(t *testing.T) {
	t.Parallel()
	pool := opened(t)
	s := checkout.NewPostgres(pool)
	p := paid(t, pool, first, "")

	if r, err := s.Matched(t.Context(), first, p.ID(), payment.AwaitingPayment); err != nil || r != nil {
		t.Fatalf("Matched before anything was seen = %+v, %v; want nil", r, err)
	}
	seen := now.Add(time.Minute)
	if _, err := pool.Exec(t.Context(), `
		insert into attempts (account_id, payment_id, id, scheme, network, key, authorizer, valid_before, status, created_at)
		values ($1, $2, 'a1', 'eip3009', 'polygon', 'k', '0x11', $3, 'confirming', $4)`, first, p.ID(), now.Add(15*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		insert into observations (account_id, payment_id, attempt_id, network, key, tx, position, block_height, block_hash,
		                          block_time, asset, authorizer, sender, recipient, value, reason, implementation, seen_at, final_at)
		values ($1, $2, 'a1', 'polygon', 'k', '0xtx', 0, 78123, '0xblock', $3, 'a', '0x11', '0x11', '0xab', '1000000000000000000000',
		        'matched', '', $3, $4)`, first, p.ID(), seen, seen.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	confirming, err := s.Matched(t.Context(), first, p.ID(), payment.AwaitingFinality)
	if err != nil || confirming == nil || confirming.Tx != "0xtx" || confirming.BlockHeight != 78123 || !confirming.Confirming || confirming.ReceivedAt != nil {
		t.Errorf("Matched while settling = %+v, %v; want the transfer, confirming", confirming, err)
	}
	succeeded, err := s.Matched(t.Context(), first, p.ID(), payment.Succeeded)
	if err != nil || succeeded == nil || succeeded.Confirming || succeeded.ReceivedAt == nil || succeeded.Value != "1000000000000000000000" {
		t.Errorf("Matched once succeeded = %+v, %v; want the transfer, received", succeeded, err)
	}
	if r, err := s.Matched(t.Context(), other, p.ID(), payment.Succeeded); err != nil || r != nil {
		t.Errorf("another account's Matched = %+v, %v; want nil", r, err)
	}
}
