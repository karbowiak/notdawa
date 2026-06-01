package dawa

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NavngivenVej is the DAWA /navngivneveje/{uuid} response. Field order is
// significant (Go struct order controls JSON key order). darstatus is a STRING
// here (mapped from the raw status code) — unlike vejstykke, where it is an int.
type NavngivenVej struct {
	ID                     string               `json:"id"`
	Href                   string               `json:"href"`
	Darstatus              string               `json:"darstatus"`
	Navn                   *string              `json:"navn"`
	Adresseringsnavn       *string              `json:"adresseringsnavn"`
	Administrerendekommune *KommuneRef          `json:"administrerendekommune"`
	Retskrivningskontrol   string               `json:"retskrivningskontrol"`
	Udtaltvejnavn          *string              `json:"udtaltvejnavn"`
	Visueltcenter          *[2]float64          `json:"visueltcenter"`
	Bbox                   *[4]float64          `json:"bbox"`
	Historik               NavngivenVejHistorik `json:"historik"`
	Vejstykker             []VejstykkeMiniRef   `json:"vejstykker"`
	Postnumre              []PostnrRef          `json:"postnumre"`
	Beliggenhed            Beliggenhed          `json:"beliggenhed"`
}

// NavngivenVejHistorik carries the four DAWA history timestamps. None are
// derivable from the Current extract (oprettet/ikrafttrædelse need DAR's
// bitemporal history; the served ændret in DAWA comes from that same history,
// not from the geometry registration — proven by Halvdansvej, whose historik is
// the 1900-01-01 sentinel while its oprindelse.registrering is 2018). Served as
// best-effort/null and treated as DAWA-internal metadata in verify.
type NavngivenVejHistorik struct {
	Oprettet        *string `json:"oprettet"`
	Aendret         *string `json:"ændret"`
	Ikrafttraedelse *string `json:"ikrafttrædelse"`
	Nedlagt         *string `json:"nedlagt"`
}

// Beliggenhed is the navngivenvej geometry-origin block.
type Beliggenhed struct {
	Oprindelse             Oprindelse `json:"oprindelse"`
	Vejtilslutningspunkter any        `json:"vejtilslutningspunkter"` // always null in golden
	Geometritype           *string    `json:"geometritype"`
}

// Oprindelse is the data-origin sub-block; every field is a stored raw value.
type Oprindelse struct {
	Kilde               *string `json:"kilde"`
	Tekniskstandard     *string `json:"tekniskstandard"`
	Registrering        *string `json:"registrering"`
	Noejagtighedsklasse *string `json:"nøjagtighedsklasse"`
}

// darstatusName maps a raw DAR status code to DAWA's string darstatus (used by
// navngivneveje; vejstykke keeps the raw status as an int).
func darstatusName(status string) string {
	switch status {
	case "2":
		return "foreløbig"
	case "3":
		return "gældende"
	case "4":
		return "nedlagt"
	case "5":
		return "henlagt"
	default:
		return status
	}
}

// vejstykkeMiniRaw / postnrRaw are the json_agg intermediates scanned from the
// LATERAL aggregates; href is added in Go (it depends on baseURL).
type vejstykkeMiniRaw struct {
	Kommunekode string `json:"kommunekode"`
	Kode        string `json:"kode"`
	ID          string `json:"id"`
	Darstatus   int    `json:"darstatus"`
}

type postnrRaw struct {
	Nr   string  `json:"nr"`
	Navn *string `json:"navn"`
}

