package dawa

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Jordstykke struct {
	Matrikelnr                string      `json:"matrikelnr"`
	Bbox                      *[4]float64 `json:"bbox"`
	Visueltcenter             *[2]float64 `json:"visueltcenter"`
	Href                      string      `json:"href"`
	Ejerlav                   EjerlavRef  `json:"ejerlav"`
	Kommune                   *KommuneRef `json:"kommune"`
	Esrejendomsnr             string      `json:"esrejendomsnr"`
	UdvidetEsrejendomsnr      string      `json:"udvidet_esrejendomsnr"`
	Sfeejendomsnr             *string     `json:"sfeejendomsnr"`
	Bfenummer                 *int64      `json:"bfenummer"`
	Aendret                   *string     `json:"ændret"`
	GeoVersion                *int        `json:"geo_version"`
	GeoAendret                *string     `json:"geo_ændret"`
	Faelleslod                bool        `json:"fælleslod"`
	Moderjordstykke           *int64      `json:"moderjordstykke"`
	Registreretareal          *int        `json:"registreretareal"`
	Arealberegningsmetode     *string     `json:"arealberegningsmetode"`
	Vejareal                  *int        `json:"vejareal"`
	Vejarealberegningsmetode  string      `json:"vejarealberegningsmetode"`
	Vandarealberegningsmetode string      `json:"vandarealberegningsmetode"`
	Featureid                 string      `json:"featureid"`
	Region                    *RegionRef  `json:"region"`
	Sogn                      *SogneRef   `json:"sogn"`
	Retskreds                 any         `json:"retskreds"`
}

type EjerlavRef struct {
	Kode int    `json:"kode"`
	Navn string `json:"navn"`
	Href string `json:"href"`
}

type SogneRef struct {
	Href string `json:"href"`
	Kode string `json:"kode"`
	Navn string `json:"navn"`
}

const jordstykkeCols = `
	matrikelnr,
	CASE WHEN e IS NULL THEN NULL ELSE round(ST_XMin(e)::numeric, 8)::float8 END,
	CASE WHEN e IS NULL THEN NULL ELSE round(ST_YMin(e)::numeric, 8)::float8 END,
	CASE WHEN e IS NULL THEN NULL ELSE round(ST_XMax(e)::numeric, 8)::float8 END,
	CASE WHEN e IS NULL THEN NULL ELSE round(ST_YMax(e)::numeric, 8)::float8 END,
	CASE WHEN c IS NULL THEN NULL ELSE round(ST_X(c)::numeric, 8)::float8 END,
	CASE WHEN c IS NULL THEN NULL ELSE round(ST_Y(c)::numeric, 8)::float8 END,
	ejerlav_kode, ejerlav_navn, kommune_kode, kommune_navn, sfe_lokalid, bfenummer,
	aendret, faelleslod, moder_lokalid, registreretareal, arealberegningsmetode_raw,
	vejareal, featureid, region_kode, region_navn, sogn_kode, sogn_navn`

const jordstykkeFrom = `
	SELECT j.matrikelnr, j.ejerlav_kode, el.navn AS ejerlav_navn,
		j.kommune_kode, k.navn AS kommune_navn,
		j.sfe_lokalid, b.bfenummer,
		to_char(j.aendret AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS aendret,
		j.faelleslod, j.moder_lokalid, j.registreretareal, j.arealberegningsmetode_raw,
		j.vejareal, j.id_lokalid AS featureid,
		j.region_kode, r.navn AS region_navn, j.sogn_kode, s.navn AS sogn_navn,
		ST_Transform(ST_Envelope(j.geom), 4326) AS e,
		ST_Transform((ST_MaximumInscribedCircle(lp.geom)).center, 4326) AS c
	FROM mat_jordstykke j
	LEFT JOIN mat_ejerlav el ON el.kode = j.ejerlav_kode
	LEFT JOIN dagi_kommuner k ON k.kode = j.kommune_kode
	LEFT JOIN dagi_regioner r ON r.kode = j.region_kode
	LEFT JOIN dagi_sogne s ON s.kode = j.sogn_kode
	LEFT JOIN mat_sfe_bfe b ON b.id_lokalid = j.sfe_lokalid
	LEFT JOIN LATERAL (
		SELECT d.geom FROM (SELECT (ST_Dump(j.geom)).geom AS geom) d
		ORDER BY ST_Area(d.geom) DESC LIMIT 1
	) lp ON true`

