package postgres

// The runner is driven here with filesystems that are not this build's,
// including ones that fail. Exporting an entry point for that would leave a
// way to run arbitrary SQL against the pool in the package's public surface,
// for the sake of a test; the package already tests requireVerifiedTLS the
// same way.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
)

// tablesIn asks the server what it holds, over a connection of its own.
func tablesIn(t *testing.T, dsn string) []string {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := conn.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("closing: %v", err)
		}
	}()

	rows, err := conn.Query(t.Context(),
		`select tablename from pg_tables where schemaname = 'public' order by tablename`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return names
}

func TestMigrate_RefusesAMigrationThatChangedAfterItWasApplied(t *testing.T) {
	t.Parallel()
	// What ran against the database and what the file says have parted, and
	// nothing can tell which of them the database holds.
	pool, err := Open(t.Context(), postgrestest.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	before := fstest.MapFS{
		"m/0001_first.sql": {Data: []byte(`create table one (id text primary key)`)},
	}
	if _, err := pool.migrate(t.Context(), before, "m"); err != nil {
		t.Fatal(err)
	}

	after := fstest.MapFS{
		"m/0001_first.sql": {Data: []byte(`create table one (id text primary key, extra text)`)},
	}
	_, err = pool.migrate(t.Context(), after, "m")

	if !errors.Is(err, ErrMigrationChanged) {
		t.Errorf("err = %v, want ErrMigrationChanged", err)
	}
}

// unordered hands back its entries in reverse, which fs.FS is allowed to do
// and neither embed.FS nor fstest.MapFS does. Without it nothing here would
// notice the runner losing its own sort.
type unordered struct{ fstest.MapFS }

func (u unordered) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := u.MapFS.ReadDir(name)
	slices.Reverse(entries)
	return entries, err
}

func TestMigrate_AppliesInOrderAndStopsAtTheFirstFailure(t *testing.T) {
	t.Parallel()
	pool, err := Open(t.Context(), postgrestest.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	files := unordered{fstest.MapFS{
		"m/0002_second.sql": {Data: []byte(`create table second (id text primary key)`)},
		"m/0001_first.sql":  {Data: []byte(`create table first (id text primary key)`)},
		"m/0003_broken.sql": {Data: []byte(`this is not sql`)},
		"m/0004_never.sql":  {Data: []byte(`create table never (id text primary key)`)},
	}}

	applied, err := pool.migrate(t.Context(), files, "m")

	if err == nil {
		t.Fatal("want an error, got none")
	}
	if !strings.Contains(err.Error(), "0003") {
		t.Errorf("error does not name the migration that failed: %v", err)
	}
	if applied != 2 {
		t.Errorf("applied %d, want the two before the failure", applied)
	}
}

func TestMigrate_LeavesNothingBehindWhenAMigrationFailsHalfWay(t *testing.T) {
	t.Parallel()
	// The change and the record of it are written together, so a migration
	// that fails after its first statement leaves neither.
	dsn := postgrestest.Fresh(t)
	pool, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	files := fstest.MapFS{
		"m/0001_half.sql": {Data: []byte(
			`create table made (id text primary key); this is not sql`)},
	}

	if _, err := pool.migrate(t.Context(), files, "m"); err == nil {
		t.Fatal("want an error, got none")
	}

	if got := tablesIn(t, dsn); slices.Contains(got, "made") {
		t.Errorf("the table from the failed migration is still there: %v", got)
	}
}

func TestMigrate_RefusesADirectoryHoldingNoMigration(t *testing.T) {
	t.Parallel()
	// A build with no schema would otherwise report success and leave an
	// empty database behind it. A directory with something else in it is the
	// same thing: what is missing is a migration, not a file.
	pool, err := Open(t.Context(), postgrestest.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	_, err = pool.migrate(t.Context(), fstest.MapFS{"m/readme.txt": {Data: []byte("x")}}, "m")

	if !errors.Is(err, ErrNoMigrations) {
		t.Errorf("err = %v, want ErrNoMigrations", err)
	}
}

// A start that hangs on the lock is the one an operator has to act on, and
// the wait alone does not say whether another instance is applying a long
// migration or a session took the key and never let go. The pid is what
// separates them, and what terminating the session is addressed by.
func TestMigrate_NamesTheSessionHoldingTheLock(t *testing.T) {
	t.Parallel()
	dsn := postgrestest.Fresh(t)
	holding, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := holding.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	}()

	var pid int32
	if err := holding.QueryRow(t.Context(), `select pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := holding.Exec(t.Context(), `select pg_advisory_lock($1)`, migrationLock); err != nil {
		t.Fatal(err)
	}

	pool, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// The caller's deadline, not lockTimeout: what is under test is what the
	// wait says when it ends, not how long the wait is.
	waiting, stop := context.WithTimeout(t.Context(), 2*time.Second)
	defer stop()

	_, err = pool.Migrate(waiting)

	if err == nil {
		t.Fatal("the migration took a lock another session holds")
	}
	if got := err.Error(); !strings.Contains(got, fmt.Sprintf("pid %d", pid)) {
		t.Errorf("err = %q, want it to name pid %d", got, pid)
	}
}
