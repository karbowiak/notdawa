package dawa

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SimpleArea is the DAWA response shape shared by /retskredse and /politikredse.
// DAWA's key order is: the metadata trio (ændret, geo_version, geo_ændret),
// bbox, visueltcenter, then dagi_id, kode, navn, href. dagi_id is the DAGI
// internal identifier carried by these resources (distinct from kode). The
// source extract has no geo_version, so it renders null (DAWA-internal version
// metadata, not derivable from Datafordeler).
type SimpleArea struct {
	Aendret       *string    `json:"ændret"`
	GeoVersion    *int       `json:"geo_version"`
	GeoAendret    *string    `json:"geo_ændret"`
	Bbox          [4]float64 `json:"bbox"`
	Visueltcenter [2]float64 `json:"visueltcenter"`
	DagiID        string     `json:"dagi_id"`
	Kode          string     `json:"kode"`
	Navn          string     `json:"navn"`
	Href          string     `json:"href"`
}

// simpleAreaCols/Geom build the bbox (reprojected envelope) + visueltcenter (the
// precomputed polylabel point) for a simple DAGI area table. The caller supplies
// the table and the URL path segment (e.g. "retskredse"). dagi_id is carried
// straight from the source row, in DAWA's field order.
func simpleAreaCols() string {
	return `
		to_char(aendret AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS aendret,
		dagi_id, kode, navn,
		round(ST_XMin(e)::numeric, 8)::float8, round(ST_YMin(e)::numeric, 8)::float8,
		round(ST_XMax(e)::numeric, 8)::float8, round(ST_YMax(e)::numeric, 8)::float8,
		round(ST_X(c)::numeric, 8)::float8,    round(ST_Y(c)::numeric, 8)::float8`
}

func simpleAreaGeom(table string) string {
	return `
		SELECT a.aendret, a.dagi_id, a.kode, a.navn,
			ST_Transform(ST_Envelope(a.geom), 4326) AS e,
			ST_Transform(a.visueltcenter, 4326) AS c
		FROM ` + table + ` a`
}

func scanSimpleArea(row pgx.Row, baseURL, pathSeg string) (*SimpleArea, error) {
	var s SimpleArea
	if err := row.Scan(
		&s.Aendret, &s.DagiID, &s.Kode, &s.Navn,
		&s.Bbox[0], &s.Bbox[1], &s.Bbox[2], &s.Bbox[3],
		&s.Visueltcenter[0], &s.Visueltcenter[1],
	); err != nil {
		return nil, err
	}
	s.GeoAendret = s.Aendret
	s.Href = fmt.Sprintf("%s/%s/%s", baseURL, pathSeg, s.Kode)
	return &s, nil
}

// GetSimpleArea returns one row of a simple DAGI area table by kode.
func GetSimpleArea(ctx context.Context, pool *pgxpool.Pool, table, pathSeg, kode, baseURL string) (*SimpleArea, error) {
	sql := "SELECT " + simpleAreaCols() + " FROM (" + simpleAreaGeom(table) + " WHERE a.kode = $1) q"
	return scanSimpleArea(pool.QueryRow(ctx, sql, kode), baseURL, pathSeg)
}

// ListSimpleAreas returns all rows of a simple DAGI area table ordered by kode,
// which reproduces DAWA's collection order for these resources.
func ListSimpleAreas(ctx context.Context, pool *pgxpool.Pool, table, pathSeg, baseURL string) ([]*SimpleArea, error) {
	sql := "SELECT " + simpleAreaCols() + " FROM (" + simpleAreaGeom(table) + ") q ORDER BY kode"
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SimpleArea
	for rows.Next() {
		s, err := scanSimpleArea(rows, baseURL, pathSeg)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
