package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"

	"github.com/sucopay/sucopay/internal/api"
	"github.com/sucopay/sucopay/internal/postgres"
)

func serve(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("serve takes no arguments, got %q", args[0])
	}

	resolved, document, err := load()
	if err != nil {
		return err
	}

	if err := unimplementedError(document, resolved); err != nil {
		return err
	}

	cfg := resolved.Config
	if cfg.Database.URL != "" {
		db, err := postgres.Open(ctx, cfg.Database.URL)
		if err != nil {
			return err
		}
		defer db.Close()

		// Applied at every start rather than by a command an operator has to
		// know about, which would leave an evaluator with an empty database
		// and nothing saying why. [postgres.Pool.Migrate] says what makes
		// that safe to repeat.
		applied, err := db.Migrate(ctx)
		if err != nil {
			return err
		}
		if applied > 0 {
			fmt.Fprintf(stdout, "Applied %d migration(s)\n", applied)
		}
	}

	addr := net.JoinHostPort(cfg.Listen.Host, strconv.Itoa(cfg.Listen.Port))
	server, err := api.Listen(addr, api.Handler())
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "suco Pay is serving %s\n", cfg.Listen.BaseURL)
	return server.Run(ctx)
}
