package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A command has no exported surface, so these tests call run directly. Every
// case goes through it rather than through the individual commands, because
// dispatch and the exit behaviour are what a user meets first.

func runArgs(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err = run(t.Context(), args, &out, &errOut)
	return out.String(), errOut.String(), err
}

func TestRun_WithoutArgumentsWritesUsageAndFails(t *testing.T) {
	stdout, stderr, err := runArgs(t)

	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage", err)
	}
	if !strings.Contains(stderr, "suco serve") {
		t.Errorf("stderr does not carry the usage text: %q", stderr)
	}
	if strings.Contains(stderr, "usage\n") {
		t.Errorf("the sentinel leaked into the output: %q", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
}

func TestRun_UnknownCommandNamesIt(t *testing.T) {
	_, stderr, err := runArgs(t, "nope")

	if err == nil {
		t.Fatal("want an error, got none")
	}
	if !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("error does not name the command: %v", err)
	}
	if !strings.Contains(stderr, "suco serve") {
		t.Errorf("stderr does not carry the usage text: %q", stderr)
	}
}

func TestRun_HelpWritesUsageToStdoutAndSucceeds(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		t.Run(arg, func(t *testing.T) {
			stdout, stderr, err := runArgs(t, arg)

			if err != nil {
				t.Fatalf("err = %v, want none", err)
			}
			if !strings.Contains(stdout, "suco serve") {
				t.Errorf("stdout does not carry the usage text: %q", stdout)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want nothing", stderr)
			}
		})
	}
}

