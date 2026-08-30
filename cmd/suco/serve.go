package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"strconv"

	"github.com/sucopay/sucopay/internal/api"
	"github.com/sucopay/sucopay/internal/config"
)

func serve(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("serve takes no arguments, got %q", args[0])
	}

	document := config.Path(os.LookupEnv, defaultDocument)
	resolved, err := config.Load(document, os.LookupEnv)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no configuration at %s. Run `suco init` to write one", document)
	}
	if err != nil {
		return err
	}

	cfg := resolved.Config
	addr := net.JoinHostPort(cfg.Listen.Host, strconv.Itoa(cfg.Listen.Port))
	server, err := api.Listen(addr, api.Handler())
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "suco Pay is serving %s\n", cfg.Listen.BaseURL)
	return server.Run(ctx)
}
