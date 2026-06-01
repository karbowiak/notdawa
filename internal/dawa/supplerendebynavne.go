package dawa

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SupplerendeBynavn is the DAWA /supplerendebynavne2/{dagi_id} response. Field
// order: metadata trio, bbox, visueltcenter, href, dagi_id, navn, darstatus(int),
// kommune ref, postnumre[]. The path segment is 'supplerendebynavne2' (with 2).
type SupplerendeBynavn struct {
	Aendret       *string     `json:"ændret"`
	GeoVersion    *int        `json:"geo_version"`
	GeoAendret    *string     `json:"geo_ændret"`
	Bbox          [4]float64  `json:"bbox"`
	Visueltcenter [2]float64  `json:"visueltcenter"`
	Href          string      `json:"href"`
	DagiID        string      `json:"dagi_id"`
	Navn          string      `json:"navn"`
	Darstatus     int         `json:"darstatus"`
	Kommune       *KommuneRef `json:"kommune"`
	Postnumre     []PostnrRef `json:"postnumre"`
}

const supplerendeBynavnSelect = `
	to_char(aendret AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS aendret,
	dagi_id, navn, darstatus,
	round(ST_XMin(e)::numeric, 8)::float8, round(ST_YMin(e)::numeric, 8)::float8,
	round(ST_XMax(e)::numeric, 8)::float8, round(ST_YMax(e)::numeric, 8)::float8,
	round(ST_X(c)::numeric, 8)::float8,    round(ST_Y(c)::numeric, 8)::float8,
	kommune_kode, kommune_navn, j`

// supplerendeBynavnGeom resolves the kommune ref (via kommune_lokalid →
// dagi_kommuner) and aggregates the address-derived postnumre[] (distinct
// postnumre of the bynavn's husnumre via the DAR join UUID), ordered by nr.
const supplerendeBynavnGeom = `
	SELECT sb.aendret, sb.dagi_id, sb.navn, sb.darstatus,
		ST_Transform(ST_Envelope(sb.geom), 4326) AS e,
		ST_Transform(sb.visueltcenter, 4326) AS c,
		k.kode AS kommune_kode, k.navn AS kommune_navn,
		ps.j
	FROM dagi_supplerendebynavne sb
	LEFT JOIN dagi_kommuner k ON k.dagi_id = sb.kommune_lokalid
	LEFT JOIN LATERAL (
		SELECT COALESCE(json_agg(json_build_object('nr', t.postnr, 'navn', t.navn) ORDER BY t.postnr), '[]') AS j
		FROM (
			SELECT DISTINCT p.postnr, p.navn
			FROM dar_husnummer h
			JOIN dar_postnummer p ON p.id = h.postnummer_id
			WHERE h.supplerende_bynavn = sb.dar_uuid
		) t
	) ps ON true`

func scanSupplerendeBynavn(row pgx.Row, baseURL string) (*SupplerendeBynavn, error) {
	var s SupplerendeBynavn
	var dagiID, navn string
	var darstatus *int
	var komKode, komNavn *string
	var psJSON []byte
	if err := row.Scan(
		&s.Aendret, &dagiID, &navn, &darstatus,
		&s.Bbox[0], &s.Bbox[1], &s.Bbox[2], &s.Bbox[3],
		&s.Visueltcenter[0], &s.Visueltcenter[1],
		&komKode, &komNavn, &psJSON,
	); err != nil {
		return nil, err
	}
	s.DagiID = dagiID
	s.Navn = navn
	if darstatus != nil {
		s.Darstatus = *darstatus
	}
	s.GeoAendret = s.Aendret
	s.Href = fmt.Sprintf("%s/supplerendebynavne2/%s", baseURL, dagiID)
	s.Kommune = newKommuneRef(baseURL, komKode, komNavn)
	postnumre, err := buildPostnrRefs(psJSON, baseURL)
	if err != nil {
		return nil, err
	}
	s.Postnumre = postnumre
	return &s, nil
}

// GetSupplerendeBynavn returns the bynavn with the given dagi_id, or pgx.ErrNoRows.
func GetSupplerendeBynavn(ctx context.Context, pool *pgxpool.Pool, dagiID, baseURL string) (*SupplerendeBynavn, error) {
	sql := "SELECT " + supplerendeBynavnSelect + " FROM (" + supplerendeBynavnGeom + " WHERE sb.dagi_id = $1) q"
	return scanSupplerendeBynavn(pool.QueryRow(ctx, sql, dagiID), baseURL)
}

