package dawa

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
)

// struktur_flad.go builds DAWA's struktur=flad representation for adresser and
// adgangsadresser: a single wide flat object (73 keys for adgangsadresse, 83 for
// adresse) with the nested nestet sub-objects flattened into prefixed/lifted keys
// in DAWA's exact, independent emit order.
//
// Strategy: the nestet object is already byte-verified against live DAWA, so flad
// is assembled by TRANSFORMING the marshalled nestet JSON — every shared value is
// passed through verbatim (exact bytes, types, rounding, null behavior preserved),
// and only the two flad-only values not present in nestet are supplied separately
// via fladExtra:
//   - etrs89koordinat_øst/nord — the adgangspunkt's NATIVE EPSG:25832 coords
//     (full precision; byte-exact on most rows, the residual is sub-nanometer geo
//     drift in the shared DAR source, tolerated like other coordinate metadata).
//   - menighedsrådsafstemningsområde nummer/navn — a spatial ST_Covers lookup
//     (the value is fully reproducible; not carried by the nestet structs).
// tekstretning and højde are emitted null (the nestet adgangspunkt already nulls
// them — proven unreproducible from our extract).

// FladObject wraps the assembled flat JSON. Its MarshalJSON returns the bytes
// verbatim; the outer DAWA marshaller re-indents them (2-space) and the collection
// serializer joins them in the streaming layout, matching DAWA.
type FladObject struct{ raw json.RawMessage }

func (f *FladObject) MarshalJSON() ([]byte, error) {
	if f == nil || len(f.raw) == 0 {
		return []byte("null"), nil
	}
	return f.raw, nil
}

// fladExtra carries the flad-only values absent from the nestet representation,
// keyed per adgangsadresse (the dar_husnummer id).
type fladExtra struct {
	etrsEast *float64 // ST_X(adgangspunkt.geom) native 25832
	etrsNord *float64 // ST_Y(adgangspunkt.geom) native 25832
	mrNummer *string  // dagi_mrafstemningsomraader.nummer (text → emitted as int)
	mrNavn   *string
}

// FetchFladExtras returns, per adgangsadresse id, the native ETRS89 adgangspunkt
// coordinates and the menighedsrådsafstemningsområde (via ST_Covers over the
// adgangspunkt). Ids without an adgangspunkt geometry yield null coords + no
// menighedsråd. Used only by the flad path so the hot nestet query is untouched.
func FetchFladExtras(ctx context.Context, pool *pgxpool.Pool, adgIDs []string) (map[string]fladExtra, error) {
	out := make(map[string]fladExtra, len(adgIDs))
	if len(adgIDs) == 0 {
		return out, nil
	}
	const sql = `SELECT h.id,
		CASE WHEN ap.geom IS NULL THEN NULL ELSE ST_X(ap.geom) END,
		CASE WHEN ap.geom IS NULL THEN NULL ELSE ST_Y(ap.geom) END,
		mr.nummer, mr.navn
	FROM dar_husnummer h
	LEFT JOIN dar_adressepunkt ap ON ap.id_lokalid = h.adgangspunkt_id
	LEFT JOIN LATERAL (
		SELECT t.nummer, t.navn FROM dagi_mrafstemningsomraader t
		WHERE ap.geom IS NOT NULL AND ST_Covers(t.geom, ap.geom) LIMIT 1
	) mr ON true
	WHERE h.id = ANY($1)`
	rows, err := pool.Query(ctx, sql, adgIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var ex fladExtra
		if err := rows.Scan(&id, &ex.etrsEast, &ex.etrsNord, &ex.mrNummer, &ex.mrNavn); err != nil {
			return nil, err
		}
		out[id] = ex
	}
	return out, rows.Err()
}

// ---- JSON navigation over the marshalled nestet object ----

var jsonNull = json.RawMessage("null")

