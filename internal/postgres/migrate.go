package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var schema embed.FS

// migrationLock is the advisory lock a migration run holds. Two instances
// starting at once would otherwise both find the same migration unapplied and
// both apply it. The number is arbitrary and only has to be one nothing else
// in this database uses.
const migrationLock = 0x5c0_9a7

// lockTimeout bounds the wait for that lock. Another instance applying a long
// migration is a legitimate reason to wait, and a session holding the key and
// never letting go is not; without a bound the two look the same and a start
// hangs with nothing to read.
const lockTimeout = 2 * time.Minute

// ErrNoMigrations reports a directory holding no migration. A build with no
// schema would otherwise report success and leave an empty database behind it.
var ErrNoMigrations = errors.New("no migrations to apply")

// ErrMigrationChanged reports a migration that was edited after it was
// applied. What ran against a database and what the file says now have parted,
// and nothing can tell which of them the database holds.
var ErrMigrationChanged = errors.New("an applied migration has changed")

// Migrate brings the database up to the schema this build carries, and reports
// how many migrations it applied.
//
// It is safe to run at every start: what has been applied is recorded, and an
// advisory lock means two instances starting together do not both apply the
// same migration.
func (p *Pool) Migrate(ctx context.Context) (int, error) {
	return p.migrate(ctx, schema, "migrations")
}

// SchemaVersion reports the last migration applied, or "" for a database with
// none.
func (p *Pool) SchemaVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var version string
	err := p.pool.QueryRow(ctx,
		`select coalesce(max(version), '') from schema_migrations`).Scan(&version)
	if err != nil {
		// A database nothing has migrated has no table to read.
		if isUndefinedTable(err) {
			return "", nil
		}
		return "", fmt.Errorf("database: %w", err)
	}
	return version, nil
}

func (p *Pool) migrate(ctx context.Context, fsys fs.FS, dir string) (applied int, err error) {
	files, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return 0, fmt.Errorf("migrations: %w", err)
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".sql") {
			names = append(names, f.Name())
		}
	}
	if len(names) == 0 {
		return 0, fmt.Errorf("migrations: %w from %s", ErrNoMigrations, dir)
	}
	slices.Sort(names)

	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("database: %w", err)
	}
	// Reassigned below when the connection has to be ended rather than
	// returned. Deferred functions run in reverse, so the unlock below runs
	// first and this sees whichever way it decided.
	release := conn.Release
	defer func() { release() }()

	taking, stopTaking := context.WithTimeout(ctx, lockTimeout)
	defer stopTaking()
	if _, err := conn.Exec(taking, `select pg_advisory_lock($1)`, migrationLock); err != nil {
		return 0, fmt.Errorf("migrations: taking the lock: %w", err)
	}
	defer func() {
		// Detached from the caller's cancellation, which may be what ended
		// the migration, but bounded: every other statement here is.
		unlock, stop := context.WithTimeout(context.WithoutCancel(ctx), queryTimeout)
		defer stop()
		if _, uerr := conn.Exec(unlock, `select pg_advisory_unlock($1)`, migrationLock); uerr != nil {
			// The lock belongs to the session, not to the statement. A
			// connection going back to the pool still holding it would block
			// every later run for as long as the pool kept it, so the session
			// is ended instead of being handed on.
			hijacked := conn.Hijack()
			release = func() {
				if cerr := hijacked.Close(unlock); cerr != nil && err == nil {
					err = fmt.Errorf("migrations: ending the locked session: %w", cerr)
				}
			}
			if err == nil {
				err = fmt.Errorf("migrations: releasing the lock: %w", uerr)
			}
		}
	}()

	if _, err := conn.Exec(ctx, `
		create table if not exists schema_migrations (
			version    text        primary key,
			checksum   text        not null,
			applied_at timestamptz not null default now()
		)`); err != nil {
		return 0, fmt.Errorf("migrations: %w", err)
	}

	recorded, err := appliedChecksums(ctx, conn.Conn())
	if err != nil {
		return 0, err
	}
	if err := refuseASchemaFromTheFuture(recorded, names); err != nil {
		return 0, err
	}

	for _, name := range names {
		body, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return applied, fmt.Errorf("migrations: %w", err)
		}
		version := versionOf(name)
		sum := sha256.Sum256(body)
		checksum := hex.EncodeToString(sum[:])

		if was, ok := recorded[version]; ok {
			if was != checksum {
				return applied, fmt.Errorf("migrations: %w: %s", ErrMigrationChanged, name)
			}
			continue
		}
		if err := apply(ctx, conn.Conn(), version, checksum, string(body)); err != nil {
			return applied, fmt.Errorf("migrations: %s: %w", name, err)
		}
		applied++
	}
	return applied, nil
}

// refuseASchemaFromTheFuture stops a build starting against a database a later
// one has already changed.
//
// Rolling a deployment back is an ordinary thing to do when something is
// wrong, and it puts the previous binary in front of whatever schema the newer
// one left. A migration that dropped a column or added a constraint would then
// surface as a failed payment rather than as a refused start.
func refuseASchemaFromTheFuture(recorded map[string]string, names []string) error {
	known := make(map[string]bool, len(names))
	for _, name := range names {
		known[versionOf(name)] = true
	}
	var ahead []string
	for version := range recorded {
		if !known[version] {
			ahead = append(ahead, version)
		}
	}
	if len(ahead) == 0 {
		return nil
	}
	slices.Sort(ahead)
	return fmt.Errorf("migrations: the database has %s applied, which this build does not carry. "+
		"It was migrated by a later build", strings.Join(ahead, ", "))
}

// apply runs one migration and records it in the same transaction, so that a
// migration that fails half way leaves neither the change nor the record of
// it.
func apply(ctx context.Context, conn *pgx.Conn, version, checksum, body string) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	// A rollback after the commit below reports that the transaction is
	// closed, and one after a failure has nothing to add to the failure that
	// is already on its way back.
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // see above

	if _, err := tx.Exec(ctx, body); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`insert into schema_migrations (version, checksum) values ($1, $2)`,
		version, checksum); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func appliedChecksums(ctx context.Context, conn *pgx.Conn) (map[string]string, error) {
	rows, err := conn.Query(ctx, `select version, checksum from schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}
	defer rows.Close()

	applied := map[string]string{}
	for rows.Next() {
		var version, checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, fmt.Errorf("migrations: %w", err)
		}
		applied[version] = checksum
	}
	return applied, rows.Err()
}

// versionOf is what comes before the first underscore, so that 0001_name.sql
// is recorded as 0001 and the file can be renamed without reapplying it.
func versionOf(name string) string {
	if i := strings.Index(name, "_"); i > 0 {
		return name[:i]
	}
	return strings.TrimSuffix(name, ".sql")
}

func isUndefinedTable(err error) bool {
	var pg interface{ SQLState() string }
	return errors.As(err, &pg) && pg.SQLState() == "42P01"
}
