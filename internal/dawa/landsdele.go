package dawa

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Landsdel is the DAWA /landsdele/{nuts3} response. Keyed by nuts3 (no kode);
// field order matters (href after visueltcenter, navn before nuts3).
type Landsdel struct {
	Aendret       *string    `json:"ændret"`
	GeoVersion    *int       `json:"geo_version"`
	GeoAendret    *string    `json:"geo_ændret"`
	Bbox          [4]float64 `json:"bbox"`
	Visueltcenter [2]float64 `json:"visueltcenter"`
	Href          string     `json:"href"`
	DagiID        *string    `json:"dagi_id"`
	Navn          string     `json:"navn"`
	Nuts3         string     `json:"nuts3"`
	Region        *RegionRef `json:"region"`
}

const landsdelCols = `
	to_char(aendret AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS aendret,
	dagi_id, navn, nuts3,
	round(ST_XMin(e)::numeric, 8)::float8, round(ST_YMin(e)::numeric, 8)::float8,
	round(ST_XMax(e)::numeric, 8)::float8, round(ST_YMax(e)::numeric, 8)::float8,
	round(ST_X(c)::numeric, 8)::float8,    round(ST_Y(c)::numeric, 8)::float8,
	regionskode, region_navn`

// landsdelGeom derives bbox/visueltcenter and resolves region{} by matching the
// region whose nuts2 equals the landsdel's nuts3 prefix (e.g. DK011 -> DK01).
const landsdelGeom = `
	SELECT s.aendret, s.dagi_id, s.navn, s.nuts3, s.e, s.c,
		reg.kode AS regionskode, reg.navn AS region_navn
	FROM (
		SELECT l.aendret, l.dagi_id, l.navn, l.nuts3,
			ST_Transform(ST_Envelope(l.geom), 4326) AS e,
			ST_Transform((ST_MaximumInscribedCircle(lp.geom)).center, 4326) AS c
		FROM dagi_landsdele l
		CROSS JOIN LATERAL (
			SELECT d.geom FROM (SELECT (ST_Dump(l.geom)).geom AS geom) d
			ORDER BY ST_Area(d.geom) DESC LIMIT 1
		) lp
	) s
	LEFT JOIN dagi_regioner reg ON reg.nuts2 = left(s.nuts3, 4)`

func scanLandsdel(row pgx.Row, baseURL string) (*Landsdel, error) {
	var l Landsdel
	var regKode, regNavn *string
	if err := row.Scan(
		&l.Aendret, &l.DagiID, &l.Navn, &l.Nuts3,
		&l.Bbox[0], &l.Bbox[1], &l.Bbox[2], &l.Bbox[3],
		&l.Visueltcenter[0], &l.Visueltcenter[1],
		&regKode, &regNavn,
	); err != nil {
		return nil, err
	}
	l.GeoAendret = l.Aendret
	l.Href = fmt.Sprintf("%s/landsdele/%s", baseURL, l.Nuts3)
	l.Region = newRegionRef(baseURL, regKode, regNavn)
	return &l, nil
}

// GetLandsdel returns the landsdel with the given nuts3, or pgx.ErrNoRows.
func GetLandsdel(ctx context.Context, pool *pgxpool.Pool, nuts3, baseURL string) (*Landsdel, error) {
	sql := "SELECT " + landsdelCols + " FROM (" + landsdelGeom + " WHERE s.nuts3 = $1) q"
	return scanLandsdel(pool.QueryRow(ctx, sql, nuts3), baseURL)
}

// ListLandsdele returns all landsdele ordered by nuts3 (DAWA's default order).
func ListLandsdele(ctx context.Context, pool *pgxpool.Pool, baseURL string) ([]*Landsdel, error) {
	sql := "SELECT " + landsdelCols + " FROM (" + landsdelGeom + ") q ORDER BY nuts3"
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Landsdel
	for rows.Next() {
		l, err := scanLandsdel(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
