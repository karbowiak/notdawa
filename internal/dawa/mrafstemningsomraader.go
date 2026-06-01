package dawa

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Menighedsraadsafstemningsomraade is the DAWA
// /menighedsraadsafstemningsomraader/{kommunekode}/{nummer} response. The Go
// struct field order IS the JSON key order (controls byte-exactness via
// MarshalDAWA) and matches the live DAWA enumeration exactly:
//
//	ændret, geo_version, geo_ændret, bbox, visueltcenter, href, dagi_id, nummer,
//	navn, kommune{href,kode,navn}, sogn{href,kode,navn}
//
// The single-resource path key is the composite (kommune kode, nummer): DAWA's
// href is /menighedsraadsafstemningsomraader/{kommunekode-unpadded}/{nummer}
// (e.g. .../101/1). geo_version is DAWA-internal and not derivable from the
// Datafordeler extract, so it renders null — the same documented non-substantive
// divergence as the other DAGI area resources.
type Menighedsraadsafstemningsomraade struct {
	Aendret       *string     `json:"ændret"`
	GeoVersion    *int        `json:"geo_version"`
	GeoAendret    *string     `json:"geo_ændret"`
	Bbox          [4]float64  `json:"bbox"`
	Visueltcenter [2]float64  `json:"visueltcenter"`
	Href          string      `json:"href"`
	DagiID        string      `json:"dagi_id"`
	Nummer        string      `json:"nummer"`
	Navn          string      `json:"navn"`
	Kommune       *KommuneRef `json:"kommune"`
	Sogn          *MrSogneRef `json:"sogn"`
}

// MrSogneRef is the nested sogn reference {href,kode,navn} embedded in a
// menighedsråd afstemningsområde.
type MrSogneRef struct {
	Href string `json:"href"`
	Kode string `json:"kode"`
	Navn string `json:"navn"`
}

func newMrSogneRef(baseURL string, kode, navn *string) *MrSogneRef {
	if kode == nil {
		return nil
	}
	var n string
	if navn != nil {
		n = *navn
	}
	return &MrSogneRef{
		Href: fmt.Sprintf("%s/sogne/%s", baseURL, *kode),
		Kode: *kode,
		Navn: n,
	}
}

// mrafstemningsomraadeSelect lists the scanned columns in scan order. bbox = the
// reprojected envelope of the area's own geom; visueltcenter from the stored
// (precomputed) column reprojected to 4326. nummer is taken straight from the
// stored column (fully populated in the extract); kommune comes from the area's
// own kommune_lokalid joined to dagi_kommuner by dagi_id, and sogn from
// sogn_lokalid joined to dagi_sogne by dagi_id with a spatial (ST_Covers on the
// visueltcenter) fallback for the handful of rows whose sogn_lokalid link is not
// present in the dagi_sogne extract.
const mrafstemningsomraadeSelect = `
	to_char(aendret AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS aendret,
	dagi_id, nummer, navn,
	round(ST_XMin(e)::numeric, 8)::float8, round(ST_YMin(e)::numeric, 8)::float8,
	round(ST_XMax(e)::numeric, 8)::float8, round(ST_YMax(e)::numeric, 8)::float8,
	round(ST_X(c)::numeric, 8)::float8,    round(ST_Y(c)::numeric, 8)::float8,
	komm_kode, komm_navn,
	sogn_kode, sogn_navn`

const mrafstemningsomraadeGeom = `
	SELECT m.aendret, m.dagi_id, m.nummer, m.navn,
		ST_Transform(ST_Envelope(m.geom), 4326) AS e,
		ST_Transform(m.visueltcenter, 4326) AS c,
		kk.kode AS komm_kode, kk.navn AS komm_navn,
		COALESCE(sj.kode, sp.kode) AS sogn_kode,
		COALESCE(sj.navn, sp.navn) AS sogn_navn
	FROM dagi_mrafstemningsomraader m
	LEFT JOIN dagi_kommuner kk ON kk.dagi_id = m.kommune_lokalid
	LEFT JOIN dagi_sogne sj ON sj.dagi_id = m.sogn_lokalid
	LEFT JOIN LATERAL (
		SELECT s.kode, s.navn FROM dagi_sogne s
		WHERE sj.dagi_id IS NULL AND ST_Covers(s.geom, m.visueltcenter)
		LIMIT 1
	) sp ON true`

func scanMrafstemningsomraade(row pgx.Row, baseURL string) (*Menighedsraadsafstemningsomraade, error) {
	var m Menighedsraadsafstemningsomraade
	var kommKode, kommNavn, sognKode, sognNavn *string
	if err := row.Scan(
		&m.Aendret, &m.DagiID, &m.Nummer, &m.Navn,
		&m.Bbox[0], &m.Bbox[1], &m.Bbox[2], &m.Bbox[3],
		&m.Visueltcenter[0], &m.Visueltcenter[1],
		&kommKode, &kommNavn, &sognKode, &sognNavn,
	); err != nil {
		return nil, err
	}
	m.GeoAendret = m.Aendret
	// nummer is rendered with leading zeros stripped ("01" -> "1"), matching DAWA.
	m.Nummer = stripZeros(m.Nummer)
	// href is keyed by the area's OWN kommune kode (unpadded) + nummer (unpadded).
	m.Href = fmt.Sprintf("%s/menighedsraadsafstemningsomraader/%s/%s", baseURL, stripZeros(deref(kommKode)), m.Nummer)
	m.Kommune = newKommuneRef(baseURL, kommKode, kommNavn)
	m.Sogn = newMrSogneRef(baseURL, sognKode, sognNavn)
	return &m, nil
}

