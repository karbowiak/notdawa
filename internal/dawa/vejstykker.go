package dawa

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Vejstykke is the DAWA /vejstykker/{kommunekode}/{kode} response — one row per
// status-3 NavngivenVejKommunedel, inheriting navn/geometry/postnumre from its
// parent navngivenvej. Field order is significant. darstatus is an INT here
// (the raw status code), unlike navngivenvej where it is a string.
type Vejstykke struct {
	ID               string              `json:"id"`
	Darstatus        int                 `json:"darstatus"`
	Href             string              `json:"href"`
	Kode             string              `json:"kode"`
	Navn             *string             `json:"navn"`
	Adresseringsnavn *string             `json:"adresseringsnavn"`
	Navngivenvej     NavngivenvejMiniRef `json:"navngivenvej"`
	Kommune          *KommuneRef         `json:"kommune"`
	Postnumre        []PostnrRef         `json:"postnumre"`
	Historik         VejstykkeHistorik   `json:"historik"`
	Bbox             *[4]float64         `json:"bbox"`
	Visueltcenter    *[2]float64         `json:"visueltcenter"`
}

// VejstykkeHistorik has three keys (no ikrafttrædelse, unlike navngivenvej)
// rendered in LOCAL time with NO trailing Z. oprettet derives (since
// 2026-06-12) from the kommunedel's bitemporal chain (dar_nvkommunedel_hist:
// first virkningFra); ændret and nedlagt are null on live DAWA even for
// multi-version chains (verified), so they stay null.
type VejstykkeHistorik struct {
	Oprettet *string `json:"oprettet"`
	Aendret  *string `json:"ændret"`
	Nedlagt  *string `json:"nedlagt"`
}

// stripZeros removes leading zeros from a numeric code (the /vejstykker entity
// href uses int(kommune)/int(vejkode), e.g. "0101"/"0004" -> "101/4").
func stripZeros(s string) string {
	if n, err := strconv.Atoi(s); err == nil {
		return strconv.Itoa(n)
	}
	return strings.TrimLeft(s, "0")
}

// vejstykkeSelect lists the scanned columns in scan order. bbox/visueltcenter
// come from the PARENT navngivenvej line (DAR stores no per-kommunedel geometry).
const vejstykkeSelect = `
	kd.id, kd.status::int, kd.kommune, kd.vejkode,
	nv.navn, nv.adresseringsnavn, nv.id, nv.status::int,
	k.navn AS kommune_navn,
	CASE WHEN nv.geom IS NULL THEN NULL ELSE round(ST_XMin(g.env)::numeric, 8)::float8 END,
	CASE WHEN nv.geom IS NULL THEN NULL ELSE round(ST_YMin(g.env)::numeric, 8)::float8 END,
	CASE WHEN nv.geom IS NULL THEN NULL ELSE round(ST_XMax(g.env)::numeric, 8)::float8 END,
	CASE WHEN nv.geom IS NULL THEN NULL ELSE round(ST_YMax(g.env)::numeric, 8)::float8 END,
	CASE WHEN nv.geom IS NULL THEN NULL ELSE round(ST_X(g.vc)::numeric, 8)::float8 END,
	CASE WHEN nv.geom IS NULL THEN NULL ELSE round(ST_Y(g.vc)::numeric, 8)::float8 END,
	ps.j,
	-- historik.oprettet = the kommunedel chain's first virkningFra, rendered
	-- LOCAL time without Z like live. ændret is null on live even for
	-- multi-version chains (verified 2026-06-12 on 219/398 and 360/2104).
	(SELECT to_char(MIN(kh.virkning_start) AT TIME ZONE 'Europe/Copenhagen', 'YYYY-MM-DD"T"HH24:MI:SS.MS')
		FROM dar_nvkommunedel_hist kh WHERE kh.id = kd.id) AS h_oprettet`

