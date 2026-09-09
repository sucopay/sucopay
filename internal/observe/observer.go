package observe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sucopay/sucopay/internal/adapter/chain"
	"github.com/sucopay/sucopay/internal/invisible"
	"github.com/sucopay/sucopay/internal/payment"
)

// The words one round leaves behind for whoever asks whether this deployment
// is reading a chain. One network has one of them at a time.
const (
	// observing is a round that read the finalised range and wrote what it
	// found. It stands only while rounds keep finishing: one that has not for
	// [Stale] is stalled.
	observing = "observing"
	// noPosition is a network no round has finished on, whether or not a
	// position has been written down for it.
	noPosition = "no-position"
	// unreachable is a provider that did not answer.
	unreachable = "unreachable"
	// noFinalized is a provider that will not answer for the finalized block.
	// The word is spelt the way the tag it comes from is spelt, because it is
	// that tag going unanswered that this says.
	noFinalized = "no-finalized"
	// stalled is a network no round has finished on for [Stale], which is a
	// reader that stopped rather than a chain that is quiet. An instance says
	// it of its own rounds and of the rounds of whoever holds the lease, and
	// reads a different thing to know: its own last round, or when the
	// position was last written.
	stalled = "stalled"
	// chainMismatch is a chain that is not the one the document names.
	chainMismatch = "chain-mismatch"
	// finalizedBehind is a provider whose final block is below the position,
	// which is a block another provider had already called final.
	finalizedBehind = "finalized-behind"
	// finalizedChanged is a chain that no longer holds the block the position
	// names. Nothing moves until somebody puts the position somewhere the
	// chain does hold.
	finalizedChanged = "finalized-changed"
)

// RenewBelow is how little of a lease may be left when a round is about to
// call a chain. Less than this and the round puts the lease out again first,
// so that a lease cannot lapse between two calls of one round: a call is given
// half of what a lease runs for.
const RenewBelow = 15 * time.Second

// MaxInterval is the longest a round waits for the one after it. A provider
// refusing everything is asked 288 times a day at this, which is a rate
// nothing has to be spared from.
const MaxInterval = 300 * time.Second

// RecoverAfter is how many rounds have to read their span before the span goes
// back up. A provider that refused a span once tends to refuse it again, so
// the way back is slow.
const RecoverAfter = 100

// Stale is how long a network may go without a round before an instance that
// cannot take the lease says nobody is reading it. It is twice the term of a
// lease, which leaves whoever takes over time to finish a round of their own.
const Stale = 2 * LeaseTTL

// ReadImplementationEvery is how often the code behind an asset is read where
// no block said it changed, and how often the chain is asked what it is. Both
// are insurance: an upgrade that emits nothing, and an endpoint pointed at
// another chain. A minute comes to 1440 calls a day, each.
const ReadImplementationEvery = time.Minute

// minWidth is the narrowest span a round asks for. Narrower than this and a
// round stops keeping up with a chain that makes a block every two seconds,
// whatever the provider will answer. It is the narrowest a document may set as
// well, for a reason of its own: this package reads no configuration, so the
// two are the same number by agreement rather than by construction.
const minWidth = 10

// maxErrorBytes bounds what somebody else's failure can put in a line.
const maxErrorBytes = 512

// shown is an error as a line carries one: cut short and quoted. What a round
// has to report comes from a provider or from the database, and nothing here
// vouches for the characters either of them wrote. slog's JSON handler is the
// one a deployment writes with, and it passes a zero-width space or an
// override of the reading order through as the bytes it was given.
//
// err is one there is: this is called where a round has something to report,
// and a line saying nothing went wrong is not one of those places.
func shown(err error) string { return invisible.Shown(err.Error(), maxErrorBytes) }

// errLost says the lease is somebody else's now. The round stops where it is,
// and whoever is running it goes back to asking for the lease.
var errLost = errors.New("observe: the lease is held by another instance")

// The words an asset carries: whether the code behind it is still the code
// that was behind it when this instance started reading.
const (
	unchanged = "unchanged"
	changed   = "changed"
)

