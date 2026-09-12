package finality

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
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

// second is an account of somebody else, made where a test needs one.
const second = payment.AccountID("00000000-0000-0000-0000-000000000002")

// local is the network the payments here are on.
const local = payment.Network("local")

// decider is a worker over a chain inside the process and a database of its
// own, with one payment awaiting payment, one attempt at it, and the transfer
// that paid it recorded the way a round of the observer records what it read
// in the finalised range. What the chain says about that transfer now is the
// test's to arrange.
type decider struct {
	worker  *Worker
	chain   *simulated.Chain
	store   *payment.Postgres
	pool    *pgxpool.Pool
	asset   payment.Asset
	payment *payment.Payment
	attempt *payment.Attempt
	tx      string
	// at is what the worker's clock says. A test that wants the deadline of a
	// payment behind it moves this rather than waiting.
	at     time.Time
	leases *instance
	log    *bytes.Buffer
}

// instance is the lease as one instance sees it: whether this instance holds
// the name, and a count of what it was asked for.
type instance struct {
	mu       sync.Mutex
	holds    bool
	refuse   error
	name     string
	acquired int
	released int
}

func (i *instance) Acquire(_ context.Context, name string) (bool, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.acquired, i.name = i.acquired+1, name
	if i.refuse != nil {
		return false, i.refuse
	}
	return i.holds, nil
}

func (i *instance) Release(_ context.Context, _ string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.released++
	return nil
}

// asked is how many times the lease was asked for, and how many times it was
// put down.
func (i *instance) asked() (acquired, released int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.acquired, i.released
}

// held is the name the lease was asked for under.
func (i *instance) held() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.name
}

// decide opens everything one round reads and writes, with the transfer in a
// block the chain has not called final yet.
func decide(t *testing.T, misses int, endpoints ...chain.Chain) *decider {
	t.Helper()
	pool, err := postgres.Open(t.Context(), postgrestest.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	asset, err := payment.NewAsset(local, "0x"+strings.Repeat("cd", 20), "JPYC", 18)
	if err != nil {
		t.Fatal(err)
	}
	d := &decider{
		chain: simulated.New(), store: payment.NewPostgres(pool.Conns()), pool: pool.Conns(),
		asset: asset, at: time.Now(), leases: &instance{holds: true}, log: &bytes.Buffer{},
	}
	d.payment, d.attempt = d.attempted(t, held)
	d.tx = d.recorded(t, d.paying(d.payment, d.attempt.Key()))
	if len(endpoints) == 0 {
		endpoints = []chain.Chain{d.chain}
	}
	d.worker = New(Network{Name: string(local), Endpoints: endpoints,
		Misses: misses, Wait: time.Hour, Recheck: time.Millisecond},
		// JSON, which is what a deployment writes with. The text handler
		// escapes what a reader cannot see and would hide a line that did not.
		d.store, d.leases, slog.New(slog.NewJSONHandler(d.log, nil)),
		func() time.Time { return d.at })
	return d
}

// attempted stores a payment of one account, made payable, with an attempt
// at it.
func (d *decider) attempted(t *testing.T, account payment.AccountID) (*payment.Payment, *payment.Attempt) {
	t.Helper()
	amount, err := payment.ParseUnits(d.asset, "20000")
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
		ExpiresAt:   d.at.Add(time.Hour),
	}, d.at)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.store.Create(t.Context(), account, p); err != nil {
		t.Fatal(err)
	}
	_, at, err := d.store.Find(t.Context(), account, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Await(); err != nil {
		t.Fatal(err)
	}
	if err := d.store.Save(t.Context(), account, p, at, payment.Event{}); err != nil {
		t.Fatal(err)
	}
	a, err := payment.NewAttempt(p, d.at)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.store.Issue(t.Context(), account, a); err != nil {
		t.Fatal(err)
	}
	return p, a
}

// paying is the transfer a payer doing what they were asked leaves behind.
func (d *decider) paying(p *payment.Payment, key string) chain.Transfer {
	payer := "0x" + strings.Repeat("11", 20)
	return chain.Transfer{
		Scheme:     string(payment.EIP3009),
		Asset:      d.asset.Reference(),
		Key:        key,
		Authorizer: payer,
		From:       payer,
		To:         string(p.Destination()),
		Value:      p.Amount().Amount().String(),
	}
}

