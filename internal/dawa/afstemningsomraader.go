package dawa

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Afstemningsomraade is the DAWA /afstemningsomraader/{kommunekode}/{nummer}
// response. The Go struct field order IS the JSON key order (controls
// byte-exactness via MarshalDAWA) and matches the golden enumeration exactly:
//
//	ændret, geo_version, geo_ændret, bbox, visueltcenter, href, dagi_id, nummer,
//	navn, afstemningssted{navn, adgangsadresse{...}} | null, kommune{href,kode,navn},
//	region{href,kode,navn}, opstillingskreds{href,nummer,navn},
//	storkreds{href,nummer,navn}, valglandsdel{href,bogstav,navn}
//
// The single-resource path key is the composite (kommune kode, nummer): DAWA's
// href is /afstemningsomraader/{kommunekode-unpadded}/{nummer} (e.g. .../101/1).
type Afstemningsomraade struct {
	Aendret          *string              `json:"ændret"`
	GeoVersion       *int                 `json:"geo_version"`
	GeoAendret       *string              `json:"geo_ændret"`
	Bbox             [4]float64           `json:"bbox"`
	Visueltcenter    [2]float64           `json:"visueltcenter"`
	Href             string               `json:"href"`
	DagiID           string               `json:"dagi_id"`
	Nummer           string               `json:"nummer"`
	Navn             string               `json:"navn"`
	Afstemningssted  *Afstemningssted     `json:"afstemningssted"`
	Kommune          *KommuneRef          `json:"kommune"`
	Region           *RegionRef           `json:"region"`
	Opstillingskreds *OpstillingskredsRef `json:"opstillingskreds"`
	Storkreds        *StorkredsRef        `json:"storkreds"`
	Valglandsdel     *ValglandsdelRef     `json:"valglandsdel"`
}

// Afstemningssted is the nested afstemningssted object {navn, adgangsadresse}.
type Afstemningssted struct {
	Navn           string                  `json:"navn"`
	Adgangsadresse *AfstemningsstedAdresse `json:"adgangsadresse"`
}

// AfstemningsstedAdresse is the embedded adgangsadresse sub-block of an
// afstemningssted: only {href, id, adressebetegnelse, koordinater}.
type AfstemningsstedAdresse struct {
	Href              string      `json:"href"`
	ID                string      `json:"id"`
	Adressebetegnelse string      `json:"adressebetegnelse"`
	Koordinater       *[2]float64 `json:"koordinater"`
}

// afstemningsomraadeSelect lists the scanned columns in scan order. bbox = the
// reprojected envelope of the area's own geom; visueltcenter = the precomputed
// polylabel point (stored 25832). The afstemningssted columns come from the
// stored DAR adresse UUID (afstemningssted_adresse_lokalid -> dar_husnummer.id)
// and its adgangspunkt; adressebetegnelse is built in Go from the parts.
const afstemningsomraadeSelect = `
	to_char(aendret AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS aendret,
	dagi_id, nummer, navn,
	round(ST_XMin(e)::numeric, 8)::float8, round(ST_YMin(e)::numeric, 8)::float8,
	round(ST_XMax(e)::numeric, 8)::float8, round(ST_YMax(e)::numeric, 8)::float8,
	round(ST_X(c)::numeric, 8)::float8,    round(ST_Y(c)::numeric, 8)::float8,
	komm_kode, komm_navn,
	region_kode, region_navn,
	opstil_nummer, opstil_navn,
	stork_nummer, stork_navn,
	vl_bogstav, vl_navn,
	st_navn,
	adr_id, adr_vejnavn, adr_husnr, adr_suppl_bynavn, adr_postnr, adr_postnrnavn,
	adr_lon, adr_lat`