// vejstykkeFrom joins each status-3 kommunedel to its parent navngivenvej (also
// status 3), the kommune (for navn), the parent-line geometry, and the parent's
// status-3 postnumre[].
const vejstykkeFrom = `
	FROM dar_navngivenvej_kommunedel kd
	JOIN dar_navngivenvej nv ON nv.id = kd.navngivenvej AND nv.status = '3'
	LEFT JOIN dagi_kommuner k ON k.kode = kd.kommune
	LEFT JOIN LATERAL (
		SELECT ST_Transform(ST_Envelope(nv.geom), 4326) AS env,
		       ST_Transform(ST_ClosestPoint(nv.geom, ST_Centroid(nv.geom)), 4326) AS vc
	) g ON nv.geom IS NOT NULL
	-- A vejstykke is a road WITHIN one kommune, so its postnumre[] is the
	-- branch1 ∪ branch2 set (see navngivneveje.go) scoped to this kommunedel's
	-- kommune — precomputed into vejstykke_postnumre by
	-- BuildVejstykkePostnumre (per-row the union's spatial branch measured
	-- ~9.5ms; 1–2s cold autocomplete pages). No row = no postnumre.
	LEFT JOIN vejstykke_postnumre ps0 ON ps0.navngivenvej = nv.id AND ps0.kommune = kd.kommune
	LEFT JOIN LATERAL (SELECT COALESCE(ps0.postnumre, '[]'::jsonb) AS j) ps ON true`

// BuildVejstykkePostnumre rebuilds the vejstykke_postnumre derived table: per
// (navngivenvej, kommune), the postnumre[] union the serving LATERAL used to
// evaluate per row — branch1 = the road's husnummer postnumre in that kommune,
// branch2 = postnummer polygons the road∩kommune clip crosses by >7 m (gade-
// postnumre 1000–1999 excluded). roadkom materializes each road∩kommune clip
// once so the postnummer probe runs against PostGIS-prepared geometry instead
// of recomputing the clip per candidate. One crash-atomic TRUNCATE+INSERT tx.
// SQL lives here next to the serving join so the two cannot drift.
func BuildVejstykkePostnumre(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "TRUNCATE vejstykke_postnumre"); err != nil {
		return 0, err
	}
	ct, err := tx.Exec(ctx, `
		WITH roadkom AS MATERIALIZED (
			SELECT nv.id AS nvid, kc.kode AS kommune, ST_Intersection(nv.geom, kc.geom) AS rkgeom
			FROM dar_navngivenvej nv
			JOIN dagi_kommuner kc ON nv.geom IS NOT NULL AND ST_Intersects(nv.geom, kc.geom)
		)
		INSERT INTO vejstykke_postnumre (navngivenvej, kommune, postnumre)
		SELECT t.nvid, t.kommune,
			json_agg(json_build_object('nr', t.postnr, 'navn', t.navn) ORDER BY t.postnr)::jsonb
		FROM (
			SELECT h.navngivenvej AS nvid, kk.kode AS kommune, p.postnr, p.navn
			FROM dar_husnummer h
			JOIN dagi_kommuner kk ON kk.dagi_id = h.kommune
			JOIN dar_postnummer p ON p.id = h.postnummer_id
			UNION
			SELECT r.nvid, r.kommune, pn.nr AS postnr, pn.navn
			FROM roadkom r
			JOIN dagi_postnumre pn ON ST_Intersects(r.rkgeom, pn.geom)
			 AND ST_Length(ST_Intersection(r.rkgeom, pn.geom)) > 7
			WHERE pn.nr ~ '^[0-9]+$' AND NOT (pn.nr::int BETWEEN 1000 AND 1999)
		) t
		GROUP BY t.nvid, t.kommune`)
	if err != nil {
		return 0, fmt.Errorf("build vejstykke_postnumre: %w", err)
	}
	n := int(ct.RowsAffected())
	if n == 0 {
		return 0, fmt.Errorf("build vejstykke_postnumre: derivation produced 0 rows")
	}
	if _, err := tx.Exec(ctx, "ANALYZE vejstykke_postnumre"); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return n, nil
}

