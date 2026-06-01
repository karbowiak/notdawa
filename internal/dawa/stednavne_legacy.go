package dawa

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// stednavne_legacy.go implements DAWA's OLDER /stednavne resource — distinct from
// /stednavne2. Captured live, the legacy element is keyed by a single id (the
// place's id_lokalId) and is a flatter shape than /steder: it carries the PRIMARY
// name inlined as navn/navnestatus (rather than primærtnavn/primærnavnestatus),
// has NO bbox and NO sekundærenavne[], and otherwise matches /steder field-for-
// field. The verified field ORDER is:
//
//	id, hovedtype, undertype, navn, navnestatus, ændret, geo_ændret,
//	geo_version, href, egenskaber, visueltcenter, kommuner
//
// id/undertype/navn/navnestatus/egenskaber/visueltcenter/kommuner all reproduce
// /steder byte-for-byte (same source columns). ændret/geo_ændret/geo_version are
// the DAWA import-batch metadata (not in the Datafordeler extract), the same
// documented non-substantive timestamp class as everywhere else.
//
// HONEST GAP — hovedtype spelling: legacy /stednavne uses the SAME hovedtype as
// /steder for most place types (Bygning, Bebyggelse, Naturareal, Landskabsform,
// Fortidsminde, Vandløb, Sø, Seværdighed, Farvand, …) but for a few types DAWA's
// legacy display string differs from the DS-extract Hovedtype our ingest stored:
// legacy "Andentopografi punkt"/"Andentopografi flade" vs our "Anden topografi";
// "Urentfarvand" vs "Urent farvand"; "Idraetsanlæg" vs "Idrætsanlæg";
// "Navigationsanlaeg" vs "Navigationsanlæg". Those are cosmetic deprecation-era
// label strings not present in our columns, so for those (rare) types the legacy
// hovedtype differs; we emit our stored, /steder-consistent value. See the build
// report.

// LegacyStednavn is one element of the /stednavne resource. Field order matches
// the captured live shape exactly.
type LegacyStednavn struct {
	ID            string       `json:"id"`
	Hovedtype     string       `json:"hovedtype"`
	Undertype     *string      `json:"undertype"`
	Navn          *string      `json:"navn"`
	Navnestatus   *string      `json:"navnestatus"`
	Aendret       *string      `json:"ændret"`
	GeoAendret    *string      `json:"geo_ændret"`
	GeoVersion    *int         `json:"geo_version"`
	Href          string       `json:"href"`
	Egenskaber    any          `json:"egenskaber"`
	Visueltcenter [2]float64   `json:"visueltcenter"`
	Kommuner      []KommuneRef `json:"kommuner"`
}

// legacyBebEgenskaber is the egenskaber object the LEGACY /stednavne resource
// emits for hovedtype "Bebyggelse". Unlike /steder and /stednavne2, legacy
// carries ONLY bebyggelseskode — there is NO indbyggerantal key (verified against
// live DAWA). bebyggelseskode is nullable (ds_steder.bebyggelseskode is NULL on
// rows where DAWA itself has no code; legacy still emits {"bebyggelseskode":null}).
type legacyBebEgenskaber struct {
	Bebyggelseskode *int `json:"bebyggelseskode"`
}

// legacyStedEgenskaber returns the legacy egenskaber value: {bebyggelseskode} for
// a Bebyggelse place, otherwise the empty object that renders as `{}`.
func legacyStedEgenskaber(hovedtype string, bebKode *int) any {
	if hovedtype == hovedtypeBebyggelse {
		return legacyBebEgenskaber{Bebyggelseskode: bebKode}
	}
	return emptyEgenskaber{}
}

// legacyStednavneSelect lists the legacy columns in DAWA field order: identity +
// type, the inlined PRIMARY name (status), the ændret metadata, and the
// visueltcenter + aggregated kommuner[]. visueltcenter is the stored
// pole-of-inaccessibility reprojected to WGS84 (same as /steder).
const legacyStednavneSelect = `
	s.id_lokalId,
	s.hovedtype,
	s.undertype,
	nm.primaer_navn,
	nm.primaer_status,
	to_char(s.aendret AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS aendret,
	round(ST_X(ST_Transform(s.visueltcenter, 4326))::numeric, 8)::float8 AS vc_x,
	round(ST_Y(ST_Transform(s.visueltcenter, 4326))::numeric, 8)::float8 AS vc_y,
	km.j AS kommuner,
	s.bebyggelseskode`

