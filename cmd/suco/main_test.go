package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
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

func TestUsage_NamesOnlyTheCommandsThatExist(t *testing.T) {
	var out bytes.Buffer

	usage(&out)

	for _, absent := range []string{"init", "dev", "listen", "migrate", "doctor", "upgrade"} {
		if strings.Contains(out.String(), " "+absent+" ") {
			t.Errorf("usage names %q, which is not implemented", absent)
		}
	}
}
