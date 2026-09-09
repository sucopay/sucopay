package observe

import (
	"context"
	"errors"
	"log/slog"
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
		Assets: []payment.Asset{asset, other},
		Poll:   3 * time.Second,
		Width:  50,
	}, pool.Conns(), store, slog.New(slog.DiscardHandler), time.Now)
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

// round is one turn of the observer, which fails the test rather than handing
// back an error nobody looks at.
func (w *watching) round(t *testing.T) {
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
	w.round(t)
	w.paid(t, w.attempt.Key())

	w.round(t)

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
	w.round(t)
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
	w.round(t)
	before := w.touched(t)

	w.round(t)

	if called := w.chain.Calls()["Keys"]; called != 0 {
		t.Errorf("the chain was asked for keys %d times, and nothing new was finalised", called)
	}
	if after := w.touched(t); !after.After(before) {
		t.Errorf("the position was last written at %s, and a round has run since", after)
	}
	if w.observer.word != observing {
		t.Errorf("the network is %q, want %q", w.observer.word, observing)
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
			w.round(t)
			w.chain.Send(c.sending(w, w.sending(w.attempt.Key())))
			w.chain.Finalize(w.chain.Mine())

			w.round(t)

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
	w.round(t)
	w.paid(t, strings.Repeat("f0", 32))

	w.round(t)

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
	if w.observer.word != chainMismatch {
		t.Errorf("the network is %q, want %q", w.observer.word, chainMismatch)
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
	w.round(t)
	w.paid(t, w.attempt.Key())

	w.round(t)

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
	w.round(t)
	w.chain.Send(w.sending(w.attempt.Key()))
	w.chain.Mine()

	w.round(t)

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
	w.round(t)
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
	w.round(t)
	w.paid(t, w.attempt.Key())
	w.chain.Mine()
	before := w.position(t)

	width := w.observer.network.Width

	w.chain.FailAt("Keys", 2, chain.ErrTooWide)
	w.round(t)

	// What a provider refuses ahead of finality says nothing about the width:
	// the range ends at the newest block, and a node behind the one that named
	// it refuses that range whatever its width.
	if w.observer.network.Width != width {
		t.Errorf("the round reads %d blocks now, and it read %d", w.observer.network.Width, width)
	}
	if w.observer.word != observing {
		t.Errorf("the network is %q, want %q", w.observer.word, observing)
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
	w.round(t)
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
	w.round(t)
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
	w.round(t)
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

	w.round(t)

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
	w.round(t)
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
	w.round(t)
	w.paid(t, w.attempt.Key())

	w.round(t)

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
	w.round(t)
	w.paid(t, w.attempt.Key())

	w.round(t)

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

	w.round(t)

	if w.observer.word != noPosition {
		t.Errorf("the network is %q, want %q", w.observer.word, noPosition)
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
	w.round(t)
	w.observer.network.Width = 2
	w.chain.Mine()
	w.chain.Mine()
	w.chain.Send(w.sending(w.attempt.Key()))
	w.chain.Finalize(w.chain.Mine())

	w.round(t)

	// The transfer is in the third block above the position, and two is what
	// this round reads.
	if at := w.position(t); at.Height != 2 {
		t.Fatalf("the position is at %d, and two blocks is what one round reads", at.Height)
	}
	if rows := w.rows(t); len(rows) != 0 {
		t.Errorf("the round wrote %+v out of a block it had not reached", rows)
	}

	w.round(t)
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

			if w.observer.word != c.word {
				t.Errorf("the network is %q, want %q", w.observer.word, c.word)
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
	w.round(t)
	sent := w.sending(w.attempt.Key())
	sent.Scheme = "somebody else's scheme"
	w.chain.Send(sent)
	w.chain.Finalize(w.chain.Mine())

	w.round(t)

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
	w.round(t)
	w.paid(t, w.attempt.Key())
	before := w.position(t)
	w.observer.network.Width = 0

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
	w.round(t)
	behind := &standing{Chain: w.chain, code: "0xabc"}
	w.observer.network.Chain = behind
	w.chain.Send(w.sending(w.attempt.Key()))
	w.chain.Send(w.paying(second, w.attempt2.Key()))
	w.chain.Finalize(w.chain.Mine())

	w.round(t)

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
	w.round(t)
	w.chain.Send(w.sending(w.attempt.Key()))
	w.chain.Send(w.sending(w.attempt.Key()))
	w.chain.Finalize(w.chain.Mine())

	w.round(t)

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
			w.round(t)
			for range c.ahead {
				w.chain.Mine()
			}
			if c.ahead > 0 {
				w.chain.Finalize(c.ahead)
			}
			before := w.chain.Calls()["Block"]

			w.round(t)

			if asked := w.chain.Calls()["Block"] - before; asked != c.asks {
				t.Errorf("the chain was asked for %d blocks, want %d", asked, c.asks)
			}
			if w.observer.word != observing {
				t.Errorf("the network is %q, want %q", w.observer.word, observing)
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
			w.round(t)
			w.paid(t, w.attempt.Key())
			for range ahead {
				w.chain.Finalize(w.chain.Mine())
			}
			network := payment.Network(w.observer.network.Name)
			elsewhere := Position{Height: 0, Hash: "0x" + strings.Repeat("99", 32)}
			if _, _, err := w.observer.cursors.Set(t.Context(), network, elsewhere, time.Now()); err != nil {
				t.Fatal(err)
			}

			w.round(t)

			if w.observer.word != finalizedChanged {
				t.Errorf("the network is %q, want %q", w.observer.word, finalizedChanged)
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
			w.round(t)
			if w.observer.word != observing {
				t.Errorf("the network is %q after the position was put back", w.observer.word)
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
	w.round(t)
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

	w.round(t)

	if w.observer.word != finalizedBehind {
		t.Errorf("the network is %q, want %q", w.observer.word, finalizedBehind)
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
	w.round(t)

	if w.observer.word != observing {
		t.Errorf("the network is %q, want %q", w.observer.word, observing)
	}
}

// A transfer that comes back in another block is the same transfer. The row it
// already has moves to the block it is in now, and a merchant asking what
// arrived is not shown it twice.
func TestTick_MovesARecordToTheBlockATransactionCameBackIn(t *testing.T) {
	t.Parallel()
	w := watch(t)
	w.round(t)
	w.chain.Mine()
	w.chain.Send(w.sending(w.attempt.Key()))
	w.chain.Mine()

	w.round(t)

	rows := w.rows(t)
	if len(rows) != 1 || rows[0].Height != 2 {
		t.Fatalf("the round wrote %+v, want one row in block 2", rows)
	}

	// The chain drops both blocks and puts the transaction in one of its own.
	w.chain.Reorg(1, rows[0].Tx)
	w.round(t)

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
	w.round(t)
	w.chain.Mine()
	w.chain.Send(w.sending(w.attempt.Key()))
	w.chain.Mine()
	w.round(t)
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
	w.round(t)

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
	w.round(t)

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
	w.round(t)
	said := &strings.Builder{}
	w.observer.log = slog.New(slog.NewJSONHandler(said, nil))
	if _, _, err := w.observer.cursors.Set(t.Context(), payment.Network(w.observer.network.Name),
		Position{Height: 0, Hash: "0x" + strings.Repeat("99", 32)}, time.Now()); err != nil {
		t.Fatal(err)
	}

	for range 3 {
		w.round(t)
	}

	if w.observer.word != finalizedChanged {
		t.Fatalf("the network is %q", w.observer.word)
	}
	if count := strings.Count(said.String(), "no longer holds"); count != 1 {
		t.Errorf("the rounds said it %d times, want once", count)
	}
}