// objOf parses a raw JSON value into its member map; nil for null/non-object.
func objOf(raw json.RawMessage) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// fld returns member key's raw value, or the null token when absent/empty/nil map.
func fld(m map[string]json.RawMessage, key string) json.RawMessage {
	if m == nil {
		return jsonNull
	}
	if v, ok := m[key]; ok && len(v) > 0 {
		return v
	}
	return jsonNull
}

// sub returns member key parsed as a nested object map.
func sub(m map[string]json.RawMessage, key string) map[string]json.RawMessage {
	return objOf(fld(m, key))
}

// arrIdx returns element i of member key (a JSON array), or null.
func arrIdx(m map[string]json.RawMessage, key string, i int) json.RawMessage {
	var arr []json.RawMessage
	if json.Unmarshal(fld(m, key), &arr) != nil || i < 0 || i >= len(arr) {
		return jsonNull
	}
	return arr[i]
}

// fladKV is one ordered output member.
type fladKV struct {
	k string
	v json.RawMessage
}

// assembleFlad renders the ordered members as a compact JSON object. Keys are
// ASCII + Danish letters (ø/æ/å) only — no quotes/backslashes/controls — so they
// are appended raw inside quotes (valid UTF-8 JSON). The outer marshaller indents.
func assembleFlad(pairs []fladKV) *FladObject {
	b := make([]byte, 0, 2048)
	b = append(b, '{')
	for i, p := range pairs {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '"')
		b = append(b, p.k...)
		b = append(b, '"', ':')
		if len(p.v) == 0 {
			b = append(b, jsonNull...)
		} else {
			b = append(b, p.v...)
		}
	}
	b = append(b, '}')
	return &FladObject{raw: b}
}

// rawFloat marshals an optional float as a JSON number (full precision) or null.
func rawFloat(p *float64) json.RawMessage {
	if p == nil {
		return jsonNull
	}
	b, err := json.Marshal(*p)
	if err != nil {
		return jsonNull
	}
	return b
}

// rawIntStr marshals an optional numeric STRING as a JSON integer (DAWA emits the
// menighedsråd nummer as an int even though our column is text), or null.
func rawIntStr(p *string) json.RawMessage {
	if p == nil {
		return jsonNull
	}
	n, err := strconv.Atoi(*p)
	if err != nil {
		return jsonNull
	}
	return json.RawMessage(strconv.Itoa(n))
}

// rawStr marshals an optional string as a JSON string or null.
func rawStr(p *string) json.RawMessage {
	if p == nil {
		return jsonNull
	}
	b, err := json.Marshal(*p)
	if err != nil {
		return jsonNull
	}
	return b
}