// NetworkWords are every word a network can be in, in name order.
//
// Whoever reads [Observer.Words] decides what to do about each of them, which
// is a list of its own somewhere else. This is what a test beside that list
// holds it against, so that a word renamed or added here fails there rather
// than falling to whatever a word it does not know happens to get.
func NetworkWords() []string {
	words := []string{observing, noPosition, unreachable, noFinalized,
		stalled, chainMismatch, finalizedBehind, finalizedChanged}
	slices.Sort(words)
	return words
}

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
	// Assets are the tokens payments on this network arrive in, by the name
	// the document gives each. A transfer of anything else is not read.
	//
	// By name, because a name is what anybody is told about an asset: one
	// token has one reference on four chains, and a reference would say which
	// of them nothing.
	Assets map[string]payment.Asset
	// Poll is how long to wait between rounds.
	Poll time.Duration
	// Width is the most blocks one round reads.
	Width int
}

// blocks is what one round asks for now, as a count of blocks. A width that is
// not one reads as none, and a round that would read no blocks stops rather
// than asking for a span nobody named.
func (o *Observer) blocks() uint64 {
	if o.width <= 0 {
		return 0
	}
	return uint64(o.width)
}

// after moves the span and the wait on what the round came to.
//
// A refusal of the span narrows it, a refusal for the rate of calls lengthens
// the wait, and rounds that go through undo both. A provider that has not
// caught up says nothing about either: that round read nothing.
func (o *Observer) after(err error) {
	if o.said() == finalizedBehind {
		return
	}
	if err == nil {
		o.interval = max(o.interval/2, o.network.Poll)
		o.passed++
		if o.passed >= RecoverAfter {
			o.width, o.passed = min(2*o.width, o.network.Width), 0
		}
		return
	}
	o.passed = 0
	var limited chain.RateLimited
	switch {
	case errors.As(err, &limited):
		// What the provider asked to be waited for, where that is a wait this
		// would take anyway. Longer than the longest is the provider's idea of
		// a day off, and how long to stay away is this deployment's to decide;
		// shorter than the document's own interval is not an invitation to
		// read a chain faster than it was set to.
		if after := limited.RetryAfter; after > 0 && after <= MaxInterval {
			o.interval = max(after, o.network.Poll)
			return
		}
		o.interval = min(2*o.interval, MaxInterval)
	case errors.Is(err, chain.ErrTooWide):
		// Never wider than it was: a refusal is not a reason to ask for more,
		// whatever the narrowest a round reads happens to be.
		if o.width > minWidth {
			o.width = max(o.width/2, minWidth)
		}
	}
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
	leases  *Leases
	store   *payment.Postgres
	log     *slog.Logger
	now     func() time.Time
	// What one round asks for and how long the one after it waits, as the
	// provider's answers have moved them. Only the round reads or writes them.
	width    int
	interval time.Duration
	passed   int
	// identified and looked are when the chain was last asked what it is and
	// what is behind its assets. Neither changes without somebody doing
	// something, so neither is asked every round.
	identified time.Time
	looked     time.Time
	// started is what was behind each asset when this instance began reading,
	// by the name the document gives it. What is behind one now is compared
	// with this.
	started map[string]string
	// succeeded is when a round last read the finalised range and wrote what
	// it found. It is what tells a deployment still getting somewhere from one
	// whose rounds have stopped finishing. Only the round reads or writes it.
	succeeded time.Time
	// held is when the lease was last taken or put out again, by this
	// process's clock. What decides that a lease has lapsed is the database's
	// clock, so this is read to renew early and to decide nothing. Only the
	// round reads or writes it.
	held time.Time
	// named is the document's name for each asset, by the reference the chain
	// writes it as. A chain says things about references, and whoever asks
	// after this deployment is answered in names.
	named map[string]string

	// What a round leaves behind is read by whoever is answering somebody
	// else's question about this deployment, which is another goroutine.
	mu     sync.Mutex
	word   string
	assets map[string]string
}

