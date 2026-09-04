package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sucopay/sucopay/internal/credential"
	"github.com/sucopay/sucopay/internal/postgres"
)

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

	resolved, document, err := load()
	if err != nil {
		return err
	}
	if err := unimplementedError(document, resolved); err != nil {
		return err
	}
	cfg := resolved.Config
	// The default database.managed is a document that names no database,
	// and a credential has to be stored somewhere. The words are the ones
	// serve refuses a document asking for one with, because a deployment
	// with neither meets them at whichever command runs first.
	if cfg.Database.URL == "" {
		return fmt.Errorf("%s: %s", document, ownDatabase)
	}

	db, err := postgres.Open(ctx, cfg.Database.URL)
	if err != nil {
		return err
	}
	defer db.Close()
	key, err := credential.ParseKey(cfg.Credentials.Key)
	if err != nil {
		return err
	}
	store := credential.NewPostgres(db.Conns(), key, cfg.Credentials.KeyID)

	// serve applies the schema and logs what it applied. A command that
	// answers a person with one line would apply it in silence, so this one
	// asks for it to have been applied.
	version, err := db.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if version == "" {
		return errors.New("the database has no schema. Run `suco serve` once to apply it")
	}

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
func capabilityOf(args []string) (credential.Capability, error) {
	if len(args) != 1 {
		return "", errors.New("credential new takes exactly one of --read-only and --read-write")
	}
	switch args[0] {
	case "--read-only":
		return credential.ReadOnly, nil
	case "--read-write":
		return credential.ReadWrite, nil
	}
	return "", fmt.Errorf("credential new takes --read-only or --read-write, got %q", args[0])
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
