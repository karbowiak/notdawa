// Package db provides the Postgres+PostGIS connection pool and migration runner.
package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a pgx pool against the given DSN.
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
		if _, err := pool.Exec(ctx, string(sqlBytes)); err != nil {
			return applied, fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO schema_migrations (filename) VALUES ($1) ON CONFLICT DO NOTHING`, name); err != nil {
			return applied, fmt.Errorf("record %s: %w", name, err)
		}
		applied = append(applied, name)
	}
	return applied, nil
}
