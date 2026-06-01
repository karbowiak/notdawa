package dawa

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// pad4Code left-pads a numeric code to 4 digits with leading zeros, matching the
// way kommunekode/vejkode are stored in dar_navngivenvej_kommunedel ("0101",
// "0004"). DAWA accepts the leading-zero-stripped form in the path
// (/vejstykker/101/4), so the handler value may be unpadded; padding here makes
// the DB lookup match. A non-numeric or already-4+-char value is returned as-is.
func pad4Code(s string) string {
	if len(s) >= 4 {
		return s
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return s
		}
	}
	return strings.Repeat("0", 4-len(s)) + s
}

// naboer.go serves DAWA's road-neighbour endpoints:
//
//	/vejstykker/{kommunekode}/{kode}/naboer
//	/navngivneveje/{id}/naboer
//
// Both return the SAME element shape as their parent collection (a vejstykker
// list element / a navngivneveje list element). The neighbour set is the roads
// whose geometry lies within `afstand` metres of the target road's geometry,
// excluding the target itself, ordered by ascending distance (DAWA's order).
//
// Vejstykke geometry source: DAWA's per-vejstykke geometry is the named-road
// centerline clipped to ONE kommune (a named road can span several kommuner;
// each vejstykke is the road's segment inside a single kommune). The bulk DAR
// extract carries only the parent dar_navngivenvej.geom (the whole road across
// all kommuner), so we materialise the per-vejstykke clip
// (ST_Intersection(parent line, kommune polygon)) once into vejstykke_geom
// (see migration 028) and run the spatial filter over THAT. Both target and
// every candidate are compared on their own clipped geometry, so the neighbour
// SET matches DAWA's even for cross-kommune roads (DAWA crosses kommune borders
// too — there is no same-kommune restriction). All geoms are EPSG:25832, so
// ST_DWithin/ST_Distance are already metric. The BODY of each returned road
// element is the byte-exact parent /vejstykker list shape.
//
// afstand defaults to 0 (DAWA's default), which still returns roads that touch
// (distance 0). limit/offset page the neighbour list after the distance sort.

// VejstykkeNaboer returns the status-3 vejstykker within afstand metres of the
// (kommunekode, vejkode) road's clipped-to-kommune geometry, excluding the
// target road itself, ordered by ascending distance then kommune/vejkode. Each
// element is the full vejstykke list shape. f threads srid (output
// reprojection); the spatial filter is ignored here (naboer IS the spatial
// query). limit/offset page the result. An absent target (no vejstykke_geom
// row) yields an empty list.
func VejstykkeNaboer(ctx context.Context, pool *pgxpool.Pool, kommunekode, vejkode, baseURL string, afstand float64, limit, offset int, f ListFilter) ([]*Vejstykke, error) {
	// The target geometry is the requested vejstykke's precomputed clip
	// (vejstykke_geom = parent line ∩ its kommune polygon). A CROSS JOIN binds it
	// once; each candidate joins its OWN clipped geometry from vejstykke_geom and
	// is kept iff within afstand metres of the target's clip. The WHERE excludes
	// the target vejstykke (same kommune+vejkode); there is NO same-kommune
	// restriction (DAWA neighbours cross kommune borders). The shared
	// vejstykkeSelect/From are reused verbatim so each element is byte-identical
	// to the /vejstykker list shape.
	sql := "SELECT " + f.applySRID(vejstykkeSelect+vejstykkeFrom) + `
		JOIN vejstykke_geom vg ON vg.kommune = kd.kommune AND vg.vejkode = kd.vejkode
		CROSS JOIN (
			SELECT geom AS g FROM vejstykke_geom WHERE kommune = $1 AND vejkode = $2
		) tgt
		WHERE kd.status = '3' AND tgt.g IS NOT NULL
		  AND NOT (kd.kommune = $1 AND kd.vejkode = $2)
		  AND ST_DWithin(vg.geom, tgt.g, $3)
		ORDER BY ST_Distance(vg.geom, tgt.g), kd.kommune, kd.vejkode`
	sql = appendLimitOffset(sql, limit, offset)

	rows, err := pool.Query(ctx, sql, pad4Code(kommunekode), pad4Code(vejkode), afstand)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Vejstykke{}
	for rows.Next() {
		v, err := scanVejstykke(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// NavngivenvejNaboer returns the status-3 navngivneveje within afstand metres of
// the target navngivenvej's geometry, excluding the target, ordered by ascending
// distance then id. Each element is the full navngivenvej list shape. An absent
// target or a target with null geom yields an empty list (DAWA behaviour).
func NavngivenvejNaboer(ctx context.Context, pool *pgxpool.Pool, id, baseURL string, afstand float64, limit, offset int, f ListFilter) ([]*NavngivenVej, error) {
	sql := "SELECT " + f.applySRID(navngivnevejSelect+navngivnevejFrom) + `
		CROSS JOIN (
			SELECT geom AS g FROM dar_navngivenvej WHERE id = $1 AND status = '3'
		) tgt
		WHERE nv.status = '3' AND tgt.g IS NOT NULL AND nv.geom IS NOT NULL
		  AND nv.id <> $1
		  AND ST_DWithin(nv.geom, tgt.g, $2)
		ORDER BY ST_Distance(nv.geom, tgt.g), nv.id`
	sql = appendLimitOffset(sql, limit, offset)

	rows, err := pool.Query(ctx, sql, id, afstand)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NavngivenVej{}
	for rows.Next() {
		nv, err := scanNavngivenVej(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, nv)
	}
	return out, rows.Err()
}
