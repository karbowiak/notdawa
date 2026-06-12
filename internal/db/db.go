// Package db provides the Postgres+PostGIS connection pool and migration runner.
package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a pgx pool against the given DSN. Used by the import/migrate
// lanes, which legitimately run multi-minute statements — no statement timeout.
func Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

// serveStatementTimeout bounds every statement the API server runs. A request
// that would exceed it gets a loud 500 instead of pinning a pool connection
// (and the DB) indefinitely — degenerate geometry input, pathological LIKE
// patterns, full-table dumps. Normal serving queries run in milliseconds; the
// heaviest legitimate paged queries stay well under this.
const serveStatementTimeout = "60s"

// serveDefaultMaxConns bounds the serving pool when the DSN doesn't set
// pool_max_conns explicitly. pgxpool's default is max(4, NumCPU) — and in a
// container without a CPU limit NumCPU is the NODE's core count, which lets two
// replicas exhaust postgres max_connections under load.
const serveDefaultMaxConns = 16

// ConnectServe opens the API-serving pool: like Connect, but with a server-side
// statement_timeout on every connection and a bounded pool size.
func ConnectServe(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	if _, set := cfg.ConnConfig.RuntimeParams["statement_timeout"]; !set {
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = serveStatementTimeout
	}
	// ParseConfig leaves MaxConns at pgxpool's default unless the DSN carries
	// pool_max_conns; detect that by re-checking the raw DSN.
	if !strings.Contains(dsn, "pool_max_conns") {
		cfg.MaxConns = serveDefaultMaxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

// Migrate applies every *.sql file in dir, in lexical order, that has not been
// applied before, recording each in a schema_migrations ledger. Files are still
// expected to be idempotent (IF NOT EXISTS), but tracking means a re-run is a
// cheap set of catalog reads rather than re-issuing every CREATE/ALTER — which
// matters when migrate runs (e.g. as a deploy hook) while a bulk import holds
// table locks: skipped migrations take no locks, so the run can't block on the
// import. Returns the list of files applied THIS run (empty when up to date).
func Migrate(ctx context.Context, pool *pgxpool.Pool, dir string) ([]string, error) {
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		filename   text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return nil, fmt.Errorf("ensure schema_migrations: %w", err)
	}

	done := map[string]bool{}
	rows, err := pool.Query(ctx, `SELECT filename FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan schema_migrations: %w", err)
		}
		done[f] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".sql" {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	var applied []string
	for _, name := range files {
		if done[name] {
			continue
		}
		sqlBytes, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return applied, fmt.Errorf("read %s: %w", name, err)
		}
		// Apply + record atomically, serialised by a tx-scoped advisory lock:
		// the post-deploy migrate hook and a provision Job's built-in Migrate
		// can run concurrently, and IF-NOT-EXISTS DDL races error spuriously.
		// The lock releases at commit/rollback; a crash between apply and
		// record can no longer happen (single tx).
		tx, err := pool.Begin(ctx)
		if err != nil {
			return applied, err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrateLockKey); err != nil {
			tx.Rollback(ctx)
			return applied, fmt.Errorf("lock for %s: %w", name, err)
		}
		// Another runner may have applied this file while we waited.
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE filename = $1)`, name).Scan(&exists); err != nil {
			tx.Rollback(ctx)
			return applied, fmt.Errorf("recheck %s: %w", name, err)
		}
		if exists {
			tx.Rollback(ctx)
			continue
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			tx.Rollback(ctx)
			return applied, fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (filename) VALUES ($1)`, name); err != nil {
			tx.Rollback(ctx)
			return applied, fmt.Errorf("record %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return applied, fmt.Errorf("commit %s: %w", name, err)
		}
		applied = append(applied, name)
	}
	return applied, nil
}

// migrateLockKey is the advisory-lock key serialising migration runners
// (distinct from the ingest import lock).
const migrateLockKey = 0x6e74_6477_6d69_6772 // "ntdwmigr"
