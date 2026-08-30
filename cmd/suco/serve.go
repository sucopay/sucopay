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
	"strings"

	"github.com/sucopay/sucopay/internal/api"
	"github.com/sucopay/sucopay/internal/config"
)

// unimplemented names the sections a document sets that no part of this build
// reads. Accepting them would start a server that silently ignores what it was
// told, and refusing them in the schema would take the settings out before the
// code that needs them arrives.
func unimplemented(r config.Resolved) []string {
	var out []string
	for _, path := range []string{"database.managed", "database.url"} {
		if r.Sources[path].Origin != config.FromDefault {
			out = append(out, "database")
			break
		}
	}
	if len(r.Config.Networks) > 0 {
		out = append(out, "networks")
	}
	return out
}

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

	if missing := unimplemented(resolved); len(missing) > 0 {
		return fmt.Errorf("%s configures %s, which this build does not act on. Remove the section rather than run a server that ignores it",
			document, strings.Join(missing, " and "))
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
