package dawa

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Adgangsadresse is the DAWA /adgangsadresser/{id} response. The Go struct field
// order IS the JSON key order (controls byte-exactness via MarshalDAWA), and
// matches the golden enumeration exactly:
//
//	href, id, adressebetegnelse, kvh, status, darstatus, vejstykke, husnr,
//	navngivenvej, supplerendebynavn, supplerendebynavn2, postnummer,
//	stormodtagerpostnummer, kommune, ejerlav, esrejendomsnr, matrikelnr,
//	historik, adgangspunkt, vejpunkt, DDKN, sogn, region, landsdel, retskreds,
//	politikreds, opstillingskreds, afstemningsområde, storkreds, valglandsdel,
//	zone, jordstykke, bebyggelser, brofast
type Adgangsadresse struct {
	Href                   string                 `json:"href"`
	ID                     string                 `json:"id"`
	Adressebetegnelse      string                 `json:"adressebetegnelse"`
	Kvh                    string                 `json:"kvh"`
	Status                 int                    `json:"status"`
	Darstatus              int                    `json:"darstatus"`
	Vejstykke              *AdgVejstykkeRef       `json:"vejstykke"`
	Husnr                  string                 `json:"husnr"`
	Navngivenvej           *NavngivenvejIDRef     `json:"navngivenvej"`
	Supplerendebynavn      *string                `json:"supplerendebynavn"`
	Supplerendebynavn2     *Supplerendebynavn2Ref `json:"supplerendebynavn2"`
	Postnummer             *AdgPostnrRef          `json:"postnummer"`
	Stormodtagerpostnummer *AdgPostnrRef          `json:"stormodtagerpostnummer"`
	Kommune                *KommuneRef            `json:"kommune"`
	Ejerlav                *AdgEjerlavRef         `json:"ejerlav"`
	Esrejendomsnr          *string                `json:"esrejendomsnr"`
	Matrikelnr             *string                `json:"matrikelnr"`
	Historik               AdgHistorik            `json:"historik"`
	Adgangspunkt           *Adgangspunkt          `json:"adgangspunkt"`
	Vejpunkt               *Vejpunkt              `json:"vejpunkt"`
	DDKN                   *DDKN                  `json:"DDKN"`
	Sogn                   *SogneRef              `json:"sogn"`
	Region                 *RegionRef             `json:"region"`
	Landsdel               *LandsdelRef           `json:"landsdel"`
	Retskreds              *KodeNavnRef           `json:"retskreds"`
	Politikreds            *KodeNavnRef           `json:"politikreds"`
	Opstillingskreds       *KodeNavnRef           `json:"opstillingskreds"`
	Afstemningsomraade     *AfstemningsomraadeRef `json:"afstemningsområde"`
	Storkreds              *StorkredsRef          `json:"storkreds"`
	Valglandsdel           *ValglandsdelRef       `json:"valglandsdel"`
	Zone                   string                 `json:"zone"`
	Jordstykke             *JordstykkeAdgRef      `json:"jordstykke"`
	Bebyggelser            []BebyggelseRef        `json:"bebyggelser"`
	Brofast                bool                   `json:"brofast"`
}

// AdgVejstykkeRef is the nested vejstykke object {href,navn,adresseringsnavn,kode}.
// href uses unpadded kommunekode/vejkode; kode is the 4-padded vejkode.
type AdgVejstykkeRef struct {
	Href             string  `json:"href"`
	Navn             *string `json:"navn"`
	Adresseringsnavn *string `json:"adresseringsnavn"`
	Kode             string  `json:"kode"`
}

// NavngivenvejIDRef is the nested navngivenvej object {href,id} (or null).
type NavngivenvejIDRef struct {
	Href string `json:"href"`
	ID   string `json:"id"`
}

// Supplerendebynavn2Ref is the nested supplerendebynavn2 object {href,dagi_id}.
type Supplerendebynavn2Ref struct {
	Href   string `json:"href"`
	DagiID string `json:"dagi_id"`
}

// AdgPostnrRef is the nested postnummer/stormodtagerpostnummer {href,nr,navn}.
type AdgPostnrRef struct {
	Href string `json:"href"`
	Nr   string `json:"nr"`
	Navn string `json:"navn"`
}

// AdgEjerlavRef is the TOP-LEVEL ejerlav object {kode,navn} (kode is int, no href;
// distinct from the jordstykke.ejerlav which carries an href).
type AdgEjerlavRef struct {
	Kode int    `json:"kode"`
	Navn string `json:"navn"`
}

// AdgHistorik is the address historik {oprettet,ændret,ikrafttrædelse,nedlagt}.
// Timestamps are DAWA-internal (rendered without trailing Z), best-effort here.
type AdgHistorik struct {
	Oprettet        *string `json:"oprettet"`
	Aendret         *string `json:"ændret"`
	Ikrafttraedelse *string `json:"ikrafttrædelse"`
	Nedlagt         *string `json:"nedlagt"`
}

