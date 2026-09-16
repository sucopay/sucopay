package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/sucopay/sucopay/internal/config"
	"github.com/sucopay/sucopay/internal/credential"
	"github.com/sucopay/sucopay/internal/invisible"
	"github.com/sucopay/sucopay/internal/postgres"
)

// opened is what a command that reads or writes the database has once
// [openStore] returns: the assets and networks the document lists, how it says
// to write a line, the database it names, and the store of credentials over
// it. The document's path is for a refusal to name.
//
// These and not the whole configuration: the rest of it holds the credential
// key, which no command has a use for once the store is open.
type opened struct {
	document string
	assets   config.Assets
	networks map[string]config.Network
	// log is how the one command that writes a line rather than answering a
	// person is to write it.
	log         config.Log
	db          *postgres.Pool
	credentials *credential.Postgres
}

// openStore reads the document and opens the database it names, with the
// store of credentials over it, for the commands that read or write either.
// A document serve refuses is refused here in serve's words, and so is one
// naming no database: a deployment with either meets the refusal at
// whichever command runs first, and two sentences for one problem read as
// two problems.
func openStore(ctx context.Context) (opened, error) {
	resolved, document, err := load()
	if err != nil {
		return opened{}, err
	}
	if err := unimplementedError(document, resolved); err != nil {
		return opened{}, err
	}
	cfg := resolved.Config
	// The default database.managed is a document that names no database.
	if cfg.Database.URL == "" {
		return opened{}, fmt.Errorf("%s: %s", document, ownDatabase)
	}
	// Read before the database is opened: a malformed key needs no
	// connection to be refused.
	key, err := credential.ParseKey(cfg.Credentials.Key.Expose())
	if err != nil {
		return opened{}, err
	}

	db, err := postgres.Open(ctx, cfg.Database.URL.Expose())
	if err != nil {
		return opened{}, err
	}
	// serve applies the schema and logs what it applied. A command that
	// answers a person with a line or two would apply it in silence, so
	// these ask for it to have been applied.
	version, err := db.SchemaVersion(ctx)
	if err != nil {
		db.Close()
		return opened{}, err
	}
	if version == "" {
		db.Close()
		return opened{}, errors.New("the database has no schema. Run `suco serve` once to apply it")
	}
	return opened{
		document:    document,
		assets:      cfg.Assets,
		networks:    cfg.Networks,
		log:         cfg.Log,
		db:          db,
		credentials: credential.NewPostgres(db.Conns(), key, cfg.Credentials.KeyID),
	}, nil
}

// oneAccount is the account a command acts on. Which accounts there are is
// the database's to say, and a command here acts on exactly one of them.
// This build has no way for an operator to name one, so it acts on the one
// there is, and refuses to choose among more.
func oneAccount(ctx context.Context, store *credential.Postgres) (credential.AccountID, error) {
	accounts, err := store.Accounts(ctx)
	if err != nil {
		return "", err
	}
	if len(accounts) != 1 {
		return "", fmt.Errorf("the database has %d accounts, and this acts on one of them; naming one is not implemented", len(accounts))
	}
	return accounts[0], nil
}

// credentialNew makes one credential and writes its token to a file in the
// working directory, named by the credential's ID so that the file and the
// row list shows can be matched without opening it.
//
// The token goes to the file and nowhere else. What is printed is kept by
// whatever ran the command: a terminal's scrollback, a CI job's log, which
// is readable by more people for longer than the database is.
func credentialNew(ctx context.Context, args []string, stdout io.Writer) error {
	access, err := accessOf(args)
	if err != nil {
		return err
	}
	o, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer o.db.Close()

	account, err := oneAccount(ctx, o.credentials)
	if err != nil {
		return err
	}
	id, token, err := o.credentials.Create(ctx, account, access, time.Now())
	if err != nil {
		return err
	}
	name := "credential-" + string(id) + ".token"
	if err := writeNew(name, []byte(token)); err != nil {
		err = fmt.Errorf("token file: %w", err)
		// A row whose token was never written is one nobody can present,
		// and one list would show as in force until somebody revoked it.
		// Revoked past the context: a signal between the insert and the
		// write is not a reason to leave it.
		if revokeErr := o.credentials.Revoke(context.WithoutCancel(ctx), id, time.Now()); revokeErr != nil {
			return errors.Join(err, fmt.Errorf("credential %s is in force with no token written, and revoking it failed: %w", id, revokeErr))
		}
		return err
	}

	fmt.Fprintf(stdout, "Wrote %s\n\nA client presents the token in it as `Authorization: Bearer <token>`.\n", name)
	return nil
}

// accessOf reads the one argument new takes. There is no default: which of
// the two a credential may do is the operator's to choose, and a default is
// what every credential made without a thought would have.
//
// Surplus arguments are counted and an unknown one is "neither": no error
// repeats a word typed after suco, for the reason at [errUnknown].
func accessOf(args []string) (credential.Access, error) {
	switch {
	case len(args) > 1:
		return "", fmt.Errorf("credential new takes one of --read-only and --read-write, got %d arguments", len(args))
	case len(args) == 0:
		return "", errors.New("credential new takes --read-only or --read-write")
	case args[0] == "--read-only":
		return credential.ReadOnly, nil
	case args[0] == "--read-write":
		return credential.ReadWrite, nil
	}
	return "", errors.New("credential new takes --read-only or --read-write, got neither")
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
	o, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer o.db.Close()

	all, err := o.credentials.List(ctx)
	if err != nil {
		return err
	}
	// Laid out in full before any of it is written, as doctor's report is.
	var table bytes.Buffer
	tw := tabwriter.NewWriter(&table, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tKEY ID\tSCOPE\tACCESS\tLAST USED")
	for _, c := range all {
		lastUsed := "never"
		if !c.LastUsedAt.IsZero() {
			lastUsed = c.LastUsedAt.Format(time.RFC3339)
		}
		// The key identifier is whatever the document that made the row
		// held, quoted as doctor quotes what a document holds.
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", c.ID, invisible.Quote(c.KeyID), c.Scope, c.Access, lastUsed)
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
	o, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer o.db.Close()

	if err := o.credentials.Revoke(ctx, id, time.Now()); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Revoked %s\n", id)
	return nil
}

// idOf reads the one argument revoke takes, before anything else is read.
// As accessOf, it repeats none of what it was given.
func idOf(args []string) (credential.ID, error) {
	switch {
	case len(args) > 1:
		return "", fmt.Errorf("credential revoke takes one ID, got %d arguments", len(args))
	case len(args) == 0:
		return "", errors.New("credential revoke takes the ID of one credential, as list shows it")
	}
	return credential.ParseID(args[0])
}
