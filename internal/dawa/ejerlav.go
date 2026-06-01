package dawa

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Ejerlav is the DAWA /ejerlav/{kode} response. Minimal shape: no DAWA-internal
// metadata, and kode is an INTEGER (not a zero-padded string).
type Ejerlav struct {
	Href          string     `json:"href"`
	Kode          int        `json:"kode"`
	Navn          string     `json:"navn"`
	Bbox          [4]float64 `json:"bbox"`
	Visueltcenter [2]float64 `json:"visueltcenter"`
}

const ejerlavCols = `
	kode, navn,
	round(ST_XMin(e)::numeric, 8)::float8, round(ST_YMin(e)::numeric, 8)::float8,
	round(ST_XMax(e)::numeric, 8)::float8, round(ST_YMax(e)::numeric, 8)::float8,
	round(ST_X(c)::numeric, 8)::float8,    round(ST_Y(c)::numeric, 8)::float8`

const ejerlavGeom = `
	SELECT m.kode, m.navn,
		ST_Transform(ST_Envelope(m.geom), 4326) AS e,
		ST_Transform((ST_MaximumInscribedCircle(lp.geom)).center, 4326) AS c
	FROM mat_ejerlav m
	CROSS JOIN LATERAL (
		SELECT d.geom FROM (SELECT (ST_Dump(m.geom)).geom AS geom) d
		ORDER BY ST_Area(d.geom) DESC LIMIT 1
	) lp`

func scanEjerlav(row pgx.Row, baseURL string) (*Ejerlav, error) {
	var e Ejerlav
	if err := row.Scan(
		&e.Kode, &e.Navn,
		&e.Bbox[0], &e.Bbox[1], &e.Bbox[2], &e.Bbox[3],
		&e.Visueltcenter[0], &e.Visueltcenter[1],
	); err != nil {
		return nil, err
	}
	e.Href = fmt.Sprintf("%s/ejerlav/%d", baseURL, e.Kode)
	return &e, nil
}

// GetEjerlav returns the ejerlav with the given kode, or pgx.ErrNoRows.
func GetEjerlav(ctx context.Context, pool *pgxpool.Pool, kode int, baseURL string) (*Ejerlav, error) {
	sql := "SELECT " + ejerlavCols + " FROM (" + ejerlavGeom + " WHERE m.kode = $1) q"
	return scanEjerlav(pool.QueryRow(ctx, sql, kode), baseURL)
}

// ListEjerlav returns all ejerlav ordered by kode (DAWA's default order).
func ListEjerlav(ctx context.Context, pool *pgxpool.Pool, baseURL string) ([]*Ejerlav, error) {
	sql := "SELECT " + ejerlavCols + " FROM (" + ejerlavGeom + ") q ORDER BY kode"
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Ejerlav
	for rows.Next() {
		e, err := scanEjerlav(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
