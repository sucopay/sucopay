package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/sucopay/sucopay/internal/accepted"
	"github.com/sucopay/sucopay/internal/adapter/chain"
	"github.com/sucopay/sucopay/internal/config"
	"github.com/sucopay/sucopay/internal/credential"
	"github.com/sucopay/sucopay/internal/finality"
	"github.com/sucopay/sucopay/internal/invisible"
	"github.com/sucopay/sucopay/internal/observe"
	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/webhook"
)

// maxDescription bounds what a database can put in front of an operator.
// Nothing limits the length of what a server sends.
const maxDescription = 64

// maxReason bounds what somebody else says went wrong. Wider than a version,
// because a reason has to carry enough of what happened to act on; the adapter
// has already cut a provider's own words to less than this.
const maxReason = 512

func doctor(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) > 0 {
		// Counted: no error repeats a word typed after suco, for the reason
		// at errUnknown.
		return fmt.Errorf("doctor takes no arguments, got %d", len(args))
	}

	resolved, document, err := load()
	if err != nil {
		return err
	}

	// What this command produces is the bytes, and half of them is worse than
	// none: a full disk would otherwise leave a truncated report behind a
	// status saying it went well.
	var report bytes.Buffer
	fmt.Fprintf(&report, "%s\n\n", document)
	tw := tabwriter.NewWriter(&report, 0, 0, 2, ' ', 0)
	for _, line := range resolved.Report() {
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", line.Path, line.Value, line.Source)
	}
	tw.Flush()

	// A server that answers slowly must not take away the part of the report
	// that needed nothing from it.
	if _, err := stdout.Write(report.Bytes()); err != nil {
		return fmt.Errorf("writing the report: %w", err)
	}

	// Opened once. What the server says about itself, what credentials are in
	// force, and how far each network has been read all come off it.
	var (
		db       *postgres.Pool
		schema   string
		cursors  *observe.Cursors
		payments *payment.Postgres
		takes    *accepted.Postgres
		accounts []credential.AccountID
	)
	database, reach := "none configured", error(nil)
	var credentials credential.InForce
	if cfg := resolved.Config; cfg.Database.URL != "" {
		db, reach = postgres.Open(ctx, cfg.Database.URL.Expose())
		if reach != nil {
			database, reach = "unreachable", errors.New(invisible.Quote(reach.Error()))
		} else {
			defer db.Close()
			database, credentials, schema, accounts, reach = describeDatabase(ctx, db, resolved.Config)
		}
	}
	// A database with no schema has no table a cursor could be in. The
	// chains are still read: an operator who has not run serve yet wants to
	// know whether their endpoint answers, and a line saying the table is
	// missing would say that twice and the endpoint not at all.
	if db != nil && schema != "" {
		cursors = observe.NewCursors(db.Conns())
		payments = payment.NewPostgres(db.Conns())
		takes = accepted.NewPostgres(db.Conns())
	}

	var tail bytes.Buffer
	fmt.Fprintf(&tail, "\ndatabase: %s\n", database)
	if credentials != "" {
		fmt.Fprintf(&tail, "credentials: %s\n", credentials)
	}
	// The deliveries, by account: what waits for its next attempt and what
	// was given up on. Counted from the database rather than asked of the
	// worker, which is in the process that serves, as what waits to settle
	// is. Only where there is a schema to count in.
	if db != nil && schema != "" {
		describeWebhooks(ctx, &tail, db, resolved.Config.Credentials.KeyID)
	}
	// The networks after the database, because how far each has been read is
	// written there. A network that could not be read is said and not failed
	// on: the instance still serves, and its probe is what says it is unfit.
	describeNetworks(ctx, &tail, resolved.Config, cursors, payments, paidTo(ctx, takes, accounts))
	tail.WriteString("\n")
	if _, err := stdout.Write(tail.Bytes()); err != nil {
		return fmt.Errorf("writing the report: %w", err)
	}

	// A report that showed a settled configuration and left these out would be
	// read as saying the instance will start.
	if err := unimplementedError(document, resolved); err != nil {
		return err
	}
	return reach
}

