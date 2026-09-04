package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"

	"github.com/sucopay/sucopay/internal/api"
	"github.com/sucopay/sucopay/internal/credential"
	"github.com/sucopay/sucopay/internal/invisible"
	"github.com/sucopay/sucopay/internal/postgres"
)

// serve writes to the log rather than to stdout. It is the one command that
// keeps running, and what a running process says about itself is its log;
// init and doctor answer a person and keep printing.
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
	log, err := newLogger(cfg.Log, stdout)
	if err != nil {
		return err
	}

	var (
		ready       api.Ready
		credentials api.Credentials
	)
	if cfg.Database.URL != "" {
		db, err := postgres.Open(ctx, cfg.Database.URL)
		if err != nil {
			return err
		}
		defer db.Close()
		ready = db.Ping
		key, err := credential.ParseKey(cfg.Credentials.Key)
		if err != nil {
			return err
		}
		credentials = credential.NewPostgres(db.Conns(), key, cfg.Credentials.KeyID)

		// Applied at every start rather than by a command an operator has to
		// know about, which would leave an evaluator with an empty database
		// and nothing saying why. [postgres.Pool.Migrate] says what makes
		// that safe to repeat.
		applied, err := db.Migrate(ctx)
		if err != nil {
			return err
		}
		version, err := db.SchemaVersion(ctx)
		if err != nil {
			return err
		}
		log.InfoContext(ctx, "schema applied",
			slog.Int("migrations", applied),
			slog.String("version", invisible.Shown(version, maxDescription)))
	}

	addr := net.JoinHostPort(cfg.Listen.Host, strconv.Itoa(cfg.Listen.Port))
	server, err := api.Listen(addr, api.Handler(log, ready, credentials), log)
	if err != nil {
		return err
	}

	log.InfoContext(ctx, "serving", slog.String("base_url", cfg.Listen.BaseURL))
	err = server.Run(ctx)
	log.InfoContext(context.WithoutCancel(ctx), "stopped")
	return err
}
