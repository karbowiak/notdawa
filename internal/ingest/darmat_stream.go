package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
)

// synthV3File builds a FileDownload descriptor from a local extract path for
// offline ingest (NOTDAWA_INGEST_FILE), parameterised by register. It mirrors
// synthMATFile but is reusable for DAR. Both DAR and MAT national json Current
// TotalDownloads share the V1/V3/V4-same-generation ambiguity, so offline ingest
// must not depend on the (paginated, version-inconsistent) listing — it trusts
// the local file, parsing the generation from the trailing "_NNN" of the name.
func synthV3File(path, register, entity string) datafordeler.FileDownload {
	f := synthMATFile(path, entity)
	f.Register = register
	return f
}

// resolveV3 picks the national json Current TotalDownload for an entity,
// preferring the V3 schema (the one carrying the relational *LokalId fields)
// but falling back to the highest-generation any-version national total when V3
// is absent — the DAR Husnummer/Adresse/Adressepunkt extracts have the same
// V1/V3/V4-same-generation ambiguity as MAT (see matResolveV3).
func resolveV3(files []datafordeler.FileDownload, entity string) (datafordeler.FileDownload, bool) {
	if f, ok := pickLatestTotalVersion(files, entity, "json", "Current", "_V3_"); ok {
		return f, true
	}
	return pickLatestTotalVersion(files, entity, "json", "Current", "")
}

// acquireV3 is matAcquireV3 generalised to any register (DAR/MAT): V3-pinned
// resolution with the NOTDAWA_INGEST_FILE offline bypass. The caller owns the
// returned temp path (must os.Remove it) and finishes the run.
func acquireV3(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client, register, entity string) (datafordeler.FileDownload, string, int64, error) {
	var file datafordeler.FileDownload
	if override := os.Getenv("NOTDAWA_INGEST_FILE"); override != "" {
		// Offline ingest: trust the local file, skip the network listing.
		file = synthV3File(override, register, entity)
	} else {
		files, err := client.ListAvailable(ctx, register)
		if err != nil {
			return datafordeler.FileDownload{}, "", 0, err
		}
		var ok bool
		file, ok = resolveV3(files, entity)
		if !ok {
			return datafordeler.FileDownload{}, "", 0, fmt.Errorf("no Current json TotalDownload for %s/%s", register, entity)
		}
	}
	runID, err := startRun(ctx, pool, register, entity, file)
	if err != nil {
		return file, "", 0, err
	}
	path, err := acquireExtract(ctx, client, file)
	if err != nil {
		failRun(ctx, pool, runID, err)
		return file, "", runID, err
	}
	markDownloaded(ctx, pool, runID)
	return file, path, runID, nil
}

// matStreamLoadGeneric is matStreamLoad generalised to any register (DAR/MAT):
// V3-pinned acquire + offline bypass, then stream-decode the top-level JSON
// array element-by-element and insert kept rows in pgx.Batch chunks within ONE
// TRUNCATE+insert transaction. table/insertSQL are code-controlled constants.
func matStreamLoadGeneric[F any](ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client,
	register, entity, table, insertSQL string, keep func(F) bool, args func(F, int) []any) (Result, error) {

	res := Result{Register: register, Entity: entity}
	file, path, runID, err := acquireV3(ctx, pool, client, register, entity)
	if err != nil {
		return res, err
	}
	return streamZipRows(ctx, pool, res, file, path, runID, table, insertSQL, keep, args)
}

// streamZipRows is the shared post-acquire streaming body: decode the top-level
// JSON array of the zip at path element-by-element and insert kept rows in
// pgx.Batch chunks within ONE TRUNCATE+insert transaction. It owns (removes)
// the temp file and finishes the ledger run. Split out so loaders with a
// different acquire step (e.g. the Bitemporal historik totals) reuse it.
func streamZipRows[F any](ctx context.Context, pool *pgxpool.Pool, res Result,
	file datafordeler.FileDownload, path string, runID int64,
	table, insertSQL string, keep func(F) bool, args func(F, int) []any) (Result, error) {

	defer os.Remove(path)
	res.GenerationNumber = file.GenerationNumber

	zr, rc, err := openZipMember(path)
	if err != nil {
		failRun(ctx, pool, runID, err)
		return res, err
	}
	defer zr.Close()
	defer rc.Close()

	dec := json.NewDecoder(rc)
	if _, err = dec.Token(); err != nil {
		failRun(ctx, pool, runID, err)
		return res, fmt.Errorf("read array start in %s: %w", file.FileName, err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		failRun(ctx, pool, runID, err)
		return res, err
	}
	defer tx.Rollback(ctx)

	if _, err = tx.Exec(ctx, "TRUNCATE "+table); err != nil {
		failRun(ctx, pool, runID, err)
		return res, fmt.Errorf("truncate %s: %w", table, err)
	}

	batch := &pgx.Batch{}
	n := 0
	for dec.More() {
		var f F
		if err = dec.Decode(&f); err != nil {
			failRun(ctx, pool, runID, err)
			return res, fmt.Errorf("decode feature in %s: %w", file.FileName, err)
		}
		if keep != nil && !keep(f) {
			continue
		}
		batch.Queue(insertSQL, args(f, file.GenerationNumber)...)
		n++
		if batch.Len() >= streamChunk {
			if err = drainBatch(ctx, tx, batch); err != nil {
				failRun(ctx, pool, runID, err)
				return res, fmt.Errorf("insert into %s: %w", table, err)
			}
			batch = &pgx.Batch{}
		}
	}
	if batch.Len() > 0 {
		if err = drainBatch(ctx, tx, batch); err != nil {
			failRun(ctx, pool, runID, err)
			return res, fmt.Errorf("insert into %s: %w", table, err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		failRun(ctx, pool, runID, err)
		return res, err
	}
	if err = finishRun(ctx, pool, runID, n); err != nil {
		fmt.Fprintf(os.Stderr, "notdawa: ingest committed but ledger update failed for run %d: %v\n", runID, err)
	}
	res.RowsLoaded = n
	return res, nil
}
