package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"text/tabwriter"

	"github.com/sucopay/sucopay/internal/config"
	"github.com/sucopay/sucopay/internal/credential"
	"github.com/sucopay/sucopay/internal/invisible"
	"github.com/sucopay/sucopay/internal/observe"
	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/postgres"
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
	)
	database, reach := "none configured", error(nil)
	var credentials credential.InForce
	if cfg := resolved.Config; cfg.Database.URL != "" {
		db, reach = postgres.Open(ctx, cfg.Database.URL.Expose())
		if reach != nil {
			database, reach = "unreachable", errors.New(invisible.Quote(reach.Error()))
		} else {
			defer db.Close()
			database, credentials, schema, reach = describeDatabase(ctx, db, resolved.Config)
		}
	}
	// A database with no schema has no table a cursor could be in. The
	// chains are still read: an operator who has not run serve yet wants to
	// know whether their endpoint answers, and a line saying the table is
	// missing would say that twice and the endpoint not at all.
	if db != nil && schema != "" {
		cursors = observe.NewCursors(db.Conns())
		payments = payment.NewPostgres(db.Conns())
	}

	var tail bytes.Buffer
	fmt.Fprintf(&tail, "\ndatabase: %s\n", database)
	if credentials != "" {
		fmt.Fprintf(&tail, "credentials: %s\n", credentials)
	}
	// The networks after the database, because how far each has been read is
	// written there. A network that could not be read is said and not failed
	// on: the instance still serves, and its probe is what says it is unfit.
	describeNetworks(ctx, &tail, resolved.Config, cursors, payments)
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
func describeDatabase(ctx context.Context, db *postgres.Pool, cfg config.Config) (database string, credentials credential.InForce, schema string, err error) {
	version, err := db.ServerVersion(ctx)
	if err != nil {
		return "unreachable", "", "", errors.New(invisible.Quote(err.Error()))
	}
	// What an instance would find there, which is not the same question as
	// whether it answered.
	schema, err = db.SchemaVersion(ctx)
	if err != nil {
		return "unreachable", "", "", errors.New(invisible.Quote(err.Error()))
	}
	database = describeVersion(version) + ", schema " + describeSchema(schema)
	if schema == "" {
		return database, "", schema, nil
	}
	// The store hashes under the key, and this asks it nothing that hashes.
	// The key is parsed all the same, so that a store is only ever built as
	// serve builds one. What the document holds passed the same check when
	// it was read, so this cannot fail past that.
	key, err := credential.ParseKey(cfg.Credentials.Key.Expose())
	if err != nil {
		return database, "", schema, err
	}
	credentials, err = credential.NewPostgres(db.Conns(), key, cfg.Credentials.KeyID).InForce(ctx)
	if err != nil {
		return database, "", schema, errors.New(invisible.Quote(err.Error()))
	}
	return database, credentials, schema, nil
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
	cursors *observe.Cursors, payments *payment.Postgres) {
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
	fmt.Fprintln(w, "\nnetworks:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	defer tw.Flush()
	for _, n := range networks {
		// Once for the network and everything on it. What is behind each asset
		// is a call to the provider, and asking again for the lines below
		// would double what a report costs whoever is paying for the endpoint.
		report, err := observe.Probe(ctx, n, nil)
		fmt.Fprintf(tw, "  %s\t%s\t%s%s\n", invisible.Quote(n.Name), cfg.Networks[n.Name].Kind,
			standing(ctx, n, report, err, cursors), undecided(ctx, payments, n.Name))
		for _, name := range slices.Sorted(maps.Keys(n.Assets)) {
			fmt.Fprintf(tw, "    %s\t\t%s\n", invisible.Quote(name), behind(report, err, name))
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
	where := fmt.Sprintf("chain %s, latest %d, final %d",
		invisible.Shown(report.Identity, maxDescription), report.Head.Latest.Height, report.Head.Final.Height)
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

// undecided is how many of a network's recorded transfers are waiting for the
// endpoints to be asked about them, which is what a report can say about the
// settling without a worker to ask. Nothing where there is no database to
// count in, and nothing where the count fails: the network's own line has
// already said whatever is wrong with reading it.
func undecided(ctx context.Context, payments *payment.Postgres, network string) string {
	if payments == nil {
		return ""
	}
	waiting, err := payments.Undecided(ctx, payment.Network(network))
	if err != nil {
		return ", and what is waiting to settle could not be counted"
	}
	return fmt.Sprintf(", %d waiting to settle", waiting)
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