// afstemningsomraadeGeom resolves the nested refs. The election chain mirrors
// /opstillingskredse: opstillingskreds via opstillingskreds_lokalid -> storkreds
// via storkreds_lokalid -> valglandsdel via valglandsdelsbogstav. kommune/region
// come from the area's own kommune_lokalid. The afstemningssted's adgangsadresse
// is resolved by joining the stored DAR adresse UUID (lowercased — the DAGI
// extract stores it uppercase) to dar_husnummer (only the columns needed for
// href, id, adressebetegnelse and koordinater).
const afstemningsomraadeGeom = `
	SELECT a.aendret, a.dagi_id, a.nummer, a.navn,
		ST_Transform(ST_Envelope(a.geom), 4326) AS e,
		ST_Transform(a.visueltcenter, 4326) AS c,
		kk.kode AS komm_kode, kk.navn AS komm_navn,
		reg.kode AS region_kode, reg.navn AS region_navn,
		opk.nummer AS opstil_nummer, opk.navn AS opstil_navn,
		st.nummer AS stork_nummer, st.navn AS stork_navn,
		st.valglandsdelsbogstav AS vl_bogstav, vl.navn AS vl_navn,
		a.afstemningssted_navn AS st_navn,
		h.id AS adr_id,
		nv.navn AS adr_vejnavn, h.husnummertekst AS adr_husnr,
		sb.navn AS adr_suppl_bynavn, p.postnr AS adr_postnr, p.navn AS adr_postnrnavn,
		CASE WHEN ap.geom IS NULL THEN NULL ELSE round(ST_X(ST_Transform(ap.geom, 4326))::numeric, 8)::float8 END AS adr_lon,
		CASE WHEN ap.geom IS NULL THEN NULL ELSE round(ST_Y(ST_Transform(ap.geom, 4326))::numeric, 8)::float8 END AS adr_lat
	FROM dagi_afstemningsomraader a
	LEFT JOIN dagi_kommuner kk ON kk.dagi_id = a.kommune_lokalid
	LEFT JOIN dagi_regioner reg ON reg.dagi_id = kk.region_lokalid
	LEFT JOIN dagi_opstillingskredse opk ON opk.dagi_id = a.opstillingskreds_lokalid
	LEFT JOIN dagi_storkredse st ON st.dagi_id = opk.storkreds_lokalid
	LEFT JOIN dagi_valglandsdele vl ON vl.bogstav = st.valglandsdelsbogstav
	LEFT JOIN dar_husnummer h ON h.id = lower(a.afstemningssted_adresse_lokalid) AND h.status = '3'
	LEFT JOIN dar_navngivenvej nv ON nv.id = h.navngivenvej
	LEFT JOIN dar_postnummer p ON p.id = h.postnummer_id
	LEFT JOIN dagi_supplerendebynavne sb ON sb.dar_uuid = h.supplerende_bynavn
	LEFT JOIN dar_adressepunkt ap ON ap.id_lokalid = h.adgangspunkt_id`