// recorded puts transfers into one block of the chain and records them the
// way a round that read the finalised range does, whether or not the chain
// has called the block final. It returns the transaction of the first.
func (d *decider) recorded(t *testing.T, transfers ...chain.Transfer) string {
	t.Helper()
	for _, transfer := range transfers {
		d.chain.Send(transfer)
	}
	height := d.chain.Mine()
	keys := make([]string, 0, len(transfers))
	for _, transfer := range transfers {
		keys = append(keys, transfer.Key)
	}
	hits, err := d.store.Consumed(t.Context(), local, keys)
	if err != nil {
		t.Fatal(err)
	}
	against := make(map[string]payment.Hit, len(hits))
	for _, hit := range hits {
		against[hit.Attempt.Key()] = hit
	}
	scan, err := d.chain.Keys(t.Context(), height, height, []string{d.asset.Reference()})
	if err != nil {
		t.Fatal(err)
	}
	var seen []payment.Seen
	var first string
	for _, consumed := range scan.Consumed {
		if first == "" {
			first = consumed.Tx
		}
		carried, err := d.chain.Receipt(t.Context(), consumed.Tx)
		if err != nil {
			t.Fatal(err)
		}
		for _, transfer := range carried {
			hit := against[transfer.Key]
			read := payment.Transfer{
				Scheme: payment.Scheme(transfer.Scheme), Asset: transfer.Asset, Key: transfer.Key,
				Authorizer: transfer.Authorizer, From: transfer.From, To: transfer.To,
				Value: transfer.Value, Tx: transfer.Tx, Position: transfer.Position,
				BlockHeight: transfer.Block.Height, BlockHash: transfer.Block.Hash,
				BlockTime: transfer.Block.Time,
			}
			reason, judged := payment.Judge(read, hit.Attempt, hit.Payment)
			if !judged {
				t.Fatalf("the transfer of %s was judged nobody's", transfer.Key)
			}
			seen = append(seen, payment.Seen{Hit: hit, Transfer: read, Reason: reason})
		}
	}
	tx, err := d.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.store.Record(t.Context(), tx, local, height, height, true, seen, d.at); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return first
}