// FladAdgangsadresse projects a full adgangsadresse to its struktur=flad shape (73
// keys, DAWA's exact order). Shared values pass through from the marshalled nestet
// object; etrs89 + menighedsråd come from ex.
func FladAdgangsadresse(a *Adgangsadresse, ex fladExtra) *FladObject {
	raw, err := MarshalDAWA(a)
	if err != nil {
		return &FladObject{raw: jsonNull}
	}
	t := objOf(raw)
	vejstykke := sub(t, "vejstykke")
	postnummer := sub(t, "postnummer")
	kommune := sub(t, "kommune")
	ap := sub(t, "adgangspunkt")
	vp := sub(t, "vejpunkt")
	ddkn := sub(t, "DDKN")
	region := sub(t, "region")
	jord := sub(t, "jordstykke")
	jordEjerlav := sub(jord, "ejerlav")
	sogn := sub(t, "sogn")
	politikreds := sub(t, "politikreds")
	retskreds := sub(t, "retskreds")
	opstil := sub(t, "opstillingskreds")
	afstem := sub(t, "afstemningsområde")
	storkreds := sub(t, "storkreds")
	valglandsdel := sub(t, "valglandsdel")
	landsdel := sub(t, "landsdel")
	suppl2 := sub(t, "supplerendebynavn2")
	navngivenvej := sub(t, "navngivenvej")

	return assembleFlad([]fladKV{
		{"id", fld(t, "id")},
		{"status", fld(t, "status")},
		{"darstatus", fld(t, "darstatus")},
		{"oprettet", fld(sub(t, "historik"), "oprettet")},
		{"ændret", fld(sub(t, "historik"), "ændret")},
		{"vejkode", fld(vejstykke, "kode")},
		{"vejnavn", fld(vejstykke, "navn")},
		{"adresseringsvejnavn", fld(vejstykke, "adresseringsnavn")},
		{"husnr", fld(t, "husnr")},
		{"supplerendebynavn", fld(t, "supplerendebynavn")},
		{"postnr", fld(postnummer, "nr")},
		{"postnrnavn", fld(postnummer, "navn")},
		{"stormodtagerpostnr", jsonNull},
		{"stormodtagerpostnrnavn", jsonNull},
		{"kommunekode", fld(kommune, "kode")},
		{"kommunenavn", fld(kommune, "navn")},
		{"ejerlavkode", fld(sub(t, "ejerlav"), "kode")},
		{"ejerlavnavn", fld(sub(t, "ejerlav"), "navn")},
		{"matrikelnr", fld(t, "matrikelnr")},
		{"esrejendomsnr", fld(t, "esrejendomsnr")},
		{"etrs89koordinat_øst", rawFloat(ex.etrsEast)},
		{"etrs89koordinat_nord", rawFloat(ex.etrsNord)},
		{"wgs84koordinat_bredde", arrIdx(ap, "koordinater", 1)},
		{"wgs84koordinat_længde", arrIdx(ap, "koordinater", 0)},
		{"nøjagtighed", fld(ap, "nøjagtighed")},
		{"kilde", fld(ap, "kilde")},
		{"tekniskstandard", fld(ap, "tekniskstandard")},
		{"tekstretning", fld(ap, "tekstretning")},
		{"adressepunktændringsdato", fld(ap, "ændret")},
		{"ddkn_m100", fld(ddkn, "m100")},
		{"ddkn_km1", fld(ddkn, "km1")},
		{"ddkn_km10", fld(ddkn, "km10")},
		{"regionskode", fld(region, "kode")},
		{"regionsnavn", fld(region, "navn")},
		{"jordstykke_ejerlavkode", fld(jordEjerlav, "kode")},
		{"jordstykke_matrikelnr", fld(jord, "matrikelnr")},
		{"jordstykke_esrejendomsnr", fld(jord, "esrejendomsnr")},
		{"jordstykke_ejerlavnavn", fld(jordEjerlav, "navn")},
		{"højde", fld(ap, "højde")},
		{"adgangspunktid", fld(ap, "id")},
		{"vejpunkt_id", fld(vp, "id")},
		{"vejpunkt_kilde", fld(vp, "kilde")},
		{"vejpunkt_nøjagtighed", fld(vp, "nøjagtighed")},
		{"vejpunkt_tekniskstandard", fld(vp, "tekniskstandard")},
		{"vejpunkt_x", arrIdx(vp, "koordinater", 0)},
		{"vejpunkt_y", arrIdx(vp, "koordinater", 1)},
		{"sognekode", fld(sogn, "kode")},
		{"sognenavn", fld(sogn, "navn")},
		{"politikredskode", fld(politikreds, "kode")},
		{"politikredsnavn", fld(politikreds, "navn")},
		{"retskredskode", fld(retskreds, "kode")},
		{"retskredsnavn", fld(retskreds, "navn")},
		{"opstillingskredskode", fld(opstil, "kode")},
		{"opstillingskredsnavn", fld(opstil, "navn")},
		{"menighedsrådsafstemningsområdenummer", rawIntStr(ex.mrNummer)},
		{"menighedsrådsafstemningsområdenavn", rawStr(ex.mrNavn)},
		{"zone", fld(t, "zone")},
		{"afstemningsområdenummer", fld(afstem, "nummer")},
		{"afstemningsområdenavn", fld(afstem, "navn")},
		{"brofast", fld(t, "brofast")},
		{"supplerendebynavn_dagi_id", fld(suppl2, "dagi_id")},
		{"navngivenvej_id", fld(navngivenvej, "id")},
		{"vejpunkt_ændret", fld(vp, "ændret")},
		{"ikrafttrædelse", fld(sub(t, "historik"), "ikrafttrædelse")},
		{"nedlagt", fld(sub(t, "historik"), "nedlagt")},
		{"storkredsnummer", fld(storkreds, "nummer")},
		{"storkredsnavn", fld(storkreds, "navn")},
		{"valglandsdelsbogstav", fld(valglandsdel, "bogstav")},
		{"valglandsdelsnavn", fld(valglandsdel, "navn")},
		{"landsdelsnuts3", fld(landsdel, "nuts3")},
		{"landsdelsnavn", fld(landsdel, "navn")},
		{"betegnelse", fld(t, "adressebetegnelse")},
		{"kvh", fld(t, "kvh")},
	})
}

