package dawa

import (
	"context"
	"net/url"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"
)

// autocomplete.go implements the DAWA /{resource}/autocomplete?q=... endpoints by
// reusing each resource's existing List* representation. Each element is a small
// wrapper {tekst, <resourceKey>} where <resourceKey> is the FULL nested resource
// object (byte-identical to its single-GET representation). stednavne2 is the one
// exception: its autocomplete element IS the stednavne2 list element (no wrapper).
//
// q matching is prefix/substring, case- and diacritic-insensitive (matching
// DAWA's behaviour for the captured goldens, e.g. q=K%C3%B8ben → "København").
//
// The result ORDER is DAWA's DETERMINISTIC autocomplete relevance scorer, ported
// faithfully in autocomplete_score.go from the open-source dawa_autocomplete.js
// + dawa_levenshtein.js + dawa_adresseTextMatch.js. The address resources
// (adgangsadresser/adresser) and the aggregate /autocomplete use sortAdresse
// (scoreAddressElement + compareProcessedResults); the name resources
// (vejnavne and the code-list/area autocompletes) use the vejnavn scorer
// (scoreAddressElement(navn,q,1) + whitespace-stripped name tie-break). This is
// NOT a proprietary heuristic — it is the published DAWA logic, so the captured
// goldens are reproduced byte-for-byte where the data permits.

