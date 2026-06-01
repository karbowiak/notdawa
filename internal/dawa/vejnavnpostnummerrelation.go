package dawa

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// VejnavnPostnummerRelation is the DAWA /vejnavnpostnummerrelationer element: one
// row per distinct (postnr, vejnavn) pair. Field order is significant (Go struct
// order drives JSON key order to match DAWA byte-for-byte).
//
//	betegnelse    = "<vejnavn>, <postnr> <postnavn>"
//	postnummer    = {href, nr, navn}
//	bbox          = ST_Envelope of the relation geom (25832) reprojected 25832→4326;
//	                NULL when the relation has no geometry (address-only relations)
//	                OR when the envelope degenerates to a non-polygon (a single
//	                straight line clip), exactly as DAWA's GEOMETRY(Polygon) bbox
//	                column goes NULL in those cases. Hence *[4]float64.
//	visueltcenter = ST_ClosestPoint(geom, ST_Centroid(geom)) reprojected 25832→4326;
//	                NULL only when the relation has no geometry. Hence *[2]float64.
//	kommuner[]    = the distinct kommuner the group's roads belong to, ordered by kode.
type VejnavnPostnummerRelation struct {
	Betegnelse    string       `json:"betegnelse"`
	Href          string       `json:"href"`
	Vejnavn       string       `json:"vejnavn"`
	Postnummer    VnprPostnr   `json:"postnummer"`
	Bbox          *[4]float64  `json:"bbox"`
	Visueltcenter *[2]float64  `json:"visueltcenter"`
	Kommuner      []KommuneRef `json:"kommuner"`
}

// VnprPostnr is the nested postnummer reference in a VNPR element: {href, nr,
// navn}. (Distinct from PostnrRef, whose navn is *string — here navn is always
// present in the captured shape but kept nullable for robustness.)
type VnprPostnr struct {
	Href string  `json:"href"`
	Nr   string  `json:"nr"`
	Navn *string `json:"navn"`
}

// vnprCTE is DAWA's navngivenvejpostnummerrelation derivation, then aggregated by
// vejnavn — a faithful port of DAWA's own SQL
// (psql/schema/navngivenvejpostnummerrelation.sql + vejnavnpostnummerrelation.sql).
//
// Per (navngivenvej_id, postnr) the relation geom is the ST_Union of three sources:
//
//	geo  : postnr polygon ∩ road geom, for postnumre that are NOT gadepostnumre
//	       (NOT 1000..1999) where the clipped length (line) or area (område) > 7.
//	addr : DISTINCT (navngivenvej, postnr) from the husnummer→postnr assignment
//	       (status-3 husnumre). geom = NULL — this is what makes a relation exist
//	       even where geo gives nothing (and is the ONLY membership for gadepostnumre,
//	       which the geo branch excludes — e.g. 1050 → Kongens Nytorv only).
//	gade : for the addr relations whose postnr IS a gadepostnummer (1000..1999),
//	       recompute the road∩postnr clip so gadepostnumre still get geometry when
//	       an address ties the road to them.
//
// The served table aggregates by vejnavn (text): geom = ST_Union over all
// same-named navngivenveje + postnr. agg exposes (vejnavn, postnr, postnavn, geom);
// bbox / visueltcenter / kommuner are computed in vnprSelect over agg.geom.
//
// NOTE: our dar_navngivenvej.geom is currently MultiLineString only (the ingest
// reads vejnavnebeliggenhed_vejnavnelinje); when the område column is also ingested
// the same COALESCE(linje, område) DAWA uses kicks in here automatically (the geo
// branch's >7 test already covers the ST_Area side). DAWA intersects against a
// tiled dagi_postnumre_divided purely for speed; intersecting dagi_postnumre
// directly yields the identical membership/geometry.
const vnprCTE = `
	WITH geo AS (
		SELECT nv.id AS nvid, gp.nr::int AS postnr, ST_Intersection(gp.geom, nv.geom) AS geom
		FROM dar_navngivenvej nv
		JOIN dagi_postnumre gp ON ST_Intersects(gp.geom, nv.geom)
		WHERE nv.status = '3' AND nv.navn IS NOT NULL AND nv.geom IS NOT NULL
		  AND NOT (gp.nr::int BETWEEN 1000 AND 1999)
		  AND ST_Length(ST_Intersection(nv.geom, gp.geom)) > 7
	),
	addr AS (
		SELECT DISTINCT h.navngivenvej AS nvid, p.postnr::int AS postnr
		FROM dar_husnummer h
		JOIN dar_postnummer p ON h.postnummer_id = p.id
		WHERE h.status = '3'
	),
	gade AS (
		SELECT a.nvid, a.postnr, ST_Intersection(gp.geom, nv.geom) AS geom
		FROM addr a
		JOIN dagi_postnumre gp ON a.postnr = gp.nr::int
		JOIN dar_navngivenvej nv ON a.nvid = nv.id AND nv.status = '3' AND nv.geom IS NOT NULL
		WHERE a.postnr BETWEEN 1000 AND 1999
	),
	u AS (
		SELECT nvid, postnr, geom FROM geo
		UNION SELECT nvid, postnr, NULL::geometry(Geometry, 25832) FROM addr
		UNION SELECT nvid, postnr, geom FROM gade
	),
	perrel AS (
		SELECT nvid, postnr,
		       CASE WHEN ST_IsEmpty(ST_Union(geom)) THEN NULL ELSE ST_Union(geom) END AS geom
		FROM u GROUP BY nvid, postnr
	),
	agg AS (
		SELECT nv.navn AS vejnavn, r.postnr, ST_Union(r.geom) AS geom
		FROM perrel r
		JOIN dar_navngivenvej nv ON r.nvid = nv.id
		WHERE nv.navn IS NOT NULL
		GROUP BY nv.navn, r.postnr
	)`

