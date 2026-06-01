package dawa

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sogn is the DAWA /sogne/{kode} response (standalone, like a region without
// nuts2). Field order matters.
type Sogn struct {
	Aendret       *string    `json:"ændret"`
	GeoVersion    *int       `json:"geo_version"`
	GeoAendret    *string    `json:"geo_ændret"`
	Bbox          [4]float64 `json:"bbox"`
	Visueltcenter [2]float64 `json:"visueltcenter"`
	DagiID        *string    `json:"dagi_id"`
	Kode          string     `json:"kode"`
	Navn          string     `json:"navn"`
	Href          string     `json:"href"`
}

const sogneCols = `
	to_char(aendret AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS aendret,
	dagi_id, kode, navn,
	round(ST_XMin(e)::numeric, 8)::float8, round(ST_YMin(e)::numeric, 8)::float8,
	round(ST_XMax(e)::numeric, 8)::float8, round(ST_YMax(e)::numeric, 8)::float8,
	round(ST_X(c)::numeric, 8)::float8,    round(ST_Y(c)::numeric, 8)::float8`

const sogneGeom = `
	SELECT s.aendret, s.dagi_id, s.kode, s.navn,
		ST_Transform(ST_Envelope(s.geom), 4326) AS e,
		ST_Transform((ST_MaximumInscribedCircle(lp.geom)).center, 4326) AS c
	FROM dagi_sogne s
	CROSS JOIN LATERAL (
		SELECT d.geom FROM (SELECT (ST_Dump(s.geom)).geom AS geom) d
		ORDER BY ST_Area(d.geom) DESC LIMIT 1
	) lp`

func scanSogn(row pgx.Row, baseURL string) (*Sogn, error) {
	var s Sogn
	if err := row.Scan(
		&s.Aendret, &s.DagiID, &s.Kode, &s.Navn,
		&s.Bbox[0], &s.Bbox[1], &s.Bbox[2], &s.Bbox[3],
		&s.Visueltcenter[0], &s.Visueltcenter[1],
	); err != nil {
		return nil, err
	}
	s.GeoAendret = s.Aendret
	s.Href = fmt.Sprintf("%s/sogne/%s", baseURL, s.Kode)
	return &s, nil
}

// GetSogn returns the sogn with the given kode, or pgx.ErrNoRows.
func GetSogn(ctx context.Context, pool *pgxpool.Pool, kode, baseURL string) (*Sogn, error) {
	sql := "SELECT " + sogneCols + " FROM (" + sogneGeom + " WHERE s.kode = $1) q"
	return scanSogn(pool.QueryRow(ctx, sql, kode), baseURL)
}

// ListSogne returns all sogne ordered by kode (DAWA's default order).
func ListSogne(ctx context.Context, pool *pgxpool.Pool, baseURL string) ([]*Sogn, error) {
	sql := "SELECT " + sogneCols + " FROM (" + sogneGeom + ") q ORDER BY kode"
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Sogn
	for rows.Next() {
		s, err := scanSogn(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