func scanAfstemningsomraade(row pgx.Row, baseURL string) (*Afstemningsomraade, error) {
	var a Afstemningsomraade
	var kommKode, kommNavn, regKode, regNavn *string
	var opstilNummer, opstilNavn, storkNummer, storkNavn, vlBogstav, vlNavn *string
	var stNavn *string
	var adrID, adrVejnavn, adrHusnr, adrSupplBynavn, adrPostnr, adrPostnrnavn *string
	var adrLon, adrLat *float64
	if err := row.Scan(
		&a.Aendret, &a.DagiID, &a.Nummer, &a.Navn,
		&a.Bbox[0], &a.Bbox[1], &a.Bbox[2], &a.Bbox[3],
		&a.Visueltcenter[0], &a.Visueltcenter[1],
		&kommKode, &kommNavn, &regKode, &regNavn,
		&opstilNummer, &opstilNavn, &storkNummer, &storkNavn,
		&vlBogstav, &vlNavn,
		&stNavn,
		&adrID, &adrVejnavn, &adrHusnr, &adrSupplBynavn, &adrPostnr, &adrPostnrnavn,
		&adrLon, &adrLat,
	); err != nil {
		return nil, err
	}
	a.GeoAendret = a.Aendret
	// nummer is rendered with leading zeros stripped ("01" -> "1"), matching DAWA.
	a.Nummer = stripZeros(a.Nummer)
	// href is keyed by the area's OWN kommune kode (unpadded) + nummer (unpadded).
	a.Href = fmt.Sprintf("%s/afstemningsomraader/%s/%s", baseURL, stripZeros(deref(kommKode)), a.Nummer)
	a.Kommune = newKommuneRef(baseURL, kommKode, kommNavn)
	a.Region = newRegionRef(baseURL, regKode, regNavn)
	a.Opstillingskreds = newOpstillingskredsRef(baseURL, opstilNummer, opstilNavn)
	a.Storkreds = newStorkredsRef(baseURL, storkNummer, storkNavn)
	a.Valglandsdel = newValglandsdelRef(baseURL, vlBogstav, vlNavn)

	// afstemningssted: present only when the area carries an afstemningssted navn.
	// Its embedded adgangsadresse is present only when the stored DAR adresse UUID
	// resolves to a (status-3) adgangsadresse.
	if stNavn != nil {
		stedt := &Afstemningssted{Navn: *stNavn}
		if adrID != nil {
			adr := &AfstemningsstedAdresse{
				Href: fmt.Sprintf("%s/adgangsadresser/%s", baseURL, *adrID),
				ID:   *adrID,
				Adressebetegnelse: adressebetegnelse(
					deref(adrVejnavn), formatHusnr(deref(adrHusnr)), "", "",
					deref(adrSupplBynavn), deref(adrPostnr), deref(adrPostnrnavn), true),
			}
			if adrLon != nil && adrLat != nil {
				adr.Koordinater = &[2]float64{*adrLon, *adrLat}
			}
			stedt.Adgangsadresse = adr
		}
		a.Afstemningssted = stedt
	}
	return &a, nil
}

// GetAfstemningsomraade returns the afstemningsområde identified by its dagi_id,
// or pgx.ErrNoRows. (The composite (kommunekode, nummer) also identifies it; the
// dagi_id is the stable primary key, so it is used here for the verify path.)
func GetAfstemningsomraade(ctx context.Context, pool *pgxpool.Pool, dagiID, baseURL string) (*Afstemningsomraade, error) {
	sql := "SELECT " + afstemningsomraadeSelect + " FROM (" + afstemningsomraadeGeom + " WHERE a.dagi_id = $1) q"
	return scanAfstemningsomraade(pool.QueryRow(ctx, sql, dagiID), baseURL)
}

// GetAfstemningsomraadeByKommuneNummer returns the afstemningsområde identified
// by the composite (kommune kode, nummer) — DAWA's single-resource path key
// /afstemningsomraader/{kommunekode}/{nummer}. kommunekode may be padded or not;
// it is matched against the area's own kommune kode (4-padded), and nummer is
// matched numerically (the DB stores "01" but the path carries "1"). Returns
// pgx.ErrNoRows when absent.
func GetAfstemningsomraadeByKommuneNummer(ctx context.Context, pool *pgxpool.Pool, kommunekode, nummer, baseURL string) (*Afstemningsomraade, error) {
	sql := "SELECT " + afstemningsomraadeSelect + " FROM (" + afstemningsomraadeGeom +
		" WHERE kk.kode = $1 AND a.nummer::int = $2::int) q"
	return scanAfstemningsomraade(pool.QueryRow(ctx, sql, kode4(kommunekode), nummer), baseURL)
}

// ListAfstemningsomraader returns all afstemningsområder ordered the way DAWA
// orders them: by kommune kode, then nummer numerically.
func ListAfstemningsomraader(ctx context.Context, pool *pgxpool.Pool, baseURL string) ([]*Afstemningsomraade, error) {
	sql := "SELECT " + afstemningsomraadeSelect + " FROM (" + afstemningsomraadeGeom + ") q ORDER BY komm_kode, nummer::int"
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Afstemningsomraade
	for rows.Next() {
		a, err := scanAfstemningsomraade(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
