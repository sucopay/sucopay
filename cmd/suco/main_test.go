package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/config"
	"github.com/sucopay/sucopay/internal/credential"
	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
)

// A command has no exported surface, so these tests call run directly. Every
// case goes through it rather than through the individual commands, because
// dispatch and the exit behaviour are what a user meets first.

// runArgs runs a command under a deadline. serve keeps running once it binds,
// so a command that got further than its test expected would otherwise hang
// the run rather than fail it.
func runArgs(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	// Comfortably longer than anything a command waits for on its own, so a
	// slow database reports what it is rather than tripping this.
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	var out, errOut bytes.Buffer
	err = run(ctx, args, &out, &errOut)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%q was still running after the deadline, so it went further than this test expects", args)
	}
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

func TestRun_WithoutACommandWritesUsageAndFails(t *testing.T) {
	t.Parallel()
	// suco alone, and a command that is only a group of commands alone.
	for _, args := range [][]string{{}, {"credential"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			stdout, stderr, err := runArgs(t, args...)

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
		})
	}
}

func TestRun_AnUnknownCommandIsRefusedWithTheUsageTextAndNothingOfTheWord(t *testing.T) {
	t.Parallel()
	// The word is a token. What is typed after suco may be one, from the
	// file credential new wrote into the working directory, and an error is
	// what a CI log keeps.
	token := string(credential.New())
	for _, args := range [][]string{{token}, {"credential", token}} {
		t.Run(strings.Join(append([]string{"suco"}, args[:len(args)-1]...), " ")+" and then a token", func(t *testing.T) {
			t.Parallel()
			stdout, stderr, err := runArgs(t, args...)

			if !errors.Is(err, errUnknown) {
				t.Fatalf("err = %v, want errUnknown", err)
			}
			if !strings.Contains(stderr, "suco serve") {
				t.Errorf("stderr does not carry the usage text: %q", stderr)
			}
			for name, text := range map[string]string{"error": err.Error(), "stdout": stdout, "stderr": stderr} {
				if strings.Contains(text, token[:16]) {
					t.Errorf("%s repeats the word: %q", name, text)
				}
			}
		})
	}
}

func TestRun_AnUnknownWordUnderAGroupIsRefusedNamingTheGroup(t *testing.T) {
	t.Parallel()
	_, _, err := runArgs(t, "credential", "nope")

	if !errors.Is(err, errUnknown) {
		t.Fatalf("err = %v, want errUnknown", err)
	}
	if !strings.HasSuffix(err.Error(), "under credential") {
		t.Errorf("error does not name the group: %v", err)
	}
}

