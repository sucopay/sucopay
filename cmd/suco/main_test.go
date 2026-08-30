package main

import (
	"bytes"
	"context"
	"errors"
	"io"
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

// document writes a configuration document and points SUCO_CONFIG at it.
func document(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "suco.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUCO_CONFIG", path)
	return path
}

// reportLine returns the line of a report for one setting, so that a value and
// a source are asserted against the setting they belong to rather than against
// the whole report.
func reportLine(t *testing.T, report, path string) string {
	t.Helper()
	for _, line := range strings.Split(report, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), path+" ") {
			return line
		}
	}
	t.Fatalf("the report has no line for %s:\n%s", path, report)
	return ""
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

// Every command reading a document meets a missing one through load, so the
// sentence it gives is asserted once rather than once per command.
func TestRun_WithoutADocumentNamesTheCommandThatWritesOne(t *testing.T) {
	for _, name := range []string{"serve", "doctor"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SUCO_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))

			_, _, err := runArgs(t, name)

			if err == nil {
				t.Fatal("want an error, got none")
			}
			if !strings.Contains(err.Error(), "suco init") {
				t.Errorf("error does not name the command that writes a document: %v", err)
			}
			if !strings.Contains(err.Error(), "absent.yaml") {
				t.Errorf("error does not name the document it looked for: %v", err)
			}
		})
	}
}

func TestRun_EveryCommandNamesAnArgumentItDoesNotTake(t *testing.T) {
	for _, c := range commands {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("SUCO_CONFIG", filepath.Join(t.TempDir(), "suco.yaml"))

			_, _, err := runArgs(t, c.name, "extra")

			if err == nil {
				t.Fatal("want an error, got none")
			}
			if !strings.Contains(err.Error(), `"extra"`) {
				t.Errorf("error does not name the argument: %v", err)
			}
		})
	}
}

func TestRun_ServeReportsAConfigurationProblem(t *testing.T) {
	document(t, "listen:\n  port: 99999\n")

	_, _, err := runArgs(t, "serve")

	if err == nil {
		t.Fatal("want an error, got none")
	}
	if !strings.Contains(err.Error(), "listen.port") {
		t.Errorf("error does not name the key: %v", err)
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
	for _, absent := range []string{"dev", "listen", "migrate", "upgrade"} {
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
			document(t, c.doc)

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

func TestRun_DoctorGivesEverySettingItsValueAndSource(t *testing.T) {
	path := document(t, "listen:\n  port: 9000\n")

	stdout, _, err := runArgs(t, "doctor")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if !strings.Contains(stdout, path) {
		t.Errorf("the report does not name the document it read:\n%s", stdout)
	}
	// Every setting the resolver reads. A report that quietly stopped naming
	// one would leave an operator certain of a value nothing had shown them.
	for _, want := range []struct{ path, value, source string }{
		{"database.managed", "true", "default"},
		{"database.url", "not set", "default"},
		{"listen.base_url", "http://localhost:9000", "default"},
		{"listen.host", "127.0.0.1", "default"},
		{"listen.port", "9000", "file"},
	} {
		line := reportLine(t, stdout, want.path)
		if !strings.Contains(line, want.value) || !strings.Contains(line, want.source) {
			t.Errorf("%s reads %q, want the value %s from %s", want.path, line, want.value, want.source)
		}
	}
}

func TestRun_DoctorSaysWhetherASecretIsSetAndNeverItsValue(t *testing.T) {
	const password = "hunter2"

	for _, c := range []struct {
		name     string
		document string
		value    string
		source   string
		// A configured database is a section this build refuses, which
		// TestRun_DoctorPrintsTheReportAndThenRefusesASectionNothingActsOn
		// covers. The report is written before that, and it is the report
		// under test here.
		refused bool
	}{
		{
			name:     "supplied by a variable",
			document: "database:\n  managed: false\n  url: ${SUCO_DATABASE_URL}\n",
			value:    "set",
			source:   "${SUCO_DATABASE_URL}",
			refused:  true,
		},
		{
			name:     "absent from the document",
			document: "listen:\n  port: 9000\n",
			value:    "not set",
			source:   "default",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			document(t, c.document)
			t.Setenv("SUCO_DATABASE_URL", "postgres://admin:"+password+"@db.internal/sucopay")

			stdout, _, err := runArgs(t, "doctor")

			if c.refused && err == nil {
				t.Fatal("want the refusal, got none")
			}
			if !c.refused && err != nil {
				t.Fatalf("err = %v, want none", err)
			}
			if strings.Contains(stdout, password) {
				t.Errorf("the report carries the password:\n%s", stdout)
			}
			line := reportLine(t, stdout, "database.url")
			if !strings.Contains(line, c.value) {
				t.Errorf("database.url reads %q, want it to say %q", line, c.value)
			}
			if !strings.Contains(line, c.source) {
				t.Errorf("database.url reads %q, want the source %s", line, c.source)
			}
		})
	}
}

func TestRun_DoctorPassesEveryConfigurationProblemThrough(t *testing.T) {
	document(t, "listen:\n  port: 99999\n  base_url: nonsense\n")

	_, _, err := runArgs(t, "doctor")

	if err == nil {
		t.Fatal("want an error, got none")
	}
	for _, want := range []string{"listen.port", "listen.base_url"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %s: %v", want, err)
		}
	}
}

func TestRun_DoctorPrintsTheReportAndThenRefusesASectionNothingActsOn(t *testing.T) {
	path := document(t, "networks:\n  local:\n    kind: simulated\n")

	stdout, _, err := runArgs(t, "doctor")

	if err == nil {
		t.Fatal("want an error, got none")
	}
	if !strings.Contains(err.Error(), "networks") {
		t.Errorf("error does not name the section: %v", err)
	}
	if !strings.Contains(err.Error(), "Remove the section") {
		t.Errorf("error does not say what to do about it: %v", err)
	}
	if !strings.Contains(err.Error(), filepath.Base(path)) {
		t.Errorf("error does not name the document: %v", err)
	}
	if !strings.Contains(stdout, "listen.port") {
		t.Errorf("the report was withheld, leaving nothing to diagnose:\n%s", stdout)
	}
}

// failingWriter stands in for a full disk or a closed pipe.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("no space left on device")
}

func TestRun_DoctorFailsWhenTheReportCannotBeWritten(t *testing.T) {
	document(t, "listen:\n  port: 9000\n")

	err := run(t.Context(), []string{"doctor"}, failingWriter{}, io.Discard)

	if err == nil {
		t.Fatal("want an error, got none: the report is what this command produces")
	}
	if !strings.Contains(err.Error(), "no space left on device") {
		t.Errorf("error does not carry what went wrong: %v", err)
	}
}