func scanVejstykke(row pgx.Row, baseURL string) (*Vejstykke, error) {
	var v Vejstykke
	var id, kommune, vejkode, nvID string
	var darstatus, nvDarstatus int
	var navn, adr, komNavn *string
	var bx0, bx1, bx2, bx3, vx, vy *float64
	var psJSON []byte
	var hOpr *string
	if err := row.Scan(
		&id, &darstatus, &kommune, &vejkode,
		&navn, &adr, &nvID, &nvDarstatus,
		&komNavn,
		&bx0, &bx1, &bx2, &bx3, &vx, &vy,
		&psJSON,
		&hOpr,
	); err != nil {
		return nil, err
	}

	v.ID = id
	v.Darstatus = darstatus
	v.Href = fmt.Sprintf("%s/vejstykker/%s/%s", baseURL, stripZeros(kommune), stripZeros(vejkode))
	v.Kode = vejkode
	v.Navn = navn
	v.Adresseringsnavn = adr
	v.Navngivenvej = NavngivenvejMiniRef{
		Href:      fmt.Sprintf("%s/navngivneveje/%s", baseURL, nvID),
		ID:        nvID,
		Darstatus: nvDarstatus,
	}
	v.Kommune = newKommuneRef(baseURL, &kommune, komNavn)
	postnumre, err := buildPostnrRefs(psJSON, baseURL)
	if err != nil {
		return nil, err
	}
	v.Postnumre = postnumre
	v.Historik = VejstykkeHistorik{Oprettet: hOpr} // ændret/nedlagt: null on live
	if bx0 != nil && bx1 != nil && bx2 != nil && bx3 != nil {
		v.Bbox = &[4]float64{*bx0, *bx1, *bx2, *bx3}
	}
	if vx != nil && vy != nil {
		v.Visueltcenter = &[2]float64{*vx, *vy}
	}
	return &v, nil
}

// GetVejstykke returns the status-3 vejstykke for a kommunekode+kode (both
// 4-char zero-padded), or pgx.ErrNoRows.
func GetVejstykke(ctx context.Context, pool *pgxpool.Pool, kommunekode, kode, baseURL string) (*Vejstykke, error) {
	sql := "SELECT " + vejstykkeSelect + vejstykkeFrom +
		" WHERE kd.status = '3' AND kd.kommune = $1 AND kd.vejkode = $2"
	// DAWA's entity href is unpadded (e.g. /vejstykker/101/4), but the DAR keys
	// are stored 4-char zero-padded (0101/0004). Pad both so either form resolves.
	return scanVejstykke(pool.QueryRow(ctx, sql, kode4(kommunekode), kode4(kode)), baseURL)
}

// ListVejstykker returns status-3 vejstykker ordered by kommunekode then kode
// (DAWA's default order). limit <= 0 returns all.
func ListVejstykker(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit int) ([]*Vejstykke, error) {
	return ListVejstykkerFiltered(ctx, pool, baseURL, limit, 0, ListFilter{})
}

// ListVejstykkerFiltered is ListVejstykker with SQL-side per-field filters
// (kommunekode, kode) and q= over navn/adresseringsnavn, plus offset paging.
// A zero filter reproduces ListVejstykker byte-for-byte. Vejstykker have no
// stored geometry of their own, so srid only affects the output bbox/
// visueltcenter (parent line) coordinates; spatial filters are not applied here.
func ListVejstykkerFiltered(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit, offset int, f ListFilter) ([]*Vejstykke, error) {
	var wb whereBuilder
	if v, ok := f.Filters["kommunekode"]; ok {
		wb.addCode("kd.kommune", v)
	}
	if v, ok := f.Filters["kode"]; ok {
		wb.addCode("kd.vejkode", v)
	}
	wb.addQ(f.Q, "nv.navn", "nv.adresseringsnavn")

	sql := "SELECT " + f.applySRID(vejstykkeSelect+vejstykkeFrom) + " WHERE kd.status = '3'"
	if w := wb.sql(); w != "" {
		sql += " AND " + w
	}
	sql += " ORDER BY kd.kommune, kd.vejkode"
	sql = appendLimitOffset(sql, limit, offset)

	rows, err := pool.Query(ctx, sql, wb.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Vejstykke
	for rows.Next() {
		v, err := scanVejstykke(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
