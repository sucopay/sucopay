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

// indexState is what the server holds under an index name: nothing, an index
// that is valid, or one that is not.
func indexState(t *testing.T, dsn, name string) string {
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

	var valid bool
	err = conn.QueryRow(t.Context(),
		`select indisvalid from pg_index where indexrelid = to_regclass($1)`, name).Scan(&valid)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "absent"
	case err != nil:
		t.Fatal(err)
	case valid:
		return "valid"
	}
	return "invalid"
}

func TestMigrate_RunsAMarkedMigrationOutsideATransaction(t *testing.T) {
	t.Parallel()
	// CREATE INDEX CONCURRENTLY refuses to run inside a transaction, which is
	// where every other migration runs.
	dsn := postgrestest.Fresh(t)
	pool, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	files := fstest.MapFS{
		"m/0001_table.sql": {Data: []byte(`create table t (x text)`)},
		"m/0002_index.sql": {Data: []byte(outsideMarker + `
-- What the index is for.
create index concurrently t_by_x on t (x);
`)},
	}

	applied, err := pool.migrate(t.Context(), files, "m")

	if err != nil {
		t.Fatal(err)
	}
	if applied != 2 {
		t.Errorf("applied %d, want both", applied)
	}
	if got := indexState(t, dsn, "t_by_x"); got != "valid" {
		t.Errorf("the index is %s, want valid", got)
	}
	if again, err := pool.migrate(t.Context(), files, "m"); err != nil || again != 0 {
		t.Errorf("a second run applied %d, %v; want nothing, recorded already", again, err)
	}
}

func TestMigrate_ReplacesTheIndexAFailedRunLeftBehind(t *testing.T) {
	t.Parallel()
	// A CREATE INDEX CONCURRENTLY that fails leaves an index of its name that
	// is not valid, which "if not exists" would take for the index and skip.
	dsn := postgrestest.Fresh(t)
	before, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := before.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	}()
	for _, statement := range []string{
		`create table t (x text)`,
		`insert into t values ('a'), ('a')`,
	} {
		if _, err := before.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := before.Exec(t.Context(),
		`create unique index concurrently t_by_x on t (x)`); err == nil {
		t.Fatal("a unique index over two equal rows was made")
	}
	if got := indexState(t, dsn, "t_by_x"); got != "invalid" {
		t.Fatalf("the failed index is %s, want invalid", got)
	}
	if _, err := before.Exec(t.Context(), `delete from t`); err != nil {
		t.Fatal(err)
	}

	pool, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	files := fstest.MapFS{
		"m/0001_index.sql": {Data: []byte(outsideMarker + `
create unique index concurrently t_by_x on t (x);
`)},
	}

	applied, err := pool.migrate(t.Context(), files, "m")

	if err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Errorf("applied %d, want the one", applied)
	}
	if got := indexState(t, dsn, "t_by_x"); got != "valid" {
		t.Errorf("the index is %s, want valid", got)
	}
}

func TestMigrate_RefusesAMarkedMigrationThatIsNotOneConcurrentIndex(t *testing.T) {
	t.Parallel()
	// Outside a transaction, a migration that fails half way leaves what it
	// did behind. The marker admits the one statement that has to run there,
	// in the one shape that cannot hold a second.
	cases := map[string]string{
		"a table":        `create table made (id text primary key)`,
		"two statements": `create index concurrently i on t (x); create table made (id text primary key)`,
		"if not exists":  `create index concurrently if not exists i on t (x)`,
		"a quoted name":  `create index concurrently "i" on t (x)`,
		"a dollar quote": `create index concurrently i on t (x) where x = $$;$$`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dsn := postgrestest.Fresh(t)
			pool, err := Open(t.Context(), dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			files := fstest.MapFS{
				"m/0001_table.sql":  {Data: []byte(`create table t (x text)`)},
				"m/0002_marked.sql": {Data: []byte(outsideMarker + "\n" + body)},
			}

			applied, err := pool.migrate(t.Context(), files, "m")

			if err == nil {
				t.Fatal("want an error, got none")
			}
			if applied != 1 {
				t.Errorf("applied %d, want the one before it", applied)
			}
			if got := tablesIn(t, dsn); slices.Contains(got, "made") {
				t.Errorf("the refused migration ran: %v", got)
			}
			if got := indexState(t, dsn, "i"); got != "absent" {
				t.Errorf("the refused migration made an index that is %s", got)
			}
		})
	}
}

// A start that finds the lock held asks for it again rather than waiting on
// it. A session waiting in pg_advisory_lock is a transaction holding a
// snapshot, and a CREATE INDEX CONCURRENTLY the holder runs waits for every
// such transaction to end: the two wait on each other, and the server ends one
// of them. The holder here stands for another instance applying a marked
// migration, which is the case the marker exists for.
func TestMigrate_WaitsForTheLockWithoutHoldingUpTheHoldersIndex(t *testing.T) {
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
	if _, err := holding.Exec(t.Context(), `create table t (x text)`); err != nil {
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

	done := make(chan error, 1)
	go func() {
		_, err := pool.migrate(t.Context(), fstest.MapFS{
			"m/0001_x.sql": {Data: []byte(`create table x (id text primary key)`)},
		}, "m")
		done <- err
	}()
	// Until the other instance has asked for the lock once: it is then idle
	// with the ask as its last statement, or is waiting on the lock.
	asked := false
	for range 200 {
		var sessions int
		if err := holding.QueryRow(t.Context(), `
			select count(*) from pg_stat_activity
			 where datname = current_database() and pid <> pg_backend_pid()
			   and (wait_event_type = 'Lock' or (state = 'idle' and query like '%advisory_lock%'))`,
		).Scan(&sessions); err != nil {
			t.Fatal(err)
		}
		if sessions > 0 {
			asked = true
			break
		}
		select {
		case err := <-done:
			t.Fatalf("the waiting instance stopped before asking for the lock: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !asked {
		t.Fatal("the waiting instance did not ask for the lock")
	}

	_, err = holding.Exec(t.Context(), `create index concurrently t_by_x on t (x)`)

	if err != nil {
		t.Errorf("the holder's concurrent index failed while another instance waited: %v", err)
	}
	if _, err := holding.Exec(t.Context(), `select pg_advisory_unlock($1)`, migrationLock); err != nil {
		t.Error(err)
	}
	if err := <-done; err != nil {
		t.Errorf("the waiting instance did not migrate once the lock was let go: %v", err)
	}
}

// A migration marked to run outside a transaction is one concurrent index,
// or every start on this build fails at it. Read from the files this build
// carries, so that the one that is wrong is named before any database is
// reached.
func TestMigrations_MarkedOnesAreEachOneConcurrentIndex(t *testing.T) {
	t.Parallel()
	entries, err := fs.ReadDir(schema, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		body, err := fs.ReadFile(schema, "migrations/"+entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if !marked(string(body)) {
			continue
		}
		if _, err := indexOf(string(body)); err != nil {
			t.Errorf("%s: %v", entry.Name(), err)
		}
	}
}