// Adgangspunkt is the adgangspunkt object. Key order: id, koordinater, højde,
// nøjagtighed, kilde, tekniskstandard, tekstretning, ændret. kilde is an int.
type Adgangspunkt struct {
	ID              string      `json:"id"`
	Koordinater     *[2]float64 `json:"koordinater"`
	Hoejde          *float64    `json:"højde"`
	Noejagtighed    *string     `json:"nøjagtighed"`
	Kilde           *int        `json:"kilde"`
	Tekniskstandard *string     `json:"tekniskstandard"`
	Tekstretning    *float64    `json:"tekstretning"`
	Aendret         *string     `json:"ændret"`
}

// Vejpunkt is the vejpunkt object. DIFFERENT key order than adgangspunkt: id,
// kilde, nøjagtighed, tekniskstandard, koordinater, ændret. kilde is a STRING.
type Vejpunkt struct {
	ID              string      `json:"id"`
	Kilde           *string     `json:"kilde"`
	Noejagtighed    *string     `json:"nøjagtighed"`
	Tekniskstandard *string     `json:"tekniskstandard"`
	Koordinater     *[2]float64 `json:"koordinater"`
	Aendret         *string     `json:"ændret"`
}

// DDKN is the Det Danske Kvadratnet object {m100,km1,km10}.
type DDKN struct {
	M100 string `json:"m100"`
	Km1  string `json:"km1"`
	Km10 string `json:"km10"`
}

// LandsdelRef is the nested landsdel object {href,nuts3,navn} (note nuts3, not kode).
type LandsdelRef struct {
	Href  string `json:"href"`
	Nuts3 string `json:"nuts3"`
	Navn  string `json:"navn"`
}

// KodeNavnRef is the generic nested {href,kode,navn} ref (retskreds, politikreds,
// opstillingskreds).
type KodeNavnRef struct {
	Href string `json:"href"`
	Kode string `json:"kode"`
	Navn string `json:"navn"`
}

// AfstemningsomraadeRef is the nested afstemningsområde {href,nummer,navn}. href
// uses the UNPADDED kommunekode + nummer.
type AfstemningsomraadeRef struct {
	Href   string `json:"href"`
	Nummer string `json:"nummer"`
	Navn   string `json:"navn"`
}

// JordstykkeAdgRef is the nested jordstykke {href,ejerlav{kode,navn,href},
// matrikelnr,esrejendomsnr}.
type JordstykkeAdgRef struct {
	Href          string     `json:"href"`
	Ejerlav       EjerlavRef `json:"ejerlav"`
	Matrikelnr    string     `json:"matrikelnr"`
	Esrejendomsnr string     `json:"esrejendomsnr"`
}

// BebyggelseRef is one bebyggelse element {id,kode,type,navn,href}.
type BebyggelseRef struct {
	ID   string `json:"id"`
	Kode *int   `json:"kode"`
	Type string `json:"type"`
	Navn string `json:"navn"`
	Href string `json:"href"`
}

// ---- kvh / adressebetegnelse helpers (pure, byte-exact per the goldens) ----

// kode4 zero-pads a numeric code to 4 chars (kode4String: ('0000'+n).slice(-4)).
func kode4(s string) string {
	if len(s) >= 4 {
		return s[len(s)-4:]
	}
	return strings.Repeat("0", 4-len(s)) + s
}

// padUnderscore left-pads v with underscores to len (padUnderscore in DAWA).
func padUnderscore(v string, length int) string {
	if len(v) >= length {
		return v[len(v)-length:]
	}
	return strings.Repeat("_", length-len(v)) + v
}

// formatHusnr strips leading zeros off the numeric part and uppercases the
// optional letter: regex ^(\d*)([A-ZÆØÅ]?)$. e.g. "0068" -> "68", "41A" -> "41A".
func formatHusnr(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	numPart := s[:i]
	letter := strings.ToUpper(s[i:])
	if numPart != "" {
		if n, err := strconv.Atoi(numPart); err == nil {
			numPart = strconv.Itoa(n)
		}
	}
	return numPart + letter
}

// kvh = kode4(kommunekode) + kode4(vejkode) + padUnderscore(formatHusnr(husnr), 4).
// e.g. kommune 0461, vej 3436, husnr 68 -> "04613436__68".
func computeKvh(kommunekode, vejkode, husnr string) string {
	return kode4(kommunekode) + kode4(vejkode) + padUnderscore(formatHusnr(husnr), 4)
}

// adressebetegnelse builds DAWA's address string. adgangOnly=true omits etage/dør
// (adgangsadresse); adgangOnly=false includes ", {etage}. {dør}" (adresse).
func adressebetegnelse(vejnavn, husnr, etage, doer, supplBynavn, postnr, postnrnavn string, adgangOnly bool) string {
	var b strings.Builder
	b.WriteString(vejnavn)
	if husnr != "" {
		b.WriteString(" ")
		b.WriteString(husnr)
	}
	if !adgangOnly && (etage != "" || doer != "") {
		b.WriteString(",")
		if etage != "" {
			b.WriteString(" ")
			b.WriteString(etage)
			b.WriteString(".")
		}
		if doer != "" {
			b.WriteString(" ")
			b.WriteString(doer)
		}
	}
	b.WriteString(", ")
	if supplBynavn != "" {
		b.WriteString(supplBynavn)
		b.WriteString(", ")
	}
	b.WriteString(postnr)
	b.WriteString(" ")
	b.WriteString(postnrnavn)
	return b.String()
}

