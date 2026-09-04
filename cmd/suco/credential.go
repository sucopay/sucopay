package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/sucopay/sucopay/internal/credential"
	"github.com/sucopay/sucopay/internal/invisible"
	"github.com/sucopay/sucopay/internal/postgres"
)

// openStore reads the document and opens the store of credentials it names,
// for the commands that read or write one. A document serve refuses is
// refused here in serve's words, and so is one naming no database: a
// deployment with either meets the refusal at whichever command runs first,
// and two sentences for one problem read as two problems.
func openStore(ctx context.Context) (*postgres.Pool, *credential.Postgres, error) {
	resolved, document, err := load()
	if err != nil {
		return nil, nil, err
	}
	if err := unimplementedError(document, resolved); err != nil {
		return nil, nil, err
	}
	cfg := resolved.Config
	// The default database.managed is a document that names no database.
	if cfg.Database.URL == "" {
		return nil, nil, fmt.Errorf("%s: %s", document, ownDatabase)
	}
	// Read before the database is opened: a malformed key needs no
	// connection to be refused.
	key, err := credential.ParseKey(cfg.Credentials.Key)
	if err != nil {
		return nil, nil, err
	}

	db, err := postgres.Open(ctx, cfg.Database.URL)
	if err != nil {
		return nil, nil, err
	}
	// serve applies the schema and logs what it applied. A command that
	// answers a person with a line or two would apply it in silence, so
	// these ask for it to have been applied.
	version, err := db.SchemaVersion(ctx)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	if version == "" {
		db.Close()
		return nil, nil, errors.New("the database has no schema. Run `suco serve` once to apply it")
	}
	return db, credential.NewPostgres(db.Conns(), key, cfg.Credentials.KeyID), nil
}

// credentialNew makes one credential and writes its token to a file in the
// working directory, named by the credential's ID so that the file and the
// row list shows can be matched without opening it.
//
// The token goes to the file and nowhere else. What is printed is kept by
// whatever ran the command: a terminal's scrollback, a CI job's log, which
// is readable by more people for longer than the database is.
func credentialNew(ctx context.Context, args []string, stdout io.Writer) error {
	capability, err := capabilityOf(args)
	if err != nil {
		return err
	}
	db, store, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer db.Close()

	// Which accounts there are is the database's to say, and a credential
	// is of exactly one of them. This build has no way for an operator to
	// name one, so it issues to the one there is, and refuses to choose
	// among more.
	accounts, err := store.Accounts(ctx)
	if err != nil {
		return err
	}
	if len(accounts) != 1 {
		return fmt.Errorf("the database has %d accounts, and a credential is of one; naming one is not implemented", len(accounts))
	}

	id, token, err := store.Create(ctx, accounts[0], capability, time.Now())
	if err != nil {
		return err
	}
	name := "credential-" + string(id) + ".token"
	if err := writeToken(name, token); err != nil {
		// A row whose token was never written is one nobody can present,
		// and one list would show as in force until somebody revoked it.
		// Revoked past the context: a signal between the insert and the
		// write is not a reason to leave it.
		if revokeErr := store.Revoke(context.WithoutCancel(ctx), id, time.Now()); revokeErr != nil {
			return errors.Join(err, fmt.Errorf("credential %s is in force with no token written, and revoking it failed: %w", id, revokeErr))
		}
		return err
	}

	fmt.Fprintf(stdout, "Wrote %s\n\nA client presents the token in it as `Authorization: Bearer <token>`.\n", name)
	return nil
}

// capabilityOf reads the one argument new takes. There is no default: which
// of the two a credential may do is the operator's to choose, and a default
// is what every credential made without a thought would have.
//
// No credential command repeats an argument in an error, where serve and
// doctor name the one they were given. What is typed after these may be
// the token, from the file new wrote into the working directory, and an
// error is what a CI log keeps.
func capabilityOf(args []string) (credential.Capability, error) {
	switch {
	case len(args) > 1:
		return "", errors.New("credential new takes one of --read-only and --read-write, got more than one word")
	case len(args) == 0:
		return "", errors.New("credential new takes --read-only or --read-write")
	case args[0] == "--read-only":
		return credential.ReadOnly, nil
	case args[0] == "--read-write":
		return credential.ReadWrite, nil
	}
	return "", errors.New("credential new takes --read-only or --read-write, got neither")
}

// writeToken writes token, and nothing else, to a file only its owner can
// read. No newline after it: a client reads the file whole and presents
// what it read, and a token with a newline on the end is one nobody issued.
//
// O_EXCL as init writes the document: the name is a UUID's, so a file of that
// name already there is not one this run wrote, and it is not overwritten.
func writeToken(name string, token credential.Token) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	_, writeErr := io.WriteString(f, string(token))
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	return nil
}

// credentialList shows every credential in force, in the store's order: the
// most recently used first, and the never used last. The last column answers
// whether a client is presenting one, and the second which key a row was
// made under, for a deployment whose every request fails.
//
// Nothing of a token is here to show: the row holds only its hash, and what
// the store reads back holds not even that.
func credentialList(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("credential list takes no arguments, got %d", len(args))
	}
	db, store, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer db.Close()

	all, err := store.List(ctx)
	if err != nil {
		return err
	}
	// Laid out in full before any of it is written, as doctor's report is.
	var table bytes.Buffer
	tw := tabwriter.NewWriter(&table, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tKEY ID\tSCOPE\tCAPABILITY\tLAST USED")
	for _, c := range all {
		lastUsed := "never"
		if !c.LastUsedAt.IsZero() {
			lastUsed = c.LastUsedAt.Format(time.RFC3339)
		}
		// The key identifier is whatever the document that made the row
		// held, quoted as doctor quotes what a document holds.
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", c.ID, invisible.Quote(c.KeyID), c.Scope, c.Capability, lastUsed)
	}
	tw.Flush()
	if _, err := stdout.Write(table.Bytes()); err != nil {
		return fmt.Errorf("writing the list: %w", err)
	}
	return nil
}

// credentialRevoke takes one credential out of force. A route that asks for
// a credential looks it up as each request arrives and keeps nothing between
// requests, so it refuses the next request that presents this one.
func credentialRevoke(ctx context.Context, args []string, stdout io.Writer) error {
	id, err := idOf(args)
	if err != nil {
		return err
	}
	db, store, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := store.Revoke(ctx, id, time.Now()); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Revoked %s\n", id)
	return nil
}

// idOf reads the one argument revoke takes, before anything else is read.
// As capabilityOf, it repeats none of what it was given.
func idOf(args []string) (credential.ID, error) {
	switch {
	case len(args) > 1:
		return "", fmt.Errorf("credential revoke takes one ID, got %d arguments", len(args))
	case len(args) == 0:
		return "", errors.New("credential revoke takes the ID of one credential, as list shows it")
	}
	return credential.ParseID(args[0])
}