// New returns an observer of one network, reading and writing through pool.
func New(n Network, pool *pgxpool.Pool, store *payment.Postgres, log *slog.Logger, now func() time.Time) *Observer {
	named := make(map[string]string, len(n.Assets))
	assets := make(map[string]string, len(n.Assets))
	for name, asset := range n.Assets {
		named[asset.Reference()] = name
		assets[name] = unchanged
	}
	return &Observer{
		width:    n.Width,
		interval: n.Poll,
		// Nothing has been read from this network yet, and a network nothing
		// has been read from is not one to say anything better about.
		word:    unreachable,
		network: n,
		pool:    pool,
		cursors: NewCursors(pool),
		leases:  NewLeases(pool),
		store:   store,
		log:     log,
		now:     now,
		named:   named,
		assets:  assets,
	}
}

// Words are what this network and its assets have come to, in the one word
// each that whoever asks after the deployment is given.
func (o *Observer) Words() (network string, assets map[string]string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	assets = make(map[string]string, len(o.assets))
	maps.Copy(assets, o.assets)
	return o.word, assets
}

// Observers are the observers of one deployment, one to a network.
type Observers []*Observer

// Words are what every network of the deployment and every asset on them have
// come to. Asset names are the document's and belong to one network each, so
// nothing here can be two things at once.
func (s Observers) Words() (networks, assets map[string]string) {
	networks, assets = make(map[string]string, len(s)), map[string]string{}
	for _, o := range s {
		network, own := o.Words()
		networks[o.network.Name] = network
		maps.Copy(assets, own)
	}
	return networks, assets
}

// says puts the word a round came to where it can be read.
func (o *Observer) says(word string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.word = word
}

// said is the word the last round came to.
func (o *Observer) said() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.word
}

// replaced says the code behind an asset is no longer the code that was behind
// it. What a scan reports is a reference, and the word is kept under the name
// the document gave it.
func (o *Observer) replaced(references []string) {
	if len(references) == 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, reference := range references {
		if name, ok := o.named[reference]; ok {
			o.assets[name] = changed
		}
	}
}

// keep puts the lease out again where little of it is left, and says the round
// is over where somebody else holds it now.
//
// Renewed here rather than by a timer of its own: a timer would hold the
// network for an observer that had stopped getting anywhere, and nothing else
// could take it over.
func (o *Observer) keep(ctx context.Context) error {
	if o.now().Sub(o.held) < LeaseTTL-RenewBelow {
		return nil
	}
	held, err := o.leases.Renew(ctx, o.network.Name)
	if err != nil {
		return err
	}
	if !held {
		return errLost
	}
	o.held = o.now()
	return nil
}

// Report is what a network says about itself before anybody starts reading it
// round by round.
type Report struct {
	// Identity is what the chain calls itself.
	Identity string
	// Head is where the chain stands.
	Head chain.Head
	// Position is how far the network has been read.
	Position Position
	// Read says whether the network has been read at all. The position means
	// nothing without it.
	Read bool
	// Behind is the code behind each asset, by the name the document gives
	// it. It is empty for a chain whose assets cannot be replaced.
	Behind map[string]string
}

// Probe reads what a network says about itself. It writes nothing, so whoever
// is only asking whether a deployment could observe this network can ask.
func Probe(ctx context.Context, n Network, cursors *Cursors) (_ Report, err error) {
	// One probe is about one network, and the name goes on whatever it
	// reports rather than on each of the places that could report something.
	defer func() {
		if err != nil {
			err = fmt.Errorf("%s: %w", n.Name, err)
		}
	}()

	report := Report{Behind: make(map[string]string, len(n.Assets))}
	if report.Identity, err = n.Chain.Identity(ctx); err != nil {
		return Report{}, err
	}
	if report.Head, err = n.Chain.Head(ctx); err != nil {
		return Report{}, err
	}
	if report.Position, report.Read, err = cursors.Get(ctx, payment.Network(n.Name)); err != nil {
		return Report{}, err
	}
	for name, asset := range n.Assets {
		behind, err := n.Chain.Implementation(ctx, asset.Reference())
		if err != nil {
			return Report{}, err
		}
		report.Behind[name] = behind
	}
	return report, nil
}

// Run reads the network until ctx is done.
//
// One instance reads a network at a time, which is what the lease settles. An
// instance that cannot take it waits and asks again, and says meanwhile
// whether whoever holds it is still getting anywhere.
func (o *Observer) Run(ctx context.Context) error {
	o.start(ctx)
	defer func() {
		// The lease is put down on the way out so that another instance does
		// not wait out its term for a network nobody is reading.
		release, stop := context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
		defer stop()
		if err := o.leases.Release(release, o.network.Name); err != nil {
			o.log.Warn("the lease was not put down", "network", o.network.Name, "error", shown(err))
		}
	}()

	for {
		o.round(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(o.interval):
		}
	}
}