// adgangsadresseCols lists the scanned columns in scan order. Geometry-derived
// values (koordinater, DDKN, spatial DAGI refs) come from the adgangspunkt 25832
// point; kommune/region from the vejmidte kommunekode; sogn/afstemning from the
// stored DAGI inddeling UUIDs; jordstykke from the MAT jordstykke UUID.
const adgangsadresseCols = `
	h.id,
	h.dar_status,
	-- kommunekode + vejkode: prefer the vejmidte "KKKK-VVVV" fast path, else the
	-- kommunedel join by navngivenvej.
	COALESCE(split_part(h.vejmidte, '-', 1), kd.kommune) AS kommunekode,
	COALESCE(split_part(h.vejmidte, '-', 2), kd.vejkode) AS vejkode,
	h.husnummertekst,
	h.navngivenvej,
	nv.navn AS vejnavn, nv.adresseringsnavn,
	p.postnr, p.navn AS postnrnavn,
	sb.navn AS suppl_bynavn, sb.dagi_id AS suppl_bynavn2_dagi,
	k.navn AS kommune_navn,
	-- historik: oprettet/ikrafttrædelse derive from the DAR virkning chain
	-- (dar_husnummer_hist) — oprettet = the chain's first virkningFra,
	-- ikrafttrædelse = the first gældende (status 3) virkningFra. Byte-verified
	-- against live nestet on foreløbig-start chains (4/4 exact, 2026-06-12).
	-- ændret stays the current row's metadata: live's value is DAWA's OWN event
	-- clock (proven 2s off DAR on the same event) — tolerated, not derivable.
	to_char((SELECT MIN(hh.virkning_start) FROM dar_husnummer_hist hh WHERE hh.id = h.id)
		AT TIME ZONE 'Europe/Copenhagen', 'YYYY-MM-DD"T"HH24:MI:SS.MS') AS h_oprettet,
	to_char(h.aendret AT TIME ZONE 'Europe/Copenhagen', 'YYYY-MM-DD"T"HH24:MI:SS.MS') AS h_aendret,
	to_char((SELECT MIN(hh.virkning_start) FROM dar_husnummer_hist hh WHERE hh.id = h.id AND hh.dar_status = 3)
		AT TIME ZONE 'Europe/Copenhagen', 'YYYY-MM-DD"T"HH24:MI:SS.MS') AS h_ikraft,
	-- adgangspunkt
	ap.id_lokalid AS ap_id,
	CASE WHEN ap.geom IS NULL THEN NULL ELSE round(ST_X(ST_Transform(ap.geom, 4326))::numeric, 8)::float8 END AS ap_lon,
	CASE WHEN ap.geom IS NULL THEN NULL ELSE round(ST_Y(ST_Transform(ap.geom, 4326))::numeric, 8)::float8 END AS ap_lat,
	ap.hoejde, ap.noejagtighedsklasse, ap.kilde AS ap_kilde, ap.tekniskstandard,
	-- tekstretning: DAWA serves an independent adgangspunkt attribute that is NOT
	-- byte-derivable from the raw DAR extract. The extract only carries
	-- Husnummer.husnummerretning (a unit direction vector, stored as
	-- husnummerretning_dx/dy); its compass bearing matches DAWA's tekstretning on
	-- only 2 of the 5 per_side_5 golden rows (e.g. 283.77 yes, but vec (1,0)→0
	-- vs golden 200), so it is a DIFFERENT value. We therefore serve NULL rather
	-- than a wrong derived bearing — classified as known-divergence in verify.
	NULL::float8 AS ap_tekstretning,
	to_char(ap.aendret AT TIME ZONE 'Europe/Copenhagen', 'YYYY-MM-DD"T"HH24:MI:SS.MS') AS ap_aendret,
	-- vejpunkt
	vp.id_lokalid AS vp_id,
	vp.kilde AS vp_kilde, vp.noejagtighedsklasse AS vp_noej, vp.tekniskstandard AS vp_teknisk,
	CASE WHEN vp.geom IS NULL THEN NULL ELSE round(ST_X(ST_Transform(vp.geom, 4326))::numeric, 8)::float8 END AS vp_lon,
	CASE WHEN vp.geom IS NULL THEN NULL ELSE round(ST_Y(ST_Transform(vp.geom, 4326))::numeric, 8)::float8 END AS vp_lat,
	regexp_replace(to_char(vp.aendret AT TIME ZONE 'Europe/Copenhagen', 'YYYY-MM-DD"T"HH24:MI:SS.US'), '(\.[0-9]{3}[0-9]*?)0+$', '\1') AS vp_aendret,
	-- DDKN from the stored 25832 coords (nord cell first, oest second)
	CASE WHEN ap.geom IS NULL THEN NULL ELSE
		'100m_' || floor(ST_Y(ap.geom)/100)::bigint || '_' || floor(ST_X(ap.geom)/100)::bigint END AS ddkn_m100,
	CASE WHEN ap.geom IS NULL THEN NULL ELSE
		'1km_' || floor(ST_Y(ap.geom)/1000)::bigint || '_' || floor(ST_X(ap.geom)/1000)::bigint END AS ddkn_km1,
	CASE WHEN ap.geom IS NULL THEN NULL ELSE
		'10km_' || floor(ST_Y(ap.geom)/10000)::bigint || '_' || floor(ST_X(ap.geom)/10000)::bigint END AS ddkn_km10,
	-- sogn (DAGI inddeling UUID -> dagi_sogne.dagi_id)
	sg.kode AS sogn_kode, sg.navn AS sogn_navn,
	-- region (kommune.region_lokalid -> dagi_regioner)
	reg.kode AS region_kode, reg.navn AS region_navn,
	-- landsdel (spatial: ST_Covers landsdel.geom over adgangspunkt)
	ld.nuts3 AS landsdel_nuts3, ld.navn AS landsdel_navn,
	-- retskreds / politikreds (spatial ST_Covers)
	rk.kode AS retskreds_kode, rk.navn AS retskreds_navn,
	pk.kode AS politikreds_kode, pk.navn AS politikreds_navn,
	-- afstemning chain: afstemningsområde -> opstillingskreds -> storkreds -> valglandsdel
	-- afstem_komm is the afstemningsområde's OWN kommunekode (its href key), which
	-- can differ from the address's vejmidte kommune.
	af.nummer AS afstem_nummer, af.navn AS afstem_navn, afk.kode AS afstem_komm,
	opk.nummer AS opstil_nummer, opk.navn AS opstil_navn,
	sk.nummer AS storkreds_nummer, sk.navn AS storkreds_navn,
	sk.valglandsdelsbogstav AS vl_bogstav, vl.navn AS vl_navn,
	-- jordstykke (MAT) + ejerlav
	j.ejerlav_kode AS j_ejerlav_kode, el.navn AS j_ejerlav_navn,
	j.matrikelnr AS j_matrikelnr,
	-- bebyggelser membership: spatial point-in-polygon of the adgangspunkt (ap.geom,
	-- 25832) within the bebyggelse polygon (ds_steder.geom, 25832), sorted by
	-- bebyggelse id ascending to match DAWA. navn is the place's primary name from
	-- ds_stednavne (brugsprioritet='primær'); kode is the bebyggelseskode (nullable).
	COALESCE((
		SELECT jsonb_agg(jsonb_build_object(
			'id', beb.id_lokalId,
			'kode', beb.bebyggelseskode,
			'type', beb.undertype,
			'navn', (SELECT (array_agg(sn2.skrivemaade ORDER BY sn2.navnefoelgenummer NULLS LAST, sn2.skrivemaade)
				FILTER (WHERE sn2.brugsprioritet = 'primær'))[1]
				FROM ds_stednavne sn2 WHERE sn2.place_objectid = beb.objectid)
		) ORDER BY beb.id_lokalId)
		FROM ds_steder beb
		WHERE beb.hovedtype = 'bebyggelse' AND ap.geom IS NOT NULL AND ST_Contains(beb.geom, ap.geom)
	), '[]'::jsonb) AS bebyggelser_json,
	-- brofast: DAWA-computed (not a DAR field). An address is NOT brofast iff its
	-- adgangspunkt lies in a sted marked brofast=false (the non-bridge islands) in
	-- the brofasthed seed table; otherwise true. Mirrors DAWA's ikke_brofaste_adresser.
	(NOT EXISTS (
		SELECT 1 FROM brofasthed bf
		JOIN ds_steder bs ON bs.id_lokalId = bf.stedid
		WHERE bf.brofast = false AND ap.geom IS NOT NULL AND ST_Contains(bs.geom, ap.geom)
	)) AS brofast`

