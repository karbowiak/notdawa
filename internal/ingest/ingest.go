// Package ingest pulls register extracts from Datafordeler Fildownload and
// loads them into Postgres+PostGIS, transforming the raw grunddata shapes into
// the columns the DAWA-compatible serving layer needs. It is a synchronous
// errgroup-free pipeline for now (download -> unzip -> decode -> upsert); the
// ingest_runs ledger gives idempotency and resume. River/sharding arrive only
// when MAT's ~99 municipality splits and the weekly refresh need them.
package ingest

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
)

// geomExpr wraps a WKT bind placeholder into the standard DAGI geometry load:
// parse as EPSG:25832, drop Z, repair, keep polygons only, force MultiPolygon.
func geomExpr(placeholder string) string {
	return "ST_Multi(ST_CollectionExtract(ST_MakeValid(ST_Force2D(ST_GeomFromText(" + placeholder + ", 25832))), 3))"
}

// geomExprMixed parses a WKT placeholder keeping ALL geometry types (polygon,
// line, point) — for registers like DS where one table mixes geometry kinds.
// Unlike geomExpr it does NOT CollectionExtract polygons or force MultiPolygon.
func geomExprMixed(placeholder string) string {
	return "ST_MakeValid(ST_Force2D(ST_GeomFromText(" + placeholder + ", 25832)))"
}

