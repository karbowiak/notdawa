package dawa

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Kommune is the DAWA /kommuner/{kode} response. Field order is significant
// (note href sits between visueltcenter and dagi_id, unlike regioner).
type Kommune struct {
	Aendret                 *string    `json:"ændret"`
	GeoVersion              *int       `json:"geo_version"`
	GeoAendret              *string    `json:"geo_ændret"`
	Bbox                    [4]float64 `json:"bbox"`
	Visueltcenter           [2]float64 `json:"visueltcenter"`
	Href                    string     `json:"href"`
	DagiID                  *string    `json:"dagi_id"`
	Kode                    string     `json:"kode"`
	Navn                    string     `json:"navn"`
	Udenforkommuneinddeling bool       `json:"udenforkommuneinddeling"`
	Regionskode             *string    `json:"regionskode"`
	Region                  *RegionRef `json:"region"`
}

// kommuneCols selects DAWA-computed fields; reg.* come from the LEFT JOIN to the
// region (via regionLokalId -> dagi_regioner.dagi_id) and are NULL if unresolved.
const kommuneCols = `
	to_char(aendret AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS aendret,
	dagi_id, kode, navn, udenforkommuneinddeling,
	round(ST_XMin(e)::numeric, 8)::float8, round(ST_YMin(e)::numeric, 8)::float8,
	round(ST_XMax(e)::numeric, 8)::float8, round(ST_YMax(e)::numeric, 8)::float8,
	round(ST_X(c)::numeric, 8)::float8,    round(ST_Y(c)::numeric, 8)::float8,
	regionskode, region_navn`

const kommuneGeom = `
	SELECT s.aendret, s.dagi_id, s.kode, s.navn, s.udenforkommuneinddeling, s.e, s.c,
		reg.kode AS regionskode, reg.navn AS region_navn
	FROM (
		SELECT k.aendret, k.dagi_id, k.kode, k.navn, k.udenforkommuneinddeling, k.region_lokalid,
			ST_Transform(ST_Envelope(k.geom), 4326) AS e,
			ST_Transform((ST_MaximumInscribedCircle(lp.geom)).center, 4326) AS c
		FROM dagi_kommuner k
		CROSS JOIN LATERAL (
			SELECT d.geom FROM (SELECT (ST_Dump(k.geom)).geom AS geom) d
			ORDER BY ST_Area(d.geom) DESC LIMIT 1
		) lp
	) s
	LEFT JOIN dagi_regioner reg ON reg.dagi_id = s.region_lokalid`

func scanKommune(row pgx.Row, baseURL string) (*Kommune, error) {
	var k Kommune
	var regKode, regNavn *string
	if err := row.Scan(
		&k.Aendret, &k.DagiID, &k.Kode, &k.Navn, &k.Udenforkommuneinddeling,
		&k.Bbox[0], &k.Bbox[1], &k.Bbox[2], &k.Bbox[3],
		&k.Visueltcenter[0], &k.Visueltcenter[1],
		&regKode, &regNavn,
	); err != nil {
		return nil, err
	}
	k.GeoAendret = k.Aendret
	k.Href = fmt.Sprintf("%s/kommuner/%s", baseURL, k.Kode)
	k.Regionskode = regKode
	k.Region = newRegionRef(baseURL, regKode, regNavn)
	return &k, nil
}

// GetKommune returns the kommune with the given kode, or pgx.ErrNoRows.
func GetKommune(ctx context.Context, pool *pgxpool.Pool, kode, baseURL string) (*Kommune, error) {
	// kommuneGeom selects s.* / reg.*; wrap it and select the computed columns.
	sql := "SELECT " + kommuneCols + " FROM (" + kommuneGeom + " WHERE s.kode = $1) q"
	return scanKommune(pool.QueryRow(ctx, sql, kode), baseURL)
}

// ListKommuner returns all kommuner ordered by kode (DAWA's default order).
func ListKommuner(ctx context.Context, pool *pgxpool.Pool, baseURL string) ([]*Kommune, error) {
	sql := "SELECT " + kommuneCols + " FROM (" + kommuneGeom + ") q ORDER BY kode"
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Kommune
	for rows.Next() {
		k, err := scanKommune(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
