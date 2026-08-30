// Command suco is the suco Pay server and CLI.
//
//	suco serve   run the server
//
// Configuration comes from suco.yaml, or from the file SUCO_CONFIG names.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// defaultDocument is where suco looks for its configuration when SUCO_CONFIG
// does not say otherwise.
const defaultDocument = "suco.yaml"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	switch {
	case err == nil:
	case errors.Is(err, errUsage):
		// run has already written the usage text, so repeating the sentinel
		// here would put the word "usage" under it.
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

// errUsage reports that run wrote the usage text and the command should fail.
var errUsage = errors.New("usage")

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		usage(stderr)
		return errUsage
	}
	switch args[0] {
	case "serve":
		return serve(ctx, args[1:], stdout)
	case "help", "-h", "--help":
		usage(stdout)
		return nil
	}
	usage(stderr)
	return fmt.Errorf("unknown command %q", args[0])
}

func usage(w io.Writer) {
	fmt.Fprint(w, `suco Pay

Usage:
  suco serve    run the server

`)
}
