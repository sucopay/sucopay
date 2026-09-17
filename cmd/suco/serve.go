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
	"github.com/sucopay/sucopay/internal/checkout"
	"github.com/sucopay/sucopay/internal/credential"
	"github.com/sucopay/sucopay/internal/finality"
	"github.com/sucopay/sucopay/internal/invisible"
	"github.com/sucopay/sucopay/internal/observe"
	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/webhook"
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
		webhooks    api.Webhooks
		pages       api.Checkout
		chains      api.Chains
		observers   observe.Observers
		workers     finality.Workers
		decides     api.Settling
		delivering  *webhook.Worker
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
		cursors := observe.NewCursors(db.Conns())
		service := payment.NewService(store, store, cursors, time.Now)
		payments = payment.NewHTTP(service, cfg.Assets, accepted.NewPostgres(db.Conns()),
			checkout.NewLinks(key.Derive(checkout.KeyPurpose), cfg.Credentials.KeyID, cfg.Listen.BaseURL))

		// Under a key of its own, derived from the credentials key for this
		// purpose, so that a webhook secret and a credential's hash never
		// share one; and through the system's resolver, which is the one a
		// delivery will be made through.
		cipher, err := webhook.NewCipher(key.Derive(webhook.KeyPurpose))
		if err != nil {
			return err
		}
		endpoints := webhook.NewPostgres(db.Conns(), cipher, cfg.Credentials.KeyID)
		webhooks = webhook.NewHTTP(endpoints, net.DefaultResolver, time.Now)

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

		chainIDs := map[string]uint64{}
		for name, n := range cfg.Networks {
			chainIDs[name] = n.ChainID
		}
		pages = checkout.NewHTTP(checkout.Deps{
			Store: checkout.NewPostgres(db.Conns()), Payments: store, Service: service,
			Key: key.Derive(checkout.KeyPurpose), KeyID: cfg.Credentials.KeyID,
			ChainIDs: chainIDs, Chains: observers, Watched: cursors,
			// The domain of an asset's contract is the document's to give;
			// without it a wallet is handed material it will not sign.
			Domain: func(asset payment.Asset) (string, string) {
				d, _ := cfg.Domain(asset)
				return d.Name, d.Version
			},
			Now: time.Now,
		})

		// One for the deployment, under the same leases: a delivery is sent
		// by whichever instance holds the name, and once.
		delivering = webhook.NewWorker(endpoints,
			webhook.NewSender(net.DefaultResolver, nil, time.Now), leases, log, time.Now)
	}

	addr := net.JoinHostPort(cfg.Listen.Host, strconv.Itoa(cfg.Listen.Port))
	deps := api.Dependencies{Database: ready, Credentials: credentials, Payments: payments,
		Webhooks: webhooks, Chains: chains, Settling: decides, Checkout: pages}
	if delivering != nil {
		// Set only when there is one: a nil pointer in the interface would
		// read as a worker and be asked.
		deps.Delivering = delivering
	}
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
	if delivering != nil {
		rounds.Add(1)
		go func() {
			defer rounds.Done()
			if err := delivering.Run(reading); err != nil {
				log.ErrorContext(ctx, "the deliveries stopped being sent",
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