// start reads what the network says about itself and puts the word it comes to
// where it can be read. A network that cannot be read at all is one this waits
// on rather than one it gives up on: the rounds keep asking.
func (o *Observer) start(ctx context.Context) {
	report, err := Probe(ctx, o.network, o.cursors)
	if err != nil {
		o.says(unreachable)
		if errors.Is(err, chain.ErrNoFinal) {
			o.says(noFinalized)
		}
		o.log.Warn("the network could not be read into", "network", o.network.Name, "error", shown(err))
		return
	}
	o.identified, o.looked = o.now(), o.now()
	o.started = report.Behind
	if o.network.Want != "" && report.Identity != o.network.Want {
		o.says(chainMismatch)
		return
	}
	o.says(noPosition)
}

// round is one turn of the loop: read where the lease allows it, and say what
// somebody else is doing where it does not.
func (o *Observer) round(ctx context.Context) {
	held, err := o.leases.Acquire(ctx, o.network.Name)
	if err != nil {
		o.log.Warn("the lease could not be asked for", "network", o.network.Name, "error", shown(err))
		return
	}
	if !held {
		o.waiting(ctx)
		return
	}
	o.held = o.now()
	err = o.tick(ctx)
	if err != nil {
		o.log.Warn("the round did not finish", "network", o.network.Name, "error", shown(err))
		o.stalling()
	}
	o.after(err)
	if err == nil {
		o.looking(ctx)
	}
}

// went says a round finished the finalised range and wrote what it read. The
// word and the moment move together, because the word stands on the moment.
func (o *Observer) went() {
	o.says(observing)
	o.succeeded = o.now()
}

// stalling takes back the word that says rounds are getting through, once none
// has for [Stale].
//
// A round that stopped for a reason of its own has already said what it was,
// and those words stand. This is for the rounds that stop without one: every
// failure to write, and every call a round makes to the chain after the head.
// Without it the instance holding the lease is the one that cannot say it has
// stopped, while every instance waiting on it can.
func (o *Observer) stalling() {
	if o.said() == observing && o.now().Sub(o.succeeded) > Stale {
		o.says(stalled)
	}
}

// waiting says what an instance that is not reading this network can tell
// about it: whoever holds the lease writes the position every round, so when
// it was last written is whether they are still getting anywhere.
func (o *Observer) waiting(ctx context.Context) {
	touched, found, err := o.cursors.Touched(ctx, payment.Network(o.network.Name))
	switch {
	case err != nil:
		o.log.Warn("the position could not be read", "network", o.network.Name, "error", shown(err))
	case !found:
		o.says(noPosition)
	case o.now().Sub(touched) > Stale:
		o.says(stalled)
	default:
		o.says(observing)
	}
}

