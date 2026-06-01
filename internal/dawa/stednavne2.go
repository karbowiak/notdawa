package dawa

import (
	"context"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Stednavn2 is one row of DAWA /stednavne2: a single name of a place, with the
// full embedded sted. There is one Stednavn2 per ds_stednavne row. The href path
// is /stednavne2/{sted id}/{percent-encoded navn}.
type Stednavn2 struct {
	Navn           string  `json:"navn"`
	Navnestatus    *string `json:"navnestatus"`
	Brugsprioritet *string `json:"brugsprioritet"`
	Href           string  `json:"href"`
	Sted           *Sted   `json:"sted"`
}

// stednavne2Select is the three name columns followed by the embedded sted's
// columns (same order/shape as stederSelect so the sted half is scanned by the
// shared scanStedTail helper).
const stednavne2Select = `
	sn.skrivemaade AS sn_navn,
	sn.navnestatus AS sn_status,
	sn.brugsprioritet AS sn_prioritet,
` + stederSelect

// stednavne2From joins each name to its place and the same name/kommune/envelope
// laterals the sted representation needs.
const stednavne2From = `
	FROM ds_stednavne sn
	JOIN ds_steder s ON s.objectid = sn.place_objectid
	CROSS JOIN LATERAL (SELECT ST_Transform(ST_Envelope(s.geom), 4326) AS bb) env
	LEFT JOIN LATERAL (
		SELECT
			(array_agg(n.skrivemaade ORDER BY n.navnefoelgenummer NULLS LAST, n.skrivemaade)
				FILTER (WHERE n.brugsprioritet = 'primær'))[1] AS primaer_navn,
			(array_agg(n.navnestatus ORDER BY n.navnefoelgenummer NULLS LAST, n.skrivemaade)
				FILTER (WHERE n.brugsprioritet = 'primær'))[1] AS primaer_status,
			COALESCE(json_agg(json_build_object('navn', n.skrivemaade, 'navnestatus', n.navnestatus)
				ORDER BY n.navnefoelgenummer NULLS LAST, n.skrivemaade)
				FILTER (WHERE n.brugsprioritet IS DISTINCT FROM 'primær'), '[]') AS sekundaere
		FROM ds_stednavne n
		WHERE n.place_objectid = s.objectid
	) nm ON true
	LEFT JOIN LATERAL (
		SELECT COALESCE(json_agg(json_build_object('kode', k.kode, 'navn', k.navn) ORDER BY k.kode), '[]') AS j
		FROM dagi_kommuner k
		WHERE ST_Intersects(k.geom, s.geom)
	) km ON true`

// stednavne2Href builds /stednavne2/{stedID}/{percent-encoded navn}. PathEscape
// matches DAWA: space→%20, ø→%C3%B8, etc.
func stednavne2Href(baseURL, stedID, navn string) string {
	return fmt.Sprintf("%s/stednavne2/%s/%s", baseURL, stedID, url.PathEscape(navn))
}

func scanStednavn2(row pgx.Row, baseURL string) (*Stednavn2, error) {
	var sn Stednavn2
	var s Sted
	var id string
	var bxmin, bymin, bxmax, bymax *float64
	var komJSON, sekJSON []byte
	var bebKode, indbyggertal *int
	if err := row.Scan(
		&sn.Navn, &sn.Navnestatus, &sn.Brugsprioritet,
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
	sn.Sted = &s
	sn.Href = stednavne2Href(baseURL, id, sn.Navn)
	return &sn, nil
}

// GetStednavn2 returns the single stednavn2 row identified by the composite key
// (sted id_lokalId, navn/skrivemaade), or pgx.ErrNoRows. The body is byte-
// identical to the matching /stednavne2 list element.
func GetStednavn2(ctx context.Context, pool *pgxpool.Pool, stedID, navn, baseURL string) (*Stednavn2, error) {
	sql := "SELECT " + stednavne2Select + stednavne2From +
		" WHERE s.id_lokalId = $1 AND sn.skrivemaade = $2 LIMIT 1"
	return scanStednavn2(pool.QueryRow(ctx, sql, stedID, navn), baseURL)
}

// ListStednavne2 returns name rows ordered by sted id then name. limit <= 0 = all.
func ListStednavne2(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit, offset int) ([]*Stednavn2, error) {
	return ListStednavne2Filtered(ctx, pool, baseURL, limit, offset, ListFilter{})
}

// ListStednavne2CoveringPoint returns the stednavn2 rows whose place geometry
// COVERS the WGS84 point (x=lon, y=lat) — DAWA's /stednavne2?x=&y= reverse-via-
// query. Rows are ordered by the place id then navnefoelgenummer (the default
// list order); DAWA's own ordering of multi-place containment results is internal
// and not reproducible (see build report). Empty result = empty array.
func ListStednavne2CoveringPoint(ctx context.Context, pool *pgxpool.Pool, x, y float64, baseURL string) ([]*Stednavn2, error) {
	sql := "SELECT " + stednavne2Select + stednavne2From +
		" WHERE ST_Covers(s.geom, ST_Transform(ST_SetSRID(ST_Point($1, $2), 4326), 25832))" +
		" ORDER BY s.id_lokalId, sn.navnefoelgenummer NULLS LAST, sn.skrivemaade"
	return queryStednavne2(ctx, pool, baseURL, sql, x, y)
}

// NearestStednavne2 returns a one-element slice with the stednavn2 of the place
// nearest to (x,y) — DAWA's /stednavne2?nærmeste=true&x=&y= (KNN: ORDER BY
// place geom <-> point LIMIT 1). Empty result = empty array.
func NearestStednavne2(ctx context.Context, pool *pgxpool.Pool, x, y float64, baseURL string) ([]*Stednavn2, error) {
	sql := "SELECT " + stednavne2Select + stednavne2From +
		" ORDER BY s.geom <-> ST_Transform(ST_SetSRID(ST_Point($1, $2), 4326), 25832)" +
		" LIMIT 1"
	return queryStednavne2(ctx, pool, baseURL, sql, x, y)
}

// queryStednavne2 runs a Stednavn2-shaped query with the given args and scans the
// rows into a slice (shared by the spatial reverse-via-query helpers).
func queryStednavne2(ctx context.Context, pool *pgxpool.Pool, baseURL, sql string, args ...any) ([]*Stednavn2, error) {
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Stednavn2
	for rows.Next() {
		sn, err := scanStednavn2(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, sn)
	}
	return out, rows.Err()
}

// ListStednavne2Filtered is ListStednavne2 with SQL-side stedid= equality (the
// embedded place's id_lokalId), q= over this name, srid output reprojection and
// an optional spatial filter (against the place geometry), plus offset paging.
// Pushing stedid= into SQL is what makes the previously-not-served ?stedid=
// request return only that place's name rows (instead of materialising the whole
// multi-million-row table). A zero filter reproduces ListStednavne2 byte-for-byte.
func ListStednavne2Filtered(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit, offset int, f ListFilter) ([]*Stednavn2, error) {
	var wb whereBuilder
	if v, ok := f.Filters["stedid"]; ok {
		wb.addEq("s.id_lokalId", v)
	}
	wb.addQ(f.Q, "sn.skrivemaade")
	if f.Spatial != nil {
		wb.addSpatial(f.Spatial, "s.geom")
	}

	sql := "SELECT " + f.applySRID(stednavne2Select+stednavne2From)
	if w := wb.sql(); w != "" {
		sql += " WHERE " + w
	}
	sql += " ORDER BY s.id_lokalId, sn.navnefoelgenummer NULLS LAST, sn.skrivemaade"
	sql = appendLimitOffset(sql, limit, offset)

	rows, err := pool.Query(ctx, sql, wb.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Stednavn2
	for rows.Next() {
		sn, err := scanStednavn2(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, sn)
	}
	return out, rows.Err()
}
