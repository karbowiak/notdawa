package dawa

import (
	"context"
	"fmt"
	"net/url"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// stednavntyper.go serves DAWA's /stednavntyper (+ /stednavntyper/{hovedtype})
// type catalog: the list of place hovedtyper, each with its sorted undertyper.
//
// DATA SOURCE: DAWA's published catalog is a FIXED metadata table — it does NOT
// vary with the row data. The local mirror's ds_steder cannot reproduce it by a
// plain SELECT DISTINCT hovedtype/undertype, for two concrete reasons proven
// against the live capture:
//
//  1. Our DS ingest (internal/ingest/ds.go, dsPlaceTypes) MERGES the two
//     AndenTopografi extracts (Flade + Punkt) under one stored hovedtype
//     "Anden topografi", whereas DAWA keeps them SPLIT as "Andentopografi flade"
//     and "Andentopografi punkt" with different undertyper. The split is not
//     recoverable from the merged rows (their undertyper overlap: kilde,
//     vejkryds appear in both).
//  2. DAWA lists undertyper that have ZERO rows in our extract (e.g. Vej:
//     "låningsvej","parkeringsplads"; Andentopografi punkt: "strandpost"), which
//     a DISTINCT over our rows can never emit; and several stored hovedtype
//     display strings differ from DAWA's (Idrætsanlæg→"Idraetsanlæg",
//     Navigationsanlæg→"Navigationsanlaeg", Urent farvand→"Urentfarvand",
//     bebyggelse→"Bebyggelse", Campingplads undertype set, …).
//
// So the catalog below is transcribed verbatim from the live DAWA capture
// (https://api.dataforsyningen.dk/stednavntyper, 2026-05-30) — DAWA's own
// authoritative type catalog, the same kind of fixed mapping table as
// dsPlaceTypes. It is byte-exact with live DAWA. Undertyper are pre-sorted as
// DAWA emits them; the list is ordered by hovedtype as DAWA orders it.
//
// hovedtypeQueryConstrains: the served catalog is independent of pool — pool is
// accepted only to match the package's Get*/List* signature convention and to
// allow a future data-derived variant without changing the handler wiring.

// Stednavntype is one /stednavntyper element: {href, hovedtype, undertyper[]}.
// Field order is significant.
type Stednavntype struct {
	Href       string   `json:"href"`
	Hovedtype  string   `json:"hovedtype"`
	Undertyper []string `json:"undertyper"`
}

// stednavntypeCatalog is DAWA's published hovedtype→undertyper catalog,
// transcribed verbatim from the live capture (2026-05-31). IMPORTANT: the
// undertyper here are in DAWA's DECLARED (source) order — exactly what the SINGLE
// endpoint /stednavntyper/{hovedtype} returns. DAWA's COLLECTION endpoint, by
// contrast, returns each undertyper list SORTED ascending; ListStednavntyper
// reproduces that by sorting a copy (Go's byte-order sort.Strings matches DAWA's
// collection ordering exactly, verified against the live capture). The catalog is
// ordered by hovedtype as DAWA orders it.
var stednavntypeCatalog = []struct {
	hovedtype  string
	undertyper []string
}{
	{"Andentopografi flade", []string{"landsdel", "stenbrud", "vejkryds", "kilde", "vindmøllepark", "køretekniskAnlæg"}},
	{"Andentopografi punkt", []string{"sten", "kilde", "varde", "vejkryds", "udsigtspunkt", "rastepladsUdenService", "rastepladsMedService", "sluse", "ledLåge", "bro", "vandfald", "grænsestenGrænsepæl", "gravsted", "mindesten", "motorvejskryds", "udsigtstårn", "brønd", "strandpost", "løvtræ", "nåletræ"}},
	{"Bebyggelse", []string{"bydel", "spredtBebyggelse", "by", "sommerhusområde", "industriområde", "kolonihave", "sommerhusområdedel"}},
	{"Begravelsesplads", []string{"kristen", "jødisk", "muslimsk", "andenReligion"}},
	{"Bygning", []string{"gård", "hus", "kirkeProtestantisk", "rådhus", "slot", "herregård", "hospital", "universitet", "museumSamling", "skadestue", "moske", "synagoge", "kirkeAndenKristen", "hal", "fængsel", "hotel", "vejrmølle", "vandmølle", "gymnasium", "folkeskole", "akvarium", "terrarium", "observatorium", "vandkraftværk", "kraftvarmeværk", "kursuscenter", "kommunekontor", "regionsgård", "andenBygning", "vandrerhjem", "feriecenter", "søredningsstation", "terminal", "fagskole", "efterskoleUngdomsskole", "folkehøjskole", "privatskoleFriskole", "daginstitution", "uddannelsescenter", "specialskole", "turistbureau", "proffesionshøjskole", "forskningscenter", "friluftsgård"}},
	{"Campingplads", []string{"Campingplads"}},
	{"Farvand", []string{"sund", "bugt", "hav", "fjord", "nor", "bredning", "løb", "sejlløb"}},
	{"Fortidsminde", []string{"gravhøj", "voldVoldsted", "helleristning", "ruin", "runesten", "bautasten", "dysse", "skanse", "oldtidsager", "fundsted", "tomt", "langdysse", "runddysse", "jættestue", "hellekiste", "røse", "skibssætning", "vej", "oldtidsvej", "fæstningsanlæg", "batteri", "vikingeborg", "krigergrav", "boplads", "oldtidsminde", "historiskMindeHistoriskAnlæg", "køkkenmøding"}},
	{"Friluftsbad", []string{"sø", "hav", "land"}},
	{"Havnebassin", []string{"lystbådehavn", "trafikhavn", "fiskerihavn"}},
	{"Idraetsanlæg", []string{"golfbane", "stadion", "skydebane", "motorbane", "cykelbane", "hestevæddeløbsbane", "motocrossbane", "hundevæddeløbsbane", "goKartbane"}},
	{"Jernbane", []string{"jernbanetunnel", "veteranjernbane"}},
	{"Landskabsform", []string{"bakke", "halvø", "højdedrag", "ø", "klint", "odde", "dal", "pynt", "hage", "tange", "øgruppe", "skær", "hule", "højBanke", "slugt", "næs", "ås", "lavning", "skræntNaturlig", "kløft"}},
	{"Lufthavn", []string{"størreLufthavn", "mindreLufthavn", "flyveplads", "heliport", "landingsplads", "svæveflyveplads"}},
	{"Naturareal", []string{"skovPlantage", "hede", "klippeIOverfladen", "eng", "moseSump", "strand", "sandKlit", "parkAnlæg", "agerMark", "marsk"}},
	{"Navigationsanlaeg", []string{"fyrtårn", "fyr", "båke"}},
	{"Restriktionsareal", []string{"naturpark", "nationalpark", "reservat"}},
	{"Seværdighed", []string{"zoologiskHave", "dyrepark", "frilandsmuseum", "botaniskHave", "arboret", "forlystelsespark", "blomsterpark", "andenSeværdighed"}},
	{"Standsningssted", []string{"tog"}},
	{"Sø", []string{"sø"}},
	{"Terrænkontur", []string{"dige", "dæmning"}},
	{"Urentfarvand", []string{"overskylledeSten", "tørtVedLavvande", "undersøiskGrund"}},
	{"Vandløb", []string{"vandløb"}},
	{"Vej", []string{"sti", "vejstrækning", "plads", "vejbro", "vejtunnel", "ebbevej", "låningsvej", "parkeringsplads"}},
}

// renderStednavntype builds one catalog element (href percent-encodes the
// hovedtype path segment exactly as DAWA does: space→%20, ø→%C3%B8, …).
func renderStednavntype(hovedtype string, undertyper []string, baseURL string) *Stednavntype {
	return &Stednavntype{
		Href:       fmt.Sprintf("%s/stednavntyper/%s", baseURL, url.PathEscape(hovedtype)),
		Hovedtype:  hovedtype,
		Undertyper: undertyper,
	}
}

// ListStednavntyper returns the full hovedtype→undertyper catalog ordered by
// hovedtype. Unlike the single endpoint, DAWA's COLLECTION sorts each undertyper
// list ascending, so each list is sorted on a COPY here (the catalog itself stays
// in declared order for GetStednavntype). The pool argument is unused (the catalog
// is fixed DAWA metadata; see the file comment) but kept for signature symmetry.
func ListStednavntyper(ctx context.Context, pool *pgxpool.Pool, baseURL string) ([]*Stednavntype, error) {
	_ = ctx
	_ = pool
	out := make([]*Stednavntype, 0, len(stednavntypeCatalog))
	for _, e := range stednavntypeCatalog {
		sorted := make([]string, len(e.undertyper))
		copy(sorted, e.undertyper)
		sort.Strings(sorted)
		out = append(out, renderStednavntype(e.hovedtype, sorted, baseURL))
	}
	return out, nil
}

// GetStednavntype returns the catalog entry for a single hovedtype, or
// pgx.ErrNoRows when it is not a known hovedtype. The lookup is exact-match on the
// DAWA display string; the catalog is small so a linear scan is fine.
func GetStednavntype(ctx context.Context, pool *pgxpool.Pool, hovedtype, baseURL string) (*Stednavntype, error) {
	_ = ctx
	_ = pool
	for _, e := range stednavntypeCatalog {
		if e.hovedtype == hovedtype {
			return renderStednavntype(e.hovedtype, e.undertyper, baseURL), nil
		}
	}
	return nil, pgx.ErrNoRows
}
