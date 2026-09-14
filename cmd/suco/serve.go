package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/sucopay/sucopay/internal/accepted"
	"github.com/sucopay/sucopay/internal/api"
	"github.com/sucopay/sucopay/internal/credential"
	"github.com/sucopay/sucopay/internal/finality"
	"github.com/sucopay/sucopay/internal/invisible"
	"github.com/sucopay/sucopay/internal/observe"
	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/postgres"
)

// serve writes to the log rather than answering a person. It is the one
// command that keeps running, and what a running process says about itself is
// its log; init and doctor answer a person and keep printing.
//
// network cursor writes a line too, for a different reason: what it did is
// worth keeping wherever the lines are kept, not only in the terminal of
// whoever ran it.
func serve(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) > 0 {
		// Counted: no error repeats a word typed after suco, for the reason
		// at errUnknown.
		return fmt.Errorf("serve takes no arguments, got %d", len(args))
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
		payments    api.Payments
		chains      api.Chains
		observers   observe.Observers
		workers     finality.Workers
		decides     api.Settling
	)
	if cfg.Database.URL != "" {
		db, err := postgres.Open(ctx, cfg.Database.URL.Expose())
		if err != nil {
			return err
		}
		defer db.Close()
		ready = db.Ping
		key, err := credential.ParseKey(cfg.Credentials.Key.Expose())
		if err != nil {
			return err
		}
		credentials = credential.NewPostgres(db.Conns(), key, cfg.Credentials.KeyID)
		store := payment.NewPostgres(db.Conns())
		payments = payment.NewHTTP(
			payment.NewService(store, store, observe.NewCursors(db.Conns()), time.Now),
			cfg.Assets, accepted.NewPostgres(db.Conns()))

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

		// After the schema: a round writes its position and takes a lease,
		// and both want tables to be there. Opened here rather than beside
		// the document, because what reads a chain has nowhere to write down
		// how far it has read without a database.
		networks, err := openChains(cfg)
		if err != nil {
			return err
		}
		for _, n := range networks {
			observers = append(observers, observe.New(n, db.Conns(), store, log, time.Now))
		}
		chains = observers

		// One holder for every network this instance settles, because one
		// process is one instance. The names differ, so nothing here competes
		// with itself.
		settling, err := openSettling(cfg)
		if err != nil {
			return err
		}
		leases := observe.NewLeases(db.Conns())
		for _, n := range settling {
			workers = append(workers, finality.New(n, store, leases, log, time.Now))
		}
		decides = workers
	}

	addr := net.JoinHostPort(cfg.Listen.Host, strconv.Itoa(cfg.Listen.Port))
	deps := api.Dependencies{Database: ready, Credentials: credentials, Payments: payments,
		Chains: chains, Settling: decides}
	server, err := api.Listen(addr, api.Handler(log, deps), log)
	if err != nil {
		return err
	}

	// The rounds run beside the server rather than before it. A network is
	// read for as long as the process lives, so waiting for one to get
	// anywhere would be waiting forever; and what it has got to is what
	// /readyz answers with, which is how anybody learns it has got nowhere.
	//
	// On a context of their own, because the server stops for reasons of its
	// own as well as for this one: a listener that dies hands its error back
	// with the process's context still live. The rounds would go on reading a
	// chain nobody can ask about any more, and the wait below would be a wait
	// for the operator to notice.
	reading, stopReading := context.WithCancel(ctx)
	defer stopReading()
	var rounds sync.WaitGroup
	for _, o := range observers {
		rounds.Add(1)
		go func() {
			defer rounds.Done()
			if err := o.Run(reading); err != nil {
				log.ErrorContext(ctx, "a network stopped being read",
					slog.String("error", invisible.Shown(err.Error(), maxDescription)))
			}
		}()
	}
	for _, w := range workers {
		rounds.Add(1)
		go func() {
			defer rounds.Done()
			if err := w.Run(reading); err != nil {
				log.ErrorContext(ctx, "a network stopped being settled",
					slog.String("error", invisible.Shown(err.Error(), maxDescription)))
			}
		}()
	}

	log.InfoContext(ctx, "serving", slog.String("base_url", cfg.Listen.BaseURL))
	err = server.Run(ctx)
	stopReading()
	// Waited for before the database is closed, which is deferred above: a
	// round on its way out puts its lease down, and a lease nobody puts down
	// is a network the next instance waits out the term of.
	rounds.Wait()
	log.InfoContext(context.WithoutCancel(ctx), "stopped")
	return err
}