// adgangsadresseFrom resolves all the joins. The kommunedel join (kd) is the
// fallback for kommunekode/vejkode when vejmidte is absent. The spatial DAGI
// refs use ST_Covers against the adgangspunkt 25832 point. It is the anchor
// (FROM dar_husnummer h) + the shared join graph; the adresse query reuses
// adgangsadresseJoins after its own dar_adresse-anchored FROM.
const adgangsadresseFrom = "\n\tFROM dar_husnummer h" + adgangsadresseJoins

// adgangsadresseJoins is the JOIN-only portion (no leading FROM), so the adresse
// query can anchor on dar_adresse, JOIN dar_husnummer h, then append these.
const adgangsadresseJoins = `
	LEFT JOIN dar_navngivenvej nv ON nv.id = h.navngivenvej
	LEFT JOIN LATERAL (
		-- One kommunedel per husnummer (the kommunekode/vejkode fallback when vejmidte
		-- is absent). A road spanning >1 kommune has >1 status-3 kommunedel; a plain
		-- join then duplicated the husnummer row. Pick the kommunedel whose kommune
		-- matches the husnummer's own kommune (h.kommune → kode), so vejkode is the
		-- right one, and LIMIT 1 guarantees no fan-out.
		SELECT t.kommune, t.vejkode
		FROM dar_navngivenvej_kommunedel t
		WHERE t.navngivenvej = h.navngivenvej AND t.status = '3'
		ORDER BY (t.kommune = (SELECT kk.kode FROM dagi_kommuner kk WHERE kk.dagi_id = h.kommune)) DESC NULLS LAST, t.kommune
		LIMIT 1
	) kd ON true
	LEFT JOIN dar_postnummer p ON p.id = h.postnummer_id
	LEFT JOIN dagi_supplerendebynavne sb ON sb.dar_uuid = h.supplerende_bynavn
	LEFT JOIN dagi_kommuner k ON k.dagi_id = h.kommune
	LEFT JOIN dagi_regioner reg ON reg.dagi_id = k.region_lokalid
	LEFT JOIN dar_adressepunkt ap ON ap.id_lokalid = h.adgangspunkt_id
	LEFT JOIN dar_adressepunkt vp ON vp.id_lokalid = h.vejpunkt_id
	LEFT JOIN dagi_sogne sg ON sg.dagi_id = h.sogn_dagi_id
	LEFT JOIN LATERAL (SELECT t.nummer, t.navn, t.opstillingskreds_lokalid, t.kommune_lokalid FROM dagi_afstemningsomraader t WHERE ap.geom IS NOT NULL AND ST_Covers(t.geom, ap.geom) LIMIT 1) af ON true
	LEFT JOIN dagi_kommuner afk ON afk.dagi_id = af.kommune_lokalid
	LEFT JOIN dagi_opstillingskredse opk ON opk.dagi_id = af.opstillingskreds_lokalid
	LEFT JOIN dagi_storkredse sk ON sk.dagi_id = opk.storkreds_lokalid
	LEFT JOIN dagi_valglandsdele vl ON vl.bogstav = sk.valglandsdelsbogstav
	LEFT JOIN mat_jordstykke j ON j.id_lokalid = h.jordstykke_id
	LEFT JOIN mat_ejerlav el ON el.kode = j.ejerlav_kode
	LEFT JOIN LATERAL (SELECT t.nuts3, t.navn FROM dagi_landsdele t WHERE ap.geom IS NOT NULL AND ST_Covers(t.geom, ap.geom) LIMIT 1) ld ON true
	LEFT JOIN LATERAL (SELECT t.kode, t.navn FROM dagi_retskredse t WHERE ap.geom IS NOT NULL AND ST_Covers(t.geom, ap.geom) LIMIT 1) rk ON true
	LEFT JOIN LATERAL (SELECT t.kode, t.navn FROM dagi_politikredse t WHERE ap.geom IS NOT NULL AND ST_Covers(t.geom, ap.geom) LIMIT 1) pk ON true`