func TestRun_ServeWithoutADocumentNamesTheCommandThatWritesOne(t *testing.T) {
	t.Setenv("SUCO_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))

	_, _, err := runArgs(t, "serve")

	if err == nil {
		t.Fatal("want an error, got none")
	}
	if !strings.Contains(err.Error(), "suco init") {
		t.Errorf("error does not name the command that writes a document: %v", err)
	}
	if !strings.Contains(err.Error(), "absent.yaml") {
		t.Errorf("error does not name the document it looked for: %v", err)
	}
}

func TestRun_ServeReportsAConfigurationProblem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suco.yaml")
	if err := os.WriteFile(path, []byte("listen:\n  port: 99999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUCO_CONFIG", path)

	_, _, err := runArgs(t, "serve")

	if err == nil {
		t.Fatal("want an error, got none")
	}
	if !strings.Contains(err.Error(), "listen.port") {
		t.Errorf("error does not name the key: %v", err)
	}
}

func TestRun_ServeRejectsExtraArguments(t *testing.T) {
	_, _, err := runArgs(t, "serve", "extra")

	if err == nil {
		t.Fatal("want an error, got none")
	}
	if !strings.Contains(err.Error(), `"extra"`) {
		t.Errorf("error does not name the argument: %v", err)
	}
}

func TestUsage_NamesEveryCommandThatExistsAndNoOther(t *testing.T) {
	var out bytes.Buffer

	usage(&out)

	// A word boundary rather than surrounding spaces: a command at the end of
	// a line would slip past a space-delimited search.
	named := func(name string) bool {
		return regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`).MatchString(out.String())
	}
	for _, c := range commands {
		if !named(c.name) {
			t.Errorf("usage does not name %s:\n%s", c.name, out.String())
		}
	}
	if !named("help") {
		t.Errorf("usage does not name help, which run dispatches:\n%s", out.String())
	}
	for _, absent := range []string{"dev", "listen", "migrate", "doctor", "upgrade"} {
		if named(absent) {
			t.Errorf("usage names %q, which is not implemented", absent)
		}
	}
}

// TestRun_DispatchesEveryCommandItNames keeps the usage text and the dispatch
// from drifting apart by reading the same list both do.
func TestRun_DispatchesEveryCommandItNames(t *testing.T) {
	for _, c := range commands {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("SUCO_CONFIG", filepath.Join(t.TempDir(), "suco.yaml"))
			_, _, err := runArgs(t, c.name, "an-argument-no-command-takes")

			if err == nil {
				t.Fatal("want an error, got none")
			}
			if strings.Contains(err.Error(), "unknown command") {
				t.Errorf("%s is named in the usage text but not dispatched", c.name)
			}
		})
	}
}

func TestRun_InitWritesADocumentAndNamesWhatToDoNext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "suco.yaml")
	t.Setenv("SUCO_CONFIG", path)

	stdout, stderr, err := runArgs(t, "init")

	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("no document at %s: %v", path, statErr)
	}
	if !strings.Contains(stdout, path) {
		t.Errorf("stdout does not name the document it wrote: %q", stdout)
	}
	if !strings.Contains(stdout, "suco serve") {
		t.Errorf("stdout does not name the next step: %q", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing", stderr)
	}
}

func TestRun_InitRefusesToOverwriteADocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suco.yaml")
	const edited = "listen:\n  port: 9999\n"
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUCO_CONFIG", path)

	_, _, err := runArgs(t, "init")

	if err == nil {
		t.Fatal("want an error, got none")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error does not name the document: %v", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != edited {
		t.Errorf("the document was changed:\n%s", got)
	}
}

func TestRun_InitRejectsExtraArguments(t *testing.T) {
	t.Setenv("SUCO_CONFIG", filepath.Join(t.TempDir(), "suco.yaml"))

	_, _, err := runArgs(t, "init", "extra")

	if err == nil {
		t.Fatal("want an error, got none")
	}
	if !strings.Contains(err.Error(), `"extra"`) {
		t.Errorf("error does not name the argument: %v", err)
	}
}

// TestRun_ServeAcceptsWhatInitWrote is the pair the Defaults rule asks for:
// serve requires a document, so init has to produce one serve takes.
func TestRun_ServeAcceptsWhatInitWrote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suco.yaml")
	t.Setenv("SUCO_CONFIG", path)
	if _, _, err := runArgs(t, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}

	// serve binds the port the document names and blocks, so the run is cut
	// short by a cancelled context. Reaching that point means the document
	// resolved and the port was free.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out, errOut bytes.Buffer

	err := run(ctx, []string{"serve"}, &out, &errOut)

	if err != nil && strings.Contains(err.Error(), "listen on") {
		t.Skipf("the port the document names is held by something else: %v", err)
	}
	if err != nil {
		t.Fatalf("serve rejected the document init wrote: %v", err)
	}
}

func TestRun_InitWritesADocumentOnlyItsOwnerCanRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suco.yaml")
	t.Setenv("SUCO_CONFIG", path)

	if _, _, err := runArgs(t, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// The document is where a database URL and an RPC endpoint end up.
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("mode = %04o, want nothing for group or other", mode)
	}
}

func TestRun_ServeRefusesASectionNothingActsOn(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		says string
	}{
		{"database", "database:\n  managed: true\n", "database"},
		{"networks", "networks:\n  local:\n    kind: simulated\n", "networks"},
		{"both", "database:\n  managed: true\nnetworks:\n  local:\n    kind: simulated\n", "database and networks"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "suco.yaml")
			if err := os.WriteFile(path, []byte(c.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SUCO_CONFIG", path)

			_, _, err := runArgs(t, "serve")

			if err == nil {
				t.Fatal("want an error, got none")
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("error does not name the section: %v", err)
			}
		})
	}
}

func TestRun_ServeAcceptsADocumentWithoutThoseSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suco.yaml")
	t.Setenv("SUCO_CONFIG", path)
	if _, _, err := runArgs(t, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}

	// serve blocks once it binds, so the run is cut short by a context that is
	// already cancelled.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out, errOut bytes.Buffer

	err := run(ctx, []string{"serve"}, &out, &errOut)

	if err != nil && strings.Contains(err.Error(), "does not act on") {
		t.Fatalf("the document init writes was refused: %v", err)
	}
}