// looking reads what is behind each asset where it is time to look again. A
// proxy upgraded without saying so in a block is what this catches.
func (o *Observer) looking(ctx context.Context) {
	if o.now().Sub(o.looked) < ReadImplementationEvery {
		return
	}
	// Looked at the moment of looking, not once every asset has answered. An
	// asset whose slot cannot be read would otherwise put every asset back on
	// every round, which is the opposite of what asking once a minute is for.
	o.looked = o.now()
	for name, asset := range o.network.Assets {
		if err := o.keep(ctx); err != nil {
			return
		}
		code, err := o.network.Chain.Implementation(ctx, asset.Reference())
		if err != nil {
			o.log.Warn("what is behind an asset could not be read",
				"network", o.network.Name, "asset", name, "error", shown(err))
			return
		}
		if was, ok := o.started[name]; ok && was != code {
			o.replaced([]string{asset.Reference()})
			o.log.Warn("the code behind an asset is not the code that was behind it",
				"network", o.network.Name, "asset", name)
		}
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
	if o.blocks() == 0 {
		return fmt.Errorf("one round is set to read %d blocks", o.width)
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
		o.says(noPosition)
		return o.cursors.Init(ctx, network, at(head.Final), o.now())
	}

	// A provider that has not reached what another one already called final has
	// nothing to add this round. It is not behind for long, and it is not
	// something to act on, so the round waits without writing.
	if head.Final.Height < from.Height {
		o.says(finalizedBehind)
		return nil
	}
	// Reading on from a position the chain no longer holds would be reading a
	// chain other than the one the records came off. How far back to go is not
	// something this can work out: whoever put the position there is who puts
	// it somewhere else.
	held, err := o.holds(ctx, from, head.Final)
	if err != nil {
		return err
	}
	if !held {
		// Once, on the way into the state. The rounds after it find the same
		// thing until somebody moves the position, and saying so every time
		// would bury what else the deployment has to say.
		if o.said() != finalizedChanged {
			o.log.Warn("the chain no longer holds the block the position names",
				"network", o.network.Name, "height", from.Height)
		}
		o.says(finalizedChanged)
		return nil
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
	o.went()

	// Ahead of finality nothing moves the position. What is found there is
	// written as evidence and read again when the finalised range reaches it,
	// so a round that cannot read it is a round that found nothing yet.
	if err := o.ahead(ctx, network, head, behind); err != nil {
		o.log.Warn("reading ahead of the final block failed",
			"network", o.network.Name, "error", shown(err))
	}
	return nil
}

// standing is what the chain says it is and where it stands, or the word for
// why the round stops here.
func (o *Observer) standing(ctx context.Context) (chain.Head, error) {
	if err := o.keep(ctx); err != nil {
		return chain.Head{}, err
	}
	// What a chain calls itself changes when somebody points the endpoint at
	// another chain, and not otherwise, so it is asked on the same footing as
	// what is behind an asset. Until the next asking, a chain that was swapped
	// answers for blocks whose hashes do not match the position.
	if o.now().Sub(o.identified) >= ReadImplementationEvery {
		identity, err := o.network.Chain.Identity(ctx)
		if err != nil {
			o.says(unreachable)
			return chain.Head{}, err
		}
		o.identified = o.now()
		// A chain that is not the one the document names is one whose blocks
		// say nothing about these payments, so nothing of it is read or
		// written.
		if o.network.Want != "" && identity != o.network.Want {
			o.says(chainMismatch)
			return chain.Head{}, fmt.Errorf("the chain calls itself %s, and the document names %s",
				identity, o.network.Want)
		}
	}
	head, err := o.network.Chain.Head(ctx)
	if err != nil {
		o.says(unreachable)
		if errors.Is(err, chain.ErrNoFinal) {
			o.says(noFinalized)
		}
		return chain.Head{}, err
	}
	return head, nil
}

// holds reports whether the chain still has the block the position names at
// the height it names.
//
// The head answers it already where the final block is the position or the one
// above it, and a call is what it takes anywhere else.
func (o *Observer) holds(ctx context.Context, from Position, final chain.Block) (bool, error) {
	switch final.Height {
	case from.Height:
		return final.Hash == from.Hash, nil
	case from.Height + 1:
		return final.Parent == from.Hash, nil
	}
	if err := o.keep(ctx); err != nil {
		return false, err
	}
	block, err := o.network.Chain.Block(ctx, from.Height)
	if err != nil {
		return false, err
	}
	return block.Hash == from.Hash, nil
}

// settled reads the blocks from the one after the position up to the final
// block, or as many of them as one round reads, and writes what it found with
// the position it read to.
func (o *Observer) settled(ctx context.Context, network payment.Network, from Position, final chain.Block, behind map[string]string) error {
	first, last := from.Height+1, final.Height
	if reach := from.Height + o.blocks(); last > reach {
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
	if width := o.blocks(); last-first+1 > width {
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
	if err := o.keep(ctx); err != nil {
		return nil, err
	}
	scan, err := o.network.Chain.Keys(ctx, first, last, o.references())
	if err != nil {
		return nil, err
	}
	o.replaced(scan.Changed)
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
		if err := o.keep(ctx); err != nil {
			return nil, err
		}
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
	if err := o.keep(ctx); err != nil {
		return Position{}, err
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
	// In one order, so that what a provider is asked does not depend on where
	// a map put things.
	slices.Sort(out)
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
