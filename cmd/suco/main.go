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
	"maps"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"text/tabwriter"

	"github.com/sucopay/sucopay/internal/config"
	"github.com/sucopay/sucopay/internal/problem"
)

// defaultDocument is where suco looks for its configuration when SUCO_CONFIG
// does not say otherwise.
const defaultDocument = "suco.yaml"

// command is one subcommand, or a group of them. Dispatch and the usage text
// read the same list, so a command cannot exist without being named or be
// named without existing.
type command struct {
	name  string
	about string
	run   func(ctx context.Context, args []string, stdout io.Writer) error
	// sub is what a group dispatches into, and is set instead of run.
	sub []command
}

var commands = []command{
	{name: "init", about: "write a configuration document", run: func(_ context.Context, args []string, stdout io.Writer) error {
		return initialise(args, stdout)
	}},
	{name: "serve", about: "run the server", run: serve},
	{name: "doctor", about: "show the resolved configuration and its sources", run: doctor},
	{name: "credential", sub: []command{
		{name: "new", about: "make a credential, and write its token to a file", run: credentialNew},
		{name: "list", about: "show the credentials in force", run: credentialList},
		{name: "revoke", about: "take one credential out of force", run: credentialRevoke},
	}},
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

// ownDatabase is the refusal of a database suco would run itself: what a
// document asking for one is refused with, and what a document naming none
// is refused with by a command that needs one.
const ownDatabase = "a database of its own is not implemented. Set database.managed to false and give database.url"

// The refusals of a network this build would ignore, each written under the
// network's path. A name is a key of the document, and is quoted as one.
const (
	unreferencedNetwork = "no asset refers to it, so nothing reads it. Remove it or list an asset on it"
	unreadRPC           = "nothing reads it. Remove it rather than run a server that ignores it"
)

// unimplementedError refuses a document that configures something no part of
// this build reads. Refusing these in the schema would take the settings out
// before the code that needs them arrives.
//
// Every command that reads a document calls this, so the refusal doctor gives
// is the one serve will give. A default for database.managed is a document
// saying nothing about a database, which is not the same as asking for one:
// serve serves /healthz without one, and a command that needs one refuses
// the default itself.
//
// A network is read by way of the assets that refer to it, and a simulated
// one declares that nothing is observed, which is what this build does; so
// such a network is accepted. One no asset refers to is refused, and so is an
// rpc on any network, since nothing reads the URL. The kind is not looked at
// here: simulated is the one kind config admits.
func unimplementedError(document string, r config.Resolved) error {
	var refusals []string
	if source, ok := r.SourceOf("database.managed"); r.Config.Database.Managed &&
		ok && source.Origin != config.FromDefault {
		refusals = append(refusals, ownDatabase)
	}
	referred := map[string]bool{}
	for _, asset := range r.Config.Assets {
		referred[string(asset.Network())] = true
	}
	for _, name := range slices.Sorted(maps.Keys(r.Config.Networks)) {
		n := r.Config.Networks[name]
		if !referred[name] {
			refusals = append(refusals, problem.Line("networks."+name, unreferencedNetwork))
		}
		if n.RPC != "" {
			refusals = append(refusals, problem.Line("networks."+name+".rpc", unreadRPC))
		}
	}
	if len(refusals) == 0 {
		return nil
	}
	return errors.New(problem.List(document, refusals))
}

// writeNew creates name holding contents, readable by its owner alone, and
// fails rather than overwrite a file already there. Every file suco writes
// goes through it: the document an operator edits, a token, a key. Nothing
// after contents, in particular no newline: a client reads a token file
// whole and presents what it read, and a token with a newline on the end is
// one nobody issued.
//
// 0600 rather than 0644: the document is the file an operator adds a
// database URL to, and it is read on a host that may have other accounts on
// it. O_EXCL rather than a plain create: a token or key file's name is
// random, so a file of that name already there is not one this run wrote;
// and overwriting a document someone has edited is not recoverable.
func writeNew(name string, contents []byte) error {
	// #nosec G304 -- the name is SUCO_CONFIG or one suco made up; nothing a request carries reaches it
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(contents)
	return errors.Join(writeErr, f.Close())
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

// errUnknown refuses a word no command is named by, under suco or under a
// group of commands, with the usage text written and the word not repeated.
// Under a group it is wrapped with the group's name, which is the table's
// word and never the typed one.
//
// No error repeats a word typed after suco. The word may be a token, from
// the file credential new wrote into the working directory, when a script
// has lost the word before it; and an error is what a CI log keeps, where
// the line that was typed may have had its secrets masked and the output
// has not. What the person typed is on their screen. The usage text says
// what would have been taken.
var errUnknown = errors.New("unknown command")

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return dispatch(ctx, commands, "", args, stdout, stderr)
}

// dispatch runs the one of cmds that args names, and goes one level down for
// a group. under is the group cmds is the table of, and "" at the top.
func dispatch(ctx context.Context, cmds []command, under string, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		usage(stderr)
		return errUsage
	}
	switch args[0] {
	case "help", "-h", "--help":
		usage(stdout)
		return nil
	}
	for _, c := range cmds {
		if c.name != args[0] {
			continue
		}
		if c.sub != nil {
			return dispatch(ctx, c.sub, c.name, args[1:], stdout, stderr)
		}
		return c.run(ctx, args[1:], stdout)
	}
	usage(stderr)
	if under == "" {
		return errUnknown
	}
	return fmt.Errorf("%w under %s", errUnknown, under)
}

func usage(w io.Writer) {
	fmt.Fprint(w, "suco Pay\n\nUsage:\n")
	tw := tabwriter.NewWriter(w, 0, 0, 4, ' ', 0)
	lines(tw, commands, "suco")
	fmt.Fprintf(tw, "  suco %s\t%s\n", "help", "show this text")
	tw.Flush()
	fmt.Fprintln(w)
}

// lines names every command under cmds, each with the words that reach it,
// under being the words so far. A group is not a command and has no line of
// its own.
func lines(w io.Writer, cmds []command, under string) {
	for _, c := range cmds {
		if c.sub != nil {
			lines(w, c.sub, under+" "+c.name)
			continue
		}
		fmt.Fprintf(w, "  %s %s\t%s\n", under, c.name, c.about)
	}
}
