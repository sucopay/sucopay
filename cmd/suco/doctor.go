package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/sucopay/sucopay/internal/config"
	"github.com/sucopay/sucopay/internal/credential"
	"github.com/sucopay/sucopay/internal/invisible"
	"github.com/sucopay/sucopay/internal/postgres"
)

// maxDescription bounds what a database can put in front of an operator.
// Nothing limits the length of what a server sends.
const maxDescription = 64

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

	database, credentials, reach := describeDatabase(ctx, resolved.Config)
	var tail bytes.Buffer
	fmt.Fprintf(&tail, "\ndatabase: %s\n", database)
	if credentials != "" {
		fmt.Fprintf(&tail, "credentials: %s\n", credentials)
	}
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
func describeDatabase(ctx context.Context, cfg config.Config) (database string, credentials credential.InForce, err error) {
	if cfg.Database.URL == "" {
		return "none configured", "", nil
	}
	db, err := postgres.Open(ctx, cfg.Database.URL)
	if err != nil {
		return "unreachable", "", errors.New(invisible.Quote(err.Error()))
	}
	defer db.Close()

	version, err := db.ServerVersion(ctx)
	if err != nil {
		return "unreachable", "", errors.New(invisible.Quote(err.Error()))
	}
	// What an instance would find there, which is not the same question as
	// whether it answered.
	schema, err := db.SchemaVersion(ctx)
	if err != nil {
		return "unreachable", "", errors.New(invisible.Quote(err.Error()))
	}
	database = describeVersion(version) + ", schema " + describeSchema(schema)
	if schema == "" {
		return database, "", nil
	}
	// The store hashes under the key, and this asks it nothing that hashes.
	// The key is parsed all the same, so that a store is only ever built as
	// serve builds one. What the document holds passed the same check when
	// it was read, so this cannot fail past that.
	key, err := credential.ParseKey(cfg.Credentials.Key)
	if err != nil {
		return database, "", err
	}
	credentials, err = credential.NewPostgres(db.Conns(), key, cfg.Credentials.KeyID).InForce(ctx)
	if err != nil {
		return database, "", errors.New(invisible.Quote(err.Error()))
	}
	return database, credentials, nil
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
