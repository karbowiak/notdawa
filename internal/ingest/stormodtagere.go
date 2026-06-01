package ingest

// CLI: case "stormodtagere": res, err = ingest.Stormodtagere(cmd.Context(), pool)
//
// Stormodtagere ("firmapostnumre" / large-volume postal recipients) is a
// DAWA-seed register: a hand-curated CSV that DAWA ships in its own repo and
// loads verbatim, NOT a Datafordeler extract. There is nothing to derive and no
// network at runtime — we //go:embed the exact upstream CSV and TRUNCATE+INSERT.
//
// Authoritative source: data/stormodtagere.csv (26 rows). DAWA's loader
// (packages/server/psql/loadStormodtagere.js) hard-defaults its --inputFile
// option to "data/stormodtagere.csv"; the sibling stormodtager-opdateret.csv
// (47 rows) is vendored alongside for reference but is NOT what DAWA loads.
//
// PRIMARY KEY = adgangsadresseid, mirroring DAWA's table (schema/tables/
// stormodtagere.sql: nr integer, navn VARCHAR(20), adgangsadresseid UUID PK).
// Firmapostnr is NOT unique (one firmapostnummer covers many addresses, e.g.
// "1092" on 4 rows), so it cannot be the key — the access address is. One
// stormodtagere.csv row (Nordea, Torvegade 2) has an empty Adgangsadresseid;
// DAWA's NOT NULL PK rejects it, so we skip it too (→ 25 loaded rows).
//
// Column mapping note: DAWA's JS importer (components/importers/stormodtagere.js)
// quirkily stores row.Bynavn into the `navn` column. We instead store the Firma
// (recipient name) into navn — the human-meaningful value DAWA's table comment
// ("navn") and API consumers expect — and keep the postal town in `bynavn`.
// nr (Firmapostnr) and adgangsadresseid match DAWA exactly.

import (
	"bytes"
	"context"
	"embed"
	"encoding/csv"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed data/stormodtagere.csv data/stormodtager-opdateret.csv
var stormodtagereFS embed.FS

// stormodtagerCSV is the authoritative vendored file (see file header).
const stormodtagerCSV = "data/stormodtagere.csv"

// stormodtagerRow is one parsed CSV record mapped to the stormodtagere columns.
type stormodtagerRow struct {
	Adgangsadresseid string // adgangsadresse UUID -> dar_husnummer.id (PRIMARY KEY)
	Nr               string // Firmapostnr, e.g. "0800" (NOT unique)
	Navn             string // Firma (recipient name), trimmed
	Gadeadresse      string
	Postnr           string
	Bynavn           string
}

// parseStormodtagere reads the semicolon-separated DAWA CSV and returns the rows
// in file order. Header columns are: Firma;Gadeadresse;Postnr;Firmapostnr;
// Bynavn;Adgangsadresseid;Bemærkninger. All values are whitespace-trimmed (the
// upstream file has e.g. "Danske Bank A/S " with a trailing space). Entirely
// empty lines are skipped. A row with no Adgangsadresseid is skipped (it is the
// PK and DAWA's NOT NULL constraint rejects it); a row with an Adgangsadresseid
// but no Firmapostnr is an error (nr is NOT NULL in the table).
func parseStormodtagere(r io.Reader) ([]stormodtagerRow, error) {
	cr := csv.NewReader(r)
	cr.Comma = ';'
	cr.FieldsPerRecord = -1 // tolerate the optional trailing Bemærkninger column
	cr.TrimLeadingSpace = false

	records, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse stormodtagere csv: %w", err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("stormodtagere csv is empty")
	}

	col := func(rec []string, i int) string {
		if i < 0 || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	rows := make([]stormodtagerRow, 0, len(records)-1)
	for i, rec := range records {
		if i == 0 {
			continue // header
		}
		empty := true
		for _, f := range rec {
			if strings.TrimSpace(f) != "" {
				empty = false
				break
			}
		}
		if empty {
			continue
		}
		row := stormodtagerRow{
			Navn:             col(rec, 0), // Firma
			Gadeadresse:      col(rec, 1), // Gadeadresse
			Postnr:           col(rec, 2), // Postnr (real postnr)
			Nr:               col(rec, 3), // Firmapostnr
			Bynavn:           col(rec, 4), // Bynavn
			Adgangsadresseid: col(rec, 5), // Adgangsadresseid
		}
		if row.Adgangsadresseid == "" {
			// No access-address id → cannot be a row in DAWA's table (it is the
			// NOT NULL primary key). Skip, mirroring DAWA's load.
			continue
		}
		if row.Nr == "" {
			return nil, fmt.Errorf("stormodtagere csv row %d (id %s): empty Firmapostnr", i+1, row.Adgangsadresseid)
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("stormodtagere csv has no data rows")
	}
	return rows, nil
}

// Stormodtagere loads the embedded DAWA-seed stormodtagere CSV into the
// stormodtagere table (TRUNCATE then INSERT). It takes NO datafordeler client:
// the data is static and embedded, so there is no download step. The table is
// keyed by adgangsadresseid (unique in the vendored CSV); Firmapostnr is not.
func Stormodtagere(ctx context.Context, pool *pgxpool.Pool) (Result, error) {
	res := Result{Register: "DAWA-seed", Entity: "Stormodtagere"}

	raw, err := stormodtagereFS.ReadFile(stormodtagerCSV)
	if err != nil {
		return res, fmt.Errorf("read embedded %s: %w", stormodtagerCSV, err)
	}
	rows, err := parseStormodtagere(bytes.NewReader(raw))
	if err != nil {
		return res, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)

	if _, err = tx.Exec(ctx, "TRUNCATE stormodtagere"); err != nil {
		return res, fmt.Errorf("truncate stormodtagere: %w", err)
	}

	batch := &pgx.Batch{}
	for _, row := range rows {
		batch.Queue(
			`INSERT INTO stormodtagere (adgangsadresseid, nr, navn, gadeadresse, postnr, bynavn)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			row.Adgangsadresseid, row.Nr, row.Navn,
			nullIfEmpty(row.Gadeadresse), nullIfEmpty(row.Postnr), nullIfEmpty(row.Bynavn),
		)
	}
	br := tx.SendBatch(ctx, batch)
	for range rows {
		if _, err = br.Exec(); err != nil {
			br.Close()
			return res, fmt.Errorf("insert into stormodtagere: %w", err)
		}
	}
	if err = br.Close(); err != nil {
		return res, err
	}
	if err = tx.Commit(ctx); err != nil {
		return res, err
	}

	res.RowsLoaded = len(rows)
	return res, nil
}
