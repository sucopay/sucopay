package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
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

// lockPoll is how often a start that finds the lock held asks for it again.
// Asked for rather than waited on: a session waiting in pg_advisory_lock is a
// transaction holding a snapshot, and a CREATE INDEX CONCURRENTLY the holder
// is running waits for every such transaction to end, which this one would
// not do until the holder was done. Each ask is a statement that returns at
// once, and between asks the session holds nothing.
const lockPoll = time.Second

// outsideMarker is the first line of a migration that runs outside a
// transaction, which is what CREATE INDEX CONCURRENTLY has to do.
const outsideMarker = "-- suco: index concurrently"

// concurrentIndex is the one shape a marked migration has: one index, made
// concurrently, under a name plain enough to go back into a statement. The
// character class stops at anything that could end the statement or hide a
// second one. Comment lines are taken out before it is matched.
var concurrentIndex = regexp.MustCompile(
	`^create (?:unique )?index concurrently ([a-z_][a-z0-9_]*) on [^;$]+;?$`)

// ErrNoMigrations reports a directory holding no migration. A build with no
// schema would otherwise report success and leave an empty database behind it.
var ErrNoMigrations = errors.New("no migrations to apply")

// ErrMigrationChanged reports a migration that was edited after it was
// applied. What ran against a database and what the file says now have parted,
// and nothing can tell which of them the database holds.
var ErrMigrationChanged = errors.New("an applied migration has changed")

// holder names the session holding the migration lock, as the tail of a
// sentence, and is empty when nothing can be said.
//
// Waiting for the lock is what an operator sees when a start hangs, and the
// wait alone does not say whether another instance is applying a long
// migration or a session took the key and never let go. The pid is what turns
// the second into something to act on: it is what `pg_terminate_backend` and
// the server's own log are addressed by.
//
// Asked over a connection of its own: the one that just timed out may have
// been cancelled mid-statement. Nothing here fails the caller: a lock that
// cannot be attributed is reported as a lock that could not be taken, which
// is what the caller already knows.
func (p *Pool) holder(ctx context.Context) string {
	ask, stop := context.WithTimeout(context.WithoutCancel(ctx), queryTimeout)
	defer stop()

	// pg_advisory_lock takes one 64-bit key and the catalogue splits it in
	// two, with a subid of 1 marking it as the one-argument form.
	//
	// Filtered to this database, because an advisory lock is held in one and
	// the catalogue shows every database's. Two deployments in one cluster use
	// the same key, and naming the wrong one's session would send an operator
	// to terminate a backend doing nothing wrong.
	var pid int32
	if err := p.pool.QueryRow(ask, `
		select pid from pg_locks
		 where locktype = 'advisory' and granted
		   and database = (select oid from pg_database where datname = current_database())
		   and classid = $1::oid and objid = $2::oid and objsubid = 1
		 limit 1`,
		int64(uint32(migrationLock>>32)), int64(uint32(migrationLock))).Scan(&pid); err != nil {
		return ""
	}
	return fmt.Sprintf(", which is held by pid %d", pid)
}

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
	if err := takeLock(taking, conn.Conn()); err != nil {
		return 0, fmt.Errorf("migrations: taking the lock%s: %w", p.holder(ctx), err)
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
		if err := run(ctx, conn.Conn(), version, checksum, string(body)); err != nil {
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

// takeLock takes the migration lock, asking again every lockPoll until ctx
// ends.
func takeLock(ctx context.Context, conn *pgx.Conn) error {
	again := time.NewTicker(lockPoll)
	defer again.Stop()
	for {
		var taken bool
		if err := conn.QueryRow(ctx, `select pg_try_advisory_lock($1)`, migrationLock).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-again.C:
		}
	}
}

// marked reports whether a migration's first line says it runs outside a
// transaction.
func marked(body string) bool {
	first, _, _ := strings.Cut(body, "\n")
	return strings.TrimSpace(first) == outsideMarker
}

// indexOf is the name of the index a marked migration makes, or the reason
// the migration is not one the runner runs outside a transaction.
func indexOf(body string) (string, error) {
	var kept []string
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			kept = append(kept, line)
		}
	}
	found := concurrentIndex.FindStringSubmatch(strings.TrimSpace(strings.Join(kept, "\n")))
	if found == nil {
		return "", errors.New("marked to run outside a transaction, and not one " +
			"\"create [unique] index concurrently <name> on ...\" in lower case")
	}
	return found[1], nil
}

// run applies one migration the way its first line says.
func run(ctx context.Context, conn *pgx.Conn, version, checksum, body string) error {
	if marked(body) {
		return applyOutside(ctx, conn, version, checksum, body)
	}
	return apply(ctx, conn, version, checksum, body)
}

// applyOutside runs a marked migration with no transaction around it, and
// records it once the index it made is there and valid.
//
// The index is dropped first. A CREATE INDEX CONCURRENTLY that fails, or is
// cut short, leaves an index of its name behind that is not valid: it
// enforces nothing and no plan reads it. "if not exists" would take that for
// the index and skip, and the migration would be recorded over it. A run that
// made the index and then failed to record it leaves a valid one, which is
// dropped and made again rather than trusted to be what the migration says.
//
// Each statement is sent on its own. Sent together they would run in one
// transaction, which is what the marker is here to avoid.
func applyOutside(ctx context.Context, conn *pgx.Conn, version, checksum, body string) error {
	index, err := indexOf(body)
	if err != nil {
		return err
	}
	// The name goes into a statement that cannot take a parameter. It is
	// matched above to letters, digits and underscores, and comes from a
	// migration this build carries.
	if _, err := conn.Exec(ctx, "drop index concurrently if exists "+index); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, body); err != nil {
		return err
	}
	var valid bool
	if err := conn.QueryRow(ctx,
		`select indisvalid from pg_index where indexrelid = to_regclass($1)`, index).Scan(&valid); err != nil {
		return fmt.Errorf("reading whether %s is valid: %w", index, err)
	}
	if !valid {
		return fmt.Errorf("made %s, which is not a valid index", index)
	}
	_, err = conn.Exec(ctx,
		`insert into schema_migrations (version, checksum) values ($1, $2)`, version, checksum)
	return err
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
