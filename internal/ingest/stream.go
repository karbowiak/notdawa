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

// streamChunk is how many rows to buffer in a pgx.Batch before flushing.
const streamChunk = 5000

// streamLoad is the streaming analogue of dagiLoad for extracts too large to
// decode whole (DAR/MAT, ~90 MB–4.5 GB JSON). It decodes the top-level JSON
// array element-by-element and inserts kept rows in pgx.Batch chunks within ONE
// TRUNCATE+insert transaction, so memory stays bounded regardless of file size.
// Reuses the ingest_runs ledger. table/insertSQL are code-controlled constants.
func streamLoad[F any](ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client,
	register, entity, table, insertSQL string,
	keep func(*F) bool, args func(*F, int) []any) (Result, error) {

	res := Result{Register: register, Entity: entity}
	file, path, runID, err := downloadEntity(ctx, pool, client, register, entity)
	if err != nil {
		return res, err
	}
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
	if _, err = dec.Token(); err != nil { // consume the opening '['
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
	for dec.More() {
		var f F
		if err = dec.Decode(&f); err != nil {
			failRun(ctx, pool, runID, err)
			return res, fmt.Errorf("decode element in %s: %w", file.FileName, err)
		}
		if keep != nil && !keep(&f) {
			continue
		}
		batch.Queue(insertSQL, args(&f, file.GenerationNumber)...)
		res.RowsLoaded++
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
	if res.RowsLoaded == 0 {
		err = fmt.Errorf("no matching features in %s", file.FileName)
		failRun(ctx, pool, runID, err)
		return res, err
	}
	if err = tx.Commit(ctx); err != nil {
		failRun(ctx, pool, runID, err)
		return res, err
	}
	if err = finishRun(ctx, pool, runID, res.RowsLoaded); err != nil {
		fmt.Fprintf(os.Stderr, "notdawa: ingest committed but ledger update failed for run %d: %v\n", runID, err)
	}
	return res, nil
}

// drainBatch sends every queued statement and consumes all results, returning
// the first error encountered.
func drainBatch(ctx context.Context, tx pgx.Tx, batch *pgx.Batch) error {
	br := tx.SendBatch(ctx, batch)
	n := batch.Len()
	for i := 0; i < n; i++ {
		if _, err := br.Exec(); err != nil {
			br.Close()
			return err
		}
	}
	return br.Close()
}