func TestRun_HelpWritesUsageToStdoutAndSucceeds(t *testing.T) {
	t.Parallel()
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
	for _, args := range append([][]string{{"serve"}, {"doctor"}}, databaseCommands()...) {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Setenv("SUCO_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))

			_, _, err := runArgs(t, args...)

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

// paths is every command the table names, as the words that reach it. A
// group of commands has no path of its own, as it has no line in the usage
// text: it is dispatched when the commands under it are.
func paths(cmds []command, under ...string) [][]string {
	var all [][]string
	for _, c := range cmds {
		path := append(slices.Clone(under), c.name)
		if c.sub != nil {
			all = append(all, paths(c.sub, path...)...)
			continue
		}
		all = append(all, path)
	}
	return all
}

func TestRun_EveryCommandRefusesWordsItDoesNotTakeByTheirCountBeforeReadingTheDocument(t *testing.T) {
	// What the commands taking an argument take, so that the ones after it
	// are the ones not taken. Two of them, so that a count that is always
	// one is told from a count.
	takes := map[string][]string{
		"credential new":    {"--read-only"},
		"credential revoke": {string(credential.NewID())},
		"asset accept":      {"jpyc", theAddress},
	}
	for _, path := range paths(commands) {
		t.Run(strings.Join(path, " "), func(t *testing.T) {
			t.Setenv("SUCO_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
			args := append(slices.Clone(path), takes[strings.Join(path, " ")]...)
			args = append(args, "extra", "extra")

			_, _, err := runArgs(t, args...)

			if err == nil {
				t.Fatal("want an error, got none")
			}
			if strings.Contains(err.Error(), "absent.yaml") {
				t.Errorf("the document was read before the words were refused: %v", err)
			}
			// The count is of the words after the command, the one it takes
			// among them: what it got, against what it takes.
			if want := fmt.Sprintf("got %d", len(args)-len(path)); !strings.Contains(err.Error(), want) {
				t.Errorf("error does not say %q: %v", want, err)
			}
		})
	}
}

func TestRun_NoCommandRepeatsAWordItRefuses(t *testing.T) {
	// The word is a token, which any word typed after suco may be. Every
	// command gets one after what it takes, and the commands that take a
	// word get one in that word's place as well.
	token := string(credential.New())
	takes := map[string][]string{
		"credential new":    {"--read-only"},
		"credential revoke": {string(credential.NewID())},
		"asset accept":      {"jpyc", theAddress},
	}
	var cases [][]string
	for _, path := range paths(commands) {
		cases = append(cases, append(append(slices.Clone(path), takes[strings.Join(path, " ")]...), token))
	}
	cases = append(cases,
		[]string{token},
		[]string{"credential", token},
		[]string{"credential", "new", token},
		[]string{"credential", "revoke", token},
		[]string{"asset", token},
		[]string{"asset", "accept", "jpyc", token},
	)
	for _, args := range cases {
		t.Run(strings.Join(append([]string{"suco"}, args[:len(args)-1]...), " ")+" and then a token", func(t *testing.T) {
			t.Setenv("SUCO_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))

			stdout, stderr, err := runArgs(t, args...)

			if err == nil {
				t.Fatal("want an error, got none")
			}
			for name, text := range map[string]string{"error": err.Error(), "stdout": stdout, "stderr": stderr} {
				if strings.Contains(text, token[:16]) {
					t.Errorf("%s repeats the argument: %q", name, text)
				}
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
	t.Parallel()
	var out bytes.Buffer

	usage(&out)

	// A word boundary rather than surrounding spaces: a command at the end of
	// a line would slip past a space-delimited search.
	named := func(name string) bool {
		return regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`).MatchString(out.String())
	}
	for _, path := range paths(commands) {
		if name := strings.Join(path, " "); !named(name) {
			t.Errorf("usage does not name %s:\n%s", name, out.String())
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
	for _, path := range paths(commands) {
		t.Run(strings.Join(path, " "), func(t *testing.T) {
			t.Setenv("SUCO_CONFIG", filepath.Join(t.TempDir(), "suco.yaml"))
			_, _, err := runArgs(t, append(path, "an-argument-no-command-takes")...)

			if err == nil {
				t.Fatal("want an error, got none")
			}
			if errors.Is(err, errUnknown) {
				t.Errorf("%s is named in the usage text but not dispatched: %v", strings.Join(path, " "), err)
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
	if key := keyFile(t, dir); !strings.Contains(stdout, key) {
		t.Errorf("stdout does not name the key file it wrote: %q", stdout)
	}
	if !strings.Contains(stdout, "suco serve") {
		t.Errorf("stdout does not name the next step: %q", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing", stderr)
	}
}

func TestRun_InitRefusesToOverwriteADocument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "suco.yaml")
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
	if keys := keyFiles(t, dir); len(keys) != 0 {
		t.Errorf("init refused and wrote a key all the same: %q", keys)
	}
}

// TestRun_InitLeavesNoKeyWhenTheDocumentIsNotWritten covers the failure
// between the two files: the key is written first, so the document never
// names a key that is not there, and a document that then fails takes the
// key with it, or the next init would find a key it did not write.
//
// A dangling symbolic link is what makes the document fail here: it is not
// there for the check, and it is there for a create that refuses to follow
// links.
func TestRun_InitLeavesNoKeyWhenTheDocumentIsNotWritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "suco.yaml")
	if err := os.Symlink(filepath.Join(dir, "nowhere"), path); err != nil {
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
	if keys := keyFiles(t, dir); len(keys) != 0 {
		t.Errorf("init failed and left a key behind: %q", keys)
	}
}

// TestRun_InitWritesTheKeyToAFileOnlyItsOwnerCanRead is what the environment
// reads the key from, so it holds the key alone, as ParseKey reads one and
// with no newline after it, and nobody but its owner reads it.
func TestRun_InitWritesTheKeyToAFileOnlyItsOwnerCanRead(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SUCO_CONFIG", filepath.Join(dir, "suco.yaml"))
	if _, _, err := runArgs(t, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}

	name := keyFile(t, dir)

	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("mode = %04o, want nothing for group or other", mode)
	}
	contents, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := credential.ParseKey(string(contents)); err != nil {
		t.Errorf("the file holds %q, which ParseKey refuses: %v", contents, err)
	}
}

func TestRun_InitTwiceWritesTwoKeys(t *testing.T) {
	var keys []string
	for range 2 {
		dir := t.TempDir()
		t.Setenv("SUCO_CONFIG", filepath.Join(dir, "suco.yaml"))
		if _, _, err := runArgs(t, "init"); err != nil {
			t.Fatalf("init: %v", err)
		}
		key, err := os.ReadFile(keyFile(t, dir))
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, string(key))
	}

	if keys[0] == keys[1] {
		t.Error("the two runs wrote the same key")
	}
}

// TestRun_InitKeepsTheKeyOutOfTheDocument is the split init makes: the
// document is the file an operator reads and commits, so it names the key
// file's identifier and the variable the key comes from, and holds no key.
func TestRun_InitKeepsTheKeyOutOfTheDocument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "suco.yaml")
	t.Setenv("SUCO_CONFIG", path)
	if _, _, err := runArgs(t, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	name := keyFile(t, dir)
	key, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	document, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Contains(document, key) {
		t.Error("the document holds the key")
	}
	t.Setenv(config.KeyVar, string(key))
	resolved, err := config.Load(path, os.LookupEnv)
	if err != nil {
		t.Fatalf("the document does not resolve with the key set: %v", err)
	}
	if want := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(name), "credentials-"), ".key"); resolved.Config.Credentials.KeyID != want {
		t.Errorf("key_id = %q, want %q, the identifier in the key file's name", resolved.Config.Credentials.KeyID, want)
	}
	if got := resolved.Sources["credentials.key"]; got.Var != config.KeyVar {
		t.Errorf("credentials.key comes from %+v, want the variable %s", got, config.KeyVar)
	}
}

func TestRun_ServeRefusesWhatInitWroteUntilTheKeyIsSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suco.yaml")
	t.Setenv("SUCO_CONFIG", path)
	if _, _, err := runArgs(t, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	onFreePort(t, path)

	_, _, err := runArgs(t, "serve")

	if err == nil {
		t.Fatal("serve started without the key")
	}
	if !strings.Contains(err.Error(), config.KeyVar) {
		t.Errorf("error does not name the variable to set: %v", err)
	}
}

// TestRun_ServeAcceptsWhatInitWrote runs the line init prints, in a shell as
// an operator would, and starts serve with what it set. The directory has a
// space and a quote in its name, so that the line is one the shell reads
// whole.
func TestRun_ServeAcceptsWhatInitWrote(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "it's here")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "suco.yaml")
	t.Setenv("SUCO_CONFIG", path)
	stdout, _, err := runArgs(t, "init")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Setenv(config.KeyVar, exported(t, stdout, config.KeyVar))
	// init writes the port it defaults to, which something else on this
	// machine may hold. That says nothing about whether the document is
	// acceptable, so the port is moved and the rest of it is what is tested.
	onFreePort(t, path)

	// serve binds and then blocks, so the run is cut short by a context that
	// is already cancelled.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out, errOut bytes.Buffer

	err = run(ctx, []string{"serve"}, &out, &errOut)

	// The cancelled context is the only thing that should have stopped it.
	// Matching on the wording of a refusal stops matching the first time that
	// wording changes.
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("serve did not accept the document init wrote: %v", err)
	}
}

// TestRun_InitPrintsALineThatReadsAPathBeginningWithADash is the one path a
// quoted word does not make safe for cat, which reads it as an option. A
// document named relative to the working directory is what puts a dash
// first.
func TestRun_InitPrintsALineThatReadsAPathBeginningWithADash(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir("-here", 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUCO_CONFIG", filepath.Join("-here", "suco.yaml"))
	stdout, _, err := runArgs(t, "init")
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	key := exported(t, stdout, config.KeyVar)

	if _, err := credential.ParseKey(key); err != nil {
		t.Errorf("the line init printed did not set %s to the key: %v", config.KeyVar, err)
	}
}

// exported runs the one line of stdout that begins with export through sh,
// and returns what the shell then holds under name.
func exported(t *testing.T, stdout, name string) string {
	t.Helper()
	var line string
	for l := range strings.Lines(stdout) {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "export ") {
			if line != "" {
				t.Fatalf("stdout has more than one line to run:\n%s", stdout)
			}
			line = l
		}
	}
	if line == "" {
		t.Fatalf("stdout has no line to run:\n%s", stdout)
	}
	out, err := exec.Command("sh", "-c", line+"; printf %s \"$"+name+"\"").Output()
	if err != nil {
		t.Fatalf("sh refused %q: %v", line, err)
	}
	return string(out)
}

// keyFile is the one key file init wrote in dir.
func keyFile(t *testing.T, dir string) string {
	t.Helper()
	keys := keyFiles(t, dir)
	if len(keys) != 1 {
		t.Fatalf("want one key file in %s, got %q", dir, keys)
	}
	return keys[0]
}

// keyFiles lists the key files in dir, by the name init gives one.
func keyFiles(t *testing.T, dir string) []string {
	t.Helper()
	keys, err := filepath.Glob(filepath.Join(dir, "credentials-*.key"))
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

// onFreePort rewrites the port in a document to one nothing is listening on.
func onFreePort(t *testing.T, path string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replaced := regexp.MustCompile(`(?m)^  port: \d+$`).
		ReplaceAll(body, fmt.Appendf(nil, "  port: %d", freePort(t)))
	if bytes.Equal(replaced, body) {
		t.Fatalf("no port to replace in the document:\n%s", body)
	}
	if err := os.WriteFile(path, replaced, 0o600); err != nil {
		t.Fatal(err)
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
		says []string
	}{
		{"a database of its own", "database:\n  managed: true\n", []string{"database.managed"}},
		{"a network no asset refers to", "networks:\n  local:\n    kind: simulated\n", []string{"networks.local"}},
		{
			"both",
			"database:\n  managed: true\nnetworks:\n  local:\n    kind: simulated\n",
			[]string{"database.managed", "networks.local"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			document(t, c.doc)

			_, _, err := runArgs(t, "serve")

			if err == nil {
				t.Fatal("want an error, got none")
			}
			for _, says := range c.says {
				if !strings.Contains(err.Error(), says) {
					t.Errorf("error does not name %s: %v", says, err)
				}
			}
		})
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
	// Every setting the resolver reads. A report that stopped naming one would
	// leave an operator certain of a value nothing had shown them.
	for _, want := range []struct{ path, value, source string }{
		{"database.managed", "true", "default"},
		{"database.url", "not set", "default"},
		{"listen.base_url", "http://localhost:9000", "default"},
		{"listen.host", "127.0.0.1", "default"},
		{"listen.port", "9000", "file"},
		{"log.level", "info", "default"},
		{"log.format", "text", "default"},
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
		// This document names a database nothing is listening for, so the
		// command fails. That is covered elsewhere; the report is written
		// before it, and the report is what this case is about.
		refused bool
	}{
		{
			name:     "supplied by a variable",
			document: namingADatabase(),
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
			// Nothing listens on port 1, so the connection fails at once
			// without a name to look up.
			t.Setenv("SUCO_DATABASE_URL", "postgres://admin:"+password+"@127.0.0.1:1/sucopay")

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
	if !strings.Contains(err.Error(), "networks.local") {
		t.Errorf("error does not name the network: %v", err)
	}
	if !strings.Contains(err.Error(), "Remove it") {
		t.Errorf("error does not say what to do about it: %v", err)
	}
	if !strings.Contains(err.Error(), filepath.Base(path)) {
		t.Errorf("error does not name the document: %v", err)
	}
	if !strings.Contains(stdout, "listen.port") {
		t.Errorf("the report was withheld, leaving nothing to diagnose:\n%s", stdout)
	}
}

// anAssetOn is a document listing one asset on a simulated network of the
// name given, with more written into the networks section after it: a
// setting of that network at its indent, or another network at the section's.
func anAssetOn(network, more string) string {
	return "networks:\n  " + network + ":\n    kind: simulated\n" + more + anAsset(network)
}

// anAsset is the assets section listing one asset on the network named.
func anAsset(network string) string {
	return "assets:\n  jpyc:\n    network: " + network + "\n" +
		"    reference: \"0x0000000000000000000000000000000000000001\"\n" +
		"    symbol: JPYC\n    decimals: 18\n"
}

// anEVMNetwork is the networks section declaring one network of the evm kind
// with the settings that kind needs, the rpc on this machine.
func anEVMNetwork(network string) string {
	return "networks:\n  " + network + ":\n    kind: evm\n    chain_id: 1\n    rpc: http://127.0.0.1:1\n"
}

func TestRun_ServeStartsWithANetworkAnAssetRefersTo(t *testing.T) {
	document(t, fmt.Sprintf("listen:\n  port: %d\n%s", freePort(t), anAssetOn("local", "")))

	_, stop := serving(t)
	stop()
}

func TestRun_DoctorShowsTheAssetsOfADocumentItAccepts(t *testing.T) {
	document(t, anAssetOn("local", ""))

	stdout, _, err := runArgs(t, "doctor")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	for _, want := range []struct{ path, value string }{
		{"assets.jpyc.network", "local"},
		{"assets.jpyc.reference", "0x0000000000000000000000000000000000000001"},
		{"assets.jpyc.symbol", "JPYC"},
		{"assets.jpyc.decimals", "18"},
	} {
		line := reportLine(t, stdout, want.path)
		if !strings.Contains(line, want.value) || !strings.Contains(line, "file") {
			t.Errorf("%s reads %q, want the value %s from file", want.path, line, want.value)
		}
	}
}

func TestRun_ServeRefusesTwoNamesForOneAsset(t *testing.T) {
	document(t, anAssetOn("local", "")+
		"  yen:\n    network: local\n    reference: \"0x0000000000000000000000000000000000000001\"\n"+
		"    symbol: YEN\n    decimals: 6\n")

	_, _, err := runArgs(t, "serve")

	if err == nil {
		t.Fatal("serve started over a document that names one asset twice")
	}
	for _, name := range []string{"jpyc", "yen"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
}

// TestRun_ServeRefusesANetworkNothingReads checks that a refusal names the
// networks it is about and no other, in the order of their names, and that
// doctor refuses in the same words: an operator meets the refusal at whichever
// command they run first. A name is a key of the document, so naming it
// repeats nothing that was typed after suco; and a key can hold a newline, so
// one that does is quoted rather than given a line of its own.
func TestRun_ServeRefusesANetworkNothingReads(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		// The paths named, one refusal each, in the order they are written.
		names []string
	}{
		{
			"one no asset refers to",
			"networks:\n  local:\n    kind: simulated\n",
			[]string{"networks.local"},
		},
		{
			"one with an rpc, though an asset refers to it",
			anEVMNetwork("local") + anAsset("local"),
			[]string{"networks.local.rpc"},
		},
		{
			"the one no asset refers to, beside one an asset does",
			anAssetOn("local", "  other:\n    kind: simulated\n"),
			[]string{"networks.other"},
		},
		{
			"one no asset refers to, with an rpc: both are said",
			anEVMNetwork("local"),
			[]string{"networks.local", "networks.local.rpc"},
		},
		{
			"two no asset refers to, in the order of their names",
			"networks:\n  b:\n    kind: simulated\n  a:\n    kind: simulated\n",
			[]string{"networks.a", "networks.b"},
		},
		{
			"one whose name holds a newline",
			"networks:\n  \"a\\nb\":\n    kind: simulated\n",
			[]string{`"networks.a\nb"`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			document(t, c.doc)

			_, _, byServe := runArgs(t, "serve")
			_, _, byDoctor := runArgs(t, "doctor")

			if byServe == nil || byDoctor == nil {
				t.Fatalf("serve: %v; doctor: %v; want both to refuse", byServe, byDoctor)
			}
			if byServe.Error() != byDoctor.Error() {
				t.Errorf("serve refuses with:\n  %v\ndoctor with:\n  %v", byServe, byDoctor)
			}
			// One refusal is written on the document's line; more than one
			// take a line each under it.
			refusals := strings.Split(byServe.Error(), "\n")
			if len(c.names) > 1 {
				refusals = refusals[1:]
			}
			if len(refusals) != len(c.names) {
				t.Fatalf("want %d refusals, got %d:\n%v", len(c.names), len(refusals), byServe)
			}
			for i, name := range c.names {
				if !strings.Contains(refusals[i], name+":") {
					t.Errorf("refusal %d does not name %s: %s", i, name, refusals[i])
				}
			}
		})
	}
}

// readyz reads the probe, with the two nested objects a deployment reading a
// chain answers with.
func readyz(t *testing.T, port int) (int, map[string]any, string) {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/readyz", port)) //nolint:noctx // a test's own request, over a test's own lifetime
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("/readyz answered something other than a JSON object: %v\n%s", err, raw)
	}
	return resp.StatusCode, body, string(raw)
}

// wordFor is what the probe says about one network, or nothing where it says
// nothing about any.
func wordFor(body map[string]any, section, name string) string {
	entries, ok := body[section].(map[string]any)
	if !ok {
		return ""
	}
	word, _ := entries[name].(string)
	return word
}

// The whole path: serve opens the chain the document's asset settles on,
// reads it round after round, and the probe answers with what the rounds have
// come to. Nothing is asked of the chain to answer the probe.
func TestRun_ServeReadsTheNetworkItsAssetSettlesOnAndSaysSo(t *testing.T) {
	port := freePort(t)
	deployed(t)
	// A second apart, which is the shortest a document may set. What a round
	// takes is not what this is about; that a round happens at all is.
	document(t, fmt.Sprintf("listen:\n  port: %d\n%s%s", port, namingADatabase(),
		"networks:\n  local:\n    kind: simulated\n    poll: 1s\n"+anAsset("local")))
	_, stop := serving(t)
	defer stop()

	// The first round takes the position and reads nothing below it, so the
	// word a deployment settles on is the one the round after it says.
	var status int
	var body map[string]any
	for began := time.Now(); time.Since(began) < 3*time.Second; time.Sleep(50 * time.Millisecond) {
		status, body, _ = readyz(t, port)
		if wordFor(body, "networks", "local") == "observing" {
			break
		}
	}

	if word := wordFor(body, "networks", "local"); word != "observing" {
		t.Errorf("the deployment says the network is %q after three seconds, want observing", word)
	}
	if word := wordFor(body, "assets", "jpyc"); word != "unchanged" {
		t.Errorf("the deployment says the asset is %q, want unchanged", word)
	}
	if status != http.StatusOK {
		t.Errorf("/readyz = %d, want %d", status, http.StatusOK)
	}
}

// A lease left behind is a network the next instance waits out the term of
// before it reads anything, so the rounds are waited for on the way out.
func TestRun_ServePutsDownTheLeaseOfEveryNetworkItRead(t *testing.T) {
	port := freePort(t)
	d := deployed(t)
	document(t, fmt.Sprintf("listen:\n  port: %d\n%s%s", port, namingADatabase(),
		"networks:\n  local:\n    kind: simulated\n    poll: 1s\n"+anAsset("local")))
	_, stop := serving(t)

	held := func() int {
		t.Helper()
		var count int
		if err := d.pool.Conns().QueryRow(t.Context(),
			`select count(*) from leases where name = $1`, "local").Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	for began := time.Now(); held() == 0 && time.Since(began) < 3*time.Second; time.Sleep(50 * time.Millisecond) {
	}
	if held() != 1 {
		t.Fatal("the instance read a network without taking the lease on it")
	}

	stop()

	if held() != 0 {
		t.Error("the instance stopped holding the lease on a network nobody is reading")
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

// namingADatabase is a document pointing at the URL in an environment
// variable, carrying the credentials key a document naming a database has to
// carry. The key is written in rather than referenced: what these tests are
// about is the database, and a second variable per test would only be
// something else to forget to set.
func namingADatabase() string {
	return "database:\n  managed: false\n  url: ${SUCO_DATABASE_URL}\n" +
		"credentials:\n  key: " + testKey + "\n  key_id: testkey\n"
}

// testKey is 32 bytes as the setting carries them.
const testKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// freePort returns a port nothing is listening on, so that a test binding one
// does not depend on which ports this machine has free.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// serving runs serve until it announces itself. It returns what serve had
// written by then, and a function that stops it and returns everything it
// wrote, the lines after the announcement included.
//
// What serve writes is its log, so this reads log lines. All of them: serve
// writes to a pipe, and a line nobody takes blocks it where it stands, so a
// test that stopped reading early would hang rather than fail.
func serving(t *testing.T, args ...string) (announced string, stop func() string) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := run(ctx, append([]string{"serve"}, args...), writer, io.Discard)
		writer.CloseWithError(err)
		done <- err
	}()

	var mu sync.Mutex
	var written strings.Builder
	began := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		scanner := bufio.NewScanner(reader)
		announced := false
		for scanner.Scan() {
			mu.Lock()
			written.WriteString(scanner.Text() + "\n")
			mu.Unlock()
			if !announced && (strings.Contains(scanner.Text(), "msg=serving") ||
				strings.Contains(scanner.Text(), `"msg":"serving"`)) {
				announced = true
				close(began)
			}
		}
	}()

	read := func() string {
		mu.Lock()
		defer mu.Unlock()
		return written.String()
	}

	select {
	case <-began:
		return read(), func() string {
			cancel()
			<-done
			<-finished
			return read()
		}
	case <-finished:
		cancel()
		t.Fatalf("serve stopped before it began serving: %v\n%s", <-done, read())
		return "", func() string { return "" }
	}
}

func TestRun_ServeOpensTheDatabaseBeforeItServes(t *testing.T) {
	const name = "suco-serve-test"
	document(t, fmt.Sprintf("listen:\n  port: %d\n%s", freePort(t), namingADatabase()))
	t.Setenv("SUCO_DATABASE_URL", postgrestest.WithName(t, postgrestest.Fresh(t), name))

	announced, stop := serving(t)
	open := postgrestest.Backends(t, name)
	stop()

	if !strings.Contains(announced, "msg=serving") {
		t.Errorf("serve wrote %q, want it to say it is serving", announced)
	}
	if open == 0 {
		t.Error("serve was serving with no connection to the database its document named")
	}
}

func TestRun_ServeSaysWhenItStops(t *testing.T) {
	// A restart that leaves nothing behind is one an operator cannot tell
	// from a crash.
	document(t, fmt.Sprintf("listen:\n  port: %d\n", freePort(t)))

	_, stop := serving(t)

	if written := stop(); !strings.Contains(written, "msg=stopped") {
		t.Errorf("serve stopped without saying so:\n%s", written)
	}
}

func TestRun_ServeAppliesTheSchemaToAnEmptyDatabase(t *testing.T) {
	dsn := postgrestest.Fresh(t)
	document(t, fmt.Sprintf("listen:\n  port: %d\n%s", freePort(t), namingADatabase()))
	t.Setenv("SUCO_DATABASE_URL", dsn)

	announced, stop := serving(t)
	defer stop()

	pool, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	schema, err := pool.SchemaVersion(t.Context())
	if err != nil {
		t.Fatalf("reading the schema version: %v", err)
	}

	if schema == "" {
		t.Errorf("serve started against an empty database and left it empty:\n%s", announced)
	}
	if !strings.Contains(announced, "schema applied") {
		t.Errorf("serve did not say what it applied:\n%s", announced)
	}
}

func TestRun_ServeStopsWhenTheDatabaseIsUnreachable(t *testing.T) {
	const password = "hunter2"
	document(t, fmt.Sprintf("listen:\n  port: %d\n%s", freePort(t), namingADatabase()))
	t.Setenv("SUCO_DATABASE_URL", "postgres://admin:"+password+"@127.0.0.1:1/sucopay")

	stdout, _, err := runArgs(t, "serve")

	if err == nil {
		t.Fatal("want an error, got none: a server that cannot reach its database has nowhere to keep state")
	}
	// The refusal for an unimplemented section names database.managed, so
	// matching "database" alone would pass for the wrong reason.
	if !strings.HasPrefix(err.Error(), "database: ") {
		t.Errorf("error does not say the database could not be reached: %v", err)
	}
	if strings.Contains(err.Error(), password) {
		t.Errorf("the error carries the password: %v", err)
	}
	// The database is opened before the line announcing the server, so that
	// an operator is never told it is serving by a process about to stop.
	if strings.Contains(stdout, "msg=serving") {
		t.Errorf("serve said it was serving and then stopped:\n%s", stdout)
	}
}

func TestRun_DoctorNamesTheDatabaseItReached(t *testing.T) {
	dsn := postgrestest.Fresh(t)
	document(t, namingADatabase())
	t.Setenv("SUCO_DATABASE_URL", dsn)

	// What the server itself answers, so that a report naming a version nobody
	// asked it for does not pass for having reached one.
	pool, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	version, err := pool.ServerVersion(t.Context())
	pool.Close()
	if err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runArgs(t, "doctor")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if !strings.Contains(stdout, "PostgreSQL "+version) {
		t.Errorf("the report does not name the version the server gave (%s):\n%s", version, stdout)
	}
}

func TestRun_DoctorFailsWhenTheDatabaseIsUnreachableAndStillPrintsTheReport(t *testing.T) {
	document(t, namingADatabase())
	t.Setenv("SUCO_DATABASE_URL", "postgres://admin:hunter2@127.0.0.1:1/sucopay")

	stdout, _, err := runArgs(t, "doctor")

	if err == nil {
		t.Fatal("want an error, got none")
	}
	if !strings.Contains(stdout, "unreachable") {
		t.Errorf("the report does not say the database was out of reach:\n%s", stdout)
	}
	if !strings.Contains(stdout, "listen.port") {
		t.Errorf("the report was withheld, leaving nothing to diagnose:\n%s", stdout)
	}
}

// silentListener accepts connections and never speaks, standing in for a
// database that is reachable but does not answer.
func silentListener(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { conn.Close() })
		}
	}()
	return l.Addr().String()
}

func TestRun_DoctorWritesTheReportBeforeItAsksTheDatabaseAnything(t *testing.T) {
	document(t, namingADatabase())
	t.Setenv("SUCO_DATABASE_URL", "postgres://u:p@"+silentListener(t)+"/db")

	reader, writer := io.Pipe()
	go func() {
		err := run(t.Context(), []string{"doctor"}, writer, io.Discard)
		writer.CloseWithError(err)
	}()

	settings := make(chan string, 1)
	go func() {
		line, err := bufio.NewReader(reader).ReadString('\n')
		if err == nil {
			settings <- line
		}
		close(settings)
	}()

	// Far less than the wait for a database that never answers, so that
	// holding the report until it does is a failure rather than a slow pass.
	select {
	case line, ok := <-settings:
		if !ok {
			t.Fatal("doctor wrote nothing before asking the database")
		}
		if !strings.Contains(line, "suco.yaml") {
			t.Errorf("the first thing written was %q, want the report", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("doctor held the report until the database answered")
	}
	reader.Close()
}

func TestRun_DoctorNamesTheSchemaTheDatabaseHolds(t *testing.T) {
	dsn := postgrestest.Fresh(t)
	document(t, namingADatabase())
	t.Setenv("SUCO_DATABASE_URL", dsn)

	pool, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	want, err := pool.SchemaVersion(t.Context())
	pool.Close()
	if err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runArgs(t, "doctor")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if !strings.Contains(stdout, "schema "+want) {
		t.Errorf("the report does not name the schema the database holds (%s):\n%s", want, stdout)
	}
}

func TestRun_DoctorSaysWhenTheSchemaHasNotBeenApplied(t *testing.T) {
	// A database an instance can reach but has put nothing in is the state an
	// operator most needs told apart from a working one.
	document(t, namingADatabase())
	t.Setenv("SUCO_DATABASE_URL", postgrestest.Fresh(t))

	stdout, _, err := runArgs(t, "doctor")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if !strings.Contains(stdout, "not applied") {
		t.Errorf("the report does not say the schema is missing:\n%s", stdout)
	}
	if strings.Contains(stdout, "credentials:") {
		t.Errorf("the report speaks of credentials in a database with no table of them:\n%s", stdout)
	}
}