// round is one round of the worker, which fails the test rather than handing
// back an error nobody looks at.
func (d *decider) round(t *testing.T) {
	t.Helper()
	if err := d.worker.round(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// status is where a payment has got to, read back out of the database.
func (d *decider) status(t *testing.T, account payment.AccountID, p *payment.Payment) payment.Status {
	t.Helper()
	back, _, err := d.store.Find(t.Context(), account, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	return back.Status()
}

// received is what a payment says arrived, read back out of the database.
func (d *decider) received(t *testing.T) payment.Money {
	t.Helper()
	back, _, err := d.store.Find(t.Context(), held, d.payment.ID())
	if err != nil {
		t.Fatal(err)
	}
	return back.Received()
}

// attemptStatus is where the attempt has got to.
func (d *decider) attemptStatus(t *testing.T) payment.AttemptStatus {
	t.Helper()
	a, _, err := d.store.FindAttempt(t.Context(), held, d.payment.ID(), d.attempt.ID())
	if err != nil {
		t.Fatal(err)
	}
	return a.Status()
}

// reason is what the row of a transaction reads.
func (d *decider) reason(t *testing.T, tx string) string {
	t.Helper()
	var reason string
	if err := d.pool.QueryRow(t.Context(),
		`select reason from observations where network = $1 and tx = $2`, local, tx).
		Scan(&reason); err != nil {
		t.Fatal(err)
	}
	return reason
}

// event is one row of the outbox.
type event struct {
	Name    string
	Payload string
}

// events are the outbox rows of a payment, in the order they were written.
func (d *decider) events(t *testing.T, account payment.AccountID, p *payment.Payment) []event {
	t.Helper()
	rows, err := d.pool.Query(t.Context(),
		`select event, payload from outbox where account_id = $1 and payment_id = $2 order by id`,
		account, p.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []event
	for rows.Next() {
		var one event
		if err := rows.Scan(&one.Name, &one.Payload); err != nil {
			t.Fatal(err)
		}
		out = append(out, one)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRound_PaysAPaymentWhoseTransferTheEndpointCallsFinal(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)
	d.chain.Finalize(1)

	d.round(t)

	if got := d.status(t, held, d.payment); got != payment.Succeeded {
		t.Errorf("the payment is %s, want succeeded", got)
	}
	events := d.events(t, held, d.payment)
	if len(events) != 1 || events[0].Name != "payment.succeeded" {
		t.Fatalf("the outbox holds %+v, want one payment.succeeded", events)
	}
	var told map[string]any
	if err := json.Unmarshal([]byte(events[0].Payload), &told); err != nil {
		t.Fatalf("the payload is not a JSON object: %v:\n%s", err, events[0].Payload)
	}
	for field, want := range map[string]any{
		"id":       d.payment.ID().String(),
		"status":   "succeeded",
		"amount":   "20000",
		"received": "20000",
	} {
		if told[field] != want {
			t.Errorf("the payload says %s is %v, want %v", field, told[field], want)
		}
	}
}

// A payment past its deadline is still paid by a transfer that was on its
// way: that is what the state it waits in is for.
func TestRound_PaysAPaymentWaitingForFinality(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)
	p, at, err := d.store.Find(t.Context(), held, d.payment.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.AwaitFinality(p.ExpiresAt()); err != nil {
		t.Fatal(err)
	}
	if err := d.store.Save(t.Context(), held, p, at, payment.Event{}); err != nil {
		t.Fatal(err)
	}
	d.chain.Finalize(1)

	d.round(t)

	if got := d.status(t, held, d.payment); got != payment.Succeeded {
		t.Errorf("the payment is %s, want succeeded", got)
	}
}

// Two endpoints that do not say the same thing settle nothing, and a payment
// nothing settled stays where it is. Succeeded cannot be taken back, so not
// deciding is the safe side.
func TestRound_MovesNothingWhileTheEndpointsDisagree(t *testing.T) {
	t.Parallel()
	behind := simulated.New()
	d := decide(t, 2)
	d.worker.network.Endpoints = []chain.Chain{d.chain, behind}
	behind.Send(d.paying(d.payment, d.attempt.Key()))
	behind.Mine()
	d.chain.Finalize(1)

	d.round(t)

	if got := d.status(t, held, d.payment); got != payment.AwaitingPayment {
		t.Errorf("the payment is %s, want it left awaiting payment", got)
	}
	if events := d.events(t, held, d.payment); len(events) != 0 {
		t.Errorf("the outbox holds %+v, want nothing", events)
	}
}

func TestRound_MovesNothingWhileTheBlockIsNotFinal(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)

	d.round(t)

	if got := d.status(t, held, d.payment); got != payment.AwaitingPayment {
		t.Errorf("the payment is %s, want it left awaiting payment", got)
	}
}

// A transfer in another block than the one recorded is a record that is
// wrong, and the observer's next reading of the range is what puts it right.
// Nothing is paid on it and nothing is taken back. It is said once, for the
// same reason the endpoints disagreeing is.
func TestRound_MovesNothingWhenTheTransferIsInAnotherBlock(t *testing.T) {
	t.Parallel()
	d := decide(t, 1)
	d.chain.Reorg(1, d.tx)
	d.chain.Finalize(1)

	d.round(t)
	d.round(t)
	d.round(t)

	if got := d.status(t, held, d.payment); got != payment.AwaitingPayment {
		t.Errorf("the payment is %s, want it left awaiting payment", got)
	}
	if got := d.reason(t, d.tx); got != string(payment.Matched) {
		t.Errorf("the row reads %s, want it left matched", got)
	}
	if n := strings.Count(d.log.String(), "another block"); n != 1 {
		t.Errorf("the log says the transfer is in another block %d times over three rounds, want once:\n%s", n, d.log)
	}
}

// Not being found once is not being gone: the endpoint may be handed the block
// later. The rounds in a row the endpoints have to say it are the network's
// to set.
func TestRound_MarksATransferVanishedAfterTheRoundsItIsSetTo(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)
	d.chain.Reorg(1)
	d.chain.Finalize(1)

	d.round(t)

	if got := d.reason(t, d.tx); got != string(payment.Matched) {
		t.Fatalf("after one round the row reads %s, want it still matched", got)
	}
	if got := d.attemptStatus(t); got != payment.Confirming {
		t.Errorf("after one round the attempt is %s, want it left confirming", got)
	}

	d.round(t)

	if got := d.reason(t, d.tx); got != string(payment.Vanished) {
		t.Errorf("after two rounds the row reads %s, want vanished", got)
	}
	if got := d.attemptStatus(t); got != payment.Issued {
		t.Errorf("after two rounds the attempt is %s, want issued", got)
	}
	if got := d.received(t); got.IsSet() {
		t.Errorf("after two rounds received = %v, want nothing", got)
	}
	if got := d.status(t, held, d.payment); got != payment.AwaitingPayment {
		t.Errorf("the payment is %s, want it left awaiting payment", got)
	}
}

// shortOf is the same transfer for less than the payment asked.
func shortOf(transfer chain.Transfer) chain.Transfer {
	transfer.Value = "1"
	return transfer
}

// A payment holds one arrival, so a transfer short of what was asked, arriving
// first, is what the payment says it received. A transfer that matched and is
// final does not cover it, and settling it would say a payment was paid for
// less than it asked. Said once, on the way in: the state does not clear
// itself.
func TestRound_LeavesAPaymentWhoseArrivalIsShortOfWhatItAsked(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)
	theirs, attempt := d.attempted(t, held)
	d.recorded(t, shortOf(d.paying(theirs, attempt.Key())))
	d.recorded(t, d.paying(theirs, attempt.Key()))
	d.chain.Finalize(3)

	d.round(t)
	d.round(t)
	d.round(t)

	if got := d.status(t, held, theirs); got != payment.AwaitingPayment {
		t.Errorf("the payment is %s, want it left awaiting payment", got)
	}
	if events := d.events(t, held, theirs); len(events) != 0 {
		t.Errorf("the outbox holds %+v, want nothing", events)
	}
	if n := strings.Count(d.log.String(), "payment not settled"); n != 1 {
		t.Errorf("the log says the payment was not settled %d times over three rounds, want once:\n%s", n, d.log)
	}
}

// answering is an endpoint that says what a test lined up, one receipt to a
// round, with its final block far above anything recorded.
type answering struct {
	chain.Chain
	receipts [][]chain.Transfer
}

func (a *answering) Receipt(context.Context, string) ([]chain.Transfer, error) {
	next := a.receipts[0]
	a.receipts = a.receipts[1:]
	if next == nil {
		return nil, chain.ErrNoTransaction
	}
	return next, nil
}

func (a *answering) Head(context.Context) (chain.Head, error) {
	return chain.Head{Final: chain.Block{Height: 100}}, nil
}

// The rounds are counted in a row. A transfer that is found again between two
// rounds that did not find it was not gone all along.
func TestRound_CountsOnlyRoundsInARowThatDoNotFindTheTransfer(t *testing.T) {
	t.Parallel()
	elsewhere := []chain.Transfer{{Tx: "tx1", Block: chain.Block{Height: 1, Hash: "another"}}}
	endpoint := &answering{receipts: [][]chain.Transfer{nil, elsewhere, nil, nil}}
	d := decide(t, 2, endpoint)

	d.round(t)
	d.round(t)
	d.round(t)

	if got := d.reason(t, d.tx); got != string(payment.Matched) {
		t.Fatalf("after gone, elsewhere, gone the row reads %s, want it still matched", got)
	}

	d.round(t)

	if got := d.reason(t, d.tx); got != string(payment.Vanished) {
		t.Errorf("after gone, gone the row reads %s, want vanished", got)
	}
}

func TestRound_AsksNothingAboutAPaymentItPaid(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)
	d.chain.Finalize(1)
	d.round(t)
	before := d.chain.Calls()["Receipt"]

	d.round(t)

	if after := d.chain.Calls()["Receipt"]; after != before {
		t.Errorf("a paid payment's transfer was asked about %d more times", after-before)
	}
}

func TestRound_PaysThePaymentsOfEveryAccount(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)
	if _, err := d.pool.Exec(t.Context(),
		`insert into accounts (id, name) values ($1, 'second')`, second); err != nil {
		t.Fatal(err)
	}
	theirs, attempt := d.attempted(t, second)
	d.recorded(t, d.paying(theirs, attempt.Key()))
	d.chain.Finalize(2)

	d.round(t)

	if got := d.status(t, held, d.payment); got != payment.Succeeded {
		t.Errorf("the first account's payment is %s, want succeeded", got)
	}
	if got := d.status(t, second, theirs); got != payment.Succeeded {
		t.Errorf("the second account's payment is %s, want succeeded", got)
	}
}

