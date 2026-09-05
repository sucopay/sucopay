package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sucopay/sucopay/internal/config"
	"github.com/sucopay/sucopay/internal/credential"
)

// initialise writes two files into one directory: the document, and the key
// the document names the environment variable for. The key goes first, so
// that the document never names a key that was not written; and when the
// document then fails, the key is removed, or the next init would find a key
// it did not write.
//
// init cannot set the variable. A process sets the environment of what it
// starts, and init is not what starts serve; so it prints the line that does,
// for the shell that will.
func initialise(args []string, stdout io.Writer) error {
	if len(args) > 0 {
		// Counted: no error repeats a word typed after suco, for the reason
		// at errUnknown.
		return fmt.Errorf("init takes no arguments, got %d", len(args))
	}

	document := config.Path(os.LookupEnv, defaultDocument)
	if _, err := os.Stat(document); err == nil {
		return fmt.Errorf("%s already exists. Edit it, or move it aside to start over", document)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("configuration document: %w", err)
	}

	// Named by the identifier the document carries, so that the file, the
	// document and a row credential list shows agree on which key this is;
	// and in the document's directory, so that a document SUCO_CONFIG names
	// keeps the key it is read with. A token goes to the working directory
	// because it is the caller's; the key is the document's.
	keyID := credential.NewKeyID()
	keyFile := filepath.Join(filepath.Dir(document), "credentials-"+keyID+".key")
	if err := writeNew(keyFile, []byte(credential.NewKey().Hex())); err != nil {
		return fmt.Errorf("key file: %w", err)
	}
	if err := writeNew(document, config.Document(keyID)); err != nil {
		err = fmt.Errorf("configuration document: %w", err)
		if removeErr := os.Remove(keyFile); removeErr != nil {
			return errors.Join(err, fmt.Errorf("the key file is left behind, and removing it failed: %w", removeErr))
		}
		return err
	}

	// The path is one word for sh, and after -- one file for cat whatever
	// it begins with: a document named relative to the working directory
	// can begin with a dash, which cat alone reads as an option.
	fmt.Fprintf(stdout, "Wrote %s and %s\n\n"+
		"The document names the variable the key is read from, and holds no key. Set\n"+
		"the variable in the shell that will run suco:\n\n"+
		"  export %s=\"$(cat -- %s)\"\n\n"+
		"Then run `suco serve`.\n",
		document, keyFile, config.KeyVar, shellQuoted(keyFile))
	return nil
}

// shellQuoted is s as one word for sh, whatever s holds. Inside single
// quotes nothing is special, and a single quote is written as one closed,
// escaped and opened again.
func shellQuoted(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