// ReverseSupplerendeBynavn returns the FULL supplerende bynavn representation
// whose geometry COVERS the WGS84 point (x=lon, y=lat) — DAWA's
// /supplerendebynavne2/reverse?x=&y= (ST_Covers point-in-polygon). The body is
// byte-identical to the single-GET (GetSupplerendeBynavn) shape. Returns
// pgx.ErrNoRows when no bynavn covers the point (rendered as DAWA's 404).
func ReverseSupplerendeBynavn(ctx context.Context, pool *pgxpool.Pool, x, y float64, baseURL string) (*SupplerendeBynavn, error) {
	dagiID, err := reverseKey(ctx, pool, "dagi_supplerendebynavne", "dagi_id", x, y)
	if err != nil {
		return nil, err
	}
	return GetSupplerendeBynavn(ctx, pool, dagiID, baseURL)
}

// ListSupplerendebynavne returns all bynavne ordered by dagi_id. limit <= 0 = all.
func ListSupplerendebynavne(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit int) ([]*SupplerendeBynavn, error) {
	return ListSupplerendebynavneFiltered(ctx, pool, baseURL, limit, 0, ListFilter{})
}

// SupplerendeBynavnV1 is the DAWA /supplerendebynavne (v1, deprecated) element.
// It is a SIMPLER shape than /supplerendebynavne2: no metadata/bbox/visueltcenter/
// dagi_id/darstatus, just {href, navn, postnumre[], kommuner[]}. Field order is
// exactly href, navn, postnumre, kommuner. The href points to the v2 resource
// (/supplerendebynavne2/{navn}), matching the live API.
type SupplerendeBynavnV1 struct {
	Href      string       `json:"href"`
	Navn      string       `json:"navn"`
	Postnumre []PostnrRef  `json:"postnumre"`
	Kommuner  []KommuneRef `json:"kommuner"`
}

// supplerendeBynavnV1Query selects each distinct supplerende bynavn NAME (not per
// dagi row) with its address-derived postnumre[] and kommuner[], ordered
// alphabetically by navn to match DAWA. A navn can map to several dagi rows
// (several dar_uuid) — e.g. "Strandby" has 3 — and DAWA collapses them into ONE
// element whose postnumre/kommuner are MERGED across every address of every
// dar_uuid bearing that navn. So the source is DISTINCT navn, and the aggregates
// join husnumre via every dar_uuid that shares the navn. A navn with no addresses
// at all gets empty arrays — exactly as DAWA emits.
//
// The DISTINCT navn set comes from dar_supplerendebynavn (the DAR feed's names,
// ingested in full), NOT dagi_supplerendebynavne: the latter only carries the
// ~6503 geometry-bearing names, while DAWA's v1 list has 6552 — the extra ~49 are
// address-less names that exist only in the DAR register (no DAGI polygon, no
// addresses → empty arrays). dar_supplerendebynavn.navn across all DAR statuses is
// exactly DAWA's 6552-name set. The postnumre/kommuner LATERALs still join via the
// dagi_supplerendebynavne dar_uuid graph, so address-less names naturally get [].
const supplerendeBynavnV1Query = `
	SELECT sb.navn, ps.pj, ks.kj
	FROM (SELECT DISTINCT navn FROM dar_supplerendebynavn) sb
	LEFT JOIN LATERAL (
		SELECT COALESCE(json_agg(json_build_object('nr', t.postnr, 'navn', t.navn) ORDER BY t.postnr), '[]') AS pj
		FROM (
			SELECT DISTINCT p.postnr, p.navn
			FROM dagi_supplerendebynavne d
			JOIN dar_husnummer h ON h.supplerende_bynavn = d.dar_uuid
			JOIN dar_postnummer p ON p.id = h.postnummer_id
			WHERE d.navn = sb.navn AND h.status = '3'
		) t
	) ps ON true
	LEFT JOIN LATERAL (
		SELECT COALESCE(json_agg(json_build_object('kode', t.kode, 'navn', t.navn) ORDER BY t.kode), '[]') AS kj
		FROM (
			-- The bynavn's kommuner are the kommuner of its ADDRESSES, so a name
			-- with no addresses gets [] — matching DAWA, which pairs empty kommuner
			-- with empty postnumre. dar_husnummer.kommune holds the DAGI kommune
			-- id_lokalId (NOT a kommunekode), joined to dagi_kommuner.dagi_id.
			-- Only status='3' (active) husnumre count: a handful of stray
			-- status='gældende' rows carry a different (stale) kommune lokalId that
			-- DAWA does not surface.
			SELECT DISTINCT k.kode, k.navn
			FROM dagi_supplerendebynavne d
			JOIN dar_husnummer h ON h.supplerende_bynavn = d.dar_uuid
			JOIN dagi_kommuner k ON k.dagi_id = h.kommune
			WHERE d.navn = sb.navn AND h.status = '3'
		) t
	) ks ON true
	-- DAWA's v1 collection is ordered by raw Unicode CODE POINT, not by a locale
	-- collation: 'Aa' stays literal at the head and æ(U+00E6) < ø(U+00F8) sort
	-- after z, giving Aa < Aabybro < Aabylund < Aabæk < Aabølling. COLLATE "C"
	-- (byte order over UTF-8) reproduces that exactly; the en_US.utf8 DB default
	-- would fold æ/ø to a/o and reorder them, and da-DK-x-icu would contract Aa->Å
	-- and send the whole Aa-group to the tail.
	ORDER BY sb.navn COLLATE "C"`