// vnprSelect lists the scanned columns in scan order over the agg CTE joined to
// dar_postnummer for the canonical zero-padded postnr string and postnavn.
//
// bbox uses DAWA's exact idiom: the 25832 envelope corners transformed to 4326 (NOT
// transform-then-envelope, which rotates the box). bbox is NULL when the envelope is
// not a polygon (degenerate single-line clip) or when geom is NULL — mirroring
// DAWA's GEOMETRY(Polygon) bbox column. visueltcenter is ST_ClosestPoint(geom,
// Centroid) (DAWA's visueltCenterDerived; polylabel would only apply to polygon
// geoms, of which we currently serve none). kommuner[] is a correlated scalar
// subquery scoped to THIS relation: the distinct kommuner (via
// dar_navngivenvej_kommunedel) of the navngivenveje that actually contribute to
// (agg.vejnavn, agg.postnr) — i.e. the perrel membership for that postnr restricted
// to navn = agg.vejnavn. Filtering by postnr too (not just vejnavn) is essential: a
// vejnavn like "Nyhavn" exists in several kommuner nationally, but the 1051 relation
// belongs to only one.
const vnprSelect = `
	p.postnr, p.navn AS postnavn, agg.vejnavn,
	CASE WHEN ST_GeometryType(ST_Envelope(agg.geom)) = 'ST_Polygon'
	     THEN round(ST_XMin(ST_Transform(ST_Envelope(agg.geom), 4326))::numeric, 8)::float8 END,
	CASE WHEN ST_GeometryType(ST_Envelope(agg.geom)) = 'ST_Polygon'
	     THEN round(ST_YMin(ST_Transform(ST_Envelope(agg.geom), 4326))::numeric, 8)::float8 END,
	CASE WHEN ST_GeometryType(ST_Envelope(agg.geom)) = 'ST_Polygon'
	     THEN round(ST_XMax(ST_Transform(ST_Envelope(agg.geom), 4326))::numeric, 8)::float8 END,
	CASE WHEN ST_GeometryType(ST_Envelope(agg.geom)) = 'ST_Polygon'
	     THEN round(ST_YMax(ST_Transform(ST_Envelope(agg.geom), 4326))::numeric, 8)::float8 END,
	CASE WHEN agg.geom IS NOT NULL
	     THEN round(ST_X(ST_Transform(ST_ClosestPoint(agg.geom, ST_Centroid(agg.geom)), 4326))::numeric, 8)::float8 END,
	CASE WHEN agg.geom IS NOT NULL
	     THEN round(ST_Y(ST_Transform(ST_ClosestPoint(agg.geom, ST_Centroid(agg.geom)), 4326))::numeric, 8)::float8 END,
	(
		SELECT COALESCE(json_agg(t ORDER BY t.kode), '[]')
		FROM (
			SELECT DISTINCT kd.kommune AS kode, k.navn
			FROM perrel r
			JOIN dar_navngivenvej nv2 ON nv2.id = r.nvid AND nv2.status = '3' AND nv2.navn = agg.vejnavn
			JOIN dar_navngivenvej_kommunedel kd ON kd.navngivenvej = nv2.id AND kd.status = '3'
			LEFT JOIN dagi_kommuner k ON k.kode = kd.kommune
			WHERE r.postnr = agg.postnr
		) t
	) AS kommuner`

