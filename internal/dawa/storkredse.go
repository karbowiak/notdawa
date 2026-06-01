package dawa

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Storkreds is the DAWA /storkredse/{nummer} response. Field order is significant
// (href sits after visueltcenter, before nummer; nested region then valglandsdel
// last). No dagi_id in output.
type Storkreds struct {
	Aendret       *string          `json:"ændret"`
	GeoVersion    *int             `json:"geo_version"`
	GeoAendret    *string          `json:"geo_ændret"`
	Bbox          [4]float64       `json:"bbox"`
	Visueltcenter [2]float64       `json:"visueltcenter"`
	Href          string           `json:"href"`
	Nummer        string           `json:"nummer"`
	Navn          string           `json:"navn"`
	Region        *RegionRef       `json:"region"`
	Valglandsdel  *ValglandsdelRef `json:"valglandsdel"`
}

// storkredseCols selects the computed/scanned columns. bbox = reprojected
// envelope; visueltcenter = the precomputed polylabel point (stored 25832).
const storkredseCols = `
	to_char(aendret AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS aendret,
	nummer, navn,
	round(ST_XMin(e)::numeric, 8)::float8, round(ST_YMin(e)::numeric, 8)::float8,
	round(ST_XMax(e)::numeric, 8)::float8, round(ST_YMax(e)::numeric, 8)::float8,
	round(ST_X(c)::numeric, 8)::float8,    round(ST_Y(c)::numeric, 8)::float8,
	regionskode, region_navn, valglandsdelsbogstav, valglandsdel_navn`

const storkredseGeom = `
	SELECT s.aendret, s.nummer, s.navn,
		ST_Transform(ST_Envelope(s.geom), 4326) AS e,
		ST_Transform(s.visueltcenter, 4326) AS c,
		s.regionskode, reg.navn AS region_navn,
		s.valglandsdelsbogstav, vl.navn AS valglandsdel_navn
	FROM dagi_storkredse s
	LEFT JOIN dagi_regioner reg ON reg.kode = s.regionskode
	LEFT JOIN dagi_valglandsdele vl ON vl.bogstav = s.valglandsdelsbogstav`

func scanStorkreds(row pgx.Row, baseURL string) (*Storkreds, error) {
	var s Storkreds
	var regKode, regNavn, vlBogstav, vlNavn *string
	if err := row.Scan(
		&s.Aendret, &s.Nummer, &s.Navn,
		&s.Bbox[0], &s.Bbox[1], &s.Bbox[2], &s.Bbox[3],
		&s.Visueltcenter[0], &s.Visueltcenter[1],
		&regKode, &regNavn, &vlBogstav, &vlNavn,
	); err != nil {
		return nil, err
	}
	s.GeoAendret = s.Aendret
	s.Href = fmt.Sprintf("%s/storkredse/%s", baseURL, s.Nummer)
	s.Region = newRegionRef(baseURL, regKode, regNavn)
	s.Valglandsdel = newValglandsdelRef(baseURL, vlBogstav, vlNavn)
	return &s, nil
}

// GetStorkreds returns the storkreds with the given nummer, or pgx.ErrNoRows.
func GetStorkreds(ctx context.Context, pool *pgxpool.Pool, nummer, baseURL string) (*Storkreds, error) {
	sql := "SELECT " + storkredseCols + " FROM (" + storkredseGeom + " WHERE s.nummer = $1) q"
	return scanStorkreds(pool.QueryRow(ctx, sql, nummer), baseURL)
}

// ListStorkredse returns all storkredse ordered by nummer NUMERICALLY (1..10).
func ListStorkredse(ctx context.Context, pool *pgxpool.Pool, baseURL string) ([]*Storkreds, error) {
	sql := "SELECT " + storkredseCols + " FROM (" + storkredseGeom + ") q ORDER BY nummer::int"
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Storkreds
	for rows.Next() {
		s, err := scanStorkreds(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
