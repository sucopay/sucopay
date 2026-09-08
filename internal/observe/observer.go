package observe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sucopay/sucopay/internal/adapter/chain"
	"github.com/sucopay/sucopay/internal/payment"
)

// The words one round leaves behind for whoever asks whether this deployment
// is reading a chain. One network has one of them at a time.
const (
	// observing is a round that read the finalised range and wrote what it
	// found.
	observing = "observing"
	// noPosition is a network nothing has been read on yet.
	noPosition = "no-position"
	// unreachable is a provider that did not answer.
	unreachable = "unreachable"
	// noFinalized is a provider that will not answer for the finalized block.
	// The word is spelt the way the tag it comes from is spelt, because it is
	// that tag going unanswered that this says.
	noFinalized = "no-finalized"
	// chainMismatch is a chain that is not the one the document names.
	chainMismatch = "chain-mismatch"
)

// Network is one network as the observer reads it.
type Network struct {
	// Name is what the configuration document calls the network, which is the
	// name the assets of its payments carry.
	Name string
	// Want is the identity the chain is expected to have, or empty where
	// nothing said which chain to expect.
	Want string
	// Chain is what the network is read through.
	Chain chain.Chain
	// Assets are the tokens payments on this network arrive in. A transfer of
	// anything else is not read.
	Assets []payment.Asset
	// Poll is how long to wait between rounds.
	Poll time.Duration
	// Width is the most blocks one round reads.
	Width int
}

// blocks is the width as a count of blocks, and none for a width that is not
// one. The document a width comes from holds it above zero, and a round reads
// this before it reads a chain.
func (n Network) blocks() uint64 {
	if n.Width <= 0 {
		return 0
	}
	return uint64(n.Width)
}

// Observer reads one network and writes down what it finds there.
//
// One instance of a deployment observes a network at a time, and which one is
// settled by a lease rather than here: an observer reads and writes as though
// it holds one.
type Observer struct {
	network Network
	pool    *pgxpool.Pool
	cursors *Cursors
	store   *payment.Postgres
	log     *slog.Logger
	now     func() time.Time

	// word is what the last round made of the network, in the one word
	// whoever asks after this deployment is given.
	word string
}

// New returns an observer of one network, reading and writing through pool.
func New(n Network, pool *pgxpool.Pool, store *payment.Postgres, log *slog.Logger, now func() time.Time) *Observer {
	return &Observer{
		network: n,
		pool:    pool,
		cursors: NewCursors(pool),
		store:   store,
		log:     log,
		now:     now,
	}
}

// tick is one round.
//
// What the chain says it is and where it stands comes first, then the blocks
// between the position and the final block, then the blocks after it. What the
// finalised range found and the position it read to are written together, so a
// position that moved was written with everything found below it.
func (o *Observer) tick(ctx context.Context) (err error) {
	// One round is about one network, and the name goes on whatever it
	// reports rather than on each of the places that could report something.
	defer func() {
		if err != nil {
			err = fmt.Errorf("%s: %w", o.network.Name, err)
		}
	}()

	network := payment.Network(o.network.Name)
	// A round reads at least one block. A width that says otherwise would have
	// a range run backwards, which is a provider asked for nothing and a
	// position moved past blocks nobody read.
	if o.network.blocks() == 0 {
		return fmt.Errorf("one round is set to read %d blocks", o.network.Width)
	}
	head, err := o.standing(ctx)
	if err != nil {
		return err
	}
	from, found, err := o.cursors.Get(ctx, network)
	if err != nil {
		return err
	}
	if !found {
		// The first round reads nothing. An attempt is issued against the
		// position of the moment, so nothing below the block this starts at
		// was ever payable.
		o.word = noPosition
		return o.cursors.Init(ctx, network, at(head.Final), o.now())
	}

	// What is behind an asset is read once for the round, however many of its
	// ranges turn something up.
	behind := map[string]string{}
	if head.Final.Height > from.Height {
		if err := o.settled(ctx, network, from, head.Final, behind); err != nil {
			return err
		}
	} else if err := o.stood(ctx, network, from); err != nil {
		return err
	}
	o.word = observing

	// Ahead of finality nothing moves the position. What is found there is
	// written as evidence and read again when the finalised range reaches it,
	// so a round that cannot read it is a round that found nothing yet.
	if err := o.ahead(ctx, network, head, behind); err != nil {
		o.log.Warn("reading ahead of the final block failed",
			"network", o.network.Name, "error", err.Error())
	}
	return nil
}

