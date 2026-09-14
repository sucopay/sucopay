package finality

import (
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sucopay/sucopay/internal/adapter/chain"
	"github.com/sucopay/sucopay/internal/adapter/chain/evm"
	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
)

// One payment as Polygon carried it: the transaction, the token, the merchant
// it went to, the key the payer's authorisation spent, and what it moved.
const (
	carriedBy     = "0x25a2307e6e108c7aa0e5efefc171c3875cb4bf08007fc1c1bbed14d623e7ea3e"
	jpycOnPolygon = "0xe7c3d8c9a439fede00d2600032d5db0be71c3c29"
	paidTo        = "0xe3e8472795411463b59e659202d774cf14e90ce1"
	keySpent      = "c8c21cded88da8b0cbd6ac63c0f5e36bfae0637aa1a6c6378f48ab86d15674a3"
	paid          = "20000"
)

// A round against the chain itself, on a payment somebody really made. What
// the rest of these tests prove against a chain inside the process, this
// proves against the answers a provider gives: asked about a transfer
// recorded as final, the endpoint says the block is one the chain keeps, and
// the payment is paid.
//
// The transfer is recorded the way a round of the observer records what it
// read in the finalised range, from the receipt the endpoint gives for it.
func TestRound_PaysAPaymentPolygonCarried(t *testing.T) {
	t.Parallel()
	endpoint := os.Getenv("SUCO_TEST_POLYGON_RPC_URL")
	if endpoint == "" {
		t.Skip("SUCO_TEST_POLYGON_RPC_URL names no endpoint to read Polygon at")
	}
	opened, err := evm.Kind.Open(chain.Settings{Name: "polygon", ChainID: "137", RPC: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.Open(t.Context(), postgrestest.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	store := payment.NewPostgres(pool.Conns())
	p := payable(t, store)
	recordedOnPolygon(t, opened, store, pool.Conns())
	w := New(Network{Name: "polygon", Endpoints: []chain.Chain{opened}, Agreements: 1,
		Misses: 2, Recheck: time.Minute},
		store, &instance{holds: true}, slog.New(slog.DiscardHandler), time.Now)

	if _, err := w.round(t.Context()); err != nil {
		t.Fatal(err)
	}

	back, _, err := store.Find(t.Context(), held, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if back.Status() != payment.Succeeded {
		t.Errorf("the payment is %s, and the endpoint holds the transfer that paid it in a final block",
			back.Status())
	}
	if got := back.Received().Units(); got != paid {
		t.Errorf("the payment says %s arrived, want %s", got, paid)
	}
	var events []string
	rows, err := pool.Conns().Query(t.Context(),
		`select event from outbox where account_id = $1 and payment_id = $2 order by id`, held, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		events = append(events, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0] != "payment.succeeded" {
		t.Errorf("the outbox holds %v, want one payment.succeeded", events)
	}
}

// payable is the payment that transfer would have paid, the same token to the
// same merchant for the same amount, with an attempt holding the key the
// authorisation really spent. An attempt makes its own key, and this one has
// to be the payer's.
func payable(t *testing.T, store *payment.Postgres) *payment.Payment {
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
	if err := store.Save(t.Context(), held, p, at, payment.Event{}); err != nil {
		t.Fatal(err)
	}
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
	if err := store.Issue(t.Context(), held, a); err != nil {
		t.Fatal(err)
	}
	return p
}

// recordedOnPolygon records the transfer the way a round that read the
// finalised range does: from the receipt, judged against the attempt whose
// key it spent, in a block already called final.
func recordedOnPolygon(t *testing.T, reading chain.Chain, store *payment.Postgres, pool *pgxpool.Pool) {
	t.Helper()
	carried, err := reading.Receipt(t.Context(), carriedBy)
	if err != nil {
		t.Fatal(err)
	}
	if len(carried) != 1 {
		t.Fatalf("the receipt read as %d transfers, want 1", len(carried))
	}
	hits, err := store.Consumed(t.Context(), "polygon", []string{keySpent})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("the key was issued %d times, want 1", len(hits))
	}
	transfer := carried[0]
	read := payment.Transfer{
		Scheme: payment.Scheme(transfer.Scheme), Asset: transfer.Asset, Key: transfer.Key,
		Authorizer: transfer.Authorizer, From: transfer.From, To: transfer.To,
		Value: transfer.Value, Tx: transfer.Tx, Position: transfer.Position,
		BlockHeight: transfer.Block.Height, BlockHash: transfer.Block.Hash,
		BlockTime: transfer.Block.Time,
	}
	reason, judged := payment.Judge(read, hits[0].Attempt, hits[0].Payment)
	if !judged || reason != payment.Matched {
		t.Fatalf("the rules made %s of the transfer, want matched", reason)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(t.Context(), tx, "polygon", transfer.Block.Height, transfer.Block.Height,
		true, []payment.Seen{{Hit: hits[0], Transfer: read, Reason: reason}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}