// GetMrafstemningsomraade returns the menighedsråd afstemningsområde identified
// by its dagi_id, or pgx.ErrNoRows. (Used by the reverse path; the composite
// (kommunekode, nummer) also identifies it.)
func GetMrafstemningsomraade(ctx context.Context, pool *pgxpool.Pool, dagiID, baseURL string) (*Menighedsraadsafstemningsomraade, error) {
	sql := "SELECT " + mrafstemningsomraadeSelect + " FROM (" + mrafstemningsomraadeGeom + " WHERE m.dagi_id = $1) q"
	return scanMrafstemningsomraade(pool.QueryRow(ctx, sql, dagiID), baseURL)
}

// GetMrafstemningsomraadeByKommuneNummer returns the menighedsråd
// afstemningsområde identified by the composite (kommune kode, nummer) — DAWA's
// single-resource path key. kommunekode may be padded or not; it is matched
// against the area's own kommune kode (4-padded), and nummer is matched
// numerically (the path carries "1" for a stored "1"/"01"). Returns
// pgx.ErrNoRows when absent.
func GetMrafstemningsomraadeByKommuneNummer(ctx context.Context, pool *pgxpool.Pool, kommunekode, nummer, baseURL string) (*Menighedsraadsafstemningsomraade, error) {
	sql := "SELECT " + mrafstemningsomraadeSelect + " FROM (" + mrafstemningsomraadeGeom +
		" WHERE kk.kode = $1 AND m.nummer::int = $2::int) q"
	return scanMrafstemningsomraade(pool.QueryRow(ctx, sql, kode4(kommunekode), nummer), baseURL)
}

// ListMrafstemningsomraader returns all menighedsråd afstemningsområder ordered
// the way DAWA orders them: by kommune kode, then nummer numerically.
func ListMrafstemningsomraader(ctx context.Context, pool *pgxpool.Pool, baseURL string) ([]*Menighedsraadsafstemningsomraade, error) {
	sql := "SELECT " + mrafstemningsomraadeSelect + " FROM (" + mrafstemningsomraadeGeom + ") q ORDER BY komm_kode, nummer::int"
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Menighedsraadsafstemningsomraade
	for rows.Next() {
		m, err := scanMrafstemningsomraade(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ReverseMrafstemningsomraader returns ALL menighedsråd afstemningsområder
// covering (x,y), as a JSON array ordered by kommune kode then nummer. Unlike a
// regular afstemningsområde (where /reverse returns a single object because a
// point lies in exactly one), menighedsråd (parish-council) voting areas overlap
// — a single point belongs to several parishes — so live DAWA's
// /menighedsraadsafstemningsomraader/reverse returns an ARRAY of every covering
// area (verified against the live gateway: the point in central København
// returns Helligånds/1, Sankt Nikolaj/8 and Vor Frue-Vor Frelser/49). The result
// is an empty array when no area covers the point (DAWA returns [] for a
// no-match, not a 404, for this multi-feature reverse).
func ReverseMrafstemningsomraader(ctx context.Context, pool *pgxpool.Pool, x, y float64, baseURL string) ([]*Menighedsraadsafstemningsomraade, error) {
	sql := "SELECT " + mrafstemningsomraadeSelect + " FROM (" + mrafstemningsomraadeGeom +
		" WHERE ST_Covers(m.geom, ST_Transform(ST_SetSRID(ST_Point($1, $2), 4326), 25832))) q" +
		" ORDER BY komm_kode, nummer::int"
	rows, err := pool.Query(ctx, sql, x, y)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Menighedsraadsafstemningsomraade{}
	for rows.Next() {
		m, err := scanMrafstemningsomraade(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MrafstemningsomraadeAuto is one {tekst, menighedsrådsafstemningsområde}
// autocomplete element. tekst = the area navn (mirrors the afstemningsområde
// autocomplete's tekst=navn).
type MrafstemningsomraadeAuto struct {
	Tekst                            string                            `json:"tekst"`
	Menighedsraadsafstemningsomraade *Menighedsraadsafstemningsomraade `json:"menighedsrådsafstemningsområde"`
}

// AutocompleteMrafstemningsomraader returns {tekst, menighedsrådsafstemningsområde}
// elements whose navn matches q (mirrors AutocompleteAfstemningsomraader).
func AutocompleteMrafstemningsomraader(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*MrafstemningsomraadeAuto, error) {
	items, err := ListMrafstemningsomraader(ctx, pool, baseURL)
	if err != nil {
		return nil, err
	}
	out := []*MrafstemningsomraadeAuto{}
	for _, m := range items {
		if matchQ(m.Navn, q) {
			out = append(out, &MrafstemningsomraadeAuto{Tekst: m.Navn, Menighedsraadsafstemningsomraade: m})
		}
	}
	return limitTake(out, perSide, offset), nil
}
