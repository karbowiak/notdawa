package ingest

// Brofasthed is a DAWA-seed register (like stormodtagere): a curated CSV DAWA
// ships in its own repo (packages/server/data/brofasthed.csv, MIT/SDFI open data)
// and loads verbatim. It maps each sted (place/island, by ds_steder.id_lokalId) to
// a brofast boolean — whether the place is on the bridge-fast road network.
//
// brofast is NOT a DAR attribute (the DAR Husnummer object carries no brofast
// field; confirmed against the live extract). DAWA derives adgangsadresse.brofast
// from this table: an address is "ikke brofast" iff its adgangspunkt falls in a
// sted marked brofast=false (the ~377 non-connected islands). The adgangsadresse
// query computes it as NOT EXISTS(brofasthed b JOIN ds_steder s ON s.id_lokalId =
// b.stedid WHERE NOT b.brofast AND ST_Contains(s.geom, adgangspunkt)).
//
// CSV columns are semicolon-separated: stedid;navn;brofast;antal. We load only
// stedid + brofast (navn/antal are informational). brofast is 't'/'f'.

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

//go:embed data/brofasthed.csv
var brofasthedFS embed.FS

const brofasthedCSV = "data/brofasthed.csv"

// brofasthedRow is one parsed (stedid, brofast) pair.
type brofasthedRow struct {
	Stedid  string
	Brofast bool
}

// parseBrofasthed reads the semicolon-separated DAWA brofasthed CSV
// (stedid;navn;brofast;antal) and returns (stedid, brofast) rows. brofast is the
// Postgres boolean text 't'/'f'. Empty lines and rows without a stedid are skipped.
func parseBrofasthed(r io.Reader) ([]brofasthedRow, error) {
	cr := csv.NewReader(r)
	cr.Comma = ';'
	cr.FieldsPerRecord = -1
	records, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse brofasthed csv: %w", err)
	}
	if len(records) < 2 {
		return nil, fmt.Errorf("brofasthed csv has no data rows")
	}
	rows := make([]brofasthedRow, 0, len(records)-1)
	for i, rec := range records {
		if i == 0 || len(rec) < 3 { // header / malformed
			continue
		}
		stedid := strings.TrimSpace(rec[0])
		if stedid == "" {
			continue
		}
		rows = append(rows, brofasthedRow{
			Stedid:  stedid,
			Brofast: strings.EqualFold(strings.TrimSpace(rec[2]), "t") || strings.EqualFold(strings.TrimSpace(rec[2]), "true"),
		})
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("brofasthed csv has no usable rows")
	}
	return rows, nil
}

// Brofasthed loads the embedded DAWA-seed brofasthed CSV into the brofasthed table
// (TRUNCATE then INSERT). No datafordeler client: the data is static + embedded.
func Brofasthed(ctx context.Context, pool *pgxpool.Pool) (Result, error) {
	res := Result{Register: "DAWA-seed", Entity: "Brofasthed"}

	raw, err := brofasthedFS.ReadFile(brofasthedCSV)
	if err != nil {
		return res, fmt.Errorf("read embedded %s: %w", brofasthedCSV, err)
	}
	rows, err := parseBrofasthed(bytes.NewReader(raw))
	if err != nil {
		return res, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)

	if _, err = tx.Exec(ctx, "TRUNCATE brofasthed"); err != nil {
		return res, fmt.Errorf("truncate brofasthed: %w", err)
	}

	batch := &pgx.Batch{}
	for _, row := range rows {
		batch.Queue(
			`INSERT INTO brofasthed (stedid, brofast) VALUES ($1, $2)
			 ON CONFLICT (stedid) DO UPDATE SET brofast = EXCLUDED.brofast`,
			row.Stedid, row.Brofast,
		)
	}
	br := tx.SendBatch(ctx, batch)
	for range rows {
		if _, err = br.Exec(); err != nil {
			br.Close()
			return res, fmt.Errorf("insert into brofasthed: %w", err)
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