// Two transfers recorded against one payment are two candidates for one
// change. The second finds the payment moved and is passed over, and the round
// goes on to whatever is next.
func TestRound_PaysAPaymentOnceWhenTwoTransfersPaidIt(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)
	d.recorded(t, d.paying(d.payment, d.attempt.Key()))
	d.chain.Finalize(2)

	d.round(t)

	if events := d.events(t, held, d.payment); len(events) != 1 {
		t.Errorf("the outbox holds %+v, want one event", events)
	}
}

// A round cut short leaves the counts of the last round that finished. The
// rounds counted are the ones the endpoints answered, so a provider that was
// unreachable for one of them neither advances a transfer towards being given
// up on nor sends the count back to the start.
func TestRound_KeepsTheCountAcrossARoundThatWasCutShort(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)
	d.chain.Reorg(1)
	d.chain.Finalize(1)

	d.round(t)
	d.chain.FailAt("Receipt", 1, errors.New("no"))
	if err := d.worker.round(t.Context()); err == nil {
		t.Fatal("the round that could not read the chain reported nothing")
	}
	d.round(t)

	if got := d.reason(t, d.tx); got != string(payment.Vanished) {
		t.Errorf("the row reads %s, want vanished after the two rounds that answered", got)
	}
}

// An endpoint that does not answer ends the round where it is. What it was
// asked about is not moved, and nor is anything after it: the round is asked
// again later.
func TestRound_MovesNothingWhenAnEndpointDoesNotAnswer(t *testing.T) {
	t.Parallel()
	sorry := errors.New("no")
	d := decide(t, 2)
	d.chain.Finalize(1)
	d.chain.FailAt("Receipt", 1, sorry)

	err := d.worker.round(t.Context())

	if !errors.Is(err, sorry) {
		t.Errorf("round = %v, want the endpoint's own error", err)
	}
	if got := d.status(t, held, d.payment); got != payment.AwaitingPayment {
		t.Errorf("the payment is %s, want it left awaiting payment", got)
	}
}