// standing is what the chain says it is and where it stands, or the word for
// why the round stops here.
func (o *Observer) standing(ctx context.Context) (chain.Head, error) {
	identity, err := o.network.Chain.Identity(ctx)
	if err != nil {
		o.word = unreachable
		return chain.Head{}, err
	}
	// A chain that is not the one the document names is one whose blocks say
	// nothing about these payments, so nothing of it is read or written.
	if o.network.Want != "" && identity != o.network.Want {
		o.word = chainMismatch
		return chain.Head{}, fmt.Errorf("the chain calls itself %s, and the document names %s",
			identity, o.network.Want)
	}
	head, err := o.network.Chain.Head(ctx)
	if err != nil {
		o.word = unreachable
		if errors.Is(err, chain.ErrNoFinal) {
			o.word = noFinalized
		}
		return chain.Head{}, err
	}
	return head, nil
}

// settled reads the blocks from the one after the position up to the final
// block, or as many of them as one round reads, and writes what it found with
// the position it read to.
func (o *Observer) settled(ctx context.Context, network payment.Network, from Position, final chain.Block, behind map[string]string) error {
	first, last := from.Height+1, final.Height
	if reach := from.Height + o.network.blocks(); last > reach {
		last = reach
	}
	seen, err := o.seen(ctx, network, first, last, behind)
	if err != nil {
		return err
	}
	to, err := o.at(ctx, last, final)
	if err != nil {
		return err
	}
	if err := o.write(ctx, network, first, last, from, to, seen); err != nil {
		return err
	}
	o.wrote(seen, true)
	return nil
}