// FladAdresse projects a full adresse to its struktur=flad shape (83 keys, DAWA's
// exact order — independent of the adgangsadresse order). The enhedsadresse fields
// are read from the top object; everything else from the embedded adgangsadresse.
func FladAdresse(a *Adresse, ex fladExtra) *FladObject {
	raw, err := MarshalDAWA(a)
	if err != nil {
		return &FladObject{raw: jsonNull}
	}
	t := objOf(raw)
	hist := sub(t, "historik")
	adg := sub(t, "adgangsadresse")
	adgHist := sub(adg, "historik")
	vejstykke := sub(adg, "vejstykke")
	postnummer := sub(adg, "postnummer")
	kommune := sub(adg, "kommune")
	ap := sub(adg, "adgangspunkt")
	vp := sub(adg, "vejpunkt")
	ddkn := sub(adg, "DDKN")
	region := sub(adg, "region")
	jord := sub(adg, "jordstykke")
	jordEjerlav := sub(jord, "ejerlav")
	sogn := sub(adg, "sogn")
	politikreds := sub(adg, "politikreds")
	retskreds := sub(adg, "retskreds")
	opstil := sub(adg, "opstillingskreds")
	afstem := sub(adg, "afstemningsområde")
	storkreds := sub(adg, "storkreds")
	valglandsdel := sub(adg, "valglandsdel")
	landsdel := sub(adg, "landsdel")
	suppl2 := sub(adg, "supplerendebynavn2")
	navngivenvej := sub(adg, "navngivenvej")

	return assembleFlad([]fladKV{
		{"id", fld(t, "id")},
		{"status", fld(t, "status")},
		{"darstatus", fld(t, "darstatus")},
		{"oprettet", fld(hist, "oprettet")},
		{"ændret", fld(hist, "ændret")},
		{"ikrafttrædelse", fld(hist, "ikrafttrædelse")},
		{"nedlagt", fld(hist, "nedlagt")},
		{"vejkode", fld(vejstykke, "kode")},
		{"vejnavn", fld(vejstykke, "navn")},
		{"adresseringsvejnavn", fld(vejstykke, "adresseringsnavn")},
		{"husnr", fld(adg, "husnr")},
		{"etage", fld(t, "etage")},
		{"dør", fld(t, "dør")},
		{"supplerendebynavn", fld(adg, "supplerendebynavn")},
		{"postnr", fld(postnummer, "nr")},
		{"postnrnavn", fld(postnummer, "navn")},
		{"stormodtagerpostnr", jsonNull},
		{"stormodtagerpostnrnavn", jsonNull},
		{"kommunekode", fld(kommune, "kode")},
		{"kommunenavn", fld(kommune, "navn")},
		{"ejerlavkode", fld(sub(adg, "ejerlav"), "kode")},
		{"ejerlavnavn", fld(sub(adg, "ejerlav"), "navn")},
		{"matrikelnr", fld(adg, "matrikelnr")},
		{"esrejendomsnr", fld(adg, "esrejendomsnr")},
		{"etrs89koordinat_øst", rawFloat(ex.etrsEast)},
		{"etrs89koordinat_nord", rawFloat(ex.etrsNord)},
		{"wgs84koordinat_bredde", arrIdx(ap, "koordinater", 1)},
		{"wgs84koordinat_længde", arrIdx(ap, "koordinater", 0)},
		{"nøjagtighed", fld(ap, "nøjagtighed")},
		{"kilde", fld(ap, "kilde")},
		{"tekniskstandard", fld(ap, "tekniskstandard")},
		{"tekstretning", fld(ap, "tekstretning")},
		{"ddkn_m100", fld(ddkn, "m100")},
		{"ddkn_km1", fld(ddkn, "km1")},
		{"ddkn_km10", fld(ddkn, "km10")},
		{"adressepunktændringsdato", fld(ap, "ændret")},
		{"adgangsadresseid", fld(adg, "id")},
		{"adgangsadresse_status", fld(adg, "status")},
		{"adgangsadresse_darstatus", fld(adg, "darstatus")},
		{"adgangsadresse_oprettet", fld(adgHist, "oprettet")},
		{"adgangsadresse_ændret", fld(adgHist, "ændret")},
		{"adgangsadresse_ikrafttrædelse", fld(adgHist, "ikrafttrædelse")},
		{"adgangsadresse_nedlagt", fld(adgHist, "nedlagt")},
		{"regionskode", fld(region, "kode")},
		{"regionsnavn", fld(region, "navn")},
		{"jordstykke_ejerlavnavn", fld(jordEjerlav, "navn")},
		{"jordstykke_ejerlavkode", fld(jordEjerlav, "kode")},
		{"jordstykke_matrikelnr", fld(jord, "matrikelnr")},
		{"jordstykke_esrejendomsnr", fld(jord, "esrejendomsnr")},
		{"højde", fld(ap, "højde")},
		{"adgangspunktid", fld(ap, "id")},
		{"vejpunkt_x", arrIdx(vp, "koordinater", 0)},
		{"vejpunkt_y", arrIdx(vp, "koordinater", 1)},
		{"vejpunkt_id", fld(vp, "id")},
		{"vejpunkt_kilde", fld(vp, "kilde")},
		{"vejpunkt_nøjagtighed", fld(vp, "nøjagtighed")},
		{"vejpunkt_tekniskstandard", fld(vp, "tekniskstandard")},
		{"vejpunkt_ændret", fld(vp, "ændret")},
		{"sognekode", fld(sogn, "kode")},
		{"sognenavn", fld(sogn, "navn")},
		{"politikredskode", fld(politikreds, "kode")},
		{"politikredsnavn", fld(politikreds, "navn")},
		{"retskredskode", fld(retskreds, "kode")},
		{"retskredsnavn", fld(retskreds, "navn")},
		{"opstillingskredskode", fld(opstil, "kode")},
		{"opstillingskredsnavn", fld(opstil, "navn")},
		{"zone", fld(adg, "zone")},
		{"afstemningsområdenummer", fld(afstem, "nummer")},
		{"afstemningsområdenavn", fld(afstem, "navn")},
		{"menighedsrådsafstemningsområdenummer", rawIntStr(ex.mrNummer)},
		{"menighedsrådsafstemningsområdenavn", rawStr(ex.mrNavn)},
		{"brofast", fld(adg, "brofast")},
		{"supplerendebynavn_dagi_id", fld(suppl2, "dagi_id")},
		{"navngivenvej_id", fld(navngivenvej, "id")},
		{"storkredsnummer", fld(storkreds, "nummer")},
		{"storkredsnavn", fld(storkreds, "navn")},
		{"valglandsdelsbogstav", fld(valglandsdel, "bogstav")},
		{"valglandsdelsnavn", fld(valglandsdel, "navn")},
		{"landsdelsnuts3", fld(landsdel, "nuts3")},
		{"landsdelsnavn", fld(landsdel, "navn")},
		{"kvhx", fld(t, "kvhx")},
		{"kvh", fld(adg, "kvh")},
		{"betegnelse", fld(t, "adressebetegnelse")},
	})
}
