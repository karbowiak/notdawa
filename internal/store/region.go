// Package store is the data-fetch layer: it runs the PostGIS queries and returns
// neutral domain structs that both serving modes (byte-exact dawa, English api)
// render. Keeping the SQL here — once per entity — is what lets the two renderers
// share a single source of truth instead of each re-querying.
package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/domain"
)

// regionSelect lists the region columns in the order scanRegion expects. The
// first 11 expressions are byte-for-byte identical to the legacy dawa.regionCols
// (DAWA's reprojected-rectangle bbox + MIC visueltcenter) so the dawa renderer
// stays byte-exact; the trailing 5 add the true WGS84 bbox + raw GeoJSON for the
// api renderer.
const regionSelect = `
	to_char(s.aendret AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS aendret,
	s.dagi_id, s.kode, s.navn, s.nuts2,
	round(ST_XMin(s.e)::numeric, 8)::float8, round(ST_YMin(s.e)::numeric, 8)::float8,
	round(ST_XMax(s.e)::numeric, 8)::float8, round(ST_YMax(s.e)::numeric, 8)::float8,
	round(ST_X(s.c)::numeric, 8)::float8,    round(ST_Y(s.c)::numeric, 8)::float8,
	round(ST_XMin(s.g)::numeric, 8)::float8, round(ST_YMin(s.g)::numeric, 8)::float8,
	round(ST_XMax(s.g)::numeric, 8)::float8, round(ST_YMax(s.g)::numeric, 8)::float8,
	ST_AsGeoJSON(s.g, 8)`

// regionGeom derives, per region, DAWA's envelope (e) + MIC center (c) — both
// kept character-identical to the legacy query so float output is unchanged — and
// additionally the true reprojected geometry (g) used for the api WGS84 bbox and
// GeoJSON. The MIC runs on the largest-area sub-polygon (where the pole lies).
const regionGeom = `
	SELECT r.aendret, r.dagi_id, r.kode, r.navn, r.nuts2,
		ST_Transform(ST_Envelope(r.geom), 4326) AS e,
		ST_Transform((ST_MaximumInscribedCircle(lp.geom)).center, 4326) AS c,
		ST_Transform(r.geom, 4326) AS g
	FROM dagi_regioner r
	CROSS JOIN LATERAL (
		SELECT d.geom FROM (SELECT (ST_Dump(r.geom)).geom AS geom) d
		ORDER BY ST_Area(d.geom) DESC LIMIT 1
	) lp`

func scanRegion(row pgx.Row) (*domain.Region, error) {
	var r domain.Region
	if err := row.Scan(
		&r.Aendret, &r.DagiID, &r.Kode, &r.Navn, &r.Nuts2,
		&r.DawaBbox[0], &r.DawaBbox[1], &r.DawaBbox[2], &r.DawaBbox[3],
		&r.DawaVisueltcenter[0], &r.DawaVisueltcenter[1],
		&r.TrueBbox[0], &r.TrueBbox[1], &r.TrueBbox[2], &r.TrueBbox[3],
		&r.GeoJSON,
	); err != nil {
		return nil, err
	}
	return &r, nil
}

// GetRegion returns the region with the given kode, or (nil, pgx.ErrNoRows).
func GetRegion(ctx context.Context, pool *pgxpool.Pool, kode string) (*domain.Region, error) {
	sql := "SELECT " + regionSelect + " FROM (" + regionGeom + " WHERE r.kode = $1) s"
	return scanRegion(pool.QueryRow(ctx, sql, kode))
}

// ListRegions returns all regions ordered by kode (DAWA's default order).
func ListRegions(ctx context.Context, pool *pgxpool.Pool) ([]*domain.Region, error) {
	sql := "SELECT " + regionSelect + " FROM (" + regionGeom + ") s ORDER BY s.kode"
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Region
	for rows.Next() {
		r, err := scanRegion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