// A key belongs to a payer. What a round says about a payment it paid or a
// transfer that vanished names the payment and the transaction, never the key.
func TestRound_WritesNoKeyToTheLog(t *testing.T) {
	t.Parallel()
	d := decide(t, 1)
	theirs, attempt := d.attempted(t, held)
	d.recorded(t, d.paying(theirs, attempt.Key()))
	d.chain.Reorg(2)
	d.chain.Finalize(2)

	d.round(t)

	for _, want := range []string{"payment succeeded", "transfer vanished"} {
		if !strings.Contains(d.log.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, d.log)
		}
	}
	for _, key := range []string{d.attempt.Key(), attempt.Key()} {
		if strings.Contains(d.log.String(), key) {
			t.Errorf("the log carries a key:\n%s", d.log)
		}
	}
}

// Said once, on the way into the state. A deployment whose endpoints disagree
// about a transfer disagrees about it every round until somebody looks, and a
// line a round would make each time buries whatever else the log has to say.
func TestRound_SaysOnceThatTheEndpointsDisagree(t *testing.T) {
	t.Parallel()
	behind := simulated.New()
	d := decide(t, 2)
	d.worker.network.Endpoints = []chain.Chain{d.chain, behind}
	behind.Send(d.paying(d.payment, d.attempt.Key()))
	behind.Mine()
	d.chain.Finalize(1)

	d.round(t)
	d.round(t)
	d.round(t)

	if n := strings.Count(d.log.String(), "disagree"); n != 1 {
		t.Errorf("the log says the endpoints disagree %d times over three rounds, want once:\n%s", n, d.log)
	}
}

// A count under one would mark a transfer vanished the first time it was not
// found, which is what the count is there to prevent.
func TestRound_RefusesToMarkVanishedAfterNoRounds(t *testing.T) {
	t.Parallel()
	d := decide(t, 0)
	d.chain.Reorg(1)
	d.chain.Finalize(1)

	err := d.worker.round(t.Context())

	if err == nil {
		t.Fatal("a round ran with the count set to nothing")
	}
	if got := d.reason(t, d.tx); got != string(payment.Matched) {
		t.Errorf("the row reads %s, want it left matched", got)
	}
}