// vnprFrom joins the agg CTE to dar_postnummer for the canonical 4-digit postnr
// string + postnavn (DISTINCT collapses dar_postnummer's per-status duplicates).
const vnprFrom = `
	FROM agg
	JOIN (SELECT DISTINCT postnr, navn FROM dar_postnummer) p ON p.postnr::int = agg.postnr`

// vnprOrderBy is DAWA's served key order: (postnr, vejnavn).
const vnprOrderBy = ` ORDER BY agg.postnr, agg.vejnavn`

// vnprKommuneRaw is the json_agg intermediate for the kommuner[] aggregate; the
// href is built in Go.
type vnprKommuneRaw struct {
	Kode string  `json:"kode"`
	Navn *string `json:"navn"`
}

func scanVNPR(row pgx.Row, baseURL string) (*VejnavnPostnummerRelation, error) {
	var v VejnavnPostnummerRelation
	var postnr, vejnavn string
	var postnavn *string
	var bx0, bx1, bx2, bx3, vx, vy *float64
	var komJSON []byte
	if err := row.Scan(
		&postnr, &postnavn, &vejnavn,
		&bx0, &bx1, &bx2, &bx3, &vx, &vy,
		&komJSON,
	); err != nil {
		return nil, err
	}

	v.Vejnavn = vejnavn
	pn := ""
	if postnavn != nil {
		pn = *postnavn
	}
	v.Betegnelse = fmt.Sprintf("%s, %s %s", vejnavn, postnr, pn)
	v.Href = fmt.Sprintf("%s/vejnavnpostnummerrelationer/%s/%s", baseURL, postnr, url.PathEscape(vejnavn))
	v.Postnummer = VnprPostnr{
		Href: fmt.Sprintf("%s/postnumre/%s", baseURL, postnr),
		Nr:   postnr,
		Navn: postnavn,
	}
	if bx0 != nil && bx1 != nil && bx2 != nil && bx3 != nil {
		v.Bbox = &[4]float64{*bx0, *bx1, *bx2, *bx3}
	}
	if vx != nil && vy != nil {
		v.Visueltcenter = &[2]float64{*vx, *vy}
	}

	kom, err := buildVNPRKommuner(komJSON, baseURL)
	if err != nil {
		return nil, err
	}
	v.Kommuner = kom
	return &v, nil
}

// buildVNPRKommuner turns the aggregated [{kode,navn}] JSON into KommuneRef[],
// always non-nil so it renders as [] rather than null.
func buildVNPRKommuner(js []byte, baseURL string) ([]KommuneRef, error) {
	refs := []KommuneRef{}
	if len(js) == 0 {
		return refs, nil
	}
	var raw []vnprKommuneRaw
	if err := json.Unmarshal(js, &raw); err != nil {
		return nil, fmt.Errorf("parse vnpr kommuner: %w", err)
	}
	for _, r := range raw {
		navn := ""
		if r.Navn != nil {
			navn = *r.Navn
		}
		refs = append(refs, KommuneRef{
			Href: fmt.Sprintf("%s/kommuner/%s", baseURL, r.Kode),
			Kode: r.Kode,
			Navn: navn,
		})
	}
	return refs, nil
}

// GetVejnavnPostnummerRelation returns the relation for a (postnr, vejnavn) pair,
// or pgx.ErrNoRows. vejnavn is the decoded street name (the handler URL-decodes
// the path segment).
func GetVejnavnPostnummerRelation(ctx context.Context, pool *pgxpool.Pool, postnr, vejnavn, baseURL string) (*VejnavnPostnummerRelation, error) {
	sql := vnprCTE + " SELECT " + vnprSelect + vnprFrom +
		" WHERE agg.postnr = $1::int AND agg.vejnavn = $2"
	return scanVNPR(pool.QueryRow(ctx, sql, postnr, vejnavn), baseURL)
}

// ListVejnavnPostnummerRelationer returns relations ordered by (postnr, vejnavn).
// limit <= 0 returns all. Filtered variant below threads the cross-cutting params.
func ListVejnavnPostnummerRelationer(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit, offset int) ([]*VejnavnPostnummerRelation, error) {
	return ListVejnavnPostnummerRelationerFiltered(ctx, pool, baseURL, limit, offset, ListFilter{})
}

