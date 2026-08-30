// Command suco is the suco Pay server and CLI. Run "suco help" for the
// commands.
//
// Configuration comes from suco.yaml, or from the file SUCO_CONFIG names.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/sucopay/sucopay/internal/config"
)

// defaultDocument is where suco looks for its configuration when SUCO_CONFIG
// does not say otherwise.
const defaultDocument = "suco.yaml"

// command is one subcommand. Dispatch and the usage text read the same list, so
// a command cannot exist without being named or be named without existing.
type command struct {
	name  string
	about string
	run   func(ctx context.Context, args []string, stdout io.Writer) error
}

var commands = []command{
	{"init", "write a configuration document", func(_ context.Context, args []string, stdout io.Writer) error {
		return initialise(args, stdout)
	}},
	{"serve", "run the server", serve},
	{"doctor", "show the resolved configuration and its sources", doctor},
}

// load reads the configuration the way every command needing it does, so that
// a missing document says the same thing wherever it is met.
func load() (config.Resolved, string, error) {
	document := config.Path(os.LookupEnv, defaultDocument)
	resolved, err := config.Load(document, os.LookupEnv)
	if errors.Is(err, fs.ErrNotExist) {
		return config.Resolved{}, document,
			fmt.Errorf("no configuration at %s. Run `suco init` to write one", document)
	}
	return resolved, document, err
}

// unimplemented names the sections a document sets that no part of this build
// reads. Refusing them in the schema would take the settings out before the
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

// unimplementedError is the refusal both commands give, so that the one a
// reader meets from doctor is the one serve will give them.
func unimplementedError(document string, missing []string) error {
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%s configures %s, which this build does not act on. Remove the section rather than run a server that ignores it",
		document, strings.Join(missing, " and "))
}

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
	case "help", "-h", "--help":
		usage(stdout)
		return nil
	}
	for _, c := range commands {
		if c.name == args[0] {
			return c.run(ctx, args[1:], stdout)
		}
	}
	usage(stderr)
	return fmt.Errorf("unknown command %q", args[0])
}

func usage(w io.Writer) {
	fmt.Fprint(w, "suco Pay\n\nUsage:\n")
	tw := tabwriter.NewWriter(w, 0, 0, 4, ' ', 0)
	for _, c := range commands {
		fmt.Fprintf(tw, "  suco %s\t%s\n", c.name, c.about)
	}
	fmt.Fprintf(tw, "  suco %s\t%s\n", "help", "show this text")
	tw.Flush()
	fmt.Fprintln(w)
}