// dagiLoad is the shared DAGI ingest: download+decode the entity into []F, then
// in one transaction TRUNCATE table and batch-insert every feature kept by
// keep(), mapped to insertSQL's positional args by args(feature, generation).
// table/insertSQL are code-controlled constants (never user input).
func dagiLoad[F any](ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client,
	register, entity, table, insertSQL string,
	keep func(F) bool, args func(F, int) []any) (Result, error) {

	res := Result{Register: register, Entity: entity}
	var feats []F
	file, runID, err := downloadAndDecode(ctx, pool, client, register, entity, &feats)
	if err != nil {
		return res, err
	}
	res.GenerationNumber = file.GenerationNumber

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
	for _, f := range feats {
		if keep != nil && !keep(f) {
			continue
		}
		batch.Queue(insertSQL, args(f, file.GenerationNumber)...)
		res.RowsLoaded++
	}
	if res.RowsLoaded == 0 {
		err = fmt.Errorf("no matching features in %s", file.FileName)
		failRun(ctx, pool, runID, err)
		return res, err
	}

	br := tx.SendBatch(ctx, batch)
	for i := 0; i < res.RowsLoaded; i++ {
		if _, err = br.Exec(); err != nil {
			br.Close()
			failRun(ctx, pool, runID, err)
			return res, fmt.Errorf("insert into %s: %w", table, err)
		}
	}
	if err = br.Close(); err != nil {
		failRun(ctx, pool, runID, err)
		return res, err
	}
	if err = tx.Commit(ctx); err != nil {
		failRun(ctx, pool, runID, err)
		return res, err
	}
	// Data is durable; a ledger-update failure must not be reported as an
	// ingest failure — log it and report success.
	if err = finishRun(ctx, pool, runID, res.RowsLoaded); err != nil {
		fmt.Fprintf(os.Stderr, "notdawa: ingest committed but ledger update failed for run %d: %v\n", runID, err)
	}
	return res, nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nullFloatStr parses a decimal string into a *float64, returning nil for "" or
// any parse error so the value lands as SQL NULL.
func nullFloatStr(s string) *float64 {
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

// nullIntStr parses an integer string into a *int, returning nil for "" or any
// parse error so the value lands as SQL NULL.
func nullIntStr(s string) *int {
	if s == "" {
		return nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	return &v
}

// Result summarises one entity ingest.
type Result struct {
	Register         string
	Entity           string
	GenerationNumber int
	RowsLoaded       int
}

func (r Result) String() string {
	return fmt.Sprintf("%s/%s gen %d: %d rows loaded", r.Register, r.Entity, r.GenerationNumber, r.RowsLoaded)
}

// pickLatestTotal returns the latest-generation Current TotalDownload extract
// for an exact entity name in the given format, excluding municipality splits.
// The exact-name match matters: "Regionsinddeling" must not pick up the
// generalised "Regionsinddeling_250000" variants.
func pickLatestTotal(files []datafordeler.FileDownload, entity, format string) (datafordeler.FileDownload, bool) {
	return pickLatestTotalT(files, entity, format, "Current")
}

// pickLatestTotalT is pickLatestTotal parameterised by temporality (Current or
// Bitemporal). DS Stednavn (and Adgangspunkt) only publish Bitemporal totals.
func pickLatestTotalT(files []datafordeler.FileDownload, entity, format, typeOfData string) (datafordeler.FileDownload, bool) {
	var best datafordeler.FileDownload
	found := false
	for _, f := range files {
		if f.EntityName != entity || f.ContainedFileFormat != format {
			continue
		}
		if f.TypeOfData != typeOfData || f.TypeOfDownload != "TotalDownload" || f.MunicipalityCode != nil {
			continue
		}
		if !found || f.GenerationNumber > best.GenerationNumber {
			best, found = f, true
		}
	}
	return best, found
}

// openZipMember opens a Fildownload .zip and returns a streaming reader over its
// single .json member. The caller must close BOTH returned closers (member
// reader first, then the zip). Decompression is streamed, so memory stays bounded.
func openZipMember(path string) (*zip.ReadCloser, io.ReadCloser, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open zip %s: %w", path, err)
	}
	var member *zip.File
	for _, f := range zr.File {
		if strings.HasSuffix(strings.ToLower(f.Name), ".json") {
			member = f
			break
		}
	}
	if member == nil {
		zr.Close()
		return nil, nil, fmt.Errorf("no .json member in %s", path)
	}
	rc, err := member.Open()
	if err != nil {
		zr.Close()
		return nil, nil, fmt.Errorf("open %s in zip: %w", member.Name, err)
	}
	return zr, rc, nil
}

// drainZipMember reads the member to EOF so archive/zip verifies its CRC32.
// json.Decoder stops at the closing ']' and never performs the final read that
// triggers the check — a corrupted-but-still-decodable member would otherwise
// pass silently. Cheap: only the trailing bytes remain.
func drainZipMember(rc io.Reader) error {
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("zip member integrity (crc): %w", err)
	}
	return nil
}

// readZipJSON decodes the whole .json member into dst (a pointer). Suitable for
// small/medium extracts; use streamLoad for the large DAR/MAT ones.
func readZipJSON(path string, dst any) error {
	zr, rc, err := openZipMember(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	defer rc.Close()
	if err := json.NewDecoder(rc).Decode(dst); err != nil {
		return fmt.Errorf("decode zip json %s: %w", path, err)
	}
	return drainZipMember(rc)
}

// downloadEntity discovers the latest Current TotalDownload JSON for an entity,
// records a pending run, downloads it (md5/size-verified) to a temp file, and
// marks the run 'downloaded'. The CALLER owns the temp file (must os.Remove the
// returned path) and finishes the run via finishRun/failRun.
//
// Escape hatch for huge extracts over a flaky link (Husnummer 650 MB, MAT
// Jordstykke 4.5 GB): if NOTDAWA_INGEST_FILE points at a pre-downloaded .zip,
// the network download is skipped and that file is used instead — copied to a
// temp file so the caller's os.Remove deletes the copy, not the cached original.
// The ledger metadata (generation/FileName) still comes from ListAvailable, so
// runs stay accurate; the override is best-effort and ignored if unset.
func downloadEntity(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client, register, entity string) (datafordeler.FileDownload, string, int64, error) {
	return downloadEntityT(ctx, pool, client, register, entity, "Current")
}

// downloadEntityT is downloadEntity parameterised by temporality (Current or
// Bitemporal) — DS Stednavn only publishes Bitemporal totals.
func downloadEntityT(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client, register, entity, typeOfData string) (datafordeler.FileDownload, string, int64, error) {
	files, err := client.ListAvailable(ctx, register)
	if err != nil {
		return datafordeler.FileDownload{}, "", 0, err
	}
	file, ok := pickLatestTotalT(files, entity, "json", typeOfData)
	if !ok {
		return datafordeler.FileDownload{}, "", 0, fmt.Errorf("no %s TotalDownload json for %s/%s", typeOfData, register, entity)
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

// acquireExtract returns a local path to the extract zip: a temp copy of
// NOTDAWA_INGEST_FILE when set (skips the network), otherwise a fresh download.
func acquireExtract(ctx context.Context, client *datafordeler.Client, file datafordeler.FileDownload) (string, error) {
	override := os.Getenv("NOTDAWA_INGEST_FILE")
	if override == "" {
		return client.Download(ctx, file)
	}
	src, err := os.Open(override)
	if err != nil {
		return "", fmt.Errorf("NOTDAWA_INGEST_FILE %s: %w", override, err)
	}
	defer src.Close()
	tmp, err := os.CreateTemp("", "notdawa-*"+filepath.Ext(file.FileName))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("copy NOTDAWA_INGEST_FILE %s: %w", override, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	fmt.Fprintf(os.Stderr, "notdawa: using local extract %s (NOTDAWA_INGEST_FILE) instead of downloading %s\n", override, file.FileName)
	return tmp.Name(), nil
}

// downloadAndDecode downloads an entity and decodes its whole JSON member into
// dst. The caller finishes the run via finishRun. (Small/medium extracts.)
func downloadAndDecode(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client, register, entity string, dst any) (datafordeler.FileDownload, int64, error) {
	file, path, runID, err := downloadEntity(ctx, pool, client, register, entity)
	if err != nil {
		return file, runID, err
	}
	defer os.Remove(path)
	if err := readZipJSON(path, dst); err != nil {
		failRun(ctx, pool, runID, err)
		return file, runID, err
	}
	return file, runID, nil
}

// startRun records (or resets) a pending ingest_runs row and returns its id.
// The row starts as 'pending'; markDownloaded advances it once the bytes are on
// disk, so a process killed mid-download is not misreported as 'downloaded'.
func startRun(ctx context.Context, pool *pgxpool.Pool, register, entity string, f datafordeler.FileDownload) (int64, error) {
	var id int64
	err := pool.QueryRow(ctx, `
		INSERT INTO ingest_runs
			(register, entity, type_of_download, generation_number, file_name, md5_hash, file_size_bytes, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending')
		ON CONFLICT (register, entity, type_of_download, generation_number) DO UPDATE
			SET file_name = EXCLUDED.file_name, md5_hash = EXCLUDED.md5_hash,
			    file_size_bytes = EXCLUDED.file_size_bytes, status = 'pending',
			    started_at = now(), finished_at = NULL, error = NULL, rows_loaded = NULL
		RETURNING id`,
		register, entity, f.TypeOfDownload, f.GenerationNumber, f.FileName, f.Md5Hash, f.FileSizeInBytes,
	).Scan(&id)
	return id, err
}

// markDownloaded advances a run to 'downloaded' after the file lands on disk.
func markDownloaded(ctx context.Context, pool *pgxpool.Pool, id int64) {
	if _, err := pool.Exec(ctx, `UPDATE ingest_runs SET status='downloaded' WHERE id=$1`, id); err != nil {
		fmt.Fprintf(os.Stderr, "notdawa: could not mark ingest run %d downloaded: %v\n", id, err)
	}
}

// finishRun marks a run 'loaded'. It uses a detached context so the write still
// lands if the caller's ctx was cancelled right after a successful commit.
func finishRun(ctx context.Context, pool *pgxpool.Pool, id int64, rows int) error {
	_, err := pool.Exec(context.WithoutCancel(ctx),
		`UPDATE ingest_runs SET status='loaded', rows_loaded=$2, finished_at=now() WHERE id=$1`, id, rows)
	return err
}

// failRun marks a run 'failed'. It uses a detached context (the original ctx is
// often the cancelled one that caused the failure) and logs — rather than
// returns — any bookkeeping error so callers surface the real cause unobscured.
func failRun(ctx context.Context, pool *pgxpool.Pool, id int64, cause error) {
	if _, err := pool.Exec(context.WithoutCancel(ctx),
		`UPDATE ingest_runs SET status='failed', error=$2, finished_at=now() WHERE id=$1`,
		id, cause.Error()); err != nil {
		fmt.Fprintf(os.Stderr, "notdawa: could not mark ingest run %d failed: %v\n", id, err)
	}
}