// adgScan holds the scanned columns; nullable pointers track presence.
type adgScan struct {
	id          string
	darStatus   *int
	kommunekode *string
	vejkode     *string
	husnrtekst  *string
	navngivenv  *string
	vejnavn     *string
	adrnavn     *string
	postnr      *string
	postnrnavn  *string
	supplBynavn *string
	supplB2dagi *string
	kommuneNavn *string
	hOprettet   *string
	hAendret    *string
	hIkraft     *string
	apID        *string
	apLon       *float64
	apLat       *float64
	apHoejde    *float64
	apNoej      *string
	apKilde     *string
	apTeknisk   *string
	apTekstret  *float64
	apAendret   *string
	vpID        *string
	vpKilde     *string
	vpNoej      *string
	vpTeknisk   *string
	vpLon       *float64
	vpLat       *float64
	vpAendret   *string
	ddknM100    *string
	ddknKm1     *string
	ddknKm10    *string
	sognKode    *string
	sognNavn    *string
	regionKode  *string
	regionNavn  *string
	ldNuts3     *string
	ldNavn      *string
	rkKode      *string
	rkNavn      *string
	pkKode      *string
	pkNavn      *string
	afNummer    *string
	afNavn      *string
	afKomm      *string
	opNummer    *string
	opNavn      *string
	skNummer    *string
	skNavn      *string
	vlBogstav   *string
	vlNavn      *string
	jEjerlavK   *int
	jEjerlavN   *string
	jMatrikel   *string

	bebyggelserJSON []byte
	brofast         *bool
}

func scanAdgangsadresse(row pgx.Row, baseURL string) (*Adgangsadresse, error) {
	var s adgScan
	if err := row.Scan(
		&s.id, &s.darStatus, &s.kommunekode, &s.vejkode, &s.husnrtekst,
		&s.navngivenv, &s.vejnavn, &s.adrnavn, &s.postnr, &s.postnrnavn,
		&s.supplBynavn, &s.supplB2dagi, &s.kommuneNavn,
		&s.hOprettet, &s.hAendret, &s.hIkraft,
		&s.apID, &s.apLon, &s.apLat, &s.apHoejde, &s.apNoej, &s.apKilde, &s.apTeknisk, &s.apTekstret, &s.apAendret,
		&s.vpID, &s.vpKilde, &s.vpNoej, &s.vpTeknisk, &s.vpLon, &s.vpLat, &s.vpAendret,
		&s.ddknM100, &s.ddknKm1, &s.ddknKm10,
		&s.sognKode, &s.sognNavn, &s.regionKode, &s.regionNavn,
		&s.ldNuts3, &s.ldNavn, &s.rkKode, &s.rkNavn, &s.pkKode, &s.pkNavn,
		&s.afNummer, &s.afNavn, &s.afKomm, &s.opNummer, &s.opNavn, &s.skNummer, &s.skNavn, &s.vlBogstav, &s.vlNavn,
		&s.jEjerlavK, &s.jEjerlavN, &s.jMatrikel,
		&s.bebyggelserJSON, &s.brofast,
	); err != nil {
		return nil, err
	}
	return buildAdgangsadresse(&s, baseURL), nil
}