// foldString lowercases s and folds the Danish/Latin diacritics to ASCII bases
// so the q match is case- and diacritic-insensitive. æ/ø/å fold to ae/o/aa (so
// q=Koben matches København); the common accented vowels/consonants fold to
// their ASCII base; other combining marks are dropped.
func foldString(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch r {
		case 'ø':
			b.WriteByte('o')
		case 'å':
			b.WriteString("aa")
		case 'æ':
			b.WriteString("ae")
		case 'á', 'à', 'â', 'ä', 'ã':
			b.WriteByte('a')
		case 'é', 'è', 'ê', 'ë':
			b.WriteByte('e')
		case 'í', 'ì', 'î', 'ï':
			b.WriteByte('i')
		case 'ó', 'ò', 'ô', 'ö', 'õ':
			b.WriteByte('o')
		case 'ú', 'ù', 'û', 'ü':
			b.WriteByte('u')
		case 'ý', 'ÿ':
			b.WriteByte('y')
		case 'ñ':
			b.WriteByte('n')
		case 'ç':
			b.WriteByte('c')
		default:
			if unicode.Is(unicode.Mn, r) {
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// matchQ reports whether the (folded) tekst matches the (folded) q under DAWA's
// autocomplete token-prefix rule: q is split into whitespace tokens, and EVERY q
// token must be a PREFIX of some whitespace token of tekst. An empty q matches
// everything. This is per-token prefix, NOT a whole-string prefix and NOT an
// interior substring:
//
//	q="Hoved"   matches "1084 Region Hovedstaden"  (token "Hovedstaden" startsWith "Hoved")
//	q="21"      matches "2150 Nordhavn"            (token "2150"        startsWith "21")
//	q="Køben"   matches "Byen København"           (token "København"   startsWith "Køben")
//	q="benhavn" does NOT match "0101 København"    (no token startsWith "benhavn")
//
// (DAWA's underlying Danish full-text index additionally splits compound words,
// so a few interior segments — e.g. "jylland" inside "Nordjylland" — also match
// live; that compound decomposition is not reproducible from our schema and is
// not exercised by the byte-exact test queries, which are all token-prefix.)
func matchQ(tekst, q string) bool {
	q = strings.TrimSpace(q)
	if q == "" {
		return true
	}
	teksttoks := strings.Fields(foldString(tekst))
	for _, qt := range strings.Fields(foldString(q)) {
		ok := false
		for _, tt := range teksttoks {
			if strings.HasPrefix(tt, qt) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// derefStr returns the pointed-to string, or "" when the pointer is nil.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// limitTake returns at most perSide items starting at offset. perSide<=0 means
// "all". offset<0 is treated as 0.
func limitTake[T any](items []T, perSide, offset int) []T {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return items[:0]
	}
	items = items[offset:]
	if perSide > 0 && perSide < len(items) {
		items = items[:perSide]
	}
	return items
}

// ---- wrapper element types (json key order = struct field order) ----

// KommuneAuto is one {tekst, kommune} autocomplete element.
type KommuneAuto struct {
	Tekst   string   `json:"tekst"`
	Kommune *Kommune `json:"kommune"`
}

// RegionAuto is one {tekst, region} element.
type RegionAuto struct {
	Tekst  string  `json:"tekst"`
	Region *Region `json:"region"`
}

// SognAuto is one {tekst, sogn} element.
type SognAuto struct {
	Tekst string `json:"tekst"`
	Sogn  *Sogn  `json:"sogn"`
}

// PostnummerReduced is the REDUCED postnummer projection DAWA emits in
// /postnumre/autocomplete: {nr, navn, stormodtager, visueltcenter_x,
// visueltcenter_y, href} — NOT the full single-GET postnummer (no bbox, no
// visueltcenter array, no dagi_id/kommuner/stormodtageradresser).
type PostnummerReduced struct {
	Nr             string   `json:"nr"`
	Navn           string   `json:"navn"`
	Stormodtager   bool     `json:"stormodtager"`
	VisueltcenterX *float64 `json:"visueltcenter_x"`
	VisueltcenterY *float64 `json:"visueltcenter_y"`
	Href           string   `json:"href"`
}

// PostnummerAuto is one {tekst, postnummer} element with the reduced projection.
type PostnummerAuto struct {
	Tekst      string             `json:"tekst"`
	Postnummer *PostnummerReduced `json:"postnummer"`
}

// LandsdelAuto is one {tekst, landsdel} element.
type LandsdelAuto struct {
	Tekst    string    `json:"tekst"`
	Landsdel *Landsdel `json:"landsdel"`
}

// StorkredsAuto is one {tekst, storkreds} element.
type StorkredsAuto struct {
	Tekst     string     `json:"tekst"`
	Storkreds *Storkreds `json:"storkreds"`
}

// ValglandsdelAuto is one {tekst, valglandsdel} element.
type ValglandsdelAuto struct {
	Tekst        string        `json:"tekst"`
	Valglandsdel *Valglandsdel `json:"valglandsdel"`
}

// OpstillingskredsAuto is one {tekst, opstillingskreds} element.
type OpstillingskredsAuto struct {
	Tekst            string            `json:"tekst"`
	Opstillingskreds *Opstillingskreds `json:"opstillingskreds"`
}

// SimpleAreaAuto is one {tekst, <key>} element for a SimpleArea resource
// (retskredse → "retskreds", politikredse → "politikreds"). The wrapper key is
// dynamic, so it is marshalled via a custom MarshalJSON (see autocomplete_db.go).
type SimpleAreaAuto struct {
	Tekst string
	Key   string
	Area  *SimpleArea
}

// VejstykkeReduced is the REDUCED vejstykke projection DAWA emits in
// /vejstykker/autocomplete: {href, kommunekode, kode, navn} (href FIRST), NOT the
// full single-GET vejstykke.
type VejstykkeReduced struct {
	Href        string  `json:"href"`
	Kommunekode string  `json:"kommunekode"`
	Kode        string  `json:"kode"`
	Navn        *string `json:"navn"`
}

// VejstykkeAuto is one {tekst, vejstykke} element with the reduced projection.
type VejstykkeAuto struct {
	Tekst     string            `json:"tekst"`
	Vejstykke *VejstykkeReduced `json:"vejstykke"`
}

// VejnavnReduced is the REDUCED vejnavn projection DAWA emits in
// /vejnavne/autocomplete: {navn, href} only.
type VejnavnReduced struct {
	Navn string `json:"navn"`
	Href string `json:"href"`
}

// VejnavnAuto is one {tekst, vejnavn} element with the reduced projection.
type VejnavnAuto struct {
	Tekst   string          `json:"tekst"`
	Vejnavn *VejnavnReduced `json:"vejnavn"`
}

// NavngivenVejReduced is the REDUCED navngivenvej projection DAWA emits in
// /navngivneveje/autocomplete: {id, darstatus, navn, adresseringsnavn,
// administrerendekommunekode, administrerendekommunenavn, visueltcenter_x,
// visueltcenter_y, href}.
type NavngivenVejReduced struct {
	ID                         string   `json:"id"`
	Darstatus                  string   `json:"darstatus"`
	Navn                       *string  `json:"navn"`
	Adresseringsnavn           *string  `json:"adresseringsnavn"`
	Administrerendekommunekode *string  `json:"administrerendekommunekode"`
	Administrerendekommunenavn *string  `json:"administrerendekommunenavn"`
	VisueltcenterX             *float64 `json:"visueltcenter_x"`
	VisueltcenterY             *float64 `json:"visueltcenter_y"`
	Href                       string   `json:"href"`
}

// NavngivenVejAuto is one {tekst, navngivenvej} element with the reduced
// projection.
type NavngivenVejAuto struct {
	Tekst        string               `json:"tekst"`
	Navngivenvej *NavngivenVejReduced `json:"navngivenvej"`
}

// SupplerendeBynavnReduced is the REDUCED v1 supplerendebynavn projection DAWA
// emits in /supplerendebynavne/autocomplete: {navn, href} where href points at the
// v2 resource (/supplerendebynavne2/{navn}, navn percent-encoded).
type SupplerendeBynavnReduced struct {
	Navn string `json:"navn"`
	Href string `json:"href"`
}

// SupplerendeBynavnV1Auto is one {tekst, supplerendebynavn} element for the
// DEPRECATED v1 /supplerendebynavne/autocomplete (reduced {navn, href}).
type SupplerendeBynavnV1Auto struct {
	Tekst             string                    `json:"tekst"`
	Supplerendebynavn *SupplerendeBynavnReduced `json:"supplerendebynavn"`
}

// SupplerendeBynavnAuto is one {tekst, supplerendebynavn} element for the v2
// /supplerendebynavne2/autocomplete (FULL object).
type SupplerendeBynavnAuto struct {
	Tekst             string             `json:"tekst"`
	Supplerendebynavn *SupplerendeBynavn `json:"supplerendebynavn"`
}

// EjerlavReduced is the REDUCED ejerlav projection DAWA emits as the
// /ejerlav/autocomplete nested object: only {href, kode(int), navn}
// (NO bbox/visueltcenter, which are full single-GET-only).
type EjerlavReduced struct {
	Href string `json:"href"`
	Kode int    `json:"kode"`
	Navn string `json:"navn"`
}

// EjerlavAuto is one {tekst, ejerlav} element.
type EjerlavAuto struct {
	Tekst   string          `json:"tekst"`
	Ejerlav *EjerlavReduced `json:"ejerlav"`
}

// JordstykkeFlat is the REDUCED jordstykke projection DAWA emits as the
// /jordstykker/autocomplete nested object. Only 10 keys IN DAWA ORDER:
// matrikelnr, bbox, visueltcenter, href, ejerlav, kommune, esrejendomsnr,
// udvidet_esrejendomsnr, sfeejendomsnr, bfenummer. Field types mirror the full
// Jordstykke struct so marshaling is byte-identical.
type JordstykkeFlat struct {
	Matrikelnr           string      `json:"matrikelnr"`
	Bbox                 *[4]float64 `json:"bbox"`
	Visueltcenter        *[2]float64 `json:"visueltcenter"`
	Href                 string      `json:"href"`
	Ejerlav              EjerlavRef  `json:"ejerlav"`
	Kommune              *KommuneRef `json:"kommune"`
	Esrejendomsnr        string      `json:"esrejendomsnr"`
	UdvidetEsrejendomsnr string      `json:"udvidet_esrejendomsnr"`
	Sfeejendomsnr        *string     `json:"sfeejendomsnr"`
	Bfenummer            *int64      `json:"bfenummer"`
}

// JordstykkeAuto is one {tekst, jordstykke} element.
type JordstykkeAuto struct {
	Tekst      string          `json:"tekst"`
	Jordstykke *JordstykkeFlat `json:"jordstykke"`
}

// AfstemningsomraadeAuto is one {tekst, afstemningsområde} element.
type AfstemningsomraadeAuto struct {
	Tekst              string              `json:"tekst"`
	Afstemningsomraade *Afstemningsomraade `json:"afstemningsområde"`
}

// AdgangsadresseFlat is the REDUCED adgangsadresse projection DAWA emits as the
// /adgangsadresser/autocomplete nested object (NOT the full single-GET shape).
// Key order matches DAWA byte-for-byte: id,status,darstatus,vejkode,vejnavn,
// adresseringsvejnavn,husnr,supplerendebynavn,postnr,postnrnavn,stormodtagerpostnr,
// stormodtagerpostnrnavn,kommunekode,x,y,href. NO etage/dør/adgangsadresseid
// (those are adresse-only). Nullable fields serialize to JSON null when absent.
type AdgangsadresseFlat struct {
	ID                     string   `json:"id"`
	Status                 int      `json:"status"`
	Darstatus              int      `json:"darstatus"`
	Vejkode                string   `json:"vejkode"`
	Vejnavn                *string  `json:"vejnavn"`
	Adresseringsvejnavn    *string  `json:"adresseringsvejnavn"`
	Husnr                  string   `json:"husnr"`
	Supplerendebynavn      *string  `json:"supplerendebynavn"`
	Postnr                 *string  `json:"postnr"`
	Postnrnavn             *string  `json:"postnrnavn"`
	Stormodtagerpostnr     *string  `json:"stormodtagerpostnr"`
	Stormodtagerpostnrnavn *string  `json:"stormodtagerpostnrnavn"`
	Kommunekode            string   `json:"kommunekode"`
	X                      *float64 `json:"x"`
	Y                      *float64 `json:"y"`
	Href                   string   `json:"href"`
}

// AdresseFlat is the REDUCED adresse projection DAWA emits as the
// /adresser/autocomplete nested object: AdgangsadresseFlat plus etage/dør (right
// after husnr, JSON null when absent) and adgangsadresseid (right after
// kommunekode, always present). Key order matches DAWA byte-for-byte.
type AdresseFlat struct {
	ID                     string   `json:"id"`
	Status                 int      `json:"status"`
	Darstatus              int      `json:"darstatus"`
	Vejkode                string   `json:"vejkode"`
	Vejnavn                *string  `json:"vejnavn"`
	Adresseringsvejnavn    *string  `json:"adresseringsvejnavn"`
	Husnr                  string   `json:"husnr"`
	Etage                  *string  `json:"etage"`
	Doer                   *string  `json:"dør"`
	Supplerendebynavn      *string  `json:"supplerendebynavn"`
	Postnr                 *string  `json:"postnr"`
	Postnrnavn             *string  `json:"postnrnavn"`
	Stormodtagerpostnr     *string  `json:"stormodtagerpostnr"`
	Stormodtagerpostnrnavn *string  `json:"stormodtagerpostnrnavn"`
	Kommunekode            string   `json:"kommunekode"`
	Adgangsadresseid       string   `json:"adgangsadresseid"`
	X                      *float64 `json:"x"`
	Y                      *float64 `json:"y"`
	Href                   string   `json:"href"`
}

// AdgangsadresseAuto is one {tekst, adgangsadresse} element with the REDUCED flat
// projection.
type AdgangsadresseAuto struct {
	Tekst          string              `json:"tekst"`
	Adgangsadresse *AdgangsadresseFlat `json:"adgangsadresse"`
}

// AdresseAuto is one {tekst, adresse} element with the REDUCED flat projection.
type AdresseAuto struct {
	Tekst   string       `json:"tekst"`
	Adresse *AdresseFlat `json:"adresse"`
}

// adgangsadresseFlatOf builds the reduced adgangsadresse autocomplete projection
// from a full Adgangsadresse, reusing flatFromAdgangsadresse so the field
// extraction (vejkode/vejnavn/x/y/stormodtager/…) stays in one place.
func adgangsadresseFlatOf(a *Adgangsadresse) *AdgangsadresseFlat {
	d := flatFromAdgangsadresse(a)
	return &AdgangsadresseFlat{
		ID:                     d.ID,
		Status:                 d.Status,
		Darstatus:              d.Darstatus,
		Vejkode:                d.Vejkode,
		Vejnavn:                d.Vejnavn,
		Adresseringsvejnavn:    d.Adresseringsvejnavn,
		Husnr:                  d.Husnr,
		Supplerendebynavn:      d.Supplerendebynavn,
		Postnr:                 d.Postnr,
		Postnrnavn:             d.Postnrnavn,
		Stormodtagerpostnr:     d.Stormodtagerpostnr,
		Stormodtagerpostnrnavn: d.Stormodtagerpostnrnavn,
		Kommunekode:            d.Kommunekode,
		X:                      d.X,
		Y:                      d.Y,
		Href:                   d.Href,
	}
}

// adresseFlatOf builds the reduced adresse autocomplete projection from a full
// Adresse, reusing flatFromAdresse (which already adds etage/dør/adgangsadresseid).
func adresseFlatOf(a *Adresse) *AdresseFlat {
	d := flatFromAdresse(a)
	adgID := ""
	if d.Adgangsadresseid != nil {
		adgID = *d.Adgangsadresseid
	}
	return &AdresseFlat{
		ID:                     d.ID,
		Status:                 d.Status,
		Darstatus:              d.Darstatus,
		Vejkode:                d.Vejkode,
		Vejnavn:                d.Vejnavn,
		Adresseringsvejnavn:    d.Adresseringsvejnavn,
		Husnr:                  d.Husnr,
		Etage:                  d.Etage,
		Doer:                   d.Doer,
		Supplerendebynavn:      d.Supplerendebynavn,
		Postnr:                 d.Postnr,
		Postnrnavn:             d.Postnrnavn,
		Stormodtagerpostnr:     d.Stormodtagerpostnr,
		Stormodtagerpostnrnavn: d.Stormodtagerpostnrnavn,
		Kommunekode:            d.Kommunekode,
		Adgangsadresseid:       adgID,
		X:                      d.X,
		Y:                      d.Y,
		Href:                   d.Href,
	}
}

// ---- autocomplete query functions ----

// AutocompleteKommuner returns {tekst, kommune} elements whose navn matches q.
// tekst = "{kode} {navn}" (e.g. "0101 København"). Ordered by the DAWA name
// scorer on the kommune navn (startsWith first, then whitespace-stripped name).
func AutocompleteKommuner(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*KommuneAuto, error) {
	items, err := ListKommuner(ctx, pool, baseURL)
	if err != nil {
		return nil, err
	}
	out := []*KommuneAuto{}
	for _, k := range items {
		tekst := k.Kode + " " + k.Navn
		if matchQ(tekst, q) {
			out = append(out, &KommuneAuto{Tekst: tekst, Kommune: k})
		}
	}
	if q != "" {
		sortVejnavne(q, out, func(a *KommuneAuto) string { return a.Kommune.Navn })
	}
	return limitTake(out, perSide, offset), nil
}

// AutocompleteRegioner returns {tekst, region} elements. tekst = "{kode} {navn}".
// Ordered by the DAWA name scorer on the region navn.
func AutocompleteRegioner(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*RegionAuto, error) {
	items, err := ListRegions(ctx, pool, baseURL)
	if err != nil {
		return nil, err
	}
	out := []*RegionAuto{}
	for _, r := range items {
		// DAWA's regioner autocomplete tekst is "{kode} {navn}" (e.g.
		// "1084 Region Hovedstaden"), the same coded form as every other DAGI
		// family. VERIFIED live 2026-05-31 via a direct fetch of
		// /regioner/autocomplete?q=Hoved → "1084 Region Hovedstaden". A prior
		// session's " {navn}" (leading space, no kode) was fabricated and wrong.
		tekst := r.Kode + " " + r.Navn
		if matchQ(tekst, q) {
			out = append(out, &RegionAuto{Tekst: tekst, Region: r})
		}
	}
	if q != "" {
		sortVejnavne(q, out, func(a *RegionAuto) string { return a.Region.Navn })
	}
	return limitTake(out, perSide, offset), nil
}

// AutocompleteSogne returns {tekst, sogn} elements. tekst = "{kode} {navn}".
// Ordered by the DAWA name scorer on the sogn navn.
func AutocompleteSogne(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*SognAuto, error) {
	items, err := ListSogne(ctx, pool, baseURL)
	if err != nil {
		return nil, err
	}
	out := []*SognAuto{}
	for _, s := range items {
		tekst := s.Kode + " " + s.Navn
		if matchQ(tekst, q) {
			out = append(out, &SognAuto{Tekst: tekst, Sogn: s})
		}
	}
	if q != "" {
		sortVejnavne(q, out, func(a *SognAuto) string { return a.Sogn.Navn })
	}
	return limitTake(out, perSide, offset), nil
}

// AutocompletePostnumre returns {tekst, postnummer} elements. tekst = "{nr} {navn}".
// q matches the composed tekst; ordering is by the DAWA name scorer on the
// tekst (so a numeric q like "21" sorts the substring matches by name).
func AutocompletePostnumre(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*PostnummerAuto, error) {
	items, err := ListPostnumre(ctx, pool, baseURL)
	if err != nil {
		return nil, err
	}
	out := []*PostnummerAuto{}
	for _, p := range items {
		tekst := p.Nr + " " + p.Navn
		if matchQ(tekst, q) {
			red := &PostnummerReduced{
				Nr:   p.Nr,
				Navn: p.Navn,
				// stormodtager is false for normal postnumre (the only ones in the
				// /postnumre collection ListPostnumre draws from); stormodtager
				// postnumre live in a separate set DAWA's autocomplete also marks
				// false here. Our Postnummer model carries no flag, so emit false.
				Stormodtager: false,
				Href:         p.Href,
			}
			if p.Visueltcenter != [2]float64{} {
				x := p.Visueltcenter[0]
				y := p.Visueltcenter[1]
				red.VisueltcenterX = &x
				red.VisueltcenterY = &y
			}
			out = append(out, &PostnummerAuto{Tekst: tekst, Postnummer: red})
		}
	}
	if q != "" {
		sortVejnavne(q, out, func(a *PostnummerAuto) string { return a.Tekst })
	}
	return limitTake(out, perSide, offset), nil
}

// AutocompleteLandsdele returns {tekst, landsdel} elements. tekst = landsdel
// navn. Ordered by the DAWA name scorer — e.g. q="Køben" → "Københavns omegn"
// (startsWith) before "Byen København".
func AutocompleteLandsdele(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*LandsdelAuto, error) {
	items, err := ListLandsdele(ctx, pool, baseURL)
	if err != nil {
		return nil, err
	}
	out := []*LandsdelAuto{}
	for _, l := range items {
		if matchQ(l.Navn, q) {
			out = append(out, &LandsdelAuto{Tekst: l.Navn, Landsdel: l})
		}
	}
	if q != "" {
		sortVejnavne(q, out, func(a *LandsdelAuto) string { return a.Tekst })
	}
	return limitTake(out, perSide, offset), nil
}

// AutocompleteStorkredse returns {tekst, storkreds} elements. tekst = storkreds
// navn. Ordered by the DAWA name scorer.
func AutocompleteStorkredse(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*StorkredsAuto, error) {
	items, err := ListStorkredse(ctx, pool, baseURL)
	if err != nil {
		return nil, err
	}
	out := []*StorkredsAuto{}
	for _, s := range items {
		if matchQ(s.Navn, q) {
			out = append(out, &StorkredsAuto{Tekst: s.Navn, Storkreds: s})
		}
	}
	if q != "" {
		sortVejnavne(q, out, func(a *StorkredsAuto) string { return a.Tekst })
	}
	return limitTake(out, perSide, offset), nil
}

// AutocompleteValglandsdele returns {tekst, valglandsdel} elements. tekst = navn.
// Ordered by the DAWA name scorer.
func AutocompleteValglandsdele(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*ValglandsdelAuto, error) {
	items, err := ListValglandsdele(ctx, pool, baseURL)
	if err != nil {
		return nil, err
	}
	out := []*ValglandsdelAuto{}
	for _, v := range items {
		if matchQ(v.Navn, q) {
			out = append(out, &ValglandsdelAuto{Tekst: v.Navn, Valglandsdel: v})
		}
	}
	if q != "" {
		sortVejnavne(q, out, func(a *ValglandsdelAuto) string { return a.Tekst })
	}
	return limitTake(out, perSide, offset), nil
}

// AutocompleteOpstillingskredse returns {tekst, opstillingskreds} elements.
// tekst = "{kode} {navn}" (kode is the 4-padded nummer, e.g. "0003 Indre By").
// Ordered by the DAWA name scorer on the opstillingskreds navn.
func AutocompleteOpstillingskredse(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*OpstillingskredsAuto, error) {
	items, err := ListOpstillingskredse(ctx, pool, baseURL)
	if err != nil {
		return nil, err
	}
	out := []*OpstillingskredsAuto{}
	for _, o := range items {
		tekst := o.Kode + " " + o.Navn
		if matchQ(tekst, q) {
			out = append(out, &OpstillingskredsAuto{Tekst: tekst, Opstillingskreds: o})
		}
	}
	if q != "" {
		sortVejnavne(q, out, func(a *OpstillingskredsAuto) string { return a.Opstillingskreds.Navn })
	}
	return limitTake(out, perSide, offset), nil
}

// AutocompleteSimpleAreas returns {tekst, <key>} elements for a SimpleArea
// resource. tekst = "{kode} {navn}" (e.g. "1101 Københavns Byret"). key is the
// wrapper json key (e.g. "retskreds"). Ordered by the DAWA name scorer on navn.
func AutocompleteSimpleAreas(ctx context.Context, pool *pgxpool.Pool, table, pathSeg, key, q, baseURL string, perSide, offset int) ([]*SimpleAreaAuto, error) {
	items, err := ListSimpleAreas(ctx, pool, table, pathSeg, baseURL)
	if err != nil {
		return nil, err
	}
	out := []*SimpleAreaAuto{}
	for _, a := range items {
		tekst := a.Kode + " " + a.Navn
		if matchQ(tekst, q) {
			out = append(out, &SimpleAreaAuto{Tekst: tekst, Key: key, Area: a})
		}
	}
	if q != "" {
		sortVejnavne(q, out, func(a *SimpleAreaAuto) string { return a.Area.Navn })
	}
	return limitTake(out, perSide, offset), nil
}

// AutocompleteVejstykker returns {tekst, vejstykke} elements. tekst = vejstykke
// navn. The full match set is fetched then ordered by the DAWA vejnavn scorer
// (scoreAddressElement(navn,q,1) + whitespace-stripped name tie-break) and
// truncated to perSide.
func AutocompleteVejstykker(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*VejstykkeAuto, error) {
	items, err := listVejstykkerMatching(ctx, pool, q, baseURL, 0, 0)
	if err != nil {
		return nil, err
	}
	if q != "" {
		sortVejnavne(q, items, func(v *Vejstykke) string { return derefStr(v.Navn) })
	}
	items = limitTake(items, perSide, offset)
	out := []*VejstykkeAuto{}
	for _, v := range items {
		kommunekode := ""
		if v.Kommune != nil {
			kommunekode = v.Kommune.Kode
		}
		out = append(out, &VejstykkeAuto{Tekst: derefStr(v.Navn), Vejstykke: &VejstykkeReduced{
			Href:        v.Href,
			Kommunekode: kommunekode,
			Kode:        v.Kode,
			Navn:        v.Navn,
		}})
	}
	return out, nil
}

// AutocompleteVejnavne returns {tekst, vejnavn} elements. tekst = vejnavn navn.
// The full match set is fetched (no SQL LIMIT) then ordered by DAWA's
// DETERMINISTIC vejnavn scorer (scoreAddressElement(navn,q,1) + whitespace-
// stripped name tie-break, per dawa_autocomplete.js queryVejnavn), then
// truncated to perSide. This reproduces DAWA's vejnavne autocomplete ordering:
// startsWith matches first, then the alphabetical tie-break.
func AutocompleteVejnavne(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*VejnavnAuto, error) {
	items, err := listVejnavneMatching(ctx, pool, q, baseURL, 0, 0)
	if err != nil {
		return nil, err
	}
	if q != "" {
		sortVejnavne(q, items, func(v *Vejnavn) string { return v.Navn })
	}
	items = limitTake(items, perSide, offset)
	out := []*VejnavnAuto{}
	for _, v := range items {
		out = append(out, &VejnavnAuto{Tekst: v.Navn, Vejnavn: &VejnavnReduced{Navn: v.Navn, Href: v.Href}})
	}
	return out, nil
}

// AutocompleteNavngivneveje returns {tekst, navngivenvej} elements.
// tekst = "{navn}, {administrerendekommune navn} Kommune". Ordered by the DAWA
// vejnavn scorer on the road name.
func AutocompleteNavngivneveje(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*NavngivenVejAuto, error) {
	items, err := listNavngivnevejeMatchingAuto(ctx, pool, q, baseURL, 0, 0)
	if err != nil {
		return nil, err
	}
	if q != "" {
		sortVejnavne(q, items, func(a *navngivenvejAuto) string { return derefStr(a.nv.Navn) })
	}
	items = limitTake(items, perSide, offset)
	out := []*NavngivenVejAuto{}
	for _, a := range items {
		n := a.nv
		navn := derefStr(n.Navn)
		komNavn := ""
		var komKode *string
		if n.Administrerendekommune != nil {
			komNavn = n.Administrerendekommune.Navn
			k := n.Administrerendekommune.Kode
			komKode = &k
		}
		tekst := navn
		if komNavn != "" {
			tekst = navn + ", " + komNavn + " Kommune"
		}
		red := &NavngivenVejReduced{
			ID:                         n.ID,
			Darstatus:                  n.Darstatus,
			Navn:                       n.Navn,
			Adresseringsnavn:           n.Adresseringsnavn,
			Administrerendekommunekode: komKode,
			VisueltcenterX:             a.vx,
			VisueltcenterY:             a.vy,
			Href:                       n.Href,
		}
		if n.Administrerendekommune != nil {
			kn := n.Administrerendekommune.Navn
			red.Administrerendekommunenavn = &kn
		}
		out = append(out, &NavngivenVejAuto{Tekst: tekst, Navngivenvej: red})
	}
	return out, nil
}

// AutocompleteSupplerendebynavne returns {tekst, supplerendebynavn} elements.
// tekst = bynavn navn. Ordered by the DAWA name scorer.
func AutocompleteSupplerendebynavne(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*SupplerendeBynavnAuto, error) {
	items, err := listSupplerendebynavneMatching(ctx, pool, q, baseURL, 0, 0)
	if err != nil {
		return nil, err
	}
	if q != "" {
		sortVejnavne(q, items, func(s *SupplerendeBynavn) string { return s.Navn })
	}
	items = limitTake(items, perSide, offset)
	out := []*SupplerendeBynavnAuto{}
	for _, s := range items {
		out = append(out, &SupplerendeBynavnAuto{Tekst: s.Navn, Supplerendebynavn: s})
	}
	return out, nil
}

// AutocompleteSupplerendebynavneV1 returns the DEPRECATED v1
// /supplerendebynavne/autocomplete elements: {tekst, supplerendebynavn:{navn,
// href}} where href points at the v2 resource keyed by the NAME
// (/supplerendebynavne2/{navn}, navn percent-encoded — space → %20). It reuses the
// same candidate set + scorer as the v2 autocomplete but projects the reduced
// {navn, href} element.
func AutocompleteSupplerendebynavneV1(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*SupplerendeBynavnV1Auto, error) {
	items, err := listSupplerendebynavneMatching(ctx, pool, q, baseURL, 0, 0)
	if err != nil {
		return nil, err
	}
	if q != "" {
		sortVejnavne(q, items, func(s *SupplerendeBynavn) string { return s.Navn })
	}
	items = limitTake(items, perSide, offset)
	out := []*SupplerendeBynavnV1Auto{}
	for _, s := range items {
		href := baseURL + "/supplerendebynavne2/" + url.PathEscape(s.Navn)
		out = append(out, &SupplerendeBynavnV1Auto{
			Tekst:             s.Navn,
			Supplerendebynavn: &SupplerendeBynavnReduced{Navn: s.Navn, Href: href},
		})
	}
	return out, nil
}

// AutocompleteEjerlav returns {tekst, ejerlav} elements. tekst = ejerlav navn.
// Ordered by the DAWA name scorer.
func AutocompleteEjerlav(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*EjerlavAuto, error) {
	items, err := ListEjerlav(ctx, pool, baseURL)
	if err != nil {
		return nil, err
	}
	out := []*EjerlavAuto{}
	for _, e := range items {
		if matchQ(e.Navn, q) {
			out = append(out, &EjerlavAuto{
				Tekst: e.Navn,
				Ejerlav: &EjerlavReduced{
					Href: e.Href,
					Kode: e.Kode,
					Navn: e.Navn,
				},
			})
		}
	}
	if q != "" {
		sortVejnavne(q, out, func(a *EjerlavAuto) string { return a.Ejerlav.Navn })
	}
	return limitTake(out, perSide, offset), nil
}

// AutocompleteJordstykker returns {tekst, jordstykke} elements.
// tekst = "{matrikelnr} {ejerlav navn}". Ordered by the DAWA name scorer on the
// composed tekst.
func AutocompleteJordstykker(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*JordstykkeAuto, error) {
	items, err := listJordstykkerMatching(ctx, pool, q, baseURL, 0, 0)
	if err != nil {
		return nil, err
	}
	out := []*JordstykkeAuto{}
	for _, j := range items {
		tekst := j.Matrikelnr + " " + j.Ejerlav.Navn
		out = append(out, &JordstykkeAuto{
			Tekst: tekst,
			Jordstykke: &JordstykkeFlat{
				Matrikelnr:    j.Matrikelnr,
				Bbox:          j.Bbox,
				Visueltcenter: j.Visueltcenter,
				Href:          j.Href,
				Ejerlav:       j.Ejerlav,
				// DAWA's autocomplete jordstykke.kommune is always null (the
				// full single-GET KommuneRef is not projected here).
				Kommune:              nil,
				Esrejendomsnr:        j.Esrejendomsnr,
				UdvidetEsrejendomsnr: j.UdvidetEsrejendomsnr,
				Sfeejendomsnr:        j.Sfeejendomsnr,
				Bfenummer:            j.Bfenummer,
			},
		})
	}
	if q != "" {
		sortVejnavne(q, out, func(a *JordstykkeAuto) string { return a.Tekst })
	}
	return limitTake(out, perSide, offset), nil
}

// AutocompleteAfstemningsomraader returns {tekst, afstemningsområde} elements.
// tekst = afstemningsområde navn. Ordered by the DAWA name scorer — e.g.
// q="Indre" → "Indre By" (startsWith) before "3. Indre By".
func AutocompleteAfstemningsomraader(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*AfstemningsomraadeAuto, error) {
	items, err := ListAfstemningsomraader(ctx, pool, baseURL)
	if err != nil {
		return nil, err
	}
	out := []*AfstemningsomraadeAuto{}
	for _, a := range items {
		if matchQ(a.Navn, q) {
			out = append(out, &AfstemningsomraadeAuto{Tekst: a.Navn, Afstemningsomraade: a})
		}
	}
	if q != "" {
		sortVejnavne(q, out, func(a *AfstemningsomraadeAuto) string { return a.Tekst })
	}
	return limitTake(out, perSide, offset), nil
}

// AutocompleteStednavne2 returns stednavne2 list elements whose navn matches q.
// Unlike the other resources there is NO {tekst,...} wrapper. The SQL filter +
// LIMIT are applied in SQL (the table is large). The captured golden's order is
// DAWA's stednavn relevance order over multiple skrivemåder per place, which the
// scorer here does not fully reproduce — see golden_coverage_test.go.
func AutocompleteStednavne2(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*Stednavn2, error) {
	return listStednavne2Matching(ctx, pool, q, baseURL, perSide, offset)
}

// AutocompleteAdgangsadresser returns {tekst, adgangsadresse} elements.
// tekst = the adgangsadresse adressebetegnelse. The full road-name match set is
// fetched then ordered by DAWA's DETERMINISTIC address scorer (sortAdresse →
// compareProcessedResults) and truncated to perSide. With a discriminating
// husnr/postnr the scorer fully orders the page; with just a road name every
// candidate ties on score so the order falls back to the incoming SQL order
// (vejnavn, husnr-numeric, husnummertekst, id) — see golden_coverage_test.go.
func AutocompleteAdgangsadresser(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*AdgangsadresseAuto, error) {
	var items []*Adgangsadresse
	var err error
	if q == "" {
		// No query: keep SQL pagination (the unbounded fetch would materialise the
		// whole table).
		items, err = listAdgangsadresserMatching(ctx, pool, q, baseURL, perSide, offset)
	} else {
		// Full q so the tsquery ANDs every token (vejnavn+husnr+...), narrowing the
		// candidate set the way DAWA does — not just the road-name prefix.
		items, err = listAdgangsadresserMatching(ctx, pool, q, baseURL, 0, 0)
	}
	if err != nil {
		return nil, err
	}
	if q != "" {
		sortAdresseFields("adgangsadresse", q, items, func(a *Adgangsadresse) addrFieldValues { return adgFieldValues(a) })
		items = limitTake(items, perSide, offset)
	}
	out := []*AdgangsadresseAuto{}
	for _, a := range items {
		out = append(out, &AdgangsadresseAuto{Tekst: a.Adressebetegnelse, Adgangsadresse: adgangsadresseFlatOf(a)})
	}
	return out, nil
}

// AutocompleteAdresser returns {tekst, adresse} elements. tekst = the adresse
// adressebetegnelse. Ordered by the same DETERMINISTIC address scorer as
// AutocompleteAdgangsadresser (entity="adresse", adding etage/dør to the
// tie-break chain).
func AutocompleteAdresser(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*AdresseAuto, error) {
	var items []*Adresse
	var err error
	if q == "" {
		items, err = listAdresserMatching(ctx, pool, q, baseURL, perSide, offset)
	} else {
		// Full q so etage/dør tokens narrow to the matching enhedsadresse(r).
		items, err = listAdresserMatching(ctx, pool, q, baseURL, 0, 0)
	}
	if err != nil {
		return nil, err
	}
	if q != "" {
		sortAdresseFields("adresse", q, items, func(a *Adresse) addrFieldValues { return adrFieldValues(a) })
		items = limitTake(items, perSide, offset)
	}
	out := []*AdresseAuto{}
	for _, a := range items {
		out = append(out, &AdresseAuto{Tekst: a.Adressebetegnelse, Adresse: adresseFlatOf(a)})
	}
	return out, nil
}

// splitTrailingHusnr inserts a space before a trailing run of digits when the
// query is "<letters><digits>" with no separating space (DAWA's aggregate
// autocomplete behaviour for inputs like "Rådhuspladsen1").
func splitTrailingHusnr(q string) string {
	r := []rune(q)
	i := len(r)
	for i > 0 && unicode.IsDigit(r[i-1]) {
		i--
	}
	if i == 0 || i == len(r) {
		return q
	}
	if r[i-1] == ' ' {
		return q
	}
	return string(r[:i]) + " " + string(r[i:])
}

// roadNamePart returns the leading road-name portion of an aggregate query —
// everything before the first house-number token. It is used as the SQL prefix
// filter for the adgangsadresse/adresse steps (the full q is still used for
// relevance scoring). E.g. "Rådhuspladsen 1" → "Rådhuspladsen".
func roadNamePart(q string) string {
	q = strings.TrimSpace(splitTrailingHusnr(q))
	fields := strings.Fields(q)
	var name []string
	for _, f := range fields {
		r := []rune(f)
		if len(r) > 0 && unicode.IsDigit(r[0]) {
			break
		}
		name = append(name, f)
	}
	if len(name) == 0 {
		return q
	}
	return strings.Join(name, " ")
}

// AutocompleteFlatData is the flat resource projection emitted as the aggregate
// /autocomplete element's "data" object. Field order matches DAWA's emitted JSON
// byte-for-byte: id,status,darstatus,vejkode,vejnavn,adresseringsvejnavn,husnr,
// supplerendebynavn,postnr,postnrnavn,stormodtagerpostnr,stormodtagerpostnrnavn,
// kommunekode,x,y,href. For an adresse, etage/dør appear right after husnr and
// adgangsadresseid right after kommunekode (matching DAWA's adresse autocomplete
// data shape).
type AutocompleteFlatData struct {
	ID                     string   `json:"id"`
	Status                 int      `json:"status"`
	Darstatus              int      `json:"darstatus"`
	Vejkode                string   `json:"vejkode"`
	Vejnavn                *string  `json:"vejnavn"`
	Adresseringsvejnavn    *string  `json:"adresseringsvejnavn"`
	Husnr                  string   `json:"husnr"`
	Etage                  *string  `json:"etage,omitempty"`
	Doer                   *string  `json:"dør,omitempty"`
	Supplerendebynavn      *string  `json:"supplerendebynavn"`
	Postnr                 *string  `json:"postnr"`
	Postnrnavn             *string  `json:"postnrnavn"`
	Stormodtagerpostnr     *string  `json:"stormodtagerpostnr"`
	Stormodtagerpostnrnavn *string  `json:"stormodtagerpostnrnavn"`
	Kommunekode            string   `json:"kommunekode"`
	Adgangsadresseid       *string  `json:"adgangsadresseid,omitempty"`
	X                      *float64 `json:"x"`
	Y                      *float64 `json:"y"`
	Href                   string   `json:"href"`
}

// AutocompleteFlatElement is one aggregate /autocomplete element. The emitted
// JSON key order matches DAWA's on-the-wire order exactly:
// {data, stormodtagerpostnr, type, tekst, forslagstekst, caretpos}.
type AutocompleteFlatElement struct {
	Data               *AutocompleteFlatData `json:"data"`
	Stormodtagerpostnr bool                  `json:"stormodtagerpostnr"`
	Type               string                `json:"type"`
	Tekst              string                `json:"tekst"`
	Forslagstekst      string                `json:"forslagstekst"`
	Caretpos           int                   `json:"caretpos"`
}

// adgFieldValues extracts the scorer's raw per-field values from an
// Adgangsadresse (vejnavn/adresseringsvejnavn/husnr/postnr/... + stormodtager).
func adgFieldValues(a *Adgangsadresse) addrFieldValues {
	v := addrFieldValues{husnr: a.Husnr}
	if a.Vejstykke != nil {
		v.vejnavn = derefStr(a.Vejstykke.Navn)
		v.adresseringsvejnavn = derefStr(a.Vejstykke.Adresseringsnavn)
	}
	v.supplerendebynavn = derefStr(a.Supplerendebynavn)
	if a.Postnummer != nil {
		v.postnr = a.Postnummer.Nr
		v.postnrnavn = a.Postnummer.Navn
	}
	if a.Stormodtagerpostnummer != nil {
		v.stormodtagerpostnr = a.Stormodtagerpostnummer.Nr
		v.stormodtagernavn = a.Stormodtagerpostnummer.Navn
	}
	return v
}

// adrFieldValues extracts the scorer's raw per-field values from an Adresse (the
// embedded adgangsadresse plus the enhedsadresse's etage/dør).
func adrFieldValues(a *Adresse) addrFieldValues {
	v := addrFieldValues{}
	if a.Adgangsadresse != nil {
		v = adgFieldValues(a.Adgangsadresse)
	}
	v.etage = derefStr(a.Etage)
	v.dør = derefStr(a.Doer)
	return v
}

// flatFromAdgangsadresse builds the aggregate "data" projection for an
// adgangsadresse (etage/dør/adgangsadresseid omitted — they are adresse-only).
func flatFromAdgangsadresse(a *Adgangsadresse) *AutocompleteFlatData {
	d := &AutocompleteFlatData{
		ID:          a.ID,
		Status:      a.Status,
		Darstatus:   a.Darstatus,
		Husnr:       a.Husnr,
		Kommunekode: kommunekodeOf(a),
		Href:        a.Href,
	}
	if a.Vejstykke != nil {
		d.Vejkode = a.Vejstykke.Kode
		d.Vejnavn = a.Vejstykke.Navn
		d.Adresseringsvejnavn = a.Vejstykke.Adresseringsnavn
	}
	d.Supplerendebynavn = a.Supplerendebynavn
	if a.Postnummer != nil {
		d.Postnr = strPtr(a.Postnummer.Nr)
		d.Postnrnavn = strPtr(a.Postnummer.Navn)
	}
	if a.Stormodtagerpostnummer != nil {
		d.Stormodtagerpostnr = strPtr(a.Stormodtagerpostnummer.Nr)
		d.Stormodtagerpostnrnavn = strPtr(a.Stormodtagerpostnummer.Navn)
	}
	if a.Adgangspunkt != nil && a.Adgangspunkt.Koordinater != nil {
		k := *a.Adgangspunkt.Koordinater
		x := k[0]
		y := k[1]
		d.X = &x
		d.Y = &y
	}
	return d
}

// flatFromAdresse builds the aggregate "data" projection for an enhedsadresse:
// the adgangsadresse projection plus etage/dør and adgangsadresseid, and the
// adresse's own id/status/darstatus/href.
func flatFromAdresse(a *Adresse) *AutocompleteFlatData {
	d := &AutocompleteFlatData{}
	if a.Adgangsadresse != nil {
		d = flatFromAdgangsadresse(a.Adgangsadresse)
		adgID := a.Adgangsadresse.ID
		d.Adgangsadresseid = &adgID
	}
	d.ID = a.ID
	d.Status = a.Status
	d.Darstatus = a.Darstatus
	d.Href = a.Href
	d.Etage = a.Etage
	d.Doer = a.Doer
	return d
}

// kommunekodeOf returns the 4-padded kommunekode of an adgangsadresse, or "".
func kommunekodeOf(a *Adgangsadresse) string {
	if a.Kommune != nil {
		return a.Kommune.Kode
	}
	return ""
}

func strPtr(s string) *string { return &s }

// AutocompleteAggregate is DAWA's combined /autocomplete?q=... (no resource
// prefix). It escalates vejnavn → adgangsadresse → adresse exactly as
// dawa_autocomplete.js' sqlModel.processQuery does (startfra..slutmed slice of
// the entity list, shouldProceed gating, lastEntity always returns), then emits
// the flat element shape {data,stormodtagerpostnr,type,tekst,forslagstekst,
// caretpos}.
//
// requestType is DAWA's `type` param (default "adresse"); startfra is the
// `startfra` param (default "vejnavn"). slutmed = requestType. Crucially the
// emitted tekst/caretpos depends on requestType, NOT on the entity that
// actually answered: when the escalation stops at adgangsadresse but
// requestType != "adgangsadresse" (e.g. the default "adresse"), DAWA emits the
// INCOMPLETE form (formatIncompleteAdr → "vejnavn husnr, , postnr by",
// caretpos after "husnr, ") so the user can type etage/dør. This is what the
// captured golden (q="Rådhuspladsen 1", type defaulting to adresse) shows:
// type "adgangsadresse" but caretpos 17 and the ", , " gap.
//
// The result ORDER is the DETERMINISTIC scorer in autocomplete_score.go
// (sortAdresse / vejnavn scoring), so discriminating queries (whose husnr
// selects the husnr=1 candidates ordered by postnr) reproduce DAWA byte-for-byte.
func AutocompleteAggregate(ctx context.Context, pool *pgxpool.Pool, q, requestType, startfra, baseURL string, perSide, offset int) ([]*AutocompleteFlatElement, error) {
	if strings.TrimSpace(q) == "" {
		return []*AutocompleteFlatElement{}, nil
	}
	medsupplerendebynavn := true // DAWA default supplerendebynavn=true

	entityTypes := []string{"vejnavn", "adgangsadresse", "adresse"}
	idxOf := func(s string) int {
		for i, e := range entityTypes {
			if e == s {
				return i
			}
		}
		return -1
	}
	if requestType == "" {
		requestType = "adresse"
	}
	if startfra == "" {
		startfra = "vejnavn"
	}
	slutmed := requestType
	startIdx, endIdx := idxOf(startfra), idxOf(slutmed)
	if startIdx < 0 || endIdx < 0 || startIdx > endIdx {
		// Invalid combination (e.g. startfra after slutmed) → empty, as DAWA's
		// processQuery returns [[]] when index(startfra) > index(slutmed).
		return []*AutocompleteFlatElement{}, nil
	}

	for i := startIdx; i <= endIdx; i++ {
		lastEntity := i == endIdx
		switch entityTypes[i] {
		case "vejnavn":
			// shouldProceed.vejnavn = exactly 1 result AND q starts with its navn.
			vejResults, err := aggregateVejnavn(ctx, pool, q, baseURL)
			if err != nil {
				return nil, err
			}
			proceeds := len(vejResults) == 1 &&
				strings.HasPrefix(strings.ToLower(q), strings.ToLower(vejResults[0].Navn))
			if lastEntity || (len(vejResults) > 0 && !proceeds) {
				return finishVejnavnElements(vejResults, requestType, perSide, offset), nil
			}
		case "adgangsadresse":
			// shouldProceed.adgangsadresse = result.length <= 1; so return when this
			// is the last entity or there is more than one match. Full q so the
			// tsquery ANDs all tokens: an etage/dør token (e.g. "st"/"th") matches
			// NO adgangsadresse (its search text has no etage/dør), making this step
			// return 0 and escalate to the adresse step — DAWA's exact behaviour.
			adgs, err := listAdgangsadresserMatching(ctx, pool, q, baseURL, 0, 0)
			if err != nil {
				return nil, err
			}
			sortAdresseFields("adgangsadresse", q, adgs, func(a *Adgangsadresse) addrFieldValues { return adgFieldValues(a) })
			if lastEntity || len(adgs) > 1 {
				return finishAdgangsadresseElements(adgs, requestType, medsupplerendebynavn, perSide, offset), nil
			}
		case "adresse":
			// adresse is always the last searchable entity → return its results.
			adrs, err := listAdresserMatching(ctx, pool, q, baseURL, 0, 0)
			if err != nil {
				return nil, err
			}
			sortAdresseFields("adresse", q, adrs, func(a *Adresse) addrFieldValues { return adrFieldValues(a) })
			return finishAdresseElements(adrs, medsupplerendebynavn, perSide, offset), nil
		}
	}
	return []*AutocompleteFlatElement{}, nil
}

// aggregateVejnavn runs the vejnavn escalation step: query distinct road names
// matching q, scorer-sorted. Returns nil when q contains two separate digit
// groups (DAWA's /\d[^\d]+\d/ guard) so escalation proceeds to the address steps.
func aggregateVejnavn(ctx context.Context, pool *pgxpool.Pool, q, baseURL string) ([]*Vejnavn, error) {
	if twoSeparateDigitGroups(q) {
		return nil, nil
	}
	items, err := listVejnavneMatching(ctx, pool, q, baseURL, 0, 0)
	if err != nil {
		return nil, err
	}
	sortVejnavne(q, items, func(v *Vejnavn) string { return v.Navn })
	return items, nil
}

// twoSeparateDigitGroups reproduces /\d[^\d]+\d/ on the query.
func twoSeparateDigitGroups(q string) bool {
	seenDigit := false
	seenGap := false
	for _, c := range q {
		isDigit := c >= '0' && c <= '9'
		switch {
		case isDigit && !seenDigit:
			seenDigit = true
		case isDigit && seenDigit && seenGap:
			return true
		case !isDigit && seenDigit:
			seenGap = true
		}
	}
	return false
}

// finishVejnavnElements maps vejnavn results to flat elements (type=vejnavn).
func finishVejnavnElements(items []*Vejnavn, requestType string, perSide, offset int) []*AutocompleteFlatElement {
	items = limitTake(items, perSide, offset)
	out := make([]*AutocompleteFlatElement, 0, len(items))
	for _, v := range items {
		tekst := v.Navn
		if requestType != "vejnavn" {
			tekst += " "
		}
		out = append(out, &AutocompleteFlatElement{
			Data:               nil,
			Stormodtagerpostnr: false,
			Type:               "vejnavn",
			Tekst:              tekst,
			Forslagstekst:      v.Navn,
			Caretpos:           len([]rune(tekst)),
		})
	}
	return out
}

// finishAdgangsadresseElements maps adgangsadresse results to flat elements,
// applying formatAdresse / formatIncompleteAdr exactly as the JS postProcessFns.
func finishAdgangsadresseElements(items []*Adgangsadresse, requestType string, medsupplerendebynavn bool, perSide, offset int) []*AutocompleteFlatElement {
	items = limitTake(items, perSide, offset)
	out := make([]*AutocompleteFlatElement, 0, len(items))
	for _, a := range items {
		data := flatFromAdgangsadresse(a)
		forslagstekst := formatAdresseLine(data, false)
		var tekst string
		var caretpos int
		if requestType != "adgangsadresse" {
			caretpos, tekst = formatIncompleteAdr(data, medsupplerendebynavn)
		} else {
			tekst = formatAdresseLine(data, false)
			caretpos = len([]rune(tekst))
		}
		out = append(out, &AutocompleteFlatElement{
			Data:               data,
			Stormodtagerpostnr: false,
			Type:               "adgangsadresse",
			Tekst:              tekst,
			Forslagstekst:      forslagstekst,
			Caretpos:           caretpos,
		})
	}
	return out
}

// finishAdresseElements maps adresse results to flat elements (type=adresse,
// the final entity → complete tekst, caretpos = end of tekst).
func finishAdresseElements(items []*Adresse, medsupplerendebynavn bool, perSide, offset int) []*AutocompleteFlatElement {
	items = limitTake(items, perSide, offset)
	out := make([]*AutocompleteFlatElement, 0, len(items))
	for _, a := range items {
		data := flatFromAdresse(a)
		tekst := formatAdresseLine(data, false)
		out = append(out, &AutocompleteFlatElement{
			Data:               data,
			Stormodtagerpostnr: false,
			Type:               "adresse",
			Tekst:              tekst,
			Forslagstekst:      tekst,
			Caretpos:           len([]rune(tekst)),
		})
	}
	return out
}

// formatAdresseLine is a port of formatAdresse(data, false, multiline, true) →
// formatAdresseMultiline joined with ", " when multiline is false. stormodtager
// handling is not needed for the served golden (non-stormodtager).
func formatAdresseLine(d *AutocompleteFlatData, multiline bool) string {
	var b strings.Builder
	b.WriteString(derefStr(d.Vejnavn))
	if d.Husnr != "" {
		b.WriteString(" " + d.Husnr)
	}
	if derefStr(d.Etage) != "" || derefStr(d.Doer) != "" {
		b.WriteString(",")
	}
	if derefStr(d.Etage) != "" {
		b.WriteString(" " + derefStr(d.Etage) + ".")
	}
	if derefStr(d.Doer) != "" {
		b.WriteString(" " + derefStr(d.Doer))
	}
	if derefStr(d.Supplerendebynavn) != "" {
		b.WriteString("\n" + derefStr(d.Supplerendebynavn))
	}
	b.WriteString("\n" + derefStr(d.Postnr) + " " + derefStr(d.Postnrnavn))
	s := b.String()
	if multiline {
		return s
	}
	return strings.ReplaceAll(s, "\n", ", ")
}

// formatIncompleteAdr is a port of formatIncompleteAdr in dawa_autocomplete.js:
// the caret is positioned after "vejnavn husnr, " so the user can type etage/dør.
// caretpos is the rune length of textBeforeCaret. The tekst has the
// characteristic ", , " gap (empty etage/dør slot) for an adgangsadresse.
func formatIncompleteAdr(d *AutocompleteFlatData, medsupplerendebynavn bool) (int, string) {
	textBeforeCaret := derefStr(d.Vejnavn) + " " + d.Husnr + ", "
	var after strings.Builder
	if medsupplerendebynavn && derefStr(d.Supplerendebynavn) != "" {
		after.WriteString(", " + derefStr(d.Supplerendebynavn))
	}
	after.WriteString(", " + derefStr(d.Postnr) + " " + derefStr(d.Postnrnavn))
	caretpos := len([]rune(textBeforeCaret))
	return caretpos, textBeforeCaret + after.String()
}