// describeWebhooks writes what the deployment's deliveries have come to: for
// each account with any, how many wait and how many were given up on, with
// the endpoints the given-up ones were to; and how many endpoints hold a
// secret sealed under a key other than the one in force, which is a key
// swapped under them. A deployment with nothing waiting and nothing given
// up on says so in one line.
func describeWebhooks(ctx context.Context, w io.Writer, db *postgres.Pool, keyID string) {
	standing, err := webhook.Stand(ctx, db.Conns(), keyID)
	if err != nil {
		fmt.Fprintf(w, "webhooks: %s\n", invisible.Quote(err.Error()))
		return
	}
	if len(standing.Accounts) == 0 && standing.SealedElsewhere == 0 {
		fmt.Fprintf(w, "webhooks: nothing pending, nothing failed\n")
		return
	}
	fmt.Fprintf(w, "webhooks:\n")
	for _, a := range standing.Accounts {
		line := fmt.Sprintf("  %s  %d pending, %d failed", a.Account, a.Pending, a.Failed)
		if len(a.FailedTo) > 0 {
			to := make([]string, 0, len(a.FailedTo))
			for _, id := range a.FailedTo {
				to = append(to, id.String())
			}
			line += " to " + strings.Join(to, ", ")
		}
		fmt.Fprintf(w, "%s\n", line)
	}
	if standing.SealedElsewhere > 0 {
		fmt.Fprintf(w, "  %d endpoints hold a secret sealed under another key; their merchants rotate it\n",
			standing.SealedElsewhere)
	}
}

// describeDatabase reports what an instance would reach, what credentials
// it would find in force there, and separately why it would not. The
// description is written into the report either way, so that a failure is
// shown beside the setting that caused it rather than on its own.
//
// The credentials are described as /readyz describes them, in the same word,
// and left out where there is no table to ask or the table did not answer: a
// database without the schema, one that was not reached, or one whose table
// could not be read.
//
// Everything here was chosen by a server at the other end of a network, so it
// is bounded and quoted before it reaches a terminal.
func describeDatabase(ctx context.Context, db *postgres.Pool, cfg config.Config) (database string,
	credentials credential.InForce, schema string, accounts []credential.AccountID, err error) {
	version, err := db.ServerVersion(ctx)
	if err != nil {
		return "unreachable", "", "", nil, errors.New(invisible.Quote(err.Error()))
	}
	// What an instance would find there, which is not the same question as
	// whether it answered.
	schema, err = db.SchemaVersion(ctx)
	if err != nil {
		return "unreachable", "", "", nil, errors.New(invisible.Quote(err.Error()))
	}
	database = describeVersion(version) + ", schema " + describeSchema(schema)
	if schema == "" {
		return database, "", schema, nil, nil
	}
	// The store hashes under the key, and this asks it nothing that hashes.
	// The key is parsed all the same, so that a store is only ever built as
	// serve builds one. What the document holds passed the same check when
	// it was read, so this cannot fail past that.
	key, err := credential.ParseKey(cfg.Credentials.Key.Expose())
	if err != nil {
		return database, "", schema, nil, err
	}
	store := credential.NewPostgres(db.Conns(), key, cfg.Credentials.KeyID)
	credentials, err = store.InForce(ctx)
	if err != nil {
		return database, "", schema, nil, errors.New(invisible.Quote(err.Error()))
	}
	// Whose assets the report can say where they are paid to: one account's,
	// and only where there is exactly one, since naming one is not
	// implemented here or anywhere.
	accounts, err = store.Accounts(ctx)
	if err != nil {
		return database, credentials, schema, nil, errors.New(invisible.Quote(err.Error()))
	}
	return database, credentials, schema, accounts, nil
}

// paidTo is where the deployment's one account is paid for each asset, keyed
// by the asset's network and reference, and nothing where there is no store
// to ask or not exactly one account. A report is written to its end whatever
// the database holds, so two accounts are not something it fails on.
func paidTo(ctx context.Context, takes *accepted.Postgres, accounts []credential.AccountID) map[string]payment.Address {
	if takes == nil || len(accounts) != 1 {
		return nil
	}
	taken, err := takes.List(ctx, payment.AccountID(accounts[0]))
	if err != nil {
		return nil
	}
	paid := make(map[string]payment.Address, len(taken))
	for _, a := range taken {
		paid[a.Network.String()+" "+a.Reference] = a.Destination
	}
	return paid
}

// describeSchema names the migration a database has been brought up to.
func describeSchema(version string) string {
	if version == "" {
		return "not applied"
	}
	return invisible.Shown(version, maxDescription)
}

// describeVersion renders what a server said about itself for a report a
// person reads.
func describeVersion(version string) string {
	return "PostgreSQL " + invisible.Shown(version, maxDescription)
}

