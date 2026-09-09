package observe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sucopay/sucopay/internal/adapter/chain"
	"github.com/sucopay/sucopay/internal/adapter/chain/simulated"
	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
)

// held is the account the schema creates, which is the one every payment here
// belongs to.
const held = payment.AccountID("00000000-0000-0000-0000-000000000001")

// second is an account of somebody else, made where a test needs one. A
// deployment with one account can never show that a lookup is scoped to one.
const second = payment.AccountID("00000000-0000-0000-0000-000000000002")

// watching is an observer over a chain inside the process and a database of
// its own, with one payment awaiting payment and one attempt at it.
type watching struct {
	observer *Observer
	chain    *simulated.Chain
	store    *payment.Postgres
	pool     *pgxpool.Pool
	asset    payment.Asset
	other    payment.Asset
	payment  *payment.Payment
	attempt  *payment.Attempt
	// attempt2 is at a second payment of the same account, for a round that
	// has to find two transfers.
	payment2 *payment.Payment
	attempt2 *payment.Attempt
}

// watch opens everything one round reads and writes.
func watch(t *testing.T) *watching {
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

	asset, other := jpyc(t), token(t, "0x"+strings.Repeat("ee", 20), "OTHER")
	p := awaiting(t, store, asset)
	a, err := payment.NewAttempt(p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Issue(t.Context(), held, a); err != nil {
		t.Fatal(err)
	}

	made := simulated.New()
	w := &watching{
		chain: made, store: store, pool: pool.Conns(),
		asset: asset, other: other, payment: p, attempt: a,
	}
	w.observer = New(Network{
		Name:   asset.Network().String(),
		Chain:  made,
		Assets: map[string]payment.Asset{"jpyc": asset, "other": other},
		Poll:   12 * time.Second,
		Width:  50,
	}, pool.Conns(), store, slog.New(slog.DiscardHandler), time.Now)
	if taken, err := w.observer.leases.Acquire(t.Context(), w.observer.network.Name); err != nil || !taken {
		t.Fatal(taken, err)
	}
	w.observer.held = time.Now()
	return w
}

// jpyc is the asset the payments here are in. A deployment carries more than
// one, and the second is what a key spent on the wrong token is spent on.
func jpyc(t *testing.T) payment.Asset { return token(t, "0x"+strings.Repeat("cd", 20), "JPYC") }

// token is one asset of the network these rounds read.
func token(t *testing.T, reference, symbol string) payment.Asset {
	t.Helper()
	asset, err := payment.NewAsset("local", reference, symbol, 18)
	if err != nil {
		t.Fatal(err)
	}
	return asset
}

// awaiting stores a payment a payer could pay.
func awaiting(t *testing.T, store *payment.Postgres, asset payment.Asset) *payment.Payment {
	t.Helper()
	return awaitingFor(t, store, held, asset)
}

// awaitingFor stores a payment of one account.
func awaitingFor(t *testing.T, store *payment.Postgres, account payment.AccountID, asset payment.Asset) *payment.Payment {
	t.Helper()
	amount, err := payment.ParseUnits(asset, "20000")
	if err != nil {
		t.Fatal(err)
	}
	destination, err := payment.ParseAddress("0x" + strings.Repeat("ab", 20))
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
	if err := store.Create(t.Context(), account, p); err != nil {
		t.Fatal(err)
	}
	_, at, err := store.Find(t.Context(), account, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Await(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), account, p, at); err != nil {
		t.Fatal(err)
	}
	return p
}

// paid puts a transfer of the payment's amount to its destination into a block
// the chain calls final, which is what a payer doing what they were asked
// leaves behind.
func (w *watching) paid(t *testing.T, key string) {
	t.Helper()
	w.chain.Send(w.sending(key))
	w.chain.Finalize(w.chain.Mine())
}

// sending is the transfer a payer doing what they were asked leaves behind.
func (w *watching) sending(key string) chain.Transfer { return w.paying(w.payment, key) }

// paying is that transfer for one of the payments.
func (w *watching) paying(p *payment.Payment, key string) chain.Transfer {
	payer := "0x" + strings.Repeat("11", 20)
	return chain.Transfer{
		Scheme:     string(payment.EIP3009),
		Asset:      w.asset.Reference(),
		Key:        key,
		Authorizer: payer,
		From:       payer,
		To:         string(p.Destination()),
		Value:      p.Amount().Amount().String(),
	}
}

// secondPayment opens another payment of the same account, with an attempt at
// it, so that one round has two transfers to find.
func (w *watching) secondPayment(t *testing.T) *payment.Payment {
	t.Helper()
	p := awaiting(t, w.store, w.asset)
	a, err := payment.NewAttempt(p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.store.Issue(t.Context(), held, a); err != nil {
		t.Fatal(err)
	}
	w.payment2, w.attempt2 = p, a
	return p
}

// otherAttempt opens a payment and an attempt under another account, so that a
// round has two accounts' attempts to tell apart.
func (w *watching) otherAttempt(t *testing.T) *payment.Attempt {
	t.Helper()
	if _, err := w.pool.Exec(t.Context(),
		`insert into accounts (id, name) values ($1, 'second')`, second); err != nil {
		t.Fatal(err)
	}
	p := awaitingFor(t, w.store, second, w.asset)
	a, err := payment.NewAttempt(p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.store.Issue(t.Context(), second, a); err != nil {
		t.Fatal(err)
	}
	return a
}

// tick is one round of the observer, which fails the test rather than handing
// back an error nobody looks at.
func (w *watching) tick(t *testing.T) {
	t.Helper()
	if err := w.observer.tick(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// record is one observation as a round left it.
type record struct {
	Key    string
	Tx     string
	Reason string
	Height uint64
	Final  bool
}

// rows are the observations the rounds wrote, in the order the chain carried
// them.
func (w *watching) rows(t *testing.T) []record {
	t.Helper()
	rows, err := w.pool.Query(t.Context(),
		`select key, tx, reason, block_height, final_at is not null
		   from observations order by block_height, position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []record
	for rows.Next() {
		var one record
		if err := rows.Scan(&one.Key, &one.Tx, &one.Reason, &one.Height, &one.Final); err != nil {
			t.Fatal(err)
		}
		out = append(out, one)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// status is where the attempt has got to, read back out of the database.
func (w *watching) status(t *testing.T) payment.AttemptStatus {
	t.Helper()
	a, _, err := w.store.FindAttempt(t.Context(), held, w.payment.ID(), w.attempt.ID())
	if err != nil {
		t.Fatal(err)
	}
	return a.Status()
}

func TestTick_ConfirmsTheAttemptWhoseKeyATransferSpent(t *testing.T) {
	t.Parallel()
	w := watch(t)
	// The first round takes the position and reads nothing below it.
	w.tick(t)
	w.paid(t, w.attempt.Key())

	w.tick(t)

	if got := w.status(t); got != payment.Confirming {
		t.Errorf("the attempt is %s, and a transfer of the whole amount was seen", got)
	}
	rows := w.rows(t)
	if len(rows) != 1 {
		t.Fatalf("the round wrote %d observations, want 1", len(rows))
	}
	if rows[0].Reason != string(payment.Matched) || rows[0].Key != w.attempt.Key() || !rows[0].Final {
		t.Errorf("the observation reads %+v", rows[0])
	}
	p, _, err := w.store.Find(t.Context(), held, w.payment.ID())
	if err != nil {
		t.Fatal(err)
	}
	if p.Status() != payment.AwaitingPayment {
		t.Errorf("the payment is %s, and finality is what moves it on", p.Status())
	}

	// The next round starts above the block the transfer was in. A round that
	// read that block again would ask for the same receipt for as long as the
	// payment lasted.
	w.chain.Finalize(w.chain.Mine())
	w.tick(t)
	if called := w.chain.Calls()["Receipt"]; called != 1 {
		t.Errorf("the chain was asked for %d receipts, and one transfer was seen", called)
	}
	if rows := w.rows(t); len(rows) != 1 {
		t.Errorf("the rounds wrote %+v", rows)
	}
}

// A round on a chain that has finalised nothing new reads no blocks, and says
// it happened all the same: whoever asks whether this deployment is still
// reading reads when the position was last written.
func TestTick_ReadsNoBlocksAndSaysItRanWhereNothingWasFinalised(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	before := w.touched(t)

	w.tick(t)

	if called := w.chain.Calls()["Keys"]; called != 0 {
		t.Errorf("the chain was asked for keys %d times, and nothing new was finalised", called)
	}
	if after := w.touched(t); !after.After(before) {
		t.Errorf("the position was last written at %s, and a round has run since", after)
	}
	if w.observer.said() != observing {
		t.Errorf("the network is %q, want %q", w.observer.said(), observing)
	}
}

// touched is when the position was last written.
func (w *watching) touched(t *testing.T) time.Time {
	t.Helper()
	var at time.Time
	if err := w.pool.QueryRow(t.Context(),
		`select updated_at from observation_cursors where network = $1`,
		w.observer.network.Name).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

// The rules are what decide, and everything a chain carried against an
// attempt's key is written down whether or not it paid the payment. A merchant
// asked about a payer who says they paid is shown these rows.
func TestTick_WritesDownWhatArrivedAndDidNotPay(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		reason  payment.Reason
		sending func(*watching, chain.Transfer) chain.Transfer
	}{
		"a transfer to somebody else": {payment.WrongTo, func(_ *watching, sent chain.Transfer) chain.Transfer {
			sent.To = "0x" + strings.Repeat("99", 20)
			return sent
		}},
		"a transfer of less than the payment": {payment.Short, func(_ *watching, sent chain.Transfer) chain.Transfer {
			sent.Value = "1"
			return sent
		}},
		"a transfer of another token": {payment.WrongAsset, func(w *watching, sent chain.Transfer) chain.Transfer {
			sent.Asset = w.other.Reference()
			return sent
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := watch(t)
			w.tick(t)
			w.chain.Send(c.sending(w, w.sending(w.attempt.Key())))
			w.chain.Finalize(w.chain.Mine())

			w.tick(t)

			rows := w.rows(t)
			if len(rows) != 1 {
				t.Fatalf("the round wrote %d observations, want 1", len(rows))
			}
			if rows[0].Reason != c.reason.String() {
				t.Errorf("the observation says %q, want %q", rows[0].Reason, c.reason)
			}
			if got := w.status(t); got != payment.Issued {
				t.Errorf("the attempt is %s, and nothing that paid was seen", got)
			}
		})
	}
}

// A key nothing here issued is somebody else's payment on the same token. Its
// receipt is not read: what it moved, who moved it and where it went are none
// of this deployment's business.
func TestTick_ReadsNoReceiptForAKeyNobodyHereIssued(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	w.paid(t, strings.Repeat("f0", 32))

	w.tick(t)

	if rows := w.rows(t); len(rows) != 0 {
		t.Errorf("the round wrote %+v", rows)
	}
	if called := w.chain.Calls()["Receipt"]; called != 0 {
		t.Errorf("the chain was asked for %d receipts", called)
	}
}

// The chain a document names is the one whose blocks say anything about its
// payments. A provider answering for another chain is one this reads nothing
// from, and the word says why.
func TestTick_ReadsNothingFromAChainThatIsNotTheOneNamed(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.observer.network.Want = "137"

	err := w.observer.tick(t.Context())

	if err == nil {
		t.Fatal("the round read a chain calling itself something else")
	}
	if w.observer.said() != chainMismatch {
		t.Errorf("the network is %q, want %q", w.observer.said(), chainMismatch)
	}
	if calls := w.chain.Calls(); calls["Head"] != 0 || calls["Keys"] != 0 {
		t.Errorf("the chain was asked %v", calls)
	}
	if had, err := w.observer.cursors.Has(t.Context(), payment.Network(w.observer.network.Name)); err != nil || had {
		t.Errorf("the round took a position on a chain it would not read: %v", err)
	}
}

// A chain whose assets cannot be replaced leaves the column empty, and the
// column is written either way: what was behind the asset is read off the row
// when the transfer is judged again.
func TestTick_WritesWhateverTheChainSaysIsBehindTheAsset(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	w.paid(t, w.attempt.Key())

	w.tick(t)

	var behind string
	if err := w.pool.QueryRow(t.Context(), `select implementation from observations`).Scan(&behind); err != nil {
		t.Fatal(err)
	}
	if behind != "" {
		t.Errorf("the row says %q is behind the asset, and nothing here stands in front of anything", behind)
	}
}

// Ahead of the final block nothing is settled. What is found there is written
// as evidence without the moment it became final, and the position stays where
// it was: the finalised range reads the same blocks again when it reaches
// them.
func TestTick_WritesWhatItSawAheadOfFinalityWithoutCallingItFinal(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	w.chain.Send(w.sending(w.attempt.Key()))
	w.chain.Mine()

	w.tick(t)

	rows := w.rows(t)
	if len(rows) != 1 {
		t.Fatalf("the round wrote %d observations, want 1", len(rows))
	}
	if rows[0].Final {
		t.Error("the observation is stamped final, and it was read ahead of the final block")
	}
	if rows[0].Reason != payment.Matched.String() {
		t.Errorf("the observation says %q", rows[0].Reason)
	}
	// The attempt moves on evidence from either range: what is ahead of
	// finality is what a payer has just done.
	if got := w.status(t); got != payment.Confirming {
		t.Errorf("the attempt is %s", got)
	}

	// The block the transfer is in becomes final, and the round that reaches
	// it stamps the row it already wrote rather than writing a second one.
	w.chain.Finalize(1)
	w.tick(t)
	rows = w.rows(t)
	if len(rows) != 1 {
		t.Fatalf("the rounds wrote %d observations, want 1", len(rows))
	}
	if !rows[0].Final {
		t.Error("the observation is not stamped final, and the block it is in is")
	}
}

// The read ahead of finality is the one that finds a payment early, and the
// one a provider is likeliest to refuse: a node behind the one that answered
// for the newest block has not got there yet. The round is a success without
// it, because the finalised range reads the same blocks again.
func TestTick_CountsARoundThatCouldNotReadAheadOfFinalityAsARound(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	w.paid(t, w.attempt.Key())
	w.chain.Mine()
	before := w.position(t)

	width := w.observer.network.Width

	w.chain.FailAt("Keys", 2, chain.ErrTooWide)
	w.tick(t)

	// What a provider refuses ahead of finality says nothing about the width:
	// the range ends at the newest block, and a node behind the one that named
	// it refuses that range whatever its width.
	if w.observer.network.Width != width {
		t.Errorf("the round reads %d blocks now, and it read %d", w.observer.network.Width, width)
	}
	if w.observer.said() != observing {
		t.Errorf("the network is %q, want %q", w.observer.said(), observing)
	}
	if after := w.position(t); after.Height <= before.Height {
		t.Errorf("the position is at %d, and the finalised range was read", after.Height)
	}
	if rows := w.rows(t); len(rows) != 1 {
		t.Errorf("the round wrote %+v", rows)
	}
}

// position is where the round has read to.
func (w *watching) position(t *testing.T) Position {
	t.Helper()
	at, found, err := w.observer.cursors.Get(t.Context(), payment.Network(w.observer.network.Name))
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("no position")
	}
	return at
}

// A round that could not read the finalised range writes nothing and moves
// nothing. What it would have found is still there to find next time.
func TestTick_WritesNothingWhenTheFinalisedRangeCouldNotBeRead(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	w.paid(t, w.attempt.Key())
	before := w.position(t)

	w.chain.FailAt("Keys", 1, errors.New("the provider said no"))
	if err := w.observer.tick(t.Context()); err == nil {
		t.Fatal("the round reported success without having read the range")
	}

	if after := w.position(t); after != before {
		t.Errorf("the position moved to %+v", after)
	}
	if rows := w.rows(t); len(rows) != 0 {
		t.Errorf("the round wrote %+v", rows)
	}
	if got := w.status(t); got != payment.Issued {
		t.Errorf("the attempt is %s", got)
	}
}

// The position and what was found below it are written together. A position
// somebody else moved while the round was reading takes the whole round with
// it, because what was read was read from where the position used to be.
func TestTick_WritesNothingWhenThePositionMovedUnderIt(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	w.chain.Mine()
	w.chain.Mine()
	w.chain.Send(w.sending(w.attempt.Key()))
	w.chain.Finalize(w.chain.Mine())
	network := payment.Network(w.observer.network.Name)
	// Somebody puts the position at the block below the transfer while the
	// round is reading the range from the block below that.
	moved, err := w.chain.Block(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	w.observer.network.Chain = interrupted{Chain: w.chain, during: func() {
		if _, _, err := w.observer.cursors.Set(t.Context(), network, at(moved), time.Now()); err != nil {
			t.Error(err)
		}
	}}

	if err := w.observer.tick(t.Context()); !errors.Is(err, ErrMoved) {
		t.Fatalf("the round gave %v, want a position that moved", err)
	}

	if rows := w.rows(t); len(rows) != 0 {
		t.Errorf("the round wrote %+v", rows)
	}
	if got := w.status(t); got != payment.Issued {
		t.Errorf("the attempt is %s", got)
	}
}

// The round after a position was put somewhere reads from there. What was
// below it is not read again, and what is above it is.
func TestTick_ReadsFromWhereThePositionWasPut(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	w.chain.Mine()
	w.chain.Mine()
	w.chain.Send(w.sending(w.attempt.Key()))
	w.chain.Finalize(w.chain.Mine())
	moved, err := w.chain.Block(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.observer.cursors.Set(t.Context(),
		payment.Network(w.observer.network.Name), at(moved), time.Now()); err != nil {
		t.Fatal(err)
	}

	w.tick(t)

	if at := w.position(t); at.Height != 3 {
		t.Errorf("the position is at %d, and the round read from 2", at.Height)
	}
	if rows := w.rows(t); len(rows) != 1 {
		t.Errorf("the round wrote %+v", rows)
	}
}

// The block at the position is read where the head does not already answer for
// it, and a round that could not read it writes nothing: what it would have
// compared is what says the records came off this chain.
func TestTick_WritesNothingWhenTheBlockAtThePositionCouldNotBeRead(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	w.paid(t, w.attempt.Key())
	w.chain.Finalize(w.chain.Mine())
	before := w.position(t)

	w.chain.FailAt("Block", 1, errors.New("the provider said no"))
	if err := w.observer.tick(t.Context()); err == nil {
		t.Fatal("the round reported success without having compared the position")
	}

	if at := w.position(t); at != before {
		t.Errorf("the position moved to %+v", at)
	}
	if rows := w.rows(t); len(rows) != 0 {
		t.Errorf("the round wrote %+v", rows)
	}
}

// interrupted is a chain that lets a test do something in the middle of a
// round, between the reading and the writing.
type interrupted struct {
	chain.Chain
	during func()
}

func (i interrupted) Keys(ctx context.Context, first, last uint64, assets []string) (chain.Scan, error) {
	scan, err := i.Chain.Keys(ctx, first, last, assets)
	i.during()
	return scan, err
}

// A transfer names a network and a key, and the account it belongs to is what
// the lookup hands back. Two accounts holding an attempt each is where a
// lookup that forgot the account would be seen.
func TestTick_MovesTheAttemptOfTheAccountWhoseKeyWasSpent(t *testing.T) {
	t.Parallel()
	w := watch(t)
	other := w.otherAttempt(t)
	w.tick(t)
	w.paid(t, w.attempt.Key())

	w.tick(t)

	if got := w.status(t); got != payment.Confirming {
		t.Errorf("the attempt of the account that was paid is %s", got)
	}
	got, _, err := w.store.FindAttempt(t.Context(), second, other.PaymentID(), other.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got.Status() != payment.Issued {
		t.Errorf("the other account's attempt is %s, and nothing was paid against it", got.Status())
	}
	rows := w.rows(t)
	if len(rows) != 1 {
		t.Fatalf("the round wrote %d observations, want 1", len(rows))
	}
	if rows[0].Key != w.attempt.Key() {
		t.Errorf("the observation is against another key")
	}
}

// A key is what a payer spends, and what a round says it did is read by
// whoever runs the deployment and by whatever collects the logs after them.
// The line names the payment and the attempt, which are this deployment's own
// identifiers, and never the key.
func TestTick_KeepsTheKeyOutOfWhatItSaysItDid(t *testing.T) {
	t.Parallel()
	w := watch(t)
	said := &strings.Builder{}
	w.observer.log = slog.New(slog.NewJSONHandler(said, nil))
	w.tick(t)
	w.paid(t, w.attempt.Key())

	w.tick(t)

	if !strings.Contains(said.String(), w.payment.ID().String()) {
		t.Fatalf("the round said nothing about the transfer it wrote down: %s", said)
	}
	if strings.Contains(said.String(), w.attempt.Key()) {
		t.Error("the log holds the key the payer spent")
	}
}

// The first round takes the position and reads nothing. An attempt is issued
// against the position of the moment, so a block below it was never one a
// payment here could have been paid in.
func TestTick_TakesThePositionOnTheFirstRoundAndReadsNothingBelowIt(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.paid(t, w.attempt.Key())

	w.tick(t)

	if w.observer.said() != noPosition {
		t.Errorf("the network is %q, want %q", w.observer.said(), noPosition)
	}
	if called := w.chain.Calls()["Keys"]; called != 0 {
		t.Errorf("the chain was asked for keys %d times", called)
	}
	if rows := w.rows(t); len(rows) != 0 {
		t.Errorf("the round wrote %+v", rows)
	}
	at := w.position(t)
	if at.Height != 1 {
		t.Fatalf("the position is at %d, and the chain's final block is 1", at.Height)
	}
	// The hash is what says the block at that height is still the one that was
	// read there, so it is that block's own.
	block, err := w.chain.Block(t.Context(), at.Height)
	if err != nil {
		t.Fatal(err)
	}
	if at.Hash != block.Hash {
		t.Errorf("the position holds %q, and the block at %d is %q", at.Hash, at.Height, block.Hash)
	}
}

// One round reads what its width allows and no more. What it did not reach is
// still above the position, and the next round starts there.
func TestTick_ReadsNoMoreBlocksThanItsWidth(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	w.observer.width = 2
	w.chain.Mine()
	w.chain.Mine()
	w.chain.Send(w.sending(w.attempt.Key()))
	w.chain.Finalize(w.chain.Mine())

	w.tick(t)

	// The transfer is in the third block above the position, and two is what
	// this round reads.
	if at := w.position(t); at.Height != 2 {
		t.Fatalf("the position is at %d, and two blocks is what one round reads", at.Height)
	}
	if rows := w.rows(t); len(rows) != 0 {
		t.Errorf("the round wrote %+v out of a block it had not reached", rows)
	}

	w.tick(t)
	if rows := w.rows(t); len(rows) != 1 {
		t.Fatalf("the rounds wrote %d observations, want 1", len(rows))
	}
	if got := w.status(t); got != payment.Confirming {
		t.Errorf("the attempt is %s", got)
	}
}

// The word is what whoever asks after this deployment is given, and a round
// that could not read says which kind of not reading it was.
func TestTick_SaysWhyARoundThatReadNothingReadNothing(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		method string
		err    error
		word   string
	}{
		"a provider that did not answer":       {"Identity", errors.New("no answer"), unreachable},
		"a provider with no head to give":      {"Head", errors.New("no answer"), unreachable},
		"a provider that names no final block": {"Head", chain.ErrNoFinal, noFinalized},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := watch(t)
			w.chain.FailAt(c.method, 1, c.err)

			if err := w.observer.tick(t.Context()); err == nil {
				t.Fatal("the round reported success without having read the chain")
			}

			if w.observer.said() != c.word {
				t.Errorf("the network is %q, want %q", w.observer.said(), c.word)
			}
		})
	}
}

// A key says which attempt a transfer is against, and nothing else about it
// does. A transfer authorised some other way is not one of these attempts'
// transfers, whatever key it spent.
func TestTick_WritesNothingForATransferAuthorisedSomeOtherWay(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	sent := w.sending(w.attempt.Key())
	sent.Scheme = "somebody else's scheme"
	w.chain.Send(sent)
	w.chain.Finalize(w.chain.Mine())

	w.tick(t)

	if rows := w.rows(t); len(rows) != 0 {
		t.Errorf("the round wrote %+v", rows)
	}
	if got := w.status(t); got != payment.Issued {
		t.Errorf("the attempt is %s", got)
	}
}

// A round reads at least one block. A width that says otherwise would have the
// range run backwards, which a provider answers with nothing at all, and a
// round that read nothing would move the position past blocks nobody read.
func TestTick_ReadsNothingWhereARoundIsSetToReadNoBlocks(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	w.paid(t, w.attempt.Key())
	before := w.position(t)
	w.observer.width = 0

	if err := w.observer.tick(t.Context()); err == nil {
		t.Fatal("the round reported success while set to read no blocks")
	}

	if after := w.position(t); after != before {
		t.Errorf("the position moved to %+v", after)
	}
	if called := w.chain.Calls()["Keys"]; called != 0 {
		t.Errorf("the chain was asked for keys %d times", called)
	}
	if rows := w.rows(t); len(rows) != 0 {
		t.Errorf("the round wrote %+v", rows)
	}
}

// What stands behind an asset is one value for the round, however many
// transfers it turns up. Reading it for each of them would spend a call on a
// value that changes once in the life of a token.
func TestTick_ReadsWhatIsBehindAnAssetOnceForTheRound(t *testing.T) {
	t.Parallel()
	w := watch(t)
	second := w.secondPayment(t)
	w.tick(t)
	behind := &standing{Chain: w.chain, code: "0xabc"}
	w.observer.network.Chain = behind
	w.chain.Send(w.sending(w.attempt.Key()))
	w.chain.Send(w.paying(second, w.attempt2.Key()))
	w.chain.Finalize(w.chain.Mine())

	w.tick(t)

	if behind.calls != 1 {
		t.Errorf("the chain was asked what is behind the asset %d times", behind.calls)
	}
	rows := w.rows(t)
	if len(rows) != 2 {
		t.Fatalf("the round wrote %d observations, want 2", len(rows))
	}
	var codes []string
	if err := w.pool.QueryRow(t.Context(),
		`select array_agg(distinct implementation) from observations`).Scan(&codes); err != nil {
		t.Fatal(err)
	}
	if len(codes) != 1 || codes[0] != "0xabc" {
		t.Errorf("the rows say %q is behind the asset", codes)
	}
}

// standing is a chain with something behind its assets, which a chain inside
// the process has not.
type standing struct {
	chain.Chain
	code  string
	calls int
}

func (s *standing) Implementation(context.Context, string) (string, error) {
	s.calls++
	return s.code, nil
}

// A key claimed spent in two transactions comes to one receipt read and one row
// written, against the transaction the scan named first.
func TestTick_ReadsOneTransactionForOneKey(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	w.chain.Send(w.sending(w.attempt.Key()))
	w.chain.Send(w.sending(w.attempt.Key()))
	w.chain.Finalize(w.chain.Mine())

	w.tick(t)

	if called := w.chain.Calls()["Receipt"]; called != 1 {
		t.Errorf("the chain was asked for %d receipts, and one key was spent", called)
	}
	rows := w.rows(t)
	if len(rows) != 1 {
		t.Fatalf("the round wrote %+v", rows)
	}
	// The first the scan named is the one read. Which of them is the real one
	// is not something the scan says, and a provider willing to name a wrong
	// transaction first could answer the receipt with anything at all.
	scan, err := w.chain.Keys(t.Context(), 1, 1, []string{w.asset.Reference()})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Tx != scan.Consumed[0].Tx {
		t.Errorf("the round read %s, and the scan named %s first", rows[0].Tx, scan.Consumed[0].Tx)
	}
}

// The provider that named the final block is not always the one that named it
// last time. Before anything is read, the block the position names has to be
// the one the chain has at that height, and where the head already says so, it
// is not asked again.
func TestTick_ComparesThePositionWithWhatTheHeadAlreadySaid(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		ahead uint64
		asks  int
	}{
		"the final block is the position":        {0, 0},
		"the final block is one above it":        {1, 0},
		"the final block is two above it":        {2, 1},
		"the final block is a long way above it": {5, 1},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := watch(t)
			w.tick(t)
			for range c.ahead {
				w.chain.Mine()
			}
			if c.ahead > 0 {
				w.chain.Finalize(c.ahead)
			}
			before := w.chain.Calls()["Block"]

			w.tick(t)

			if asked := w.chain.Calls()["Block"] - before; asked != c.asks {
				t.Errorf("the chain was asked for %d blocks, want %d", asked, c.asks)
			}
			if w.observer.said() != observing {
				t.Errorf("the network is %q, want %q", w.observer.said(), observing)
			}
			if at := w.position(t); at.Height != c.ahead {
				t.Errorf("the position is at %d, want %d", at.Height, c.ahead)
			}
		})
	}
}

// A position naming a block the chain does not have at that height is a
// position on a chain that was replaced. Nothing is read and nothing is
// written: how far back to go is not something this can work out, and going
// back too far would pay a payment twice.
func TestTick_StopsWhereTheChainNoLongerHoldsTheBlockThePositionNames(t *testing.T) {
	t.Parallel()
	cases := map[string]uint64{
		"the final block is the position": 0,
		"the final block is one above it": 1,
		"the final block is two above it": 2,
	}
	for name, ahead := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := watch(t)
			w.tick(t)
			w.paid(t, w.attempt.Key())
			for range ahead {
				w.chain.Finalize(w.chain.Mine())
			}
			network := payment.Network(w.observer.network.Name)
			elsewhere := Position{Height: 0, Hash: "0x" + strings.Repeat("99", 32)}
			if _, _, err := w.observer.cursors.Set(t.Context(), network, elsewhere, time.Now()); err != nil {
				t.Fatal(err)
			}

			w.tick(t)

			if w.observer.said() != finalizedChanged {
				t.Errorf("the network is %q, want %q", w.observer.said(), finalizedChanged)
			}
			if at := w.position(t); at != elsewhere {
				t.Errorf("the position moved to %+v", at)
			}
			if rows := w.rows(t); len(rows) != 0 {
				t.Errorf("the round wrote %+v", rows)
			}

			// Putting the position back is the operator's to do, and the round
			// after it carries on.
			block, err := w.chain.Block(t.Context(), 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := w.observer.cursors.Set(t.Context(), network, at(block), time.Now()); err != nil {
				t.Fatal(err)
			}
			w.tick(t)
			if w.observer.said() != observing {
				t.Errorf("the network is %q after the position was put back", w.observer.said())
			}
			if rows := w.rows(t); len(rows) != 1 {
				t.Errorf("the round wrote %+v", rows)
			}
		})
	}
}

// A provider whose final block is below the position is one that has not
// caught up with what another provider already said was final. There is
// nothing to read, so nothing is read, and the word says to wait rather than
// that anything is wrong.
func TestTick_WaitsWhereTheFinalBlockIsBelowThePosition(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	for range 4 {
		w.chain.Mine()
	}
	// The operator, or the provider that was read before this one, put the
	// position above what this one calls final.
	ahead, err := w.chain.Block(t.Context(), 4)
	if err != nil {
		t.Fatal(err)
	}
	network := payment.Network(w.observer.network.Name)
	if _, _, err := w.observer.cursors.Set(t.Context(), network, at(ahead), time.Now()); err != nil {
		t.Fatal(err)
	}
	before, asked := w.touched(t), w.chain.Calls()

	w.tick(t)

	if w.observer.said() != finalizedBehind {
		t.Errorf("the network is %q, want %q", w.observer.said(), finalizedBehind)
	}
	calls := w.chain.Calls()
	if calls["Keys"] != asked["Keys"] || calls["Block"] != asked["Block"] {
		t.Errorf("the round asked the chain for blocks or logs it had nothing to do with: %v", calls)
	}
	if after := w.touched(t); after.After(before) {
		t.Error("the position was written, and the round had nothing to write")
	}

	// The provider catches up with what it was given, and the rounds carry on.
	w.chain.Finalize(4)
	w.tick(t)

	if w.observer.said() != observing {
		t.Errorf("the network is %q, want %q", w.observer.said(), observing)
	}
}

// A transfer that comes back in another block is the same transfer. The row it
// already has moves to the block it is in now, and a merchant asking what
// arrived is not shown it twice.
func TestTick_MovesARecordToTheBlockATransactionCameBackIn(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	w.chain.Mine()
	w.chain.Send(w.sending(w.attempt.Key()))
	w.chain.Mine()

	w.tick(t)

	rows := w.rows(t)
	if len(rows) != 1 || rows[0].Height != 2 {
		t.Fatalf("the round wrote %+v, want one row in block 2", rows)
	}

	// The chain drops both blocks and puts the transaction in one of its own.
	w.chain.Reorg(1, rows[0].Tx)
	w.tick(t)

	moved := w.rows(t)
	if len(moved) != 1 {
		t.Fatalf("the rounds wrote %+v, want one row", moved)
	}
	if moved[0].Height != 1 || moved[0].Tx != rows[0].Tx {
		t.Errorf("the row reads %+v, and the transaction is in block 1 now", moved[0])
	}
}

// A transfer seen ahead of finality and gone by the time the position reached
// it is a transfer that was never paid. The record says so and the attempt goes
// back to where it was, so the payer can be given something to sign again.
func TestTick_MarksWhatWentAwayAndTakesTheAttemptBack(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	w.chain.Mine()
	w.chain.Send(w.sending(w.attempt.Key()))
	w.chain.Mine()
	w.tick(t)
	if got := w.status(t); got != payment.Confirming {
		t.Fatalf("the attempt is %s, and a transfer was seen ahead of finality", got)
	}
	if rows := w.rows(t); len(rows) != 1 || rows[0].Height != 2 {
		t.Fatalf("the round wrote %+v, want one row in block 2", rows)
	}

	// The blocks are replaced by ones without the transaction in them, and
	// finality passes over the range the transfer was seen in.
	w.chain.Reorg(1)
	w.chain.Mine()
	w.chain.Finalize(2)
	w.tick(t)

	rows := w.rows(t)
	if len(rows) != 1 {
		t.Fatalf("the rounds wrote %+v, want one row", rows)
	}
	if rows[0].Reason != payment.Vanished.String() {
		t.Errorf("the row says %q, and what it recorded is not on the chain", rows[0].Reason)
	}
	if got := w.status(t); got != payment.Issued {
		t.Errorf("the attempt is %s, and nothing it was confirmed on is still there", got)
	}

	// The payer's authorisation still stands, so a second submission of it
	// pays the payment.
	w.chain.Send(w.sending(w.attempt.Key()))
	w.chain.Finalize(w.chain.Mine())
	w.tick(t)

	if got := w.status(t); got != payment.Confirming {
		t.Errorf("the attempt is %s after the transfer came back", got)
	}
	back := w.rows(t)
	if len(back) != 2 {
		t.Fatalf("the rounds wrote %+v, want the one that went and the one that came", back)
	}
	if back[1].Reason != payment.Matched.String() {
		t.Errorf("the second row says %q", back[1].Reason)
	}
}

// A position the chain does not hold stays that way until somebody moves it.
// The rounds in between find what the first one found, and saying so every few
// seconds would bury whatever else the deployment has to say.
func TestTick_SaysOnceThatTheChainNoLongerHoldsThePosition(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	said := &strings.Builder{}
	w.observer.log = slog.New(slog.NewJSONHandler(said, nil))
	if _, _, err := w.observer.cursors.Set(t.Context(), payment.Network(w.observer.network.Name),
		Position{Height: 0, Hash: "0x" + strings.Repeat("99", 32)}, time.Now()); err != nil {
		t.Fatal(err)
	}

	for range 3 {
		w.tick(t)
	}

	if w.observer.said() != finalizedChanged {
		t.Fatalf("the network is %q", w.observer.said())
	}
	if count := strings.Count(said.String(), "no longer holds"); count != 1 {
		t.Errorf("the rounds said it %d times, want once", count)
	}
}

// What a round has to report was written by a provider or by the database, and
// a line goes to a terminal and to whatever collects the lines after it. slog's
// JSON handler is the one a deployment writes with, and it escapes what JSON
// requires and nothing more.
func TestRound_QuotesWhatSomebodyElseWroteIntoALine(t *testing.T) {
	t.Parallel()
	w := watch(t)
	said := &strings.Builder{}
	w.observer.log = slog.New(slog.NewJSONHandler(said, nil))
	// An override of the reading order, which reverses what a person reads
	// after it and leaves no byte anyone would notice.
	w.chain.Fail(errors.New("the provider said \u202eon"))

	w.observer.round(t.Context())

	if strings.Contains(said.String(), "\u202e") {
		t.Errorf("a line holds a character that does not show up: %q", said.String())
	}
	if !strings.Contains(said.String(), "the provider said") {
		t.Errorf("a line does not say what went wrong: %q", said.String())
	}
}

// A round that stops without a word of its own leaves the last one standing,
// and the last one says rounds are getting through. The instance holding the
// lease is the one that has to take that back: every other instance reads when
// the position was last written, and the holder is who stopped writing it.
func TestRound_SaysStalledOnceNoRoundHasFinishedForStale(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	w.paid(t, w.attempt.Key())
	w.tick(t)
	if network, _ := w.observer.Words(); network != observing {
		t.Fatalf("a round that went through says %q, want %q", network, observing)
	}

	// A call the round makes after the head, which is where a round stops
	// without saying anything more particular than that it did.
	w.paid(t, w.attempt.Key())
	w.chain.FailAt("Keys", 1, errors.New("the provider said no"))
	w.observer.round(t.Context())
	if network, _ := w.observer.Words(); network != observing {
		t.Errorf("a round that failed inside %s says %q, want %q", Stale, network, observing)
	}

	w.chain.FailAt("Keys", 1, errors.New("the provider said no"))
	w.observer.now = func() time.Time { return time.Now().Add(Stale + time.Second) }
	w.observer.round(t.Context())
	if network, _ := w.observer.Words(); network != stalled {
		t.Errorf("a round that has not finished for %s says %q, want %q", Stale, network, stalled)
	}
}

// A round that stopped for a reason of its own has already said what it was,
// and no length of time turns that into somebody having stopped reading. Which
// of the two an operator is told decides where they go looking.
func TestRound_LeavesAWordARoundSaidForItself(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	w.paid(t, w.attempt.Key())
	w.tick(t)

	// The head is what a round reads first, and a provider that will not
	// answer for it is one the round names.
	w.chain.FailAt("Head", 1, errors.New("the provider said no"))
	w.observer.now = func() time.Time { return time.Now().Add(Stale + time.Second) }
	w.observer.round(t.Context())

	if network, _ := w.observer.Words(); network != unreachable {
		t.Errorf("the network says %q, want %q", network, unreachable)
	}
}

// What a deployment is asked about is one word for the network and one for
// each asset. The names are the document's, because a reference names one
// token on four chains and would say which of them nothing.
func TestWords_AreTheNetworkAndTheAssetsByTheNameTheDocumentGives(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	w.tick(t)

	network, assets := w.observer.Words()

	if network != observing {
		t.Errorf("the network is %q, want %q", network, observing)
	}
	if want := map[string]string{"jpyc": unchanged, "other": unchanged}; !reflect.DeepEqual(assets, want) {
		t.Errorf("the assets are %v, want %v", assets, want)
	}
}

// A token that says its code was replaced is one whose behaviour nobody here
// has read. The round carries on, because what it saw is still what the chain
// says happened, and the word is what says to go and look.
func TestWords_SayAnAssetChangedWhereTheChainSaidItsCodeWas(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	w.chain.Send(w.sending(w.attempt.Key()))
	w.chain.Upgrade(w.asset.Reference())
	w.chain.Finalize(w.chain.Mine())

	w.tick(t)

	_, assets := w.observer.Words()
	if want := map[string]string{"jpyc": changed, "other": unchanged}; !reflect.DeepEqual(assets, want) {
		t.Errorf("the assets are %v, want %v", assets, want)
	}
	if got := w.status(t); got != payment.Confirming {
		t.Errorf("the attempt is %s, and the transfer was seen all the same", got)
	}
}

// Every network of a deployment answers together, and the asset names are the
// document's, so two networks carrying one token still say which is which.
func TestWords_OfEveryObserverComeBackTogether(t *testing.T) {
	t.Parallel()
	w := watch(t)
	elsewhere := watch(t)
	elsewhere.observer.network.Name = "elsewhere"
	elsewhere.observer.assets = map[string]string{"jpyc-elsewhere": unchanged}
	w.tick(t)
	w.tick(t)

	networks, assets := Observers{w.observer, elsewhere.observer}.Words()

	if want := map[string]string{"local": observing, "elsewhere": unreachable}; !reflect.DeepEqual(networks, want) {
		t.Errorf("the networks are %v, want %v", networks, want)
	}
	want := map[string]string{"jpyc": unchanged, "other": unchanged, "jpyc-elsewhere": unchanged}
	if !reflect.DeepEqual(assets, want) {
		t.Errorf("the assets are %v, want %v", assets, want)
	}
}

// The words are written by the round and read by whoever is answering
// somebody else's question about this deployment, which is not the round's
// goroutine.
func TestWords_AreReadWhileARoundIsWritingThem(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	w.paid(t, w.attempt.Key())

	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		err = w.observer.tick(t.Context())
	}()
	for range 200 {
		w.observer.Words()
	}
	<-done

	if err != nil {
		t.Fatal(err)
	}
}

// What a provider is asked is the same request every round. The assets are
// held under the names a document gave them, and where a map puts those names
// is not something a request should show.
func TestTick_AsksAboutTheAssetsInOneOrder(t *testing.T) {
	t.Parallel()
	w := watch(t)
	asked := &asking{Chain: w.chain}
	w.observer.network.Chain = asked
	w.tick(t)
	w.paid(t, w.attempt.Key())

	w.tick(t)

	want := []string{w.asset.Reference(), w.other.Reference()}
	slices.Sort(want)
	if !slices.Equal(asked.assets, want) {
		t.Errorf("the round asked about %v, want %v", asked.assets, want)
	}
}

// asking is a chain that remembers which assets it was asked about.
type asking struct {
	chain.Chain
	assets []string
}

func (a *asking) Keys(ctx context.Context, first, last uint64, assets []string) (chain.Scan, error) {
	a.assets = append([]string(nil), assets...)
	return a.Chain.Keys(ctx, first, last, assets)
}

// A round that runs longer than the lease it started under puts the lease out
// again on the way, so that nothing else takes the network over while this is
// still writing to it.
func TestTick_PutsTheLeaseOutAgainWhereLittleOfItIsLeft(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	w.paid(t, w.attempt.Key())
	w.observer.held = time.Now().Add(-(LeaseTTL - RenewBelow) - time.Second)

	w.tick(t)

	if left := time.Since(w.observer.held); left > time.Second {
		t.Errorf("the lease was last put out %s ago, and the round should have renewed it", left)
	}
	if rows := w.rows(t); len(rows) != 1 {
		t.Errorf("the round wrote %+v", rows)
	}
}

// A lease this instance no longer holds is another instance reading the same
// network. The round stops where it is: what it has read belongs to a position
// somebody else is moving.
func TestTick_WritesNothingWhenTheLeaseCouldNotBePutOutAgain(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	w.paid(t, w.attempt.Key())
	before := w.position(t)
	w.observer.held = time.Now().Add(-LeaseTTL)

	// Somebody else takes the network over.
	expire(t, w.pool, w.observer.network.Name)
	if taken, err := NewLeases(w.pool).Acquire(t.Context(), w.observer.network.Name); err != nil || !taken {
		t.Fatal(taken, err)
	}

	if err := w.observer.tick(t.Context()); err == nil {
		t.Fatal("the round went on without the lease")
	}

	if at := w.position(t); at != before {
		t.Errorf("the position moved to %+v", at)
	}
	if rows := w.rows(t); len(rows) != 0 {
		t.Errorf("the round wrote %+v", rows)
	}
}

// expire puts a lease's deadline in the past, which is what dying looks like
// to whoever comes to take it over.
func expire(t *testing.T, pool *pgxpool.Pool, name string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(),
		`update leases set expires_at = now() - interval '1 second' where name = $1`, name); err != nil {
		t.Fatal(err)
	}
}

// A provider that refuses the span narrows what the next round asks for, and
// the rounds that go through widen it again. Neither happens all at once: the
// span that was refused is the one the next round has to get through.
func TestRun_NarrowsTheSpanItAsksForAndWidensItBack(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.observer.width = 40

	w.observer.after(fmt.Errorf("reading: %w", chain.ErrTooWide))
	if w.observer.width != 20 {
		t.Errorf("the round reads %d blocks, want 20", w.observer.width)
	}
	for range 4 {
		w.observer.after(fmt.Errorf("reading: %w", chain.ErrTooWide))
	}
	if w.observer.width != minWidth {
		t.Errorf("the round reads %d blocks, want the narrowest %d", w.observer.width, minWidth)
	}

	for range RecoverAfter - 1 {
		w.observer.after(nil)
	}
	if w.observer.width != minWidth {
		t.Errorf("the round reads %d blocks after %d that went through", w.observer.width, RecoverAfter-1)
	}
	w.observer.after(nil)
	if w.observer.width != 2*minWidth {
		t.Errorf("the round reads %d blocks, want %d", w.observer.width, 2*minWidth)
	}
}

// A provider that refuses for the rate of calls is waited for, and the wait
// comes back down as rounds go through. What the provider asked to be waited
// for is what it gets, where that is a wait this would take anyway.
func TestRun_WaitsLongerWhenAProviderRefusesForTheRateOfCalls(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		after time.Duration
		want  time.Duration
	}{
		{name: "a refusal that asks for nothing", want: 24 * time.Second},
		{name: "a refusal that asks for thirty seconds", after: 30 * time.Second, want: 30 * time.Second},
		{name: "a refusal that asks for less than the document set", after: 7 * time.Second, want: 12 * time.Second},
		{name: "a refusal that asks for longer than this waits", after: 900 * time.Second, want: 24 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := watch(t)

			w.observer.after(fmt.Errorf("reading: %w", chain.RateLimited{RetryAfter: c.after}))

			if w.observer.interval != c.want {
				t.Errorf("the next round waits %s, want %s", w.observer.interval, c.want)
			}
		})
	}
}

// The wait stops going up where a provider that refuses everything would
// otherwise be asked once a day, and comes back down to what the document set.
func TestRun_KeepsTheWaitBetweenWhatWasSetAndWhatIsWorthWaiting(t *testing.T) {
	t.Parallel()
	w := watch(t)

	for range 20 {
		w.observer.after(fmt.Errorf("reading: %w", chain.RateLimited{}))
	}
	if w.observer.interval != MaxInterval {
		t.Errorf("the next round waits %s, want %s", w.observer.interval, MaxInterval)
	}

	for range 20 {
		w.observer.after(nil)
	}
	if w.observer.interval != w.observer.network.Poll {
		t.Errorf("the next round waits %s, want the %s the document set", w.observer.interval, w.observer.network.Poll)
	}
}

// A provider that has not caught up says nothing about how fast it is being
// read or how wide a span it will take: the round read nothing.
func TestRun_LeavesTheWaitAndTheSpanWhereTheFinalBlockIsBelowThePosition(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.observer.interval = 2 * time.Minute
	w.observer.width = 40
	w.observer.says(finalizedBehind)

	w.observer.after(nil)

	if w.observer.interval != 2*time.Minute || w.observer.width != 40 {
		t.Errorf("the round waits %s and reads %d blocks", w.observer.interval, w.observer.width)
	}
}

// What an instance reads before it starts reading rounds: what the chain calls
// itself, where it stands, how far it has been read, and what is behind each
// asset. It writes nothing, so whoever is only asking can ask.
func TestProbe_ReadsWhereTheNetworkStandsWithoutWritingAnything(t *testing.T) {
	t.Parallel()
	w := watch(t)
	behind := &standing{Chain: w.chain, code: "0xabc"}
	w.observer.network.Chain = behind
	w.chain.Finalize(w.chain.Mine())

	report, err := Probe(t.Context(), w.observer.network, w.observer.cursors)

	if err != nil {
		t.Fatal(err)
	}
	if report.Identity != "simulated" {
		t.Errorf("the chain calls itself %q", report.Identity)
	}
	if report.Head.Latest.Height != 1 || report.Head.Final.Height != 1 {
		t.Errorf("the head reads as %+v", report.Head)
	}
	if report.Read {
		t.Errorf("the network reads as read to %+v, and no round has run", report.Position)
	}
	want := map[string]string{"jpyc": "0xabc", "other": "0xabc"}
	if !reflect.DeepEqual(report.Behind, want) {
		t.Errorf("the assets read as %v, want %v", report.Behind, want)
	}
	if had, err := w.observer.cursors.Has(t.Context(), payment.Network(w.observer.network.Name)); err != nil || had {
		t.Errorf("the probe took a position: %v", err)
	}
}

// Once a round has been round, the probe says where it got to.
func TestProbe_SaysHowFarTheNetworkHasBeenRead(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)

	report, err := Probe(t.Context(), w.observer.network, w.observer.cursors)

	if err != nil {
		t.Fatal(err)
	}
	if !report.Read || report.Position != w.position(t) {
		t.Errorf("the network reads as read to %+v, %v", report.Position, report.Read)
	}
}

// A caller with nowhere a position could have been written still learns what
// the chain says about itself. Refusing to answer without a database would
// leave an operator who has not set one up yet unable to find out whether
// their endpoint answers at all.
func TestProbe_ReadsTheChainWithNowhereAPositionCouldHaveBeenWritten(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)

	report, err := Probe(t.Context(), w.observer.network, nil)

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if report.Identity == "" || report.Head.Final.Hash == "" {
		t.Errorf("the probe read %+v, want what the chain says about itself", report)
	}
	// A position was written by the round above, and this was not asked for
	// it: what comes back has to say so rather than read as height zero.
	if report.Read {
		t.Errorf("the probe says it read a position it was given nowhere to read one from")
	}
}

// A chain that will not answer is one nothing can be said about.
func TestProbe_ReportsWhatItCouldNotRead(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"Identity", "Head", "Implementation"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			w := watch(t)
			w.chain.FailAt(method, 1, errors.New("the provider said no"))

			_, err := Probe(t.Context(), w.observer.network, w.observer.cursors)
			if err == nil {
				t.Fatalf("the probe read a network whose %s failed", method)
			}
			// A deployment reads several networks, and what went wrong on one
			// of them says nothing until it says which.
			if !strings.Contains(err.Error(), w.observer.network.Name) {
				t.Errorf("the probe failed with %q, which does not name %s",
					err, w.observer.network.Name)
			}
		})
	}
}

// The loop reads round after round for as long as it is left to, and puts the
// lease down on the way out so that nothing waits out its term for a network
// nobody is reading.
func TestRun_ReadsUntilItIsStoppedAndPutsTheLeaseDown(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.observer.network.Poll, w.observer.interval = time.Millisecond, time.Millisecond
	ctx, stop := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() { done <- w.observer.Run(ctx) }()
	// The round that takes the position reads nothing below it, so the payer
	// arrives after it.
	waitFor(t, func() bool {
		had, err := w.observer.cursors.Has(t.Context(), payment.Network(w.observer.network.Name))
		return err == nil && had
	})
	w.paid(t, w.attempt.Key())
	waitFor(t, func() bool { return w.status(t) == payment.Confirming })
	stop()

	if err := <-done; err != nil {
		t.Fatal(err)
	}
	taken, err := NewLeases(w.pool).Acquire(t.Context(), w.observer.network.Name)
	if err != nil || !taken {
		t.Errorf("the lease was still held: %v, %v", taken, err)
	}
}

// One network is read by one instance. The one that cannot take the lease says
// what it can tell from how long ago the position was written.
func TestRun_SaysWhatItCanWhereAnotherInstanceHoldsTheNetwork(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	held := New(w.observer.network, w.pool, w.store, slog.New(slog.DiscardHandler), time.Now)

	held.round(t.Context())

	if held.said() != observing {
		t.Errorf("the instance without the lease says %q, want %q", held.said(), observing)
	}
	// A network somebody else holds and nobody has read yet is one nothing can
	// be said about beyond that.
	if taken, err := NewLeases(w.pool).Acquire(t.Context(), "nowhere"); err != nil || !taken {
		t.Fatal(taken, err)
	}
	nowhere := New(Network{Name: "nowhere", Chain: w.chain, Poll: time.Second, Width: 50},
		w.pool, w.store, slog.New(slog.DiscardHandler), time.Now)
	nowhere.round(t.Context())
	if nowhere.said() != noPosition {
		t.Errorf("a network nobody has read says %q, want %q", nowhere.said(), noPosition)
	}
	if called := w.chain.Calls()["Keys"]; called != 0 {
		t.Errorf("the instance without the lease read %d spans", called)
	}

	// Whoever holds it stops writing the position, and after a while that is
	// what the other instance says.
	held.now = func() time.Time { return time.Now().Add(Stale + time.Second) }
	held.round(t.Context())
	if held.said() != stalled {
		t.Errorf("the instance without the lease says %q, want %q", held.said(), stalled)
	}
}

// A network that could not be read at all is one to keep asking about, and to
// say so about meanwhile.
func TestRun_SaysWhyTheFirstReadFailedAndCarriesOn(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.chain.FailAt("Head", 1, chain.ErrNoFinal)

	w.observer.start(t.Context())

	if w.observer.said() != noFinalized {
		t.Errorf("the network is %q, want %q", w.observer.said(), noFinalized)
	}
	w.tick(t)
	if w.observer.said() != noPosition {
		t.Errorf("the network is %q after a round that went through", w.observer.said())
	}
}

// A proxy upgraded without a block saying so is what the slot is read for. A
// minute is what it takes to notice, which is the price of not asking every
// round.
func TestRun_ReadsWhatIsBehindAnAssetAgainAfterAWhile(t *testing.T) {
	t.Parallel()
	w := watch(t)
	behind := &standing{Chain: w.chain, code: "0xone"}
	w.observer.network.Chain = behind
	w.observer.start(t.Context())

	w.observer.round(t.Context())
	if behind.calls != 2 {
		t.Errorf("the chain was asked %d times what is behind its assets, and the round is not due to ask", behind.calls)
	}

	behind.code = "0xtwo"
	w.observer.now = func() time.Time { return time.Now().Add(ReadImplementationEvery) }
	w.observer.round(t.Context())

	_, assets := w.observer.Words()
	if want := map[string]string{"jpyc": changed, "other": changed}; !reflect.DeepEqual(assets, want) {
		t.Errorf("the assets are %v, want %v", assets, want)
	}
}

// What a chain calls itself is asked on the same footing: once at the start,
// and once a minute after that.
func TestTick_AsksWhatTheChainIsOnceAMinute(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.observer.start(t.Context())
	asked := w.chain.Calls()["Identity"]

	w.tick(t)
	w.tick(t)

	if now := w.chain.Calls()["Identity"]; now != asked {
		t.Errorf("the chain was asked what it is %d more times", now-asked)
	}
	w.observer.now = func() time.Time { return time.Now().Add(ReadImplementationEvery) }
	w.tick(t)
	if now := w.chain.Calls()["Identity"]; now != asked+1 {
		t.Errorf("the chain was asked what it is %d times, want one more", now)
	}
}

// waitFor gives a loop running in another goroutine a moment to get somewhere.
func waitFor(t *testing.T, done func() bool) {
	t.Helper()
	for range 200 {
		if done() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the loop did not get there")
}

// An instance that dies holds its lease until the term runs out, and then
// another takes the network over and reads on from where the position is.
func TestRun_TakesOverANetworkWhoseLeaseHasLapsed(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.tick(t)
	w.paid(t, w.attempt.Key())
	expire(t, w.pool, w.observer.network.Name)

	next := New(w.observer.network, w.pool, w.store, slog.New(slog.DiscardHandler), time.Now)
	next.round(t.Context())

	if next.said() != observing {
		t.Errorf("the instance that took over says %q", next.said())
	}
	if got := w.status(t); got != payment.Confirming {
		t.Errorf("the attempt is %s, and the instance that took over read the block it was paid in", got)
	}
	if rows := w.rows(t); len(rows) != 1 {
		t.Errorf("the rounds wrote %+v", rows)
	}
}