// buildAdgangsadresse assembles the full Adgangsadresse from a scanned row,
// applying the derived computed fields (kvh, adressebetegnelse, status mapping,
// hrefs, null handling). Exported building blocks are reused by the adresser
// embedding.
func buildAdgangsadresse(s *adgScan, baseURL string) *Adgangsadresse {
	a := &Adgangsadresse{
		ID:      s.id,
		Href:    fmt.Sprintf("%s/adgangsadresser/%s", baseURL, s.id),
		Husnr:   formatHusnr(deref(s.husnrtekst)),
		Zone:    "Udfaset",
		// brofast comes from the brofasthed seed join (s.brofast); default true when
		// the column is absent/null (e.g. no adgangspunkt) — DAWA emits a bool, never null.
		Brofast: s.brofast == nil || *s.brofast,
	}
	kommunekode := deref(s.kommunekode)
	vejkode := deref(s.vejkode)
	a.Kvh = computeKvh(kommunekode, vejkode, deref(s.husnrtekst))

	a.Darstatus = derefInt(s.darStatus)
	a.Status = darStatusToDawa(a.Darstatus)

	a.Adressebetegnelse = adressebetegnelse(
		deref(s.vejnavn), a.Husnr, "", "", deref(s.supplBynavn), deref(s.postnr), deref(s.postnrnavn), true)

	// vejstykke {href (unpadded), navn, adresseringsnavn, kode (4-padded)}
	if kommunekode != "" && vejkode != "" {
		a.Vejstykke = &AdgVejstykkeRef{
			Href:             fmt.Sprintf("%s/vejstykker/%s/%s", baseURL, stripZeros(kommunekode), stripZeros(vejkode)),
			Navn:             s.vejnavn,
			Adresseringsnavn: s.adrnavn,
			Kode:             kode4(vejkode),
		}
	}

	if s.navngivenv != nil {
		a.Navngivenvej = &NavngivenvejIDRef{
			Href: fmt.Sprintf("%s/navngivneveje/%s", baseURL, *s.navngivenv),
			ID:   *s.navngivenv,
		}
	}

	a.Supplerendebynavn = s.supplBynavn
	if s.supplB2dagi != nil {
		a.Supplerendebynavn2 = &Supplerendebynavn2Ref{
			Href:   fmt.Sprintf("%s/supplerendebynavne2/%s", baseURL, *s.supplB2dagi),
			DagiID: *s.supplB2dagi,
		}
	}

	if s.postnr != nil {
		a.Postnummer = &AdgPostnrRef{
			Href: fmt.Sprintf("%s/postnumre/%s", baseURL, *s.postnr),
			Nr:   *s.postnr,
			Navn: deref(s.postnrnavn),
		}
	}

	if kommunekode != "" {
		a.Kommune = &KommuneRef{
			Href: fmt.Sprintf("%s/kommuner/%s", baseURL, kode4(kommunekode)),
			Kode: kode4(kommunekode),
			Navn: deref(s.kommuneNavn),
		}
	}

	// jordstykke + top-level ejerlav/matrikelnr/esrejendomsnr (esrejendomsnr is
	// the constant "0" in DAWA's old data model, as on /jordstykker).
	if s.jEjerlavK != nil && s.jMatrikel != nil {
		esr := "0"
		a.Esrejendomsnr = &esr
		a.Matrikelnr = s.jMatrikel
		a.Ejerlav = &AdgEjerlavRef{Kode: *s.jEjerlavK, Navn: deref(s.jEjerlavN)}
		a.Jordstykke = &JordstykkeAdgRef{
			Href: fmt.Sprintf("%s/jordstykker/%d/%s", baseURL, *s.jEjerlavK, *s.jMatrikel),
			Ejerlav: EjerlavRef{
				Kode: *s.jEjerlavK,
				Navn: deref(s.jEjerlavN),
				Href: fmt.Sprintf("%s/ejerlav/%d", baseURL, *s.jEjerlavK),
			},
			Matrikelnr:    *s.jMatrikel,
			Esrejendomsnr: "0",
		}
	}

	a.Historik = AdgHistorik{
		Oprettet:        s.hOprettet,
		Aendret:         s.hAendret,
		Ikrafttraedelse: s.hIkraft,
		Nedlagt:         nil,
	}

	if s.apID != nil {
		ap := &Adgangspunkt{
			ID:              *s.apID,
			Hoejde:          s.apHoejde,
			Noejagtighed:    s.apNoej,
			Kilde:           darKildeToInt(s.apKilde),
			Tekniskstandard: s.apTeknisk,
			Tekstretning:    s.apTekstret,
			Aendret:         s.apAendret,
		}
		if s.apLon != nil && s.apLat != nil {
			ap.Koordinater = &[2]float64{*s.apLon, *s.apLat}
		}
		a.Adgangspunkt = ap
	}

	if s.vpID != nil {
		vp := &Vejpunkt{
			ID:              *s.vpID,
			Kilde:           s.vpKilde,
			Noejagtighed:    s.vpNoej,
			Tekniskstandard: s.vpTeknisk,
			Aendret:         s.vpAendret,
		}
		if s.vpLon != nil && s.vpLat != nil {
			vp.Koordinater = &[2]float64{*s.vpLon, *s.vpLat}
		}
		a.Vejpunkt = vp
	}

	if s.ddknM100 != nil {
		a.DDKN = &DDKN{M100: *s.ddknM100, Km1: deref(s.ddknKm1), Km10: deref(s.ddknKm10)}
	}

	a.Sogn = newSogneRef(baseURL, s.sognKode, s.sognNavn)
	a.Region = newRegionRef(baseURL, s.regionKode, s.regionNavn)
	if s.ldNuts3 != nil {
		a.Landsdel = &LandsdelRef{
			Href:  fmt.Sprintf("%s/landsdele/%s", baseURL, *s.ldNuts3),
			Nuts3: *s.ldNuts3,
			Navn:  deref(s.ldNavn),
		}
	}
	a.Retskreds = newKodeNavnRef(baseURL, "retskredse", s.rkKode, s.rkNavn)
	a.Politikreds = newKodeNavnRef(baseURL, "politikredse", s.pkKode, s.pkNavn)
	a.Opstillingskreds = newKodeNavnRef(baseURL, "opstillingskredse", padOpstil(s.opNummer), s.opNavn)

	if s.afNummer != nil && s.afKomm != nil {
		// nummer is stripped of leading zeros ("03" -> "3"); the href is keyed by
		// the afstemningsområde's OWN kommunekode (afKomm), not the address's
		// vejmidte kommune — they can differ.
		afNr := stripZeros(*s.afNummer)
		a.Afstemningsomraade = &AfstemningsomraadeRef{
			Href:   fmt.Sprintf("%s/afstemningsomraader/%s/%s", baseURL, stripZeros(*s.afKomm), afNr),
			Nummer: afNr,
			Navn:   deref(s.afNavn),
		}
	}
	a.Storkreds = newStorkredsRef(baseURL, s.skNummer, s.skNavn)
	a.Valglandsdel = newValglandsdelRef(baseURL, s.vlBogstav, s.vlNavn)

	// bebyggelser membership is spatial: an address belongs to a bebyggelse when
	// its adgangspunkt falls within the bebyggelse polygon (ST_Contains). The set is
	// pre-built (sorted by bebyggelse id ascending) as JSON in adgangsadresseCols;
	// here we decode it and attach the per-element href. DAWA renders [] when none.
	a.Bebyggelser = []BebyggelseRef{}
	if len(s.bebyggelserJSON) > 0 {
		if err := json.Unmarshal(s.bebyggelserJSON, &a.Bebyggelser); err == nil {
			for i := range a.Bebyggelser {
				a.Bebyggelser[i].Href = fmt.Sprintf("%s/bebyggelser/%s", baseURL, a.Bebyggelser[i].ID)
			}
		}
	}

	return a
}