// navngivnevejSelect lists the computed/scanned columns in scan order. bbox is
// the reprojected-envelope rectangle; visueltcenter for a line is the point on
// the line nearest its centroid (byte-exact, no GEOS tolerance gap). Both are
// null when the road has no geometry.
const navngivnevejSelect = `
	nv.id, nv.status, nv.navn, nv.adresseringsnavn, nv.udtaltvejnavn,
	nv.administrerende_kommune, k.navn AS kommune_navn,
	nv.oprindelse_kilde, nv.oprindelse_teknisk_standard,
	to_char(nv.oprindelse_registrering::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS oprindelse_registrering,
	nv.oprindelse_noejagtighedsklasse,
	CASE WHEN nv.geom IS NULL THEN NULL ELSE round(ST_XMin(g.env)::numeric, 8)::float8 END,
	CASE WHEN nv.geom IS NULL THEN NULL ELSE round(ST_YMin(g.env)::numeric, 8)::float8 END,
	CASE WHEN nv.geom IS NULL THEN NULL ELSE round(ST_XMax(g.env)::numeric, 8)::float8 END,
	CASE WHEN nv.geom IS NULL THEN NULL ELSE round(ST_YMax(g.env)::numeric, 8)::float8 END,
	CASE WHEN nv.geom IS NULL THEN NULL ELSE round(ST_X(g.vc)::numeric, 8)::float8 END,
	CASE WHEN nv.geom IS NULL THEN NULL ELSE round(ST_Y(g.vc)::numeric, 8)::float8 END,
	CASE WHEN nv.geom IS NULL THEN NULL
	     WHEN GeometryType(nv.geom) IN ('LINESTRING', 'MULTILINESTRING') THEN 'vejnavnelinje'
	     WHEN GeometryType(nv.geom) IN ('POLYGON', 'MULTIPOLYGON') THEN 'vejnavneområde'
	     ELSE NULL END AS geometritype,
	vs.j, ps.j`

// navngivnevejFrom joins the administrerende kommune (for navn), computes the
// envelope/visueltcenter geometry once via LATERAL, and aggregates the status-3
// vejstykker[] and postnumre[] as JSON.
const navngivnevejFrom = `
	FROM dar_navngivenvej nv
	LEFT JOIN dagi_kommuner k ON k.kode = nv.administrerende_kommune
	LEFT JOIN LATERAL (
		SELECT ST_Transform(ST_Envelope(nv.geom), 4326) AS env,
		       ST_Transform(ST_ClosestPoint(nv.geom, ST_Centroid(nv.geom)), 4326) AS vc
	) g ON nv.geom IS NOT NULL
	LEFT JOIN LATERAL (
		SELECT COALESCE(json_agg(json_build_object(
			'kommunekode', kd.kommune, 'kode', kd.vejkode, 'id', kd.id, 'darstatus', kd.status::int
		) ORDER BY kd.kommune, kd.vejkode), '[]') AS j
		FROM dar_navngivenvej_kommunedel kd
		WHERE kd.navngivenvej = nv.id AND kd.status = '3'
	) vs ON true
	LEFT JOIN LATERAL (
		-- postnumre[] reproduces DAWA's vejstykkerpostnumremat_view: the UNION of
		-- (1) the road's address postnumre (its status-3 Husnummer rows) and
		-- (2) the DAGI postnummer polygons the road LINE intersects by >7 m,
		-- EXCLUDING gade-postnumre (nr 1000–1999). The DAR NavngivenVejPostnummer
		-- link table is a superset DAWA does not use.
		SELECT COALESCE(json_agg(json_build_object('nr', t.postnr, 'navn', t.navn) ORDER BY t.postnr), '[]') AS j
		FROM (
			SELECT p.postnr, p.navn
			FROM dar_husnummer h
			JOIN dar_postnummer p ON p.id = h.postnummer_id
			WHERE h.navngivenvej = nv.id
			UNION
			SELECT pn.nr AS postnr, pn.navn
			FROM dagi_postnumre pn
			WHERE nv.geom IS NOT NULL AND ST_Intersects(nv.geom, pn.geom)
			  AND ST_Length(ST_Intersection(nv.geom, pn.geom)) > 7
			  AND (pn.nr ~ '^[0-9]+$' AND NOT (pn.nr::int BETWEEN 1000 AND 1999))
		) t
	) ps ON true`

