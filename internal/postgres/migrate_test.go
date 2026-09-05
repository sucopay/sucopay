package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
)

func migrated(t *testing.T) *postgres.Pool {
	t.Helper()
	pool, err := postgres.Open(t.Context(), postgrestest.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return pool
}

// tables asks the server what it holds, over a connection of its own.
func tables(t *testing.T, dsn string) []string {
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

func TestMigrate_LeavesTablesAnOperatorCanRead(t *testing.T) {
	t.Parallel()
	// What a deployment is promised is that its state is in its own database.
	// A schema an operator can list is the whole of that promise.
	dsn := postgrestest.Fresh(t)
	pool, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	applied, err := pool.Migrate(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if applied == 0 {
		t.Error("applied nothing to an empty database")
	}
	got := tables(t, dsn)
	for _, want := range []string{"accepted_assets", "accounts", "credentials", "payments", "schema_migrations"} {
		if !contains(got, want) {
			t.Errorf("the database has no %s table, only %v", want, got)
		}
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestMigrate_AppliesNothingTwice(t *testing.T) {
	t.Parallel()
	pool := migrated(t)

	applied, err := pool.Migrate(t.Context())

	if err != nil {
		t.Fatalf("a second run failed: %v", err)
	}
	if applied != 0 {
		t.Errorf("applied %d migrations to a database already holding them", applied)
	}
}

func TestSchemaVersion_ReportsTheLastMigrationApplied(t *testing.T) {
	t.Parallel()
	dsn := postgrestest.Fresh(t)
	before, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer before.Close()

	// A database nothing has migrated has no table to read, which is a
	// version of none rather than a failure.
	empty, err := before.SchemaVersion(t.Context())
	if err != nil {
		t.Fatalf("reading the version of an unmigrated database: %v", err)
	}
	if empty != "" {
		t.Errorf("version = %q, want none", empty)
	}

	if _, err := before.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := before.SchemaVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after == "" {
		t.Fatal("version is none after migrating")
	}

	// Two recorded, so that the last one is a different answer from the first.
	record(t, dsn, "0000", "one a later build would not have")
	last, err := before.SchemaVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if last != after {
		t.Errorf("version = %q, want the last applied, %q", last, after)
	}
}

func TestMigrate_TwoInstancesStartingTogetherApplyEachMigrationOnce(t *testing.T) {
	t.Parallel()
	// A deployment runs this at every start, so two replicas coming up
	// together is the ordinary case rather than the unlucky one.
	dsn := postgrestest.Fresh(t)

	const instances = 4
	applied := make(chan int, instances)
	failed := make(chan error, instances)
	start := make(chan struct{})

	for range instances {
		go func() {
			pool, err := postgres.Open(t.Context(), dsn)
			if err != nil {
				failed <- err
				return
			}
			defer pool.Close()

			<-start
			n, err := pool.Migrate(t.Context())
			if err != nil {
				failed <- err
				return
			}
			applied <- n
		}()
	}
	close(start)

	total := 0
	for range instances {
		select {
		case err := <-failed:
			t.Fatalf("an instance could not migrate: %v", err)
		case n := <-applied:
			total += n
		}
	}

	// Counted rather than written down, so that adding a migration does not
	// turn this into a number somebody bumps without reading what it claims.
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := conn.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("closing: %v", err)
		}
	}()
	var recorded int
	if err := conn.QueryRow(t.Context(), `select count(*) from schema_migrations`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}

	if total != recorded {
		t.Errorf("%d instances applied %d migrations between them, but %d are recorded: "+
			"one was applied more than once", instances, total, recorded)
	}
}

func TestMigrate_RefusesADatabaseALaterBuildHasChanged(t *testing.T) {
	t.Parallel()
	// Rolling a deployment back puts the previous binary in front of whatever
	// schema the newer one left. A migration that dropped a column would then
	// surface as a failed payment rather than as a refused start.
	dsn := postgrestest.Fresh(t)
	pool, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if _, err := pool.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	record(t, dsn, "9999", "whatever a later build applied")

	_, err = pool.Migrate(t.Context())

	if err == nil {
		t.Fatal("started against a schema this build does not carry")
	}
	if !strings.Contains(err.Error(), "9999") {
		t.Errorf("error does not name what it does not know: %v", err)
	}
}

// record writes a migration into the ledger without applying anything, which
// is what a later build leaves behind.
func record(t *testing.T, dsn, version, checksum string) {
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

	if _, err := conn.Exec(t.Context(),
		`insert into schema_migrations (version, checksum) values ($1, $2)`,
		version, checksum); err != nil {
		t.Fatal(err)
	}
}