// padOpstil 4-pads the opstillingskreds nummer (golden kode "0043"), preserving
// nil.
func padOpstil(s *string) *string {
	if s == nil {
		return nil
	}
	v := kode4(*s)
	return &v
}

// newKodeNavnRef builds a {href,kode,navn} ref from a nullable kode+navn.
func newKodeNavnRef(baseURL, pathSeg string, kode, navn *string) *KodeNavnRef {
	if kode == nil {
		return nil
	}
	return &KodeNavnRef{
		Href: fmt.Sprintf("%s/%s/%s", baseURL, pathSeg, *kode),
		Kode: *kode,
		Navn: deref(navn),
	}
}

// derefInt returns *p or 0.
func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// darKilde maps the raw DAR Adressepunkt oprindelse_kilde string to DAWA's
// integer adgangspunkt.kilde code. The distinct strings in the V3/649 extract are
// "Adressemyn" (truncated "Adressemyndighed", 1.97M rows), "Ekstern" (2.68M),
// "Grundkort" (700k), "DARSystem" (34k), "Matrikelkort" (18k) and "Landinsp"
// (556). The mapping is derived empirically and confirmed across ALL TEN
// per_side_5 golden adgangspunkt rows (5 from /adgangsadresser + 5 from the
// embedded adgangsadresse in /adresser), joining each row's stored kilde string
// to its golden kilde int — every occurrence agrees:
//
//	Adressemyn   -> 5  (adgangsadr rows 1,2,3,5; adresser rows 2,3,5)
//	Ekstern      -> 4  (adgangsadr row 4;        adresser row 4)
//	Grundkort    -> 1  (adresser row 000021c5)
//	Matrikelkort -> 2  (live-confirmed 2026-06-02 across multiple rows)
//	Landinsp     -> null (live-confirmed: DAWA emits null kilde on every sampled
//	                Landinsp row — so it is intentionally absent from the map)
//
// DARSystem has no served sample (0 status-3 husnumre reference a DARSystem
// adgangspunkt), so its code is unverified and moot; left at 1 as a placeholder.
var darKilde = map[string]int{
	"Adressemyn":       5, // truncated "Adressemyndighed" in the extract
	"Adressemyndighed": 5,
	"Ekstern":          4,
	"Grundkort":        1,
	"Matrikelkort":     2,
	"DARSystem":        1, // unverified placeholder; never appears in served data
	// "Landinsp" is intentionally OMITTED → darKildeToInt returns nil (DAWA emits
	// null kilde for landinspektør-sourced adgangspunkter).
}