// stood writes down that a round happened on a network where nothing new was
// finalised. The position is written back as it was, because whoever asks
// whether this deployment is still reading reads when the position was last
// written, and a chain that made no block is not a reader that stopped.
func (o *Observer) stood(ctx context.Context, network payment.Network, from Position) (err error) {
	tx, err := o.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { err = unwind(ctx, tx, err) }()

	if err := o.cursors.Advance(ctx, tx, network, from, from, o.now()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ahead reads the blocks after the final one, which the position never
// reaches. What it finds is written without being called final.
func (o *Observer) ahead(ctx context.Context, network payment.Network, head chain.Head, behind map[string]string) error {
	if head.Latest.Height <= head.Final.Height {
		return nil
	}
	first, last := head.Final.Height+1, head.Latest.Height
	// The width is counted back from the newest block, because the newest is
	// the end a reader ahead of finality is interested in.
	if width := o.network.blocks(); last-first+1 > width {
		first = last - width + 1
	}
	seen, err := o.seen(ctx, network, first, last, behind)
	if err != nil {
		return err
	}
	if len(seen) == 0 {
		return nil
	}
	if err := o.record(ctx, network, first, last, seen); err != nil {
		return err
	}
	o.wrote(seen, false)
	return nil
}

// record writes what a round found ahead of finality, which moves no position.
func (o *Observer) record(ctx context.Context, network payment.Network, first, last uint64, seen []payment.Seen) (err error) {
	tx, err := o.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { err = unwind(ctx, tx, err) }()

	if err := o.store.Record(ctx, tx, network, first, last, false, seen, o.now()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// seen is what a range of blocks holds against the attempts of this network:
// every transfer that spent a key some attempt holds, with what the rules made
// of it.
func (o *Observer) seen(ctx context.Context, network payment.Network, first, last uint64, behind map[string]string) ([]payment.Seen, error) {
	scan, err := o.network.Chain.Keys(ctx, first, last, o.references())
	if err != nil {
		return nil, err
	}
	if len(scan.Consumed) == 0 {
		return nil, nil
	}
	hits, err := o.store.Consumed(ctx, network, keys(scan.Consumed))
	if err != nil {
		return nil, err
	}
	against := make(map[string]payment.Hit, len(hits))
	for _, hit := range hits {
		against[hit.Attempt.Key()] = hit
	}
	// A key nothing here issued is a payment somebody else is taking. Its
	// receipt is not read, so what it moved and who moved it is not looked at.
	var out []payment.Seen
	for _, tx := range transactions(scan.Consumed, against) {
		transfers, err := o.network.Chain.Receipt(ctx, tx)
		if err != nil {
			return nil, err
		}
		for _, carried := range transfers {
			hit, ok := against[carried.Key]
			if !ok {
				continue
			}
			transfer := transferred(carried)
			reason, judged := payment.Judge(transfer, hit.Attempt, hit.Payment)
			if !judged {
				continue
			}
			code, err := o.behind(ctx, carried.Asset, behind)
			if err != nil {
				return nil, err
			}
			out = append(out, payment.Seen{
				Hit: hit, Transfer: transfer, Reason: reason, Implementation: code,
			})
		}
	}
	return out, nil
}

// write puts what a round found and the position it read to into one
// transaction, so that a position that moved was written with everything found
// below it.
func (o *Observer) write(ctx context.Context, network payment.Network, first, last uint64, from, to Position, seen []payment.Seen) (err error) {
	tx, err := o.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { err = unwind(ctx, tx, err) }()

	now := o.now()
	if err := o.store.Record(ctx, tx, network, first, last, true, seen, now); err != nil {
		return err
	}
	if err := o.cursors.Advance(ctx, tx, network, from, to, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// at is the position a range read to: the final block where the round reached
// it, and the block at the height it stopped at where the width cut the range
// short.
func (o *Observer) at(ctx context.Context, last uint64, final chain.Block) (Position, error) {
	if last == final.Height {
		return at(final), nil
	}
	block, err := o.network.Chain.Block(ctx, last)
	if err != nil {
		return Position{}, err
	}
	return at(block), nil
}

// behind is the code behind an asset, read once for the round that asks and
// remembered in the map that round carries.
//
// The value written down is the one standing when the transfer was recorded,
// not the one that was behind the asset in the block the transfer is in: what
// a chain keeps of an old block is the provider's to decide, and the public
// ones keep nothing.
func (o *Observer) behind(ctx context.Context, asset string, read map[string]string) (string, error) {
	if code, ok := read[asset]; ok {
		return code, nil
	}
	code, err := o.network.Chain.Implementation(ctx, asset)
	if err != nil {
		return "", err
	}
	read[asset] = code
	return code, nil
}

// wrote says what a round put down, one line to a transfer. The key is not
// among them: a key belongs to a payer, and a log is read by whoever runs the
// deployment and by whatever collects the logs after them.
func (o *Observer) wrote(seen []payment.Seen, final bool) {
	for _, one := range seen {
		o.log.Info("transfer recorded",
			"network", o.network.Name,
			"payment", one.Payment.ID().String(),
			"attempt", one.Attempt.ID().String(),
			"tx", one.Transfer.Tx,
			"height", one.Transfer.BlockHeight,
			"reason", one.Reason.String(),
			"final", final)
	}
}

// references are the assets to watch, as the chain writes them.
func (o *Observer) references() []string {
	out := make([]string, 0, len(o.network.Assets))
	for _, asset := range o.network.Assets {
		out = append(out, asset.Reference())
	}
	return out
}

// at is a block as a position holds one.
func at(b chain.Block) Position { return Position{Height: b.Height, Hash: b.Hash} }

// keys are the keys a scan found, each once.
func keys(consumed []chain.Consumed) []string {
	seen := make(map[string]bool, len(consumed))
	out := make([]string, 0, len(consumed))
	for _, one := range consumed {
		if !seen[one.Key] {
			seen[one.Key] = true
			out = append(out, one.Key)
		}
	}
	return out
}

// transactions are the transactions a scan found that spent a key some attempt
// holds: one to a key, each once, and in the order the scan found them.
//
// A key is spent once, so a scan naming one in two transactions is naming a
// transaction that is not on the chain, and reading it costs a receipt and a
// row: the rows are kept by key and transaction together, so a scan of ten
// thousand of them would leave ten thousand rows against one key.
func transactions(consumed []chain.Consumed, against map[string]payment.Hit) []string {
	spent := make(map[string]bool, len(consumed))
	read := make(map[string]bool, len(consumed))
	var out []string
	for _, one := range consumed {
		if _, ok := against[one.Key]; !ok || spent[one.Key] {
			continue
		}
		spent[one.Key] = true
		if read[one.Tx] {
			continue
		}
		read[one.Tx] = true
		out = append(out, one.Tx)
	}
	return out
}

// transferred is a transfer as the rules read one. The adapter checked the
// shapes of its own chain; what the rules compare is these values as strings.
func transferred(t chain.Transfer) payment.Transfer {
	return payment.Transfer{
		Scheme:      payment.Scheme(t.Scheme),
		Asset:       t.Asset,
		Key:         t.Key,
		Authorizer:  t.Authorizer,
		From:        t.From,
		To:          t.To,
		Value:       t.Value,
		Tx:          t.Tx,
		Position:    t.Position,
		BlockHeight: t.Block.Height,
		BlockHash:   t.Block.Hash,
		BlockTime:   t.Block.Time,
	}
}

// unwind rolls a transaction back, and reports the rollback only where nothing
// else went wrong. After a commit it reports the transaction already closed,
// which is not something to report.
func unwind(ctx context.Context, tx pgx.Tx, err error) error {
	back, stop := context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
	defer stop()
	rollback := tx.Rollback(back)
	if rollback != nil && !errors.Is(rollback, pgx.ErrTxClosed) && err == nil {
		return rollback
	}
	return err
}