// The deadline is the moment a payment stops being payable. It moves to
// waiting for finality whether or not a transfer is on its way, because a
// transfer that was signed before the deadline can still arrive after it.
func TestRound_MovesAPaymentThatReachedItsDeadlineToWaitingForFinality(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)
	d.at = d.at.Add(time.Hour)

	d.round(t)

	if got := d.status(t, held, d.payment); got != payment.AwaitingFinality {
		t.Errorf("the payment is %s, want awaiting finality", got)
	}
	events := d.events(t, held, d.payment)
	if len(events) != 1 || events[0].Name != "payment.awaiting_finality" {
		t.Fatalf("the outbox holds %+v, want one payment.awaiting_finality", events)
	}
	var told map[string]any
	if err := json.Unmarshal([]byte(events[0].Payload), &told); err != nil {
		t.Fatalf("the payload is not a JSON object: %v:\n%s", err, events[0].Payload)
	}
	if told["status"] != "awaiting_finality" {
		t.Errorf("the payload says the status is %v, want awaiting_finality", told["status"])
	}
}

// The wait runs from the deadline. A payment reaches the end of it with
// nothing matched against it, and there is nothing left to wait for.
func TestRound_ExpiresAPaymentWhoseWaitIsOverWithNothingMatchedAgainstIt(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)
	unpaid, _ := d.attempted(t, held)

	d.at = d.at.Add(time.Hour)
	d.round(t)

	if got := d.status(t, held, unpaid); got != payment.AwaitingFinality {
		t.Fatalf("at the deadline the payment is %s, want awaiting finality", got)
	}

	d.at = d.at.Add(time.Hour)
	d.round(t)

	if got := d.status(t, held, unpaid); got != payment.Expired {
		t.Errorf("a wait past the deadline the payment is %s, want expired", got)
	}
	events := d.events(t, held, unpaid)
	if len(events) != 2 || events[1].Name != "payment.expired" {
		t.Errorf("the outbox holds %+v, want payment.awaiting_finality then payment.expired", events)
	}
}

// A transfer that settles while the payment waits settles it. Reaching the end
// of the wait is not what ends a payment; having nothing that could pay it is.
func TestRound_SettlesAPaymentThatWasPaidWhileItWaited(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)

	d.at = d.at.Add(time.Hour)
	d.round(t)

	if got := d.status(t, held, d.payment); got != payment.AwaitingFinality {
		t.Fatalf("at the deadline the payment is %s, want awaiting finality", got)
	}

	d.chain.Finalize(1)
	d.at = d.at.Add(time.Hour)
	d.round(t)

	if got := d.status(t, held, d.payment); got != payment.Succeeded {
		t.Errorf("the payment is %s, want succeeded", got)
	}
	events := d.events(t, held, d.payment)
	if len(events) != 2 || events[1].Name != "payment.succeeded" {
		t.Errorf("the outbox holds %+v, want payment.awaiting_finality then payment.succeeded", events)
	}
}

// One round for the deployment, not one per merchant. The round here is far
// enough past the deadline that both sweeps take the same payment: it stops
// being payable and is given up on without a round in between, which is what a
// worker that was down for a while comes back to. A payment with a transfer
// still matched against it is left waiting in that same round.
func TestRound_SweepsTheClockOverEveryAccount(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)
	if _, err := d.pool.Exec(t.Context(),
		`insert into accounts (id, name) values ($1, 'second')`, second); err != nil {
		t.Fatal(err)
	}
	mine, _ := d.attempted(t, held)
	theirs, _ := d.attempted(t, second)

	d.at = d.at.Add(2 * time.Hour)
	d.round(t)

	for account, p := range map[payment.AccountID]*payment.Payment{held: mine, second: theirs} {
		if got := d.status(t, account, p); got != payment.Expired {
			t.Errorf("under %s the payment is %s, want expired", account, got)
		}
	}
	if got := d.status(t, held, d.payment); got != payment.AwaitingFinality {
		t.Errorf("the payment with a transfer matched against it is %s, want it left waiting", got)
	}
}

