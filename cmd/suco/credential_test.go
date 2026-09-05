package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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

// credentialCommands is every credential command with an argument it takes,
// for the refusals all of them give alike.
func credentialCommands() [][]string {
	return [][]string{
		{"credential", "new", "--read-only"},
		{"credential", "list"},
		{"credential", "revoke", string(credential.NewID())},
	}
}

// TestRun_CredentialCommandsRefuseWhatServeRefuses checks the words, and not
// only that both fail: an operator meets the refusal at whichever command
// they run first, and two sentences for one problem read as two problems.
func TestRun_CredentialCommandsRefuseWhatServeRefuses(t *testing.T) {
	for _, c := range []struct {
		name string
		// What a credential command reads, and what serve reads, at the same
		// path. serve serves /healthz with no database, so its refusal has
		// to be asked for by name.
		forCredential, forServe string
	}{
		{"a deployment with no database", "listen:\n  port: 9000\n", "database:\n  managed: true\n"},
		{"a section nothing acts on", "networks:\n  local:\n    kind: simulated\n", "networks:\n  local:\n    kind: simulated\n"},
	} {
		for _, args := range credentialCommands() {
			t.Run(c.name+" met by "+strings.Join(args[:2], " "), func(t *testing.T) {
				path := document(t, c.forCredential)

				_, _, byCredential := runArgs(t, args...)

				if err := os.WriteFile(path, []byte(c.forServe), 0o600); err != nil {
					t.Fatal(err)
				}
				_, _, byServe := runArgs(t, "serve")
				if byCredential == nil || byServe == nil {
					t.Fatalf("%s: %v; serve: %v; want both to refuse", strings.Join(args, " "), byCredential, byServe)
				}
				if byCredential.Error() != byServe.Error() {
					t.Errorf("%s refuses with:\n  %v\nserve with:\n  %v", strings.Join(args, " "), byCredential, byServe)
				}
			})
		}
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

func TestRun_CredentialCommandsOnADatabaseWithoutTheSchemaNameWhatAppliesIt(t *testing.T) {
	for _, args := range credentialCommands() {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			document(t, namingADatabase())
			t.Setenv("SUCO_DATABASE_URL", postgrestest.Fresh(t))
			t.Chdir(t.TempDir())

			_, _, err := runArgs(t, args...)

			if err == nil {
				t.Fatal("want an error, got none: there is no table to read or write")
			}
			if !strings.Contains(err.Error(), "suco serve") {
				t.Errorf("error does not name the command that applies the schema: %v", err)
			}
		})
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

// newCredential runs new and reads back what it made: the ID from the file's
// name, and the token from the file.
func newCredential(t *testing.T, d deployment, flag string) (credential.ID, credential.Token) {
	t.Helper()
	stdout, _, err := runArgs(t, "credential", "new", flag)
	if err != nil {
		t.Fatalf("new %s: %v", flag, err)
	}
	name := fileNamed(t, stdout)
	token, err := os.ReadFile(filepath.Join(d.dir, name))
	if err != nil {
		t.Fatalf("new %s named a file it did not write: %v", flag, err)
	}
	id := strings.TrimSuffix(strings.TrimPrefix(name, "credential-"), ".token")
	return credential.ID(id), credential.Token(token)
}

// row is one line of what list shows, under its header.
type row struct {
	id, keyID, scope, capability, lastUsed string
}

// listed reads the rows list wrote, in the order it wrote them.
func listed(t *testing.T, stdout string) []row {
	t.Helper()
	header, rest, ok := strings.Cut(stdout, "\n")
	if !ok || strings.Join(strings.Fields(header), " ") != "ID KEY ID SCOPE CAPABILITY LAST USED" {
		t.Fatalf("stdout does not start with the header line:\n%s", stdout)
	}
	var rows []row
	for _, line := range strings.Split(strings.TrimSuffix(rest, "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 5 {
			t.Fatalf("a row has %d columns, want 5: %q", len(f), line)
		}
		rows = append(rows, row{f[0], f[1], f[2], f[3], f[4]})
	}
	return rows
}

// account is the one account new issues to.
func account(t *testing.T, d deployment) credential.AccountID {
	t.Helper()
	accounts, err := d.store.Accounts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 {
		t.Fatalf("%d accounts, want the one the schema makes", len(accounts))
	}
	return accounts[0]
}

func TestRun_CredentialListShowsEachCredentialsIDAndNeverItsToken(t *testing.T) {
	d := deployed(t)
	readOnly, readOnlyToken := newCredential(t, d, "--read-only")
	readWrite, readWriteToken := newCredential(t, d, "--read-write")

	stdout, _, err := runArgs(t, "credential", "list")

	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, token := range []credential.Token{readOnlyToken, readWriteToken} {
		if strings.Contains(stdout, string(token)) {
			t.Errorf("stdout carries a token:\n%s", stdout)
		}
	}
	rows := listed(t, stdout)
	if len(rows) != 2 {
		t.Fatalf("listed %d rows, want 2:\n%s", len(rows), stdout)
	}
	for _, want := range []row{
		{string(readOnly), "testkey", "account", "read", "never"},
		{string(readWrite), "testkey", "account", "write", "never"},
	} {
		if !slices.Contains(rows, want) {
			t.Errorf("no row %+v among:\n%s", want, stdout)
		}
	}
}

func TestRun_CredentialListOrdersByLastUseWithTheNeverUsedLast(t *testing.T) {
	// The order is what tells the credential a client is presenting from
	// the rows a script made and abandoned. Made in this order so that the
	// order made is not the order wanted.
	d := deployed(t)
	oldest, _ := newCredential(t, d, "--read-only")
	usedEarlier, earlierToken := newCredential(t, d, "--read-only")
	usedLater, laterToken := newCredential(t, d, "--read-write")
	newest, _ := newCredential(t, d, "--read-only")
	now := time.Now()
	uses := map[credential.ID]time.Time{
		usedEarlier: now.Add(-2 * time.Hour),
		usedLater:   now.Add(-time.Hour),
	}
	for _, token := range []credential.Token{earlierToken, laterToken} {
		found, err := d.store.FindByToken(t.Context(), token)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.store.RecordUse(t.Context(), found, uses[found.ID]); err != nil {
			t.Fatal(err)
		}
	}

	stdout, _, err := runArgs(t, "credential", "list")

	if err != nil {
		t.Fatalf("list: %v", err)
	}
	rows := listed(t, stdout)
	want := []credential.ID{usedLater, usedEarlier, newest, oldest}
	if len(rows) != len(want) {
		t.Fatalf("listed %d rows, want %d:\n%s", len(rows), len(want), stdout)
	}
	for i, r := range rows {
		if r.id != string(want[i]) {
			t.Errorf("row %d is %s, want %s:\n%s", i+1, r.id, want[i], stdout)
		}
		at, used := uses[credential.ID(r.id)]
		if !used {
			if r.lastUsed != "never" {
				t.Errorf("row %d was never used and says %q", i+1, r.lastUsed)
			}
			continue
		}
		shown, err := time.Parse(time.RFC3339, r.lastUsed)
		if err != nil {
			t.Errorf("row %d does not show its last use as RFC 3339: %v", i+1, err)
			continue
		}
		if !shown.Equal(at.Truncate(time.Second)) {
			t.Errorf("row %d shows its last use as %s, want %s", i+1, r.lastUsed, at.Format(time.RFC3339))
		}
	}
}

func TestRun_CredentialListSaysWhichKeyEachCredentialWasMadeUnder(t *testing.T) {
	// A deployment whose key was swapped has every request failing, and
	// this column is what tells that from one whose credentials were all
	// revoked: the reason shows nowhere else.
	d := deployed(t)
	ours, _ := newCredential(t, d, "--read-only")
	otherKey, err := credential.ParseKey(strings.Repeat("ff", 32))
	if err != nil {
		t.Fatal(err)
	}
	other := credential.NewPostgres(d.pool.Conns(), otherKey, "otherkey")
	theirs, theirToken, err := other.Create(t.Context(), account(t, d), credential.ReadOnly, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runArgs(t, "credential", "list")

	if err != nil {
		t.Fatalf("list: %v", err)
	}
	keyOf := map[credential.ID]string{}
	for _, r := range listed(t, stdout) {
		keyOf[credential.ID(r.id)] = r.keyID
	}
	if keyOf[ours] != "testkey" || keyOf[theirs] != "otherkey" {
		t.Errorf("list shows ours under %q and theirs under %q:\n%s", keyOf[ours], keyOf[theirs], stdout)
	}
	if _, err := d.store.FindByToken(t.Context(), theirToken); !errors.Is(err, credential.ErrNotFound) {
		t.Errorf("the deployment does not refuse a credential made under another key: %v", err)
	}
}

func TestRun_CredentialListQuotesAKeyIdentifierATerminalWouldActOn(t *testing.T) {
	// The identifier is whatever the document that made the row held, and a
	// report lays it out in columns a terminal shows.
	d := deployed(t)
	key, err := credential.ParseKey(testKey)
	if err != nil {
		t.Fatal(err)
	}
	other := credential.NewPostgres(d.pool.Conns(), key, "clear\x1b[2Jscreen")
	if _, _, err := other.Create(t.Context(), account(t, d), credential.ReadOnly, time.Now()); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runArgs(t, "credential", "list")

	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Contains(stdout, "\x1b") {
		t.Errorf("the escape reached stdout:\n%q", stdout)
	}
	if !strings.Contains(stdout, `\x1b`) {
		t.Errorf("the identifier is not shown quoted:\n%s", stdout)
	}
}

func TestRun_CredentialRevokeStopsOneCredentialAndLeavesTheOther(t *testing.T) {
	d := deployed(t)
	revoked, revokedToken := newCredential(t, d, "--read-only")
	kept, keptToken := newCredential(t, d, "--read-write")

	stdout, _, err := runArgs(t, "credential", "revoke", string(revoked))

	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if !strings.Contains(stdout, string(revoked)) {
		t.Errorf("stdout does not say which credential was revoked:\n%s", stdout)
	}
	// The store was opened before the revoke, as a running server's is: no
	// restart comes between a revoke and the request that meets it.
	if _, err := d.store.FindByToken(t.Context(), revokedToken); !errors.Is(err, credential.ErrNotFound) {
		t.Errorf("the revoked credential is still found: %v", err)
	}
	if _, err := d.store.FindByToken(t.Context(), keptToken); err != nil {
		t.Errorf("the other credential is not found: %v", err)
	}
	stdout, _, err = runArgs(t, "credential", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if rows := listed(t, stdout); len(rows) != 1 || rows[0].id != string(kept) {
		t.Errorf("list shows %+v, want the one kept", rows)
	}
}

func TestRun_CredentialRevokeTwiceSucceedsTwice(t *testing.T) {
	// Two people who noticed the same leak revoke the same credential, and
	// the second is not told it failed.
	d := deployed(t)
	id, _ := newCredential(t, d, "--read-only")

	for i := range 2 {
		if _, _, err := runArgs(t, "credential", "revoke", string(id)); err != nil {
			t.Fatalf("revoke %d: %v", i+1, err)
		}
	}
}

func TestRun_CredentialRevokeOfAnIDNoRowHasNamesIt(t *testing.T) {
	deployed(t)
	id := credential.NewID()

	_, _, err := runArgs(t, "credential", "revoke", string(id))

	if !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), string(id)) {
		t.Errorf("error does not name the ID: %v", err)
	}
}

// TestRun_CredentialListWithNothingInForceShowsTheHeaderAlone: an empty
// output would leave whether the command failed or found nothing to whoever
// reads it.
func TestRun_CredentialListWithNothingInForceShowsTheHeaderAlone(t *testing.T) {
	deployed(t)

	stdout, _, err := runArgs(t, "credential", "list")

	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if rows := listed(t, stdout); len(rows) != 0 {
		t.Errorf("listed %d rows, want none:\n%s", len(rows), stdout)
	}
}

func TestRun_CredentialRevokeRefusesAnythingButOneIDWithoutRepeatingIt(t *testing.T) {
	id := credential.NewID()
	token := string(credential.New())
	for _, c := range []struct {
		name string
		args []string
	}{
		{"nothing", nil},
		{"an ID and then a token", []string{string(id), token}},
		{"the token file's name", []string{"credential-" + string(id) + ".token"}},
		{"a token, which is what somebody holding the file and not list would reach for", []string{token}},
	} {
		t.Run(c.name, func(t *testing.T) {
			// No document, so that the refusal is seen to come before
			// anything is read.
			t.Setenv("SUCO_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))

			_, _, err := runArgs(t, append([]string{"credential", "revoke"}, c.args...)...)

			if err == nil {
				t.Fatal("want an error, got none")
			}
			if !strings.Contains(err.Error(), "ID") {
				t.Errorf("error is not about the ID: %v", err)
			}
			if strings.Contains(err.Error(), "absent.yaml") {
				t.Errorf("the document was read before the argument was: %v", err)
			}
			if strings.Contains(err.Error(), token) {
				t.Errorf("error repeats what was typed in the ID's place: %v", err)
			}
		})
	}
}
