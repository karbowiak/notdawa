package dawa

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Postnummer is the DAWA /postnumre/{nr} response. Field order is significant:
// href FIRST, dagi_id LAST, the DAWA-internal metadata trio between kommuner and
// dagi_id, and stormodtageradresser always null.
type Postnummer struct {
	Href                 string       `json:"href"`
	Nr                   string       `json:"nr"`
	Navn                 string       `json:"navn"`
	Stormodtageradresser *any         `json:"stormodtageradresser"` // always null in current DAWA
	Bbox                 [4]float64   `json:"bbox"`
	Visueltcenter        [2]float64   `json:"visueltcenter"`
	Kommuner             []KommuneRef `json:"kommuner"`
	Aendret              *string      `json:"ændret"`
	GeoAendret           *string      `json:"geo_ændret"`
	GeoVersion           *int         `json:"geo_version"`
	DagiID               *string      `json:"dagi_id"`
}

// postnumreSelect computes bbox (reprojected envelope) and visueltcenter (MIC on
// the largest sub-polygon) like the other DAGI area entities, and aggregates the
// address-based kommuner[] (ordered by kommunekode) from postnumre_kommuner.
const postnumreSelect = `
	to_char(aendret AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS aendret,
	nr, navn, dagi_id,
	round(ST_XMin(e)::numeric, 8)::float8, round(ST_YMin(e)::numeric, 8)::float8,
	round(ST_XMax(e)::numeric, 8)::float8, round(ST_YMax(e)::numeric, 8)::float8,
	round(ST_X(c)::numeric, 8)::float8,    round(ST_Y(c)::numeric, 8)::float8,
	j`

const postnumreGeom = `
	SELECT p.aendret, p.nr, p.navn, p.dagi_id,
		ST_Transform(ST_Envelope(p.geom), 4326) AS e,
		ST_Transform((ST_MaximumInscribedCircle(lp.geom)).center, 4326) AS c,
		km.j
	FROM dagi_postnumre p
	CROSS JOIN LATERAL (
		SELECT d.geom FROM (SELECT (ST_Dump(p.geom)).geom AS geom) d
		ORDER BY ST_Area(d.geom) DESC LIMIT 1
	) lp
	LEFT JOIN LATERAL (
		SELECT COALESCE(json_agg(json_build_object('kode', k.kode, 'navn', k.navn) ORDER BY k.kode), '[]') AS j
		FROM postnumre_kommuner pk
		JOIN dagi_kommuner k ON k.kode = pk.kommunekode
		WHERE pk.nr = p.nr
	) km ON true`

func scanPostnummer(row pgx.Row, baseURL string) (*Postnummer, error) {
	var p Postnummer
	var nr, navn string
	var dagiID *string
	var kmJSON []byte
	if err := row.Scan(
		&p.Aendret, &nr, &navn, &dagiID,
		&p.Bbox[0], &p.Bbox[1], &p.Bbox[2], &p.Bbox[3],
		&p.Visueltcenter[0], &p.Visueltcenter[1],
		&kmJSON,
	); err != nil {
		return nil, err
	}
	p.Nr = nr
	p.Navn = navn
	p.DagiID = dagiID
	p.Href = fmt.Sprintf("%s/postnumre/%s", baseURL, nr)
	p.GeoAendret = p.Aendret
	kommuner, err := buildKommuneRefs(kmJSON, baseURL)
	if err != nil {
		return nil, err
	}
	p.Kommuner = kommuner
	return &p, nil
}

// GetPostnummer returns the postnummer with the given nr, or pgx.ErrNoRows.
func GetPostnummer(ctx context.Context, pool *pgxpool.Pool, nr, baseURL string) (*Postnummer, error) {
	sql := "SELECT " + postnumreSelect + " FROM (" + postnumreGeom + " WHERE p.nr = $1) q"
	return scanPostnummer(pool.QueryRow(ctx, sql, nr), baseURL)
}

// ListPostnumre returns all postnumre ordered by nr (DAWA's default order).
func ListPostnumre(ctx context.Context, pool *pgxpool.Pool, baseURL string) ([]*Postnummer, error) {
	sql := "SELECT " + postnumreSelect + " FROM (" + postnumreGeom + ") q ORDER BY nr"
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Postnummer
	for rows.Next() {
		p, err := scanPostnummer(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