func scanJordstykke(row pgx.Row, baseURL string) (*Jordstykke, error) {
	var j Jordstykke
	var bxmin, bymin, bxmax, bymax, vcx, vcy *float64
	var ejerlavKode int
	var ejerlavNavn *string
	var kommuneKode, kommuneNavn *string
	var moderLokalid, arealRaw *string
	var regionKode, regionNavn, sognKode, sognNavn *string
	if err := row.Scan(
		&j.Matrikelnr,
		&bxmin, &bymin, &bxmax, &bymax, &vcx, &vcy,
		&ejerlavKode, &ejerlavNavn, &kommuneKode, &kommuneNavn, &j.Sfeejendomsnr, &j.Bfenummer,
		&j.Aendret, &j.Faelleslod, &moderLokalid, &j.Registreretareal, &arealRaw,
		&j.Vejareal, &j.Featureid, &regionKode, &regionNavn, &sognKode, &sognNavn,
	); err != nil {
		return nil, err
	}
	if bxmin != nil && bymin != nil && bxmax != nil && bymax != nil {
		j.Bbox = &[4]float64{*bxmin, *bymin, *bxmax, *bymax}
	}
	if vcx != nil && vcy != nil {
		j.Visueltcenter = &[2]float64{*vcx, *vcy}
	}
	j.Href = fmt.Sprintf("%s/jordstykker/%d/%s", baseURL, ejerlavKode, j.Matrikelnr)
	j.Ejerlav = EjerlavRef{
		Kode: ejerlavKode,
		Navn: deref(ejerlavNavn),
		Href: fmt.Sprintf("%s/ejerlav/%d", baseURL, ejerlavKode),
	}
	j.Kommune = newKommuneRef(baseURL, kommuneKode, kommuneNavn)
	j.Region = newRegionRef(baseURL, regionKode, regionNavn)
	j.Sogn = newSogneRef(baseURL, sognKode, sognNavn)
	j.Esrejendomsnr = "0"
	j.UdvidetEsrejendomsnr = "0"
	j.Vejarealberegningsmetode = "e"
	j.Vandarealberegningsmetode = "ukendt"
	j.Arealberegningsmetode = arealMetode(arealRaw)
	j.Moderjordstykke = parseInt64Ptr(moderLokalid)
	j.GeoAendret = j.Aendret
	return &j, nil
}

func arealMetode(raw *string) *string {
	if raw == nil {
		return nil
	}
	s := *raw
	if i := strings.LastIndex(s, " - "); i >= 0 {
		v := s[i+3:]
		return &v
	}
	return &s
}

func parseInt64Ptr(s *string) *int64 {
	if s == nil {
		return nil
	}
	if v, err := strconv.ParseInt(*s, 10, 64); err == nil {
		return &v
	}
	return nil
}

func newSogneRef(baseURL string, kode, navn *string) *SogneRef {
	if kode == nil {
		return nil
	}
	return &SogneRef{
		Href: fmt.Sprintf("%s/sogne/%s", baseURL, *kode),
		Kode: *kode,
		Navn: deref(navn),
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func GetJordstykke(ctx context.Context, pool *pgxpool.Pool, ejerlavKode int, matrikelnr, baseURL string) (*Jordstykke, error) {
	sql := "SELECT " + jordstykkeCols + " FROM (" + jordstykkeFrom +
		" WHERE j.ejerlav_kode = $1 AND j.matrikelnr = $2) q"
	return scanJordstykke(pool.QueryRow(ctx, sql, ejerlavKode, matrikelnr), baseURL)
}

func ListJordstykker(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit, offset int) ([]*Jordstykke, error) {
	return ListJordstykkerFiltered(ctx, pool, baseURL, limit, offset, ListFilter{})
}

// ListJordstykkerFiltered is ListJordstykker with SQL-side ejerlavkode= equality,
// srid output reprojection and an optional spatial filter (against the jordstykke
// polygon), plus offset paging. The ejerlavkode/spatial predicates are pushed
// into the inner jordstykke source (where j.geom is in scope). A zero filter
// reproduces ListJordstykker byte-for-byte.
func ListJordstykkerFiltered(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit, offset int, f ListFilter) ([]*Jordstykke, error) {
	var wb whereBuilder
	if v, ok := f.Filters["ejerlavkode"]; ok {
		wb.addEqInt("j.ejerlav_kode", v)
	}
	if f.Spatial != nil {
		wb.addSpatial(f.Spatial, "j.geom")
	}

	inner := f.applySRID(jordstykkeFrom)
	if w := wb.sql(); w != "" {
		inner += " WHERE " + w
	}
	sql := "SELECT " + jordstykkeCols + " FROM (" + inner + ") q ORDER BY ejerlav_kode, matrikelnr"
	sql = appendLimitOffset(sql, limit, offset)

	rows, err := pool.Query(ctx, sql, wb.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Jordstykke
	for rows.Next() {
		j, err := scanJordstykke(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