// describeNetworks writes what each network the assets settle on says about
// itself: what it is, what chain it calls itself, where it stands, and how far
// this deployment has read it.
//
// It reads the chains the way a start does, through the same adapters, so that
// what an operator is told here is what an instance would meet. It writes
// nothing: whoever is asking whether a deployment could observe a network is
// not the one who should be moving its cursor.
//
// cursors is nil where nothing could have written a cursor: no database, or
// one nothing has applied the schema to. The chain is read either way, and
// where the reading stands is left out.
func describeNetworks(ctx context.Context, w io.Writer, cfg config.Config,
	cursors *observe.Cursors, payments *payment.Postgres, paid map[string]payment.Address) {
	networks, err := openChains(cfg)
	if err != nil {
		// A kind nothing can open, or an endpoint the adapter refuses. The
		// configuration refuses both before this, so meeting one here is the
		// two having come apart.
		fmt.Fprintf(w, "\nnetworks: %s\n", invisible.Quote(err.Error()))
		return
	}
	if len(networks) == 0 {
		return
	}
	// The endpoints a network settles through, which are the others where
	// there is no own node. The same document opened them a line above, so
	// this cannot fail past that.
	settling := map[string]finality.Network{}
	if opened, err := openSettling(cfg); err == nil {
		for _, s := range opened {
			settling[s.Name] = s
		}
	}
	fmt.Fprintln(w, "\nnetworks:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	defer tw.Flush()
	for _, n := range networks {
		// Once for the network and everything on it. What is behind each asset
		// is a call to the provider, and asking again for the lines below
		// would double what a report costs whoever is paying for the endpoint.
		report, err := observe.Probe(ctx, n, nil)
		fmt.Fprintf(tw, "  %s\t%s\t%s%s%s\n", invisible.Quote(n.Name), cfg.Networks[n.Name].Kind,
			standing(ctx, n, report, err, cursors), others(ctx, settling[n.Name]),
			undecided(ctx, payments, n.Name))
		for _, name := range slices.Sorted(maps.Keys(n.Assets)) {
			fmt.Fprintf(tw, "    %s\t\t%s%s%s\n", invisible.Quote(name), behind(report, err, name),
				stopped(report, err, name), refusing(ctx, n.Chain, err, n.Assets[name], paid))
		}
	}
}

// standing is one network as a report says it: where the chain is, how far it
// has been read, and how far behind that leaves the reading.
//
// The chain and the cursor are read apart, so that a failure at either says
// which it was. Asked together they come back as one refusal, and an operator
// whose database is short of the table reads it as an endpoint that will not
// answer, and goes and changes their provider.
func standing(ctx context.Context, n observe.Network, report observe.Report, err error, cursors *observe.Cursors) string {
	if err != nil {
		// The provider's own code and words, which the adapter has already cut
		// short and taken its endpoint out of.
		return "could not be read: " + invisible.Shown(err.Error(), maxReason)
	}
	if n.Want != "" && report.Identity != n.Want {
		return fmt.Sprintf("chain-mismatch: it calls itself %s and the document names %s",
			invisible.Shown(report.Identity, maxDescription), n.Want)
	}
	where := fmt.Sprintf("chain %s%s, latest %d, final %d",
		invisible.Shown(report.Identity, maxDescription), testnet(report.Identity),
		report.Head.Latest.Height, report.Head.Final.Height)
	if cursors == nil {
		return where + ", and nowhere a cursor could have been written"
	}
	at, read, err := cursors.Get(ctx, payment.Network(n.Name))
	switch {
	case err != nil:
		return where + ", and the cursor could not be read: " + invisible.Shown(err.Error(), maxReason)
	case !read:
		return where + ", no cursor"
	}
	// Behind the final block and not the latest: the finalised range is what a
	// round reads, and the blocks above it are read again when they settle.
	var lag uint64
	if report.Head.Final.Height > at.Height {
		lag = report.Head.Final.Height - at.Height
	}
	return fmt.Sprintf("%s, position %d, behind %d", where, at.Height, lag)
}

// others is how many of the third parties a network settles through
// answer, for a network that settles through them: what a deployment has to
// fall back on is what an operator cannot tell from a network that is
// settling. Nothing for a network with a node of its own, which the line has
// already read.
//
// No spare is exactly as many answering as agreement takes: the next one to
// go down stops the settling. Too few is below that, which is a network that
// has stopped settling already.
func others(ctx context.Context, n finality.Network) string {
	if n.Agreements < finality.Agreements {
		return ""
	}
	answered := 0
	for _, endpoint := range n.Endpoints {
		if _, err := endpoint.Head(ctx); err == nil {
			answered++
		}
	}
	said := fmt.Sprintf(", %d of %d others answer", answered, len(n.Endpoints))
	switch {
	case answered < n.Agreements:
		return said + ", too few to settle"
	case answered == n.Agreements:
		return said + ", no spare"
	}
	return said
}

// testnets are the chains JPYC's faucet hands out test money on, by the chain
// id each answers with, as the faucet's own page lists them.
// A testnet is named as one so that a document which was copied from the
// step before production, with the name changed and nothing else, is caught
// by a report and not by a payment that never arrives.
var testnets = map[string]string{
	"80002":    "Polygon Amoy",
	"43113":    "Avalanche Fuji",
	"11155111": "Ethereum Sepolia",
	"5042002":  "Arc Testnet",
	"1001":     "Kaia Kairos",
}

// testnet is what follows a chain id that is a testnet's, and nothing after
// any other.
func testnet(identity string) string {
	name, known := testnets[identity]
	if !known {
		return ""
	}
	return fmt.Sprintf(" (%s, a testnet)", name)
}

// undecided is how many of a network's recorded transfers are waiting for the
// endpoints to be asked about them, and how many of those the endpoints
// disagree about, which is what a report can say about the settling without a
// worker to ask. Nothing where there is no database to count in, and nothing
// where the count fails: the network's own line has already said whatever is
// wrong with reading it.
//
// The disagreement is said only when there is some. Nothing settles from one
// and it does not resolve itself, so the number is one the operator acts on,
// and a zero every report would be read past.
func undecided(ctx context.Context, payments *payment.Postgres, network string) string {
	if payments == nil {
		return ""
	}
	waiting, err := payments.Undecided(ctx, payment.Network(network))
	if err != nil {
		return ", and what is waiting to settle could not be counted"
	}
	said := fmt.Sprintf(", %d waiting to settle", waiting)
	if waiting == 0 {
		// What the endpoints disagree about is among what is waiting, so
		// there is nothing to count.
		return said
	}
	disagreed, err := payments.Disagreeing(ctx, payment.Network(network))
	switch {
	case err != nil:
		return said + ", and what the endpoints disagree about could not be counted"
	case disagreed > 0:
		return fmt.Sprintf("%s, %d disagreed about", said, disagreed)
	}
	return said
}

// stopped is what a report says of whether an asset's issuer has stopped it:
// paused when it has, nothing when it has not, and that the contract would not
// say when it would not. A network that could not be read at all says so on
// its own line, and nothing here.
func stopped(report observe.Report, err error, name string) string {
	if err != nil {
		return ""
	}
	paused, read := report.Paused[name]
	switch {
	case !read:
		return ", paused not read"
	case paused:
		return ", paused"
	}
	return ""
}

// refusing is what a report says of where an asset is paid to, when the
// contract refuses transfers to that address: nothing arrives there, and the
// operator hears it from the provider's answer rather than from a merchant.
// Nothing for an asset the account has not accepted, nothing where the
// address is not refused, and nothing on a network that could not be read:
// its own line says so, and asking the same connection once more per asset
// would say it again, slowly, for a provider that times out.
func refusing(ctx context.Context, read chain.Chain, unread error, asset payment.Asset, paid map[string]payment.Address) string {
	destination, ok := paid[asset.Network().String()+" "+asset.Reference()]
	if !ok || unread != nil {
		return ""
	}
	listed, err := read.Blocklisted(ctx, asset.Reference(), string(destination))
	switch {
	case err != nil:
		return fmt.Sprintf(", and whether it refuses transfers to %s could not be read", destination)
	case listed:
		return fmt.Sprintf(", paid to %s, which is blocklisted, the provider says", destination)
	}
	return ""
}

// behind is what a report says of one asset: the code the chain runs for it,
// which is what an upgrade of a proxy changes. The probe read it along with
// the rest, so an asset costs no call of its own.
func behind(report observe.Report, err error, name string) string {
	code := report.Behind[name]
	switch {
	case err != nil:
		// The network's own line says what went wrong, and a reason under
		// every asset would say it again once per asset.
		return "not read"
	case code == "":
		return "nothing stands in front of it"
	}
	return "implementation " + invisible.Shown(code, maxDescription)
}
