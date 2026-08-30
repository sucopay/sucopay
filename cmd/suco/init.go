package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/sucopay/sucopay/internal/config"
)

func initialise(args []string, stdout io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("init takes no arguments, got %q", args[0])
	}

	document := config.Path(os.LookupEnv, defaultDocument)
	if _, err := os.Stat(document); err == nil {
		return fmt.Errorf("%s already exists. Edit it, or move it aside to start over", document)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("configuration document: %w", err)
	}

	// O_EXCL rather than a plain create: the check above leaves a window, and
	// overwriting a document someone has edited is not recoverable.
	// 0600 rather than 0644: this is the file an operator adds a database URL
	// and an RPC endpoint to, and it is read on a host that may have other
	// accounts on it.
	f, err := os.OpenFile(document, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("configuration document: %w", err)
	}
	_, writeErr := f.Write(config.Document())
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("configuration document: %w", err)
	}

	fmt.Fprintf(stdout, "Wrote %s\n\nRun `suco serve` to start.\n", document)
	return nil
}
