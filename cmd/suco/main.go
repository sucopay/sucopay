// Command suco is the suco Pay server and CLI.
//
//	suco serve     run the server: HTTP API, chain observer, webhooks, reconciliation
//	suco init      create a project and a runnable environment
//	suco dev       run the server locally, with .env loaded and a managed database
//	suco listen    stream events and forward them to a local endpoint
//	suco migrate   apply database migrations
//	suco doctor    report the effective configuration and check connectivity
//	suco upgrade   check, migrate and update to a newer version
//
// The CLI orchestrates; it is not a required control plane. Everything it does
// can also be done by operating the container image and configuration directly.
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

	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

// errUsage asks main to print how to invoke the command.
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
