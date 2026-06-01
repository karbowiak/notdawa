package dawa

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sted is the DAWA /steder/{id} response (a Danske Stednavne place). Key order is
// fixed to match DAWA byte-for-byte: identity + type, the primary name + its
// status, the DAWA-internal metadata trio, href, the (always-empty here)
// egenskaber object, visueltcenter, bbox (null for point places), the spatial
// kommuner[] and the secondary names. ændret/geo_ændret/geo_version are DAWA's
// import-batch metadata (not derivable from the Datafordeler extract).
type Sted struct {
	ID                 string          `json:"id"`
	Hovedtype          string          `json:"hovedtype"`
	Undertype          *string         `json:"undertype"`
	PrimaertNavn       *string         `json:"primærtnavn"`
	PrimaerNavnestatus *string         `json:"primærnavnestatus"`
	Aendret            *string         `json:"ændret"`
	GeoAendret         *string         `json:"geo_ændret"`
	GeoVersion         *int            `json:"geo_version"`
	Href               string          `json:"href"`
	Egenskaber         any             `json:"egenskaber"`
	Visueltcenter      [2]float64      `json:"visueltcenter"`
	Bbox               *[4]float64     `json:"bbox"`
	Kommuner           []KommuneRef    `json:"kommuner"`
	Sekundaerenavne    []SekundaerNavn `json:"sekundærenavne"`
}

// emptyEgenskaber is the per-sted extension object for every NON-Bebyggelse
// place: DAWA renders it as an empty object `{}`. Sted.Egenskaber is set to a
// value of this type for those places so it marshals to `{}`.
type emptyEgenskaber struct{}

// bebEgenskaber is the per-sted extension object DAWA emits for hovedtype
// "Bebyggelse" on /steder and /stednavne2: {bebyggelseskode, indbyggerantal}, in
// that key order. Both are nullable: bebyggelseskode is ds_steder.bebyggelseskode
// (null on rows where DAWA itself has no code) and indbyggerantal is always null
// (population is not on the Datafordeler; our ds_steder.indbyggertal is NULL on
// every row, so emitting it reproduces DAWA byte-for-byte — never fabricate one).
type bebEgenskaber struct {
	Bebyggelseskode *int `json:"bebyggelseskode"`
	Indbyggerantal  *int `json:"indbyggerantal"`
}

// hovedtypeBebyggelse is the stored (DS-extract) hovedtype value our ingest keeps
// for Bebyggelse places — lowercase, unlike DAWA's "Bebyggelse" display string.
// The egenskaber branch keys off OUR stored value, not DAWA's casing.
const hovedtypeBebyggelse = "bebyggelse"

// stedEgenskaber returns the egenskaber value for a sted: the populated
// {bebyggelseskode, indbyggerantal} object for Bebyggelse places, otherwise the
// empty object that renders as `{}`.
func stedEgenskaber(hovedtype string, bebKode, indbyggertal *int) any {
	if hovedtype == hovedtypeBebyggelse {
		return bebEgenskaber{Bebyggelseskode: bebKode, Indbyggerantal: indbyggertal}
	}
	return emptyEgenskaber{}
}

// SekundaerNavn is one non-primary name of a sted: {navn, navnestatus}.
type SekundaerNavn struct {
	Navn        string  `json:"navn"`
	Navnestatus *string `json:"navnestatus"`
}

// stederSelect lists the scalar columns + aggregated JSON in DAWA field order.
// bbox is the envelope reprojected from 25832, NULL for POINT places (DAWA emits
// null there); visueltcenter is the stored pole-of-inaccessibility reprojected.
const stederSelect = `
	s.id_lokalId,
	s.hovedtype,
	s.undertype,
	nm.primaer_navn,
	nm.primaer_status,
	to_char(s.aendret AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS aendret,
	round(ST_X(ST_Transform(s.visueltcenter, 4326))::numeric, 8)::float8 AS vc_x,
	round(ST_Y(ST_Transform(s.visueltcenter, 4326))::numeric, 8)::float8 AS vc_y,
	CASE WHEN GeometryType(s.geom) = 'POINT' THEN NULL ELSE round(ST_XMin(env.bb)::numeric, 8)::float8 END AS bxmin,
	CASE WHEN GeometryType(s.geom) = 'POINT' THEN NULL ELSE round(ST_YMin(env.bb)::numeric, 8)::float8 END AS bymin,
	CASE WHEN GeometryType(s.geom) = 'POINT' THEN NULL ELSE round(ST_XMax(env.bb)::numeric, 8)::float8 END AS bxmax,
	CASE WHEN GeometryType(s.geom) = 'POINT' THEN NULL ELSE round(ST_YMax(env.bb)::numeric, 8)::float8 END AS bymax,
	km.j AS kommuner,
	nm.sekundaere AS sekundaere,
	s.bebyggelseskode,
	s.indbyggertal`