// A wait of nothing would end a payment in the round that stopped it being
// payable, which is the wait's whole purpose.
func TestRound_RefusesToRunWithNothingToWait(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)
	d.worker.network.Wait = 0
	unpaid, _ := d.attempted(t, held)
	d.at = d.at.Add(2 * time.Hour)

	err := d.worker.round(t.Context())

	if err == nil {
		t.Fatal("a round ran with nothing set to wait")
	}
	if got := d.status(t, held, unpaid); got != payment.AwaitingPayment {
		t.Errorf("the payment is %s, want it left awaiting payment", got)
	}
}

// The round decides before it reads the clock. A transfer that settles a
// payment in the same round that its deadline passed settles it, rather than
// moving it to waiting and leaving it there until the round after.
func TestRound_SettlesBeforeItReadsTheClock(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)
	d.chain.Finalize(1)
	d.at = d.at.Add(time.Hour)

	d.round(t)

	if got := d.status(t, held, d.payment); got != payment.Succeeded {
		t.Errorf("the payment is %s, want succeeded", got)
	}
	events := d.events(t, held, d.payment)
	if len(events) != 1 || events[0].Name != "payment.succeeded" {
		t.Errorf("the outbox holds %+v, want one payment.succeeded", events)
	}
}

// running starts the worker and waits for what a test is looking for, then
// stops it and waits for it to put its lease down.
func (d *decider) running(t *testing.T, until func() bool) {
	t.Helper()
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- d.worker.Run(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for !until() {
		if time.Now().After(deadline) {
			stop()
			<-done
			t.Fatal("the worker did not get there")
		}
		time.Sleep(time.Millisecond)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want none", err)
	}
}

// The lease is asked for under a name of its own. Whoever reads the chain
// holds the network's own name, and the two run beside each other.
func TestRun_HoldsTheNetworkUnderANameOfItsOwn(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)
	d.chain.Finalize(1)

	d.running(t, func() bool { return d.status(t, held, d.payment) == payment.Succeeded })

	if name := d.leases.held(); name == string(local) || !strings.Contains(name, string(local)) {
		t.Errorf("the lease was asked for under %q, want a name of its own naming %s", name, local)
	}
	if _, released := d.leases.asked(); released != 1 {
		t.Errorf("the lease was put down %d times on the way out, want once", released)
	}
}

// One instance decides for a network. The spare asks for the lease, is told
// no, and reads nothing.
func TestRun_LeavesTheNetworkAloneWhileAnotherInstanceHoldsTheLease(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)
	d.chain.Finalize(1)
	d.leases.holds = false
	// What the recording in decide asked the chain, which is not this round's.
	before := d.chain.Calls()["Receipt"]

	d.running(t, func() bool { acquired, _ := d.leases.asked(); return acquired >= 3 })

	if got := d.status(t, held, d.payment); got != payment.AwaitingPayment {
		t.Errorf("the payment is %s, want it left awaiting payment", got)
	}
	if asked := d.chain.Calls()["Receipt"] - before; asked != 0 {
		t.Errorf("the chain was asked about %d transfers without the lease, want none", asked)
	}
}

// What somebody else's failure put in the line is bounded and has nothing
// invisible left in it. slog's JSON handler, which is what a deployment writes
// with, passes a character a reader cannot see through as the bytes it was
// given.
func TestRun_WritesNothingInvisibleToTheLog(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)
	d.leases.refuse = errors.New("before\u202eafter")

	d.running(t, func() bool { acquired, _ := d.leases.asked(); return acquired >= 2 })

	if strings.Contains(d.log.String(), "\u202e") {
		t.Errorf("the log carries a character that overrides the reading order:\n%s", d.log)
	}
	if !strings.Contains(d.log.String(), "before") {
		t.Errorf("the log lost what the failure said:\n%s", d.log)
	}
}

// A lease that cannot be asked for, and a round that cannot finish, both leave
// the worker coming round again. An instance that stopped working on a network
// because a provider was down for a minute would need somebody to restart it.
func TestRun_ComesRoundAgainAfterAFailure(t *testing.T) {
	t.Parallel()
	for what, fail := range map[string]func(*decider){
		"a lease it could not ask for": func(d *decider) { d.leases.refuse = errors.New("no") },
		"a round that did not finish":  func(d *decider) { d.worker.network.Misses = 0 },
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			d := decide(t, 2)
			fail(d)

			d.running(t, func() bool { acquired, _ := d.leases.asked(); return acquired >= 3 })

			if _, released := d.leases.asked(); released != 1 {
				t.Errorf("the lease was put down %d times, want once on the way out", released)
			}
		})
	}
}