// darKildeToInt resolves the raw kilde string pointer to a *int code (nil on
// nil; if the string is already numeric, parse it; else look it up in darKilde).
func darKildeToInt(s *string) *int {
	if s == nil {
		return nil
	}
	if v, err := strconv.Atoi(*s); err == nil {
		return &v
	}
	if v, ok := darKilde[*s]; ok {
		return &v
	}
	return nil
}

// darStatusToDawa maps the raw DAR status int to DAWA's status enum
// (1=gældende, 2=nedlagt, 3=foreløbig, 4=henlagt): DAR 2 (foreløbig) -> 3,
// 3 (gældende) -> 1, 4 (nedlagt) -> 2, 5 (henlagt) -> 4.
//
// The 2->2/4->3 swap that lived here until 2026-06-11 was invisible to the
// compat suite because every sampled row is gældende (3->1, unaffected); the
// /historik sweep — whose chains carry foreløbig and nedlagt versions —
// exposed it against live DAWA (ours=2 dawa=3 on DAR-2 rows, ours=3 dawa=2 on
// DAR-4 rows, consistently).
func darStatusToDawa(dar int) int {
	switch dar {
	case 2:
		return 3
	case 3:
		return 1
	case 4:
		return 2
	case 5:
		return 4
	default:
		return dar
	}
}

// GetAdgangsadresse returns the adgangsadresse with the given id (status-3 only),
// or pgx.ErrNoRows.
func GetAdgangsadresse(ctx context.Context, pool *pgxpool.Pool, id, baseURL string) (*Adgangsadresse, error) {
	sql := "SELECT " + adgangsadresseCols + adgangsadresseFrom + " WHERE h.id = $1 AND h.status = '3'"
	return scanAdgangsadresse(pool.QueryRow(ctx, sql, id), baseURL)
}

// adgKommuneExpr / adgVejkodeExpr reproduce the COALESCE expressions used for the
// kommunekode/vejkode output columns, so they can be referenced in a WHERE
// (output aliases are not visible there).
const adgKommuneExpr = "COALESCE(split_part(h.vejmidte, '-', 1), kd.kommune)"
const adgVejkodeExpr = "COALESCE(split_part(h.vejmidte, '-', 2), kd.vejkode)"

// adgFilterClauses builds the shared SQL-side filter clauses for the
// adgangsadresse columns (postnr, vejkode, kommunekode, q over name/husnr,
// spatial over the adgangspunkt). It is reused by the adresser query, which embeds
// the same join graph.
func adgFilterClauses(wb *whereBuilder, f ListFilter) {
	if v, ok := f.Filters["postnr"]; ok {
		wb.addCode("p.postnr", v)
	}
	if v, ok := f.Filters["kommunekode"]; ok {
		wb.addCode(adgKommuneExpr, v)
	}
	if v, ok := f.Filters["vejkode"]; ok {
		wb.addCode(adgVejkodeExpr, v)
	}
	wb.addQ(f.Q, "nv.navn", "nv.adresseringsnavn", "h.husnummertekst", "p.navn")
	if f.Spatial != nil {
		wb.addSpatial(f.Spatial, "ap.geom")
	}
}

// ListAdgangsadresser returns status-3 adgangsadresser ordered by id (DAWA's
// default order). limit <= 0 returns all.
func ListAdgangsadresser(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit, offset int) ([]*Adgangsadresse, error) {
	return ListAdgangsadresserFiltered(ctx, pool, baseURL, limit, offset, ListFilter{})
}

// ListAdgangsadresserFiltered is ListAdgangsadresser with SQL-side per-field
// filters (postnr, vejkode, kommunekode), q= over vejnavn/husnr/postnrnavn, srid
// output reprojection and an optional spatial filter (against the adgangspunkt),
// plus offset paging. A zero filter reproduces ListAdgangsadresser byte-for-byte.
func ListAdgangsadresserFiltered(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit, offset int, f ListFilter) ([]*Adgangsadresse, error) {
	var wb whereBuilder
	adgFilterClauses(&wb, f)

	sql := "SELECT " + f.applySRID(adgangsadresseCols+adgangsadresseFrom) + " WHERE h.status = '3'"
	if w := wb.sql(); w != "" {
		sql += " AND " + w
	}
	sql += " ORDER BY h.id"
	sql = appendLimitOffset(sql, limit, offset)

	rows, err := pool.Query(ctx, sql, wb.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Adgangsadresse
	for rows.Next() {
		a, err := scanAdgangsadresse(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