// stederFrom resolves, per sted: the envelope (once, via CROSS JOIN LATERAL),
// the primary/secondary names (ds_stednavne joined on objectid, primary picked by
// brugsprioritet='primær'), and the kommuner[] the geometry intersects (ordered
// by kode). The secondary names are ordered by skrivemaade under the Danish
// collation "da-x-icu" so Æ/Ø/Å sort LAST, reproducing DAWA's secondary-name
// order ("Hummelknold" < "Æ Hummelgårdsknold"). kode/navn only — the kommune href
// is built in Go.
const stederFrom = `
	FROM ds_steder s
	CROSS JOIN LATERAL (SELECT ST_Transform(ST_Envelope(s.geom), 4326) AS bb) env
	LEFT JOIN LATERAL (
		SELECT
			(array_agg(sn.skrivemaade ORDER BY sn.navnefoelgenummer NULLS LAST, sn.skrivemaade)
				FILTER (WHERE sn.brugsprioritet = 'primær'))[1] AS primaer_navn,
			(array_agg(sn.navnestatus ORDER BY sn.navnefoelgenummer NULLS LAST, sn.skrivemaade)
				FILTER (WHERE sn.brugsprioritet = 'primær'))[1] AS primaer_status,
			COALESCE(json_agg(json_build_object('navn', sn.skrivemaade, 'navnestatus', sn.navnestatus)
				ORDER BY sn.skrivemaade COLLATE "da-x-icu")
				FILTER (WHERE sn.brugsprioritet IS DISTINCT FROM 'primær'), '[]') AS sekundaere
		FROM ds_stednavne sn
		WHERE sn.place_objectid = s.objectid
	) nm ON true
	LEFT JOIN LATERAL (
		SELECT COALESCE(json_agg(json_build_object('kode', k.kode, 'navn', k.navn) ORDER BY k.kode), '[]') AS j
		FROM dagi_kommuner k
		WHERE ST_Intersects(k.geom, s.geom)
	) km ON true`