// A round reads a bounded number of candidates, so a deployment with more of
// them than that gets through them over several rounds. Reading from the
// beginning every round would leave whatever sits at the front in front of
// everything behind it.
func TestRound_ComesRoundToEveryCandidateWhenOneRoundCannotReadThemAll(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)
	d.worker.perRound = 1
	theirs, attempt := d.attempted(t, held)
	d.recorded(t, d.paying(theirs, attempt.Key()))
	d.chain.Finalize(2)

	d.round(t)

	if got := d.status(t, held, d.payment); got != payment.Succeeded {
		t.Fatalf("after one round the first payment is %s, want succeeded", got)
	}
	if got := d.status(t, held, theirs); got != payment.AwaitingPayment {
		t.Fatalf("after one round the second payment is %s, want it not yet read", got)
	}

	d.round(t)

	if got := d.status(t, held, theirs); got != payment.Succeeded {
		t.Errorf("after two rounds the second payment is %s, want succeeded", got)
	}
}

// The count is of times the endpoints were asked and found nothing, not of
// rounds. A transfer read every other round is asked about every other round,
// and what it takes to be given up on is the same number of answers.
func TestRound_CountsTheTimesItAskedWhenARoundCannotReadThemAll(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)
	d.worker.perRound = 1
	theirs, attempt := d.attempted(t, held)
	second := d.recorded(t, d.paying(theirs, attempt.Key()))
	d.chain.Reorg(1)
	d.chain.Finalize(d.chain.Mine())

	for range 3 {
		d.round(t)
	}

	for _, tx := range []string{d.tx, second} {
		if got := d.reason(t, tx); got != string(payment.Matched) {
			t.Fatalf("after one answer each, %s reads %s, want it still matched", tx, got)
		}
	}

	d.round(t)
	d.round(t)

	for _, tx := range []string{d.tx, second} {
		if got := d.reason(t, tx); got != string(payment.Vanished) {
			t.Errorf("after two answers each, %s reads %s, want vanished", tx, got)
		}
	}
}

// What a probe is answered with about this network, and what makes it change.
// A deployment that is not settling what it recorded is one somebody has to
// act on, and the word is how they find out.
func TestWord_SaysWhatTheNetworkHasComeTo(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)

	if got := d.worker.Word(); got != noRound {
		t.Errorf("before any round the word is %q, want %q", got, noRound)
	}

	d.round(t)
	d.worker.says(deciding)
	if got := d.worker.Word(); got != deciding {
		t.Errorf("after a round the word is %q, want %q", got, deciding)
	}

	d.at = d.at.Add(staleAfter*d.worker.network.Recheck + time.Second)
	if got := d.worker.Word(); got != stalled {
		t.Errorf("after the rounds it may miss the word is %q, want %q", got, stalled)
	}
}

// One word for every network this instance settles, which is what the probe
// hands on.
func TestWords_NameEveryNetworkTheDeploymentSettles(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)

	words := Workers{d.worker}.Words()

	if len(words) != 1 || words[string(local)] != noRound {
		t.Errorf("Words = %v, want %s under %s", words, noRound, local)
	}
}

// The spare says so. A deployment where another instance holds the network is
// a deployment that is settling it.
func TestRun_SaysAnotherInstanceHoldsTheNetwork(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)
	d.chain.Finalize(1)
	d.leases.holds = false

	d.running(t, func() bool { return d.worker.Word() == waiting })

	if got := d.worker.Word(); got != waiting {
		t.Errorf("the word is %q, want %q", got, waiting)
	}
}

// A round that did not finish is almost always a provider that did not answer.
func TestRun_SaysARoundThatCouldNotFinish(t *testing.T) {
	t.Parallel()
	d := decide(t, 2)
	d.worker.network.Misses = 0

	d.running(t, func() bool { return d.worker.Word() == unreachable })

	if got := d.worker.Word(); got != unreachable {
		t.Errorf("the word is %q, want %q", got, unreachable)
	}
}