func scanNavngivenVej(row pgx.Row, baseURL string) (*NavngivenVej, error) {
	var nv NavngivenVej
	var id, status string
	var navn, adr, udtalt, komKode, komNavn *string
	var kilde, teknisk, registrering, noej, geomtype *string
	var bx0, bx1, bx2, bx3, vx, vy *float64
	var vsJSON, psJSON []byte
	if err := row.Scan(
		&id, &status, &navn, &adr, &udtalt,
		&komKode, &komNavn,
		&kilde, &teknisk, &registrering, &noej,
		&bx0, &bx1, &bx2, &bx3, &vx, &vy, &geomtype,
		&vsJSON, &psJSON,
	); err != nil {
		return nil, err
	}

	nv.ID = id
	nv.Href = fmt.Sprintf("%s/navngivneveje/%s", baseURL, id)
	nv.Darstatus = darstatusName(status)
	nv.Navn = navn
	nv.Adresseringsnavn = adr
	nv.Administrerendekommune = newKommuneRef(baseURL, komKode, komNavn)
	nv.Retskrivningskontrol = "Godkendt" // best-effort constant for status 3 (not a raw field)
	nv.Udtaltvejnavn = udtalt
	if bx0 != nil && bx1 != nil && bx2 != nil && bx3 != nil {
		nv.Bbox = &[4]float64{*bx0, *bx1, *bx2, *bx3}
	}
	if vx != nil && vy != nil {
		nv.Visueltcenter = &[2]float64{*vx, *vy}
	}
	// historik is not reproducible from the Current extract — left null.
	nv.Historik = NavngivenVejHistorik{}

	vejstykker, err := buildVejstykkeMiniRefs(vsJSON, baseURL)
	if err != nil {
		return nil, err
	}
	nv.Vejstykker = vejstykker
	postnumre, err := buildPostnrRefs(psJSON, baseURL)
	if err != nil {
		return nil, err
	}
	nv.Postnumre = postnumre

	nv.Beliggenhed = Beliggenhed{
		Oprindelse: Oprindelse{
			Kilde:               kilde,
			Tekniskstandard:     teknisk,
			Registrering:        registrering,
			Noejagtighedsklasse: noej,
		},
		Vejtilslutningspunkter: nil,
		Geometritype:           geomtype,
	}
	return &nv, nil
}

// buildVejstykkeMiniRefs turns the json_agg array into vejstykker[] refs. The
// nested href keeps kommunekode 4-char zero-padded (unlike the /vejstykker
// entity href, which strips leading zeros).
func buildVejstykkeMiniRefs(j []byte, baseURL string) ([]VejstykkeMiniRef, error) {
	out := []VejstykkeMiniRef{}
	if len(j) == 0 {
		return out, nil
	}
	var raw []vejstykkeMiniRaw
	if err := json.Unmarshal(j, &raw); err != nil {
		return nil, fmt.Errorf("decode vejstykker agg: %w", err)
	}
	for _, r := range raw {
		out = append(out, VejstykkeMiniRef{
			Href:        fmt.Sprintf("%s/vejstykker/%s/%s", baseURL, r.Kommunekode, r.Kode),
			Kommunekode: r.Kommunekode,
			Kode:        r.Kode,
			ID:          r.ID,
			Darstatus:   r.Darstatus,
		})
	}
	return out, nil
}

// buildPostnrRefs turns the json_agg array into postnumre[] refs.
func buildPostnrRefs(j []byte, baseURL string) ([]PostnrRef, error) {
	out := []PostnrRef{}
	if len(j) == 0 {
		return out, nil
	}
	var raw []postnrRaw
	if err := json.Unmarshal(j, &raw); err != nil {
		return nil, fmt.Errorf("decode postnumre agg: %w", err)
	}
	for _, r := range raw {
		out = append(out, PostnrRef{
			Href: fmt.Sprintf("%s/postnumre/%s", baseURL, r.Nr),
			Nr:   r.Nr,
			Navn: r.Navn,
		})
	}
	return out, nil
}