// legacyStednavneFrom joins each place to its primary name (ds_stednavne, primary
// picked by brugsprioritet='primær') and the kommuner[] its geometry intersects
// (the same laterals /steder uses).
const legacyStednavneFrom = `
	FROM ds_steder s
	LEFT JOIN LATERAL (
		SELECT
			(array_agg(sn.skrivemaade ORDER BY sn.navnefoelgenummer NULLS LAST, sn.skrivemaade)
				FILTER (WHERE sn.brugsprioritet = 'primær'))[1] AS primaer_navn,
			(array_agg(sn.navnestatus ORDER BY sn.navnefoelgenummer NULLS LAST, sn.skrivemaade)
				FILTER (WHERE sn.brugsprioritet = 'primær'))[1] AS primaer_status
		FROM ds_stednavne sn
		WHERE sn.place_objectid = s.objectid
	) nm ON true
	LEFT JOIN LATERAL (
		SELECT COALESCE(json_agg(json_build_object('kode', k.kode, 'navn', k.navn) ORDER BY k.kode), '[]') AS j
		FROM dagi_kommuner k
		WHERE ST_Intersects(k.geom, s.geom)
	) km ON true`

// scanLegacyStednavn reads one legacyStednavneSelect row into a LegacyStednavn,
// building href and kommuner refs. geo_ændret mirrors ændret and geo_version is
// left nil (DAWA-internal version metadata not in the extract).
func scanLegacyStednavn(row pgx.Row, baseURL string) (*LegacyStednavn, error) {
	var s LegacyStednavn
	var komJSON []byte
	var bebKode *int
	if err := row.Scan(
		&s.ID, &s.Hovedtype, &s.Undertype, &s.Navn, &s.Navnestatus,
		&s.Aendret,
		&s.Visueltcenter[0], &s.Visueltcenter[1],
		&komJSON,
		&bebKode,
	); err != nil {
		return nil, err
	}
	s.GeoAendret = s.Aendret
	s.Egenskaber = legacyStedEgenskaber(s.Hovedtype, bebKode)
	s.Href = fmt.Sprintf("%s/stednavne/%s", baseURL, s.ID)
	kom, err := buildStedKommuner(komJSON, baseURL)
	if err != nil {
		return nil, err
	}
	s.Kommuner = kom
	return &s, nil
}

// GetLegacyStednavn returns the legacy stednavn with the given id (id_lokalId),
// or pgx.ErrNoRows.
func GetLegacyStednavn(ctx context.Context, pool *pgxpool.Pool, id, baseURL string) (*LegacyStednavn, error) {
	sql := "SELECT " + legacyStednavneSelect + legacyStednavneFrom + " WHERE s.id_lokalId = $1"
	return scanLegacyStednavn(pool.QueryRow(ctx, sql, id), baseURL)
}

// ListLegacyStednavne returns legacy stednavne ordered by id_lokalId. limit <= 0
// returns all rows.
//
// NOTE: live DAWA's legacy collection ordering is a DAWA-internal id ordering
// (NOT a plain lexicographic id_lokalId sort — verified against live), so the
// per-page MEMBERSHIP at a given side/per_side may differ from DAWA's. Each
// emitted row is itself byte-exact; the collection ordering is the documented
// gap.
func ListLegacyStednavne(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit, offset int) ([]*LegacyStednavn, error) {
	sql := "SELECT " + legacyStednavneSelect + legacyStednavneFrom + " ORDER BY s.id_lokalId"
	sql = appendLimitOffset(sql, limit, offset)
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*LegacyStednavn
	for rows.Next() {
		s, err := scanLegacyStednavn(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// AutocompleteLegacyStednavne returns up to perSide legacy /stednavne elements
// whose PRIMARY name matches q (folded substring), ordered by id_lokalId. Like
// /stednavne2/autocomplete (and verified against live DAWA), the legacy
// /stednavne/autocomplete element is the FLAT list element itself — there is NO
// {tekst, stednavn} wrapper. The q filter + LIMIT are applied in SQL (the table
// is large).
//
// NOTE: DAWA additionally applies a proprietary tsv relevance rank, so for a
// query matching more than perSide names the top-N selection/order may differ
// from this id_lokalId order (the same documented autocomplete-relevance class as
// every other resource); each returned element is itself byte-exact.
func AutocompleteLegacyStednavne(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*LegacyStednavn, error) {
	sql := "SELECT " + legacyStednavneSelect + legacyStednavneFrom +
		" WHERE unaccent(nm.primaer_navn) ILIKE unaccent($1)" +
		" ORDER BY s.id_lokalId" + pageClause(perSide, offset)
	rows, err := pool.Query(ctx, sql, "%"+q+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*LegacyStednavn{}
	for rows.Next() {
		s, err := scanLegacyStednavn(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