// ListVejnavnPostnummerRelationerFiltered is the List with SQL-side per-field
// filters (postnr, vejnavn) and q= over vejnavn/postnavn, plus offset paging. A
// zero filter reproduces the unfiltered List byte-for-byte. srid threads through
// applySRID to reproject the output bbox/visueltcenter.
func ListVejnavnPostnummerRelationerFiltered(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit, offset int, f ListFilter) ([]*VejnavnPostnummerRelation, error) {
	var wb whereBuilder
	if v, ok := f.Filters["postnr"]; ok {
		wb.addCode("agg.postnr::text", v)
	}
	if v, ok := f.Filters["vejnavn"]; ok {
		wb.addEq("agg.vejnavn", v)
	}
	wb.addQ(f.Q, "agg.vejnavn", "p.navn")

	sql := vnprCTE + " SELECT " + f.applySRID(vnprSelect+vnprFrom)
	if w := wb.sql(); w != "" {
		sql += " WHERE " + w
	}
	sql += vnprOrderBy
	sql = appendLimitOffset(sql, limit, offset)

	rows, err := pool.Query(ctx, sql, wb.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*VejnavnPostnummerRelation
	for rows.Next() {
		v, err := scanVNPR(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// VnprAutocomplete is one /vejnavnpostnummerrelationer/autocomplete element:
// {tekst, vejnavnpostnummerrelation:{vejnavn, postnr, postnrnavn,
// visueltcenter_x, visueltcenter_y, href}}. Field order matches the live capture.
type VnprAutocomplete struct {
	Tekst                     string             `json:"tekst"`
	VejnavnPostnummerRelation VnprAutocompleteEl `json:"vejnavnpostnummerrelation"`
}

// VnprAutocompleteEl is the nested autocomplete payload.
type VnprAutocompleteEl struct {
	Vejnavn        string   `json:"vejnavn"`
	Postnr         string   `json:"postnr"`
	Postnrnavn     *string  `json:"postnrnavn"`
	VisueltcenterX *float64 `json:"visueltcenter_x"`
	VisueltcenterY *float64 `json:"visueltcenter_y"`
	Href           string   `json:"href"`
}

// AutocompleteVejnavnPostnummerRelationer returns autocomplete elements for a q=
// substring of the betegnelse (vejnavn / postnavn folded), ordered by (postnr,
// vejnavn) — the resource's default order. DAWA's proprietary relevance rank may
// pick a different top-N, but each returned element is individually correct
// (documented non-substantive ordering class).
func AutocompleteVejnavnPostnummerRelationer(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*VnprAutocomplete, error) {
	var wb whereBuilder
	wb.addQ(q, "agg.vejnavn", "p.navn")

	sel := `
		p.postnr, p.navn AS postnavn, agg.vejnavn,
		CASE WHEN agg.geom IS NOT NULL
		     THEN round(ST_X(ST_Transform(ST_ClosestPoint(agg.geom, ST_Centroid(agg.geom)), 4326))::numeric, 8)::float8 END,
		CASE WHEN agg.geom IS NOT NULL
		     THEN round(ST_Y(ST_Transform(ST_ClosestPoint(agg.geom, ST_Centroid(agg.geom)), 4326))::numeric, 8)::float8 END`
	sql := vnprCTE + " SELECT " + sel + vnprFrom
	if w := wb.sql(); w != "" {
		sql += " WHERE " + w
	}
	sql += vnprOrderBy
	sql = appendLimitOffset(sql, perSide, offset)

	rows, err := pool.Query(ctx, sql, wb.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*VnprAutocomplete
	for rows.Next() {
		var postnr, vejnavn string
		var postnavn *string
		var vx, vy *float64
		if err := rows.Scan(&postnr, &postnavn, &vejnavn, &vx, &vy); err != nil {
			return nil, err
		}
		pn := ""
		if postnavn != nil {
			pn = *postnavn
		}
		out = append(out, &VnprAutocomplete{
			Tekst: fmt.Sprintf("%s, %s %s", vejnavn, postnr, pn),
			VejnavnPostnummerRelation: VnprAutocompleteEl{
				Vejnavn:        vejnavn,
				Postnr:         postnr,
				Postnrnavn:     postnavn,
				VisueltcenterX: vx,
				VisueltcenterY: vy,
				Href:           fmt.Sprintf("%s/vejnavnpostnummerrelationer/%s/%s", baseURL, postnr, url.PathEscape(vejnavn)),
			},
		})
	}
	return out, rows.Err()
}
