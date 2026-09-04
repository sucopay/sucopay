package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/credential"
	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
)

// deployment is what credential new runs against: a database with the schema
// applied, as serve leaves one, named by the document, and an empty directory
// that is the working directory from here on. The store is the one serve
// would read the credentials with.
type deployment struct {
	dir   string
	pool  *postgres.Pool
	store *credential.Postgres
}

func deployed(t *testing.T) deployment {
	t.Helper()
	dsn := postgrestest.Fresh(t)
	pool, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	document(t, namingADatabase())
	t.Setenv("SUCO_DATABASE_URL", dsn)
	dir := t.TempDir()
	t.Chdir(dir)

	key, err := credential.ParseKey(testKey)
	if err != nil {
		t.Fatal(err)
	}
	return deployment{dir, pool, credential.NewPostgres(pool.Conns(), key, "testkey")}
}

// fileNamed reads which file new says it wrote.
func fileNamed(t *testing.T, stdout string) string {
	t.Helper()
	line, _, _ := strings.Cut(stdout, "\n")
	name, ok := strings.CutPrefix(line, "Wrote ")
	if !ok {
		t.Fatalf("stdout does not start by naming the file it wrote:\n%s", stdout)
	}
	return name
}

func TestRun_CredentialNewRefusesToChooseACapability(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"neither", nil},
		{"both", []string{"--read-only", "--read-write"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			// No document, so that the refusal is seen to come before
			// anything is read: a database refusal in its place would be
			// the wrong answer to the wrong question.
			t.Setenv("SUCO_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))

			_, _, err := runArgs(t, append([]string{"credential", "new"}, c.args...)...)

			if err == nil {
				t.Fatal("want an error, got none")
			}
			for _, flag := range []string{"--read-only", "--read-write"} {
				if !strings.Contains(err.Error(), flag) {
					t.Errorf("error does not name %s: %v", flag, err)
				}
			}
		})
	}
}

// TestRun_CredentialNewRefusesWhatServeRefuses checks the words, and not only
// that both fail: an operator meets the refusal at whichever command they
// run first, and two sentences for one problem read as two problems.
func TestRun_CredentialNewRefusesWhatServeRefuses(t *testing.T) {
	for _, c := range []struct {
		name string
		// What new reads, and what serve reads, at the same path. serve
		// serves /healthz with no database, so its refusal has to be asked
		// for by name.
		forNew, forServe string
	}{
		{"a deployment with no database", "listen:\n  port: 9000\n", "database:\n  managed: true\n"},
		{"a section nothing acts on", "networks:\n  local:\n    kind: simulated\n", "networks:\n  local:\n    kind: simulated\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := document(t, c.forNew)

			_, _, byNew := runArgs(t, "credential", "new", "--read-only")

			if err := os.WriteFile(path, []byte(c.forServe), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, byServe := runArgs(t, "serve")
			if byNew == nil || byServe == nil {
				t.Fatalf("new: %v; serve: %v; want both to refuse", byNew, byServe)
			}
			if byNew.Error() != byServe.Error() {
				t.Errorf("new refuses with:\n  %v\nserve with:\n  %v", byNew, byServe)
			}
		})
	}
}

func TestRun_CredentialNewTwiceMakesTwoCredentialsTheStoreFinds(t *testing.T) {
	d := deployed(t)

	var tokens []credential.Token
	for _, flag := range []string{"--read-only", "--read-write"} {
		stdout, _, err := runArgs(t, "credential", "new", flag)
		if err != nil {
			t.Fatalf("new %s: %v", flag, err)
		}
		// As a client reads it: the whole file, and nothing trimmed.
		token, err := os.ReadFile(filepath.Join(d.dir, fileNamed(t, stdout)))
		if err != nil {
			t.Fatalf("new %s named a file it did not write: %v", flag, err)
		}
		if strings.Contains(stdout, string(token)) {
			t.Errorf("stdout carries the token:\n%s", stdout)
		}
		tokens = append(tokens, credential.Token(token))
	}

	for i, want := range []credential.Capability{credential.ReadOnly, credential.ReadWrite} {
		got, err := d.store.FindByToken(t.Context(), tokens[i])
		if err != nil {
			t.Errorf("the store does not find credential %d: %v", i+1, err)
			continue
		}
		if got.Capability != want || got.Scope != credential.ScopeAccount {
			t.Errorf("credential %d is %s of %s, want %s of one account", i+1, got.Capability, got.Scope, want)
		}
	}
	if tokens[0] == tokens[1] {
		t.Error("the two runs wrote the same token")
	}
}

