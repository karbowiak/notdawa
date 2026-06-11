package ingest

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The data_provisions ledger records, per import-plan step key, the highest
// dataVersion that has ever been loaded successfully. `notdawa provision`
// diffs the plan against it and runs only what is missing or stale, so a
// deploy that adds a new entity (or bumps a step's dataVersion after a schema
// change) populates exactly that data — and a fresh database provisions the
// whole plan, which is the bootstrap path.
//
// Like schema_migrations, the DDL lives here rather than in migrations/*.sql:
// every import run stamps the ledger, so the table must exist even when only
// `import` (not `provision`) has ever run against the database.

// importLockKey is the advisory-lock key serialising import runs. Loads are
// TRUNCATE+reload per entity, so two concurrent runs (e.g. the weekly refresh
// CronJob and a deploy's provision Job) must not interleave.
const importLockKey = int64(0x6e6f74646177) // "notdaw"

// EnsureProvisionLedger creates the data_provisions table if missing.
func EnsureProvisionLedger(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS data_provisions (
		key            text PRIMARY KEY,
		version        int NOT NULL,
		rows_loaded    bigint,
		provisioned_at timestamptz NOT NULL DEFAULT now()
	)`)
	if err != nil {
		return fmt.Errorf("ensure data_provisions: %w", err)
	}
	return nil
}

// ProvisionedVersions returns the ledger as key → highest loaded version.
func ProvisionedVersions(ctx context.Context, pool *pgxpool.Pool) (map[string]int, error) {
	rows, err := pool.Query(ctx, `SELECT key, version FROM data_provisions`)
	if err != nil {
		return nil, fmt.Errorf("read data_provisions: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var v int
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("scan data_provisions: %w", err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read data_provisions: %w", err)
	}
	return out, nil
}

// StampProvision records a successful load of a plan step at the given
// version. The version only ever moves forward. Like finishRun it uses a
// detached context and logs — rather than returns — failures: the data is
// already durable, and a bookkeeping miss only means the step re-runs.
func StampProvision(ctx context.Context, pool *pgxpool.Pool, key string, version, rows int) {
	if _, err := pool.Exec(context.WithoutCancel(ctx), `
		INSERT INTO data_provisions (key, version, rows_loaded)
		VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE
			SET version = GREATEST(data_provisions.version, EXCLUDED.version),
			    rows_loaded = EXCLUDED.rows_loaded, provisioned_at = now()`,
		key, version, rows); err != nil {
		fmt.Fprintf(os.Stderr, "notdawa: could not stamp provision %s v%d: %v\n", key, version, err)
	}
}

// AcquireImportLock takes a session advisory lock serialising import runs,
// holding a dedicated connection until the returned release func is called.
// If another run holds the lock it announces the wait and blocks.
func AcquireImportLock(ctx context.Context, pool *pgxpool.Pool) (func(), error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire lock connection: %w", err)
	}

	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, importLockKey).Scan(&got); err != nil {
		conn.Release()
		return nil, fmt.Errorf("try import lock: %w", err)
	}
	if !got {
		fmt.Fprintln(os.Stderr, "notdawa: another import run holds the lock — waiting for it to finish…")
		if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, importLockKey); err != nil {
			conn.Release()
			return nil, fmt.Errorf("wait for import lock: %w", err)
		}
	}

	release := func() {
		// Unlock with a detached context so the lock is freed even when the
		// run was cancelled; releasing the connection would drop the session
		// lock anyway, but doing it explicitly keeps the pair symmetric.
		if _, err := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, importLockKey); err != nil {
			fmt.Fprintf(os.Stderr, "notdawa: could not release import lock: %v\n", err)
		}
		conn.Release()
	}
	return release, nil
}
