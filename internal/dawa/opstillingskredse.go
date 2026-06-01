package dawa

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Opstillingskreds is the DAWA /opstillingskredse/{kode} response. Field order is
// significant: metadata trio, bbox, visueltcenter, href, dagi_id, nummer, kode,
// navn, valgkredsnummer, then the nested refs kredskommune/region/storkreds/
// valglandsdel and the kommuner[] list.
type Opstillingskreds struct {
	Aendret         *string          `json:"ændret"`
	GeoVersion      *int             `json:"geo_version"`
	GeoAendret      *string          `json:"geo_ændret"`
	Bbox            [4]float64       `json:"bbox"`
	Visueltcenter   [2]float64       `json:"visueltcenter"`
	Href            string           `json:"href"`
	DagiID          *string          `json:"dagi_id"`
	Nummer          string           `json:"nummer"`
	Kode            string           `json:"kode"`
	Navn            string           `json:"navn"`
	Valgkredsnummer *string          `json:"valgkredsnummer"`
	Kredskommune    *KommuneRef      `json:"kredskommune"`
	Region          *RegionRef       `json:"region"`
	Storkreds       *StorkredsRef    `json:"storkreds"`
	Valglandsdel    *ValglandsdelRef `json:"valglandsdel"`
	Kommuner        []KommuneRef     `json:"kommuner"`
}

// opstillingskredseSelect lists the scanned columns. bbox = reprojected envelope
// of the opstillingskreds's own geom; visueltcenter = precomputed polylabel point.
const opstillingskredseSelect = `
	to_char(aendret AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS aendret,
	dagi_id, nummer, navn, valgkredsnummer,
	round(ST_XMin(e)::numeric, 8)::float8, round(ST_YMin(e)::numeric, 8)::float8,
	round(ST_XMax(e)::numeric, 8)::float8, round(ST_YMax(e)::numeric, 8)::float8,
	round(ST_X(c)::numeric, 8)::float8,    round(ST_Y(c)::numeric, 8)::float8,
	kk_kode, kk_navn, region_kode, region_navn,
	stork_nummer, stork_navn, vl_bogstav, vl_navn,
	kommuner_json`

// opstillingskredseGeom resolves all five nested refs:
//   - kredskommune via the static opstillingskreds_kredskommune map → dagi_kommuner
//   - region via the kredskommune's region_lokalid → dagi_regioner
//   - storkreds via storkreds_lokalid → dagi_storkredse (nummer/navn)
//   - valglandsdel via the storkreds's valglandsdelsbogstav → dagi_valglandsdele
//   - kommuner[] from the opstillingskreds_kommuner relation (ordered by kode)
const opstillingskredseGeom = `
	SELECT o.aendret, o.dagi_id, o.nummer, o.navn, o.valgkredsnummer,
		ST_Transform(ST_Envelope(o.geom), 4326) AS e,
		ST_Transform(o.visueltcenter, 4326) AS c,
		kk.kode AS kk_kode, kk.navn AS kk_navn,
		reg.kode AS region_kode, reg.navn AS region_navn,
		st.nummer AS stork_nummer, st.navn AS stork_navn,
		st.valglandsdelsbogstav AS vl_bogstav, vl.navn AS vl_navn,
		km.j AS kommuner_json
	FROM dagi_opstillingskredse o
	LEFT JOIN opstillingskreds_kredskommune okk ON okk.nummer = o.nummer
	LEFT JOIN dagi_kommuner kk ON kk.kode = okk.kommunekode
	LEFT JOIN dagi_regioner reg ON reg.dagi_id = kk.region_lokalid
	LEFT JOIN dagi_storkredse st ON st.dagi_id = o.storkreds_lokalid
	LEFT JOIN dagi_valglandsdele vl ON vl.bogstav = st.valglandsdelsbogstav
	LEFT JOIN LATERAL (
		SELECT COALESCE(json_agg(json_build_object('kode', k.kode, 'navn', k.navn) ORDER BY ok.kommunekode), '[]') AS j
		FROM opstillingskreds_kommuner ok
		JOIN dagi_kommuner k ON k.kode = ok.kommunekode
		WHERE ok.opstillingskredsnummer = o.nummer
	) km ON true`

func scanOpstillingskreds(row pgx.Row, baseURL string) (*Opstillingskreds, error) {
	var o Opstillingskreds
	var dagiID, valgkreds *string
	var kkKode, kkNavn, regKode, regNavn, storkNummer, storkNavn, vlBogstav, vlNavn *string
	var kommunerJSON []byte
	if err := row.Scan(
		&o.Aendret, &dagiID, &o.Nummer, &o.Navn, &valgkreds,
		&o.Bbox[0], &o.Bbox[1], &o.Bbox[2], &o.Bbox[3],
		&o.Visueltcenter[0], &o.Visueltcenter[1],
		&kkKode, &kkNavn, &regKode, &regNavn,
		&storkNummer, &storkNavn, &vlBogstav, &vlNavn,
		&kommunerJSON,
	); err != nil {
		return nil, err
	}
	o.DagiID = dagiID
	o.GeoAendret = o.Aendret
	o.Kode = padKode4(o.Nummer)
	// DAWA's href uses the unpadded nummer (e.g. /opstillingskredse/1), even
	// though the resource is fetchable by the 4-padded kode.
	o.Href = fmt.Sprintf("%s/opstillingskredse/%s", baseURL, o.Nummer)
	o.Valgkredsnummer = valgkreds
	o.Kredskommune = newKommuneRef(baseURL, kkKode, kkNavn)
	o.Region = newRegionRef(baseURL, regKode, regNavn)
	o.Storkreds = newStorkredsRef(baseURL, storkNummer, storkNavn)
	o.Valglandsdel = newValglandsdelRef(baseURL, vlBogstav, vlNavn)
	kommuner, err := buildKommuneRefs(kommunerJSON, baseURL)
	if err != nil {
		return nil, err
	}
	o.Kommuner = kommuner
	return &o, nil
}

// padKode4 zero-pads a numeric string to 4 digits (e.g. "1" -> "0001").
func padKode4(nummer string) string {
	if len(nummer) >= 4 {
		return nummer
	}
	return "0000"[:4-len(nummer)] + nummer
}

// GetOpstillingskreds returns the opstillingskreds with the given kode (4-digit
// zero-padded) or nummer, or pgx.ErrNoRows.
func GetOpstillingskreds(ctx context.Context, pool *pgxpool.Pool, kode, baseURL string) (*Opstillingskreds, error) {
	nummer := kode
	for len(nummer) > 1 && nummer[0] == '0' {
		nummer = nummer[1:]
	}
	sql := "SELECT " + opstillingskredseSelect + " FROM (" + opstillingskredseGeom + " WHERE o.nummer = $1) q"
	return scanOpstillingskreds(pool.QueryRow(ctx, sql, nummer), baseURL)
}

// ListOpstillingskredse returns all opstillingskredse ordered by kode (nummer
// numerically, which matches 4-digit kode ordering).
func ListOpstillingskredse(ctx context.Context, pool *pgxpool.Pool, baseURL string) ([]*Opstillingskreds, error) {
	sql := "SELECT " + opstillingskredseSelect + " FROM (" + opstillingskredseGeom + ") q ORDER BY nummer::int"
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Opstillingskreds
	for rows.Next() {
		o, err := scanOpstillingskreds(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