// scanSted reads one stederSelect row into a Sted, building href, kommuner refs
// (with hrefs) and bbox (present only when all four corners are non-null).
func scanSted(row pgx.Row, baseURL string) (*Sted, error) {
	var s Sted
	var id string
	var bxmin, bymin, bxmax, bymax *float64
	var komJSON, sekJSON []byte
	var bebKode, indbyggertal *int
	if err := row.Scan(
		&id, &s.Hovedtype, &s.Undertype, &s.PrimaertNavn, &s.PrimaerNavnestatus,
		&s.Aendret,
		&s.Visueltcenter[0], &s.Visueltcenter[1],
		&bxmin, &bymin, &bxmax, &bymax,
		&komJSON, &sekJSON,
		&bebKode, &indbyggertal,
	); err != nil {
		return nil, err
	}
	s.ID = id
	s.GeoAendret = s.Aendret
	s.Egenskaber = stedEgenskaber(s.Hovedtype, bebKode, indbyggertal)
	s.Href = fmt.Sprintf("%s/steder/%s", baseURL, id)
	if bxmin != nil && bymin != nil && bxmax != nil && bymax != nil {
		s.Bbox = &[4]float64{*bxmin, *bymin, *bxmax, *bymax}
	}
	kom, err := buildStedKommuner(komJSON, baseURL)
	if err != nil {
		return nil, err
	}
	s.Kommuner = kom
	if err := unmarshalSekundaere(sekJSON, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// unmarshalSekundaere decodes the aggregated secondary-names JSON into the sted,
// leaving a non-nil empty slice (so it renders as []) when there are none.
func unmarshalSekundaere(js []byte, s *Sted) error {
	sek := []SekundaerNavn{}
	if len(js) > 0 {
		if err := json.Unmarshal(js, &sek); err != nil {
			return fmt.Errorf("parse sekundærenavne: %w", err)
		}
	}
	s.Sekundaerenavne = sek
	return nil
}

// buildStedKommuner turns the aggregated [{kode,navn}] JSON into KommuneRef[],
// always non-nil so it renders as [] rather than null.
func buildStedKommuner(js []byte, baseURL string) ([]KommuneRef, error) {
	refs := []KommuneRef{}
	if len(js) == 0 {
		return refs, nil
	}
	var raw []struct {
		Kode string `json:"kode"`
		Navn string `json:"navn"`
	}
	if err := json.Unmarshal(js, &raw); err != nil {
		return nil, fmt.Errorf("parse kommuner: %w", err)
	}
	for _, r := range raw {
		refs = append(refs, KommuneRef{
			Href: fmt.Sprintf("%s/kommuner/%s", baseURL, r.Kode),
			Kode: r.Kode,
			Navn: r.Navn,
		})
	}
	return refs, nil
}

// GetSted returns the sted with the given id (id_lokalId), or pgx.ErrNoRows.
func GetSted(ctx context.Context, pool *pgxpool.Pool, id, baseURL string) (*Sted, error) {
	sql := "SELECT " + stederSelect + stederFrom + " WHERE s.id_lokalId = $1"
	return scanSted(pool.QueryRow(ctx, sql, id), baseURL)
}

// ListSteder returns steder ordered by id_lokalId. limit <= 0 returns all.
func ListSteder(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit, offset int) ([]*Sted, error) {
	return ListStederFiltered(ctx, pool, baseURL, limit, offset, ListFilter{})
}

// ListStederCoveringPoint returns the steder whose geometry COVERS the WGS84
// point (x=lon, y=lat) — DAWA's /steder?x=&y= reverse-via-query. Rows are ordered
// by id_lokalId (the default list order); DAWA's own ordering of multi-place
// containment results is internal and not reproducible (see build report). Empty
// result = empty array.
func ListStederCoveringPoint(ctx context.Context, pool *pgxpool.Pool, x, y float64, baseURL string) ([]*Sted, error) {
	sql := "SELECT " + stederSelect + stederFrom +
		" WHERE ST_Covers(s.geom, ST_Transform(ST_SetSRID(ST_Point($1, $2), 4326), 25832))" +
		" ORDER BY s.id_lokalId"
	return querySteder(ctx, pool, baseURL, sql, x, y)
}

// NearestSted returns a one-element slice with the sted nearest to (x,y) — DAWA's
// /steder?nærmeste=true&x=&y= (KNN: ORDER BY geom <-> point LIMIT 1). Empty
// result = empty array.
func NearestSted(ctx context.Context, pool *pgxpool.Pool, x, y float64, baseURL string) ([]*Sted, error) {
	sql := "SELECT " + stederSelect + stederFrom +
		" ORDER BY s.geom <-> ST_Transform(ST_SetSRID(ST_Point($1, $2), 4326), 25832)" +
		" LIMIT 1"
	return querySteder(ctx, pool, baseURL, sql, x, y)
}

// querySteder runs a Sted-shaped query with the given args and scans the rows
// into a slice (shared by the spatial reverse-via-query helpers).
func querySteder(ctx context.Context, pool *pgxpool.Pool, baseURL, sql string, args ...any) ([]*Sted, error) {
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Sted
	for rows.Next() {
		s, err := scanSted(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListStederFiltered is ListSteder with SQL-side q= (matching any of the place's
// ds_stednavne names), srid output reprojection and an optional spatial filter
// (against the place geometry), plus offset paging. A zero filter reproduces
// ListSteder byte-for-byte.
func ListStederFiltered(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit, offset int, f ListFilter) ([]*Sted, error) {
	var wb whereBuilder
	if strings.TrimSpace(f.Q) != "" {
		// Match any name of the place (primary or secondary).
		wb.addQ(f.Q, "(SELECT string_agg(sn2.skrivemaade, ' ') FROM ds_stednavne sn2 WHERE sn2.place_objectid = s.objectid)")
	}
	if f.Spatial != nil {
		wb.addSpatial(f.Spatial, "s.geom")
	}

	sql := "SELECT " + f.applySRID(stederSelect+stederFrom)
	if w := wb.sql(); w != "" {
		sql += " WHERE " + w
	}
	sql += " ORDER BY s.id_lokalId"
	sql = appendLimitOffset(sql, limit, offset)

	rows, err := pool.Query(ctx, sql, wb.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Sted
	for rows.Next() {
		s, err := scanSted(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
