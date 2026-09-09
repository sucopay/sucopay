package observe

import (
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/adapter/chain"
	"github.com/sucopay/sucopay/internal/adapter/chain/evm"
	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
)

// One payment as Polygon carried it: the token, the merchant it went to, the
// key the payer's authorisation spent, what it moved, and the block before the
// one it is in.
const (
	jpycOnPolygon = "0xe7c3d8c9a439fede00d2600032d5db0be71c3c29"
	paidTo        = "0xe3e8472795411463b59e659202d774cf14e90ce1"
	keySpent      = "c8c21cded88da8b0cbd6ac63c0f5e36bfae0637aa1a6c6378f48ab86d15674a3"
	paid          = "20000"
	below         = 93040156
	belowHash     = "0x0994d3a39ad50e4bcff1dfe827d8c9efe50edfd776dec7c4e746952fd8ca4511"
)

// A round against the chain itself, on a payment somebody really made. What
// the rest of these tests prove against a chain inside the process, this
// proves against the answers a provider gives.
//
// It needs an endpoint that still holds the logs of that block. Public ones
// drop them after a few days and say so, and there is no reading the range
// without them.
func TestTick_ReadsAPaymentPolygonCarried(t *testing.T) {
	t.Parallel()
	endpoint := os.Getenv("SUCO_TEST_POLYGON_RPC_URL")
	if endpoint == "" {
		t.Skip("SUCO_TEST_POLYGON_RPC_URL names no endpoint to read Polygon at")
	}
	opened, err := evm.Kind.Open(chain.Settings{Name: "polygon", ChainID: "137", RPC: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	o, store := polygon(t, opened)

	account, p := payable(t, store)
	planted(t, store, account, p)
	if _, _, err := o.cursors.Set(t.Context(), "polygon",
		Position{Height: below, Hash: belowHash}, time.Now()); err != nil {
		t.Fatal(err)
	}

	err = o.tick(t.Context())

	if errors.Is(err, chain.ErrTooWide) {
		t.Skipf("the endpoint no longer holds the logs of block %d: %v", below+1, err)
	}
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, live, err := store.Live(t.Context(), account, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !live {
		t.Fatal("the payment has no attempt that could still be paid")
	}
	if attempt.Status() != payment.Confirming {
		t.Errorf("the attempt is %s, and the transfer that spent its key is on the chain", attempt.Status())
	}
	if attempt.Authorizer() == "" {
		t.Error("the attempt names nobody who signed it")
	}
}

// polygon is an observer of the real network, over a database of its own.
func polygon(t *testing.T, reading chain.Chain) (*Observer, *payment.Postgres) {
	t.Helper()
	pool, err := postgres.Open(t.Context(), postgrestest.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	store := payment.NewPostgres(pool.Conns())
	return New(Network{
		Name:   "polygon",
		Want:   "137",
		Chain:  reading,
		Assets: []payment.Asset{token(t, jpycOnPolygon, "JPYC")},
		Poll:   3 * time.Second,
		Width:  50,
	}, pool.Conns(), store, slog.New(slog.DiscardHandler), time.Now), store
}

// payable is the payment that transfer would have paid: the same token, the
// same merchant, the same amount.
func payable(t *testing.T, store *payment.Postgres) (payment.AccountID, *payment.Payment) {
	t.Helper()
	asset, err := payment.NewAsset("polygon", jpycOnPolygon, "JPYC", 18)
	if err != nil {
		t.Fatal(err)
	}
	amount, err := payment.ParseUnits(asset, paid)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := payment.ParseAddress(paidTo)
	if err != nil {
		t.Fatal(err)
	}
	p, err := payment.New(payment.Request{
		Amount:      amount,
		Destination: destination,
		ExpiresAt:   time.Now().Add(time.Hour),
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(t.Context(), held, p); err != nil {
		t.Fatal(err)
	}
	_, at, err := store.Find(t.Context(), held, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Await(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), held, p, at); err != nil {
		t.Fatal(err)
	}
	return held, p
}

// planted issues an attempt holding the key that authorisation really spent.
// An attempt makes its own key, and this one has to be the payer's.
func planted(t *testing.T, store *payment.Postgres, account payment.AccountID, p *payment.Payment) {
	t.Helper()
	a, err := payment.RestoreAttempt(payment.StoredAttempt{
		ID:          payment.AttemptID(strings.Repeat("a1", 16)),
		PaymentID:   p.ID(),
		Scheme:      payment.EIP3009,
		Network:     p.Network(),
		Key:         keySpent,
		ValidBefore: p.ExpiresAt().Truncate(time.Second),
		Status:      payment.Issued,
		CreatedAt:   time.Now().UTC().Truncate(time.Microsecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Issue(t.Context(), account, a); err != nil {
		t.Fatal(err)
	}
}