// scanNavngivenVejAuto reads a navngivnevejSelect row PLUS two trailing
// visueltcenter point coordinates (vc_x, vc_y, reprojected to WGS84) — the extra
// columns the /navngivneveje/autocomplete reduced element needs as
// visueltcenter_x / visueltcenter_y. It reuses scanNavngivenVej's column layout
// for the leading fields by scanning into a throwaway full NavngivenVej, then
// returns that plus the coords. The full scan is reused so the road's
// id/navn/adresseringsnavn/administrerendekommune stay byte-identical.
func scanNavngivenVejAuto(row pgx.Row, baseURL string) (*NavngivenVej, *float64, *float64, error) {
	var nv NavngivenVej
	var id, status string
	var navn, adr, udtalt, komKode, komNavn *string
	var kilde, teknisk, registrering, noej, geomtype *string
	var bx0, bx1, bx2, bx3, vx, vy *float64
	var vsJSON, psJSON []byte
	var acx, acy *float64
	if err := row.Scan(
		&id, &status, &navn, &adr, &udtalt,
		&komKode, &komNavn,
		&kilde, &teknisk, &registrering, &noej,
		&bx0, &bx1, &bx2, &bx3, &vx, &vy, &geomtype,
		&vsJSON, &psJSON,
		&acx, &acy,
	); err != nil {
		return nil, nil, nil, err
	}
	nv.ID = id
	nv.Href = fmt.Sprintf("%s/navngivneveje/%s", baseURL, id)
	nv.Darstatus = darstatusName(status)
	nv.Navn = navn
	nv.Adresseringsnavn = adr
	nv.Administrerendekommune = newKommuneRef(baseURL, komKode, komNavn)
	return &nv, acx, acy, nil
}

// GetNavngivenVej returns the status-3 navngivenvej with the given id, or
// pgx.ErrNoRows.
func GetNavngivenVej(ctx context.Context, pool *pgxpool.Pool, id, baseURL string) (*NavngivenVej, error) {
	sql := "SELECT " + navngivnevejSelect + navngivnevejFrom + " WHERE nv.id = $1 AND nv.status = '3'"
	return scanNavngivenVej(pool.QueryRow(ctx, sql, id), baseURL)
}

// ListNavngivneveje returns status-3 navngivneveje ordered by id (DAWA's default
// order). limit <= 0 returns all.
func ListNavngivneveje(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit int) ([]*NavngivenVej, error) {
	return ListNavngivnevejeFiltered(ctx, pool, baseURL, limit, 0, ListFilter{})
}

// ListNavngivnevejeFiltered is ListNavngivneveje with SQL-side q= over
// navn/adresseringsnavn, srid output reprojection and an optional spatial filter
// (against the road geometry), plus offset paging. A zero filter reproduces
// ListNavngivneveje byte-for-byte.
func ListNavngivnevejeFiltered(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit, offset int, f ListFilter) ([]*NavngivenVej, error) {
	var wb whereBuilder
	wb.addQ(f.Q, "nv.navn", "nv.adresseringsnavn")
	if f.Spatial != nil {
		wb.addSpatial(f.Spatial, "nv.geom")
	}

	sql := "SELECT " + f.applySRID(navngivnevejSelect+navngivnevejFrom) + " WHERE nv.status = '3'"
	if w := wb.sql(); w != "" {
		sql += " AND " + w
	}
	sql += " ORDER BY nv.id"
	sql = appendLimitOffset(sql, limit, offset)

	rows, err := pool.Query(ctx, sql, wb.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*NavngivenVej
	for rows.Next() {
		nv, err := scanNavngivenVej(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, nv)
	}
	return out, rows.Err()
}
