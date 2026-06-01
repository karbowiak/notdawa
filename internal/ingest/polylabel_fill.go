package ingest

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// polylabelBackfillTables are the area entities whose visueltcenter is being
// migrated from ST_MaximumInscribedCircle to the byte-exact Mapbox polylabel.
var polylabelBackfillTables = []string{
	"dagi_regioner", "dagi_kommuner", "dagi_landsdele",
	"dagi_sogne", "mat_ejerlav", "dagi_postnumre",
}

// PolylabelBackfill recomputes the visueltcenter (polylabel) point for every row
// of each area table from its already-loaded geom — no re-download needed.
func PolylabelBackfill(ctx context.Context, pool *pgxpool.Pool) (Result, error) {
	res := Result{Register: "DERIVED", Entity: "polylabel_backfill"}
	for _, t := range polylabelBackfillTables {
		key := "kode"
		if t == "dagi_postnumre" {
			key = "nr"
		}
		if err := fillPolylabel(ctx, pool, t, key); err != nil {
			return res, fmt.Errorf("%s: %w", t, err)
		}
		res.RowsLoaded++
	}
	return res, nil
}

// fillPolylabel computes the Mapbox polylabel (pole of inaccessibility) of every
// row's geom in table and stores it in the visueltcenter geometry(Point,25832)
// column. This is DAWA's visueltcenter algorithm — byte-exact where
// ST_MaximumInscribedCircle is metres off. keyCol is the table's primary key.
func fillPolylabel(ctx context.Context, pool *pgxpool.Pool, table, keyCol string) error {
	return fillPolylabelWhere(ctx, pool, table, keyCol, "")
}

// fillPolylabelWhere is fillPolylabel scoped to rows matching whereClause (a SQL
// boolean expression without the WHERE keyword; empty means all rows). DS uses
// it to compute the polylabel for polygon rows only, since polylabelOfGeoJSON
// only understands (Multi)Polygon GeoJSON.
func fillPolylabelWhere(ctx context.Context, pool *pgxpool.Pool, table, keyCol, whereClause string) error {
	query := fmt.Sprintf("SELECT %s, ST_AsGeoJSON(geom) FROM %s", keyCol, table)
	if whereClause != "" {
		query += " WHERE " + whereClause
	}
	rows, err := pool.Query(ctx, query)
	if err != nil {
		return err
	}
	type job struct{ key, gj string }
	var jobs []job
	for rows.Next() {
		var k, gj string
		if err := rows.Scan(&k, &gj); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, job{k, gj})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	upd := fmt.Sprintf(
		"UPDATE %s SET visueltcenter = ST_SetSRID(ST_MakePoint($1,$2),25832) WHERE %s = $3", table, keyCol)
	for _, j := range jobs {
		pt, err := polylabelOfGeoJSON(j.gj)
		if err != nil {
			return fmt.Errorf("%s %s: %w", table, j.key, err)
		}
		if _, err := pool.Exec(ctx, upd, pt[0], pt[1], j.key); err != nil {
			return err
		}
	}
	return nil
}