// ListSupplerendebynavneV1 returns the deprecated /supplerendebynavne (v1)
// collection: distinct bynavne ordered alphabetically by navn, each with its
// postnumre[] and kommuner[]. limit <= 0 = all; offset for paging.
func ListSupplerendebynavneV1(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit, offset int) ([]*SupplerendeBynavnV1, error) {
	sql := appendLimitOffset(supplerendeBynavnV1Query, limit, offset)
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SupplerendeBynavnV1
	for rows.Next() {
		var navn string
		var pjJSON, kjJSON []byte
		if err := rows.Scan(&navn, &pjJSON, &kjJSON); err != nil {
			return nil, err
		}
		postnumre, err := buildPostnrRefs(pjJSON, baseURL)
		if err != nil {
			return nil, err
		}
		if postnumre == nil {
			postnumre = []PostnrRef{}
		}
		kommuner, err := buildV1KommuneRefs(kjJSON, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, &SupplerendeBynavnV1{
			Href:      fmt.Sprintf("%s/supplerendebynavne2/%s", baseURL, url.PathEscape(navn)),
			Navn:      navn,
			Postnumre: postnumre,
			Kommuner:  kommuner,
		})
	}
	return out, rows.Err()
}

// buildV1KommuneRefs decodes a json_agg of {kode,navn} kommune rows into the
// KommuneRef slice DAWA emits in the v1 supplerendebynavn element. A nil/empty
// aggregate yields a non-nil empty slice so the JSON renders [] (never null),
// matching the live API for bynavne with no addresses.
func buildV1KommuneRefs(raw []byte, baseURL string) ([]KommuneRef, error) {
	out := []KommuneRef{}
	if len(raw) == 0 {
		return out, nil
	}
	var rows []struct {
		Kode string `json:"kode"`
		Navn string `json:"navn"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	for _, k := range rows {
		out = append(out, KommuneRef{
			Href: fmt.Sprintf("%s/kommuner/%s", baseURL, k.Kode),
			Kode: k.Kode,
			Navn: k.Navn,
		})
	}
	return out, nil
}

// ListSupplerendebynavneFiltered is ListSupplerendebynavne with SQL-side q= over
// the navn, srid output reprojection and an optional spatial filter (against the
// bynavn geometry), plus offset paging. The q/spatial predicates are pushed into
// the inner geometry source (where sb.geom/sb.navn are in scope). A zero filter
// reproduces ListSupplerendebynavne byte-for-byte.
func ListSupplerendebynavneFiltered(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit, offset int, f ListFilter) ([]*SupplerendeBynavn, error) {
	var wb whereBuilder
	wb.addQ(f.Q, "sb.navn")
	if f.Spatial != nil {
		wb.addSpatial(f.Spatial, "sb.geom")
	}

	inner := f.applySRID(supplerendeBynavnGeom)
	if w := wb.sql(); w != "" {
		inner += " WHERE " + w
	}
	sql := "SELECT " + supplerendeBynavnSelect + " FROM (" + inner + ") q ORDER BY dagi_id"
	sql = appendLimitOffset(sql, limit, offset)

	rows, err := pool.Query(ctx, sql, wb.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SupplerendeBynavn
	for rows.Next() {
		s, err := scanSupplerendeBynavn(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
