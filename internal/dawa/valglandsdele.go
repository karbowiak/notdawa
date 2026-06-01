package dawa

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Valglandsdel is the DAWA /valglandsdele/{bogstav} response. Field order: the
// metadata trio, bbox, visueltcenter, then bogstav/navn and href LAST. No dagi_id.
type Valglandsdel struct {
	Aendret       *string    `json:"ændret"`
	GeoVersion    *int       `json:"geo_version"`
	GeoAendret    *string    `json:"geo_ændret"`
	Bbox          [4]float64 `json:"bbox"`
	Visueltcenter [2]float64 `json:"visueltcenter"`
	Bogstav       string     `json:"bogstav"`
	Navn          string     `json:"navn"`
	Href          string     `json:"href"`
}

const valglandsdeleCols = `
	to_char(aendret AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS aendret,
	bogstav, navn,
	round(ST_XMin(e)::numeric, 8)::float8, round(ST_YMin(e)::numeric, 8)::float8,
	round(ST_XMax(e)::numeric, 8)::float8, round(ST_YMax(e)::numeric, 8)::float8,
	round(ST_X(c)::numeric, 8)::float8,    round(ST_Y(c)::numeric, 8)::float8`

const valglandsdeleGeom = `
	SELECT v.aendret, v.bogstav, v.navn,
		ST_Transform(ST_Envelope(v.geom), 4326) AS e,
		ST_Transform(v.visueltcenter, 4326) AS c
	FROM dagi_valglandsdele v`

func scanValglandsdel(row pgx.Row, baseURL string) (*Valglandsdel, error) {
	var v Valglandsdel
	if err := row.Scan(
		&v.Aendret, &v.Bogstav, &v.Navn,
		&v.Bbox[0], &v.Bbox[1], &v.Bbox[2], &v.Bbox[3],
		&v.Visueltcenter[0], &v.Visueltcenter[1],
	); err != nil {
		return nil, err
	}
	v.GeoAendret = v.Aendret
	v.Href = fmt.Sprintf("%s/valglandsdele/%s", baseURL, v.Bogstav)
	return &v, nil
}

// GetValglandsdel returns the valglandsdel with the given bogstav, or pgx.ErrNoRows.
func GetValglandsdel(ctx context.Context, pool *pgxpool.Pool, bogstav, baseURL string) (*Valglandsdel, error) {
	sql := "SELECT " + valglandsdeleCols + " FROM (" + valglandsdeleGeom + " WHERE v.bogstav = $1) q"
	return scanValglandsdel(pool.QueryRow(ctx, sql, bogstav), baseURL)
}

// ListValglandsdele returns all valglandsdele ordered by bogstav (A,B,C).
func ListValglandsdele(ctx context.Context, pool *pgxpool.Pool, baseURL string) ([]*Valglandsdel, error) {
	sql := "SELECT " + valglandsdeleCols + " FROM (" + valglandsdeleGeom + ") q ORDER BY bogstav"
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Valglandsdel
	for rows.Next() {
		v, err := scanValglandsdel(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