func TestRun_CredentialNewNamesTheFileByTheCredentialsID(t *testing.T) {
	// The ID is what list shows and revoke takes, so a file named by it is
	// one an operator can match to a row without opening it.
	d := deployed(t)

	stdout, _, err := runArgs(t, "credential", "new", "--read-only")

	if err != nil {
		t.Fatalf("new: %v", err)
	}
	name := fileNamed(t, stdout)
	token, err := os.ReadFile(filepath.Join(d.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	found, err := d.store.FindByToken(t.Context(), credential.Token(token))
	if err != nil {
		t.Fatal(err)
	}
	if want := "credential-" + string(found.ID) + ".token"; name != want {
		t.Errorf("wrote %s, want %s", name, want)
	}
}

func TestRun_CredentialNewWritesATokenOnlyItsOwnerCanRead(t *testing.T) {
	d := deployed(t)

	stdout, _, err := runArgs(t, "credential", "new", "--read-write")

	if err != nil {
		t.Fatalf("new: %v", err)
	}
	info, err := os.Stat(filepath.Join(d.dir, fileNamed(t, stdout)))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("mode = %04o, want nothing for group or other", mode)
	}
}

func TestRun_CredentialNewOnADatabaseWithoutTheSchemaNamesWhatAppliesIt(t *testing.T) {
	document(t, namingADatabase())
	t.Setenv("SUCO_DATABASE_URL", postgrestest.Fresh(t))
	t.Chdir(t.TempDir())

	_, _, err := runArgs(t, "credential", "new", "--read-only")

	if err == nil {
		t.Fatal("want an error, got none: there is no account to issue to")
	}
	if !strings.Contains(err.Error(), "suco serve") {
		t.Errorf("error does not name the command that applies the schema: %v", err)
	}
}

func TestRun_CredentialNewRefusesToChooseAmongTwoAccounts(t *testing.T) {
	// Nothing makes a second account yet. One made by hand is what a
	// deployment looks like the day something does, and a new that went on
	// issuing to the first would have chosen for the operator.
	d := deployed(t)
	if _, err := d.pool.Conns().Exec(t.Context(),
		`insert into accounts (id, name) values ('00000000-0000-0000-0000-000000000002', 'second')`); err != nil {
		t.Fatal(err)
	}

	_, _, err := runArgs(t, "credential", "new", "--read-only")

	if err == nil {
		t.Fatal("want an error, got none")
	}
	if !strings.Contains(err.Error(), "2 accounts") {
		t.Errorf("error does not say how many accounts there are: %v", err)
	}
	left, err := d.store.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("%d credentials were made, want none", len(left))
	}
}

func TestRun_CredentialNewThatCannotWriteItsFileLeavesNoCredential(t *testing.T) {
	// A row whose token was never written is one nobody can present, and one
	// list would show for as long as nobody revoked it. Under root the
	// directory is writable whatever its mode, and this fails.
	d := deployed(t)
	if err := os.Chmod(d.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(d.dir, 0o700) })

	_, _, err := runArgs(t, "credential", "new", "--read-only")

	if err == nil {
		t.Fatal("want an error, got none: the token went nowhere")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Errorf("error does not carry why the file was not written: %v", err)
	}
	left, err := d.store.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("%d credentials are in force, want none", len(left))
	}
}
