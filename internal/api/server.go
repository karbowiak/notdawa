// Package api serves the DAWA-compatible HTTP layer over the local PostGIS
// mirror. Every route maps to a dawa.Get*/List* representation and the bytes are
// emitted exactly as the dawa package marshals them, so responses stay
// byte-for-byte compatible with api.dataforsyningen.dk.
//
// Pagination follows DAWA's "side"/"per_side" query parameters: when per_side is
// absent the full collection is returned; when present, page "side" (1-based) of
// "per_side" items is returned. Three wiring classes exist depending on the
// underlying dawa List signature; see the paging helpers below.
//
// Cross-cutting collection query parameters (per-field filters, q/fuzzy free
// text, struktur=nestet|flad|mini, format=json|ndjson|jsonp|csv, callback,
// noformat) are layered on top of the List* handlers by query.go
// (parseCollectionParams + writeCollection / writePagedCollection).
package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/dawa"
)

// NewServer builds the DAWA-compatible HTTP handler. baseURL is the host used in
// generated hrefs (kept fixed so output matches the goldens). It returns a
// *http.ServeMux so it can be exercised directly via httptest.
func NewServer(pool *pgxpool.Pool, baseURL string) http.Handler {
	s := &server{pool: pool, baseURL: baseURL}
	mux := http.NewServeMux()
	s.routes(mux)
	return mux
}

// ServerRoutes returns every GET route pattern that routes() registers, as the
// bare path (without the "GET " method prefix). It is the source of truth for
// the Huma drift test (TestHumaDriftCoverage), which asserts NewHumaServer's
// /openapi.json documents an operation for each. KEEP THIS IN SYNC with
// routes()/routesAutocomplete()/routesReverse()/routesTilknytninger() — it does
// not register anything, it only enumerates the served patterns for the test.
// This helper adds no behaviour to the production server.
func ServerRoutes() []string {
	routes := []string{
		// routes()
		"/regioner", "/regioner/{kode}",
		"/kommuner", "/kommuner/{kode}",
		"/sogne", "/sogne/{kode}",
		"/postnumre", "/postnumre/{nr}",
		"/landsdele", "/landsdele/{nuts3}",
		"/storkredse", "/storkredse/{nummer}",
		"/valglandsdele", "/valglandsdele/{bogstav}",
		"/opstillingskredse", "/opstillingskredse/{kode}",
		"/retskredse", "/retskredse/{kode}",
		"/politikredse", "/politikredse/{kode}",
		"/vejstykker", "/vejstykker/{kommunekode}/{kode}/naboer", "/vejstykker/{kommunekode}/{kode}",
		"/vejnavne", "/vejnavne/{navn}",
		"/navngivneveje", "/navngivneveje/{id}/naboer", "/navngivneveje/{id}",
		"/vejnavnpostnummerrelationer", "/vejnavnpostnummerrelationer/autocomplete", "/vejnavnpostnummerrelationer/{postnr}/{vejnavn}",
		"/stednavntyper", "/stednavntyper/{hovedtype}",
		"/supplerendebynavne2", "/supplerendebynavne", "/supplerendebynavne2/{dagi_id}",
		"/ejerlav", "/ejerlav/{kode}",
		"/jordstykker", "/jordstykker/{ejerlavkode}/{matrikelnr}",
		"/steder", "/steder/{id}",
		"/stednavne", "/stednavne/autocomplete", "/stednavne/{id}",
		"/stednavne2", "/stednavne2/{sted_id}/{navn}",
		"/bebyggelser", "/bebyggelser/{id}",
		"/adgangsadresser", "/adgangsadresser/{id}",
		"/adresser", "/adresser/{id}",
		"/datavask/adgangsadresser", "/datavask/adresser",
		"/afstemningsomraader", "/afstemningsomraader/{kommunekode}/{nummer}",
		"/menighedsraadsafstemningsomraader",
		"/menighedsraadsafstemningsomraader/autocomplete",
		"/menighedsraadsafstemningsomraader/reverse",
		"/menighedsraadsafstemningsomraader/{kommunekode}/{nummer}",
		// routesAutocomplete()
		"/autocomplete",
		"/kommuner/autocomplete", "/regioner/autocomplete", "/sogne/autocomplete",
		"/postnumre/autocomplete", "/landsdele/autocomplete", "/storkredse/autocomplete",
		"/valglandsdele/autocomplete", "/opstillingskredse/autocomplete",
		"/retskredse/autocomplete", "/politikredse/autocomplete", "/vejstykker/autocomplete",
		"/vejnavne/autocomplete", "/navngivneveje/autocomplete",
		"/supplerendebynavne2/autocomplete", "/supplerendebynavne/autocomplete",
		"/ejerlav/autocomplete", "/jordstykker/autocomplete",
		"/afstemningsomraader/autocomplete", "/stednavne2/autocomplete",
		"/adgangsadresser/autocomplete", "/adresser/autocomplete",
		// routesReverse()
		"/kommuner/reverse", "/regioner/reverse", "/sogne/reverse", "/postnumre/reverse",
		"/landsdele/reverse", "/storkredse/reverse", "/valglandsdele/reverse",
		"/opstillingskredse/reverse", "/retskredse/reverse", "/politikredse/reverse",
		"/afstemningsomraader/reverse", "/supplerendebynavne2/reverse",
		"/jordstykker/reverse", "/vejstykker/reverse",
		"/adgangsadresser/reverse", "/adresser/reverse",
		// replikering (registered directly in routes())
		"/replikering/senestesekvensnummer", "/replikering/transaktioner",
		"/replikering/{entitet}/udtraek", "/replikering/{entitet}/haendelser",
	}
	// routesTilknytninger() — one route per served tilknytning path.
	for _, p := range dawa.TilknytningPaths() {
		routes = append(routes, "/"+p)
	}
	return routes
}

type server struct {
	pool    *pgxpool.Pool
	baseURL string
}

func (s *server) routes(mux *http.ServeMux) {
	// Regioner
	mux.HandleFunc("GET /regioner", s.listRegioner)
	mux.HandleFunc("GET /regioner/{kode}", s.getRegion)
	// Kommuner
	mux.HandleFunc("GET /kommuner", s.listKommuner)
	mux.HandleFunc("GET /kommuner/{kode}", s.getKommune)
	// Sogne
	mux.HandleFunc("GET /sogne", s.listSogne)
	mux.HandleFunc("GET /sogne/{kode}", s.getSogn)
	// Postnumre
	mux.HandleFunc("GET /postnumre", s.listPostnumre)
	mux.HandleFunc("GET /postnumre/{nr}", s.getPostnummer)
	// Landsdele
	mux.HandleFunc("GET /landsdele", s.listLandsdele)
	mux.HandleFunc("GET /landsdele/{nuts3}", s.getLandsdel)
	// Storkredse
	mux.HandleFunc("GET /storkredse", s.listStorkredse)
	mux.HandleFunc("GET /storkredse/{nummer}", s.getStorkreds)
	// Valglandsdele
	mux.HandleFunc("GET /valglandsdele", s.listValglandsdele)
	mux.HandleFunc("GET /valglandsdele/{bogstav}", s.getValglandsdel)
	// Opstillingskredse
	mux.HandleFunc("GET /opstillingskredse", s.listOpstillingskredse)
	mux.HandleFunc("GET /opstillingskredse/{kode}", s.getOpstillingskreds)
	// Retskredse (SimpleArea: table dagi_retskredse, pathSeg retskredse)
	mux.HandleFunc("GET /retskredse", s.listRetskredse)
	mux.HandleFunc("GET /retskredse/{kode}", s.getRetskreds)
	// Politikredse (SimpleArea: table dagi_politikredse, pathSeg politikredse)
	mux.HandleFunc("GET /politikredse", s.listPolitikredse)
	mux.HandleFunc("GET /politikredse/{kode}", s.getPolitikreds)
	// Vejstykker (+ naboer: literal sub-path before the {kommunekode}/{kode} wildcard)
	mux.HandleFunc("GET /vejstykker", s.listVejstykker)
	mux.HandleFunc("GET /vejstykker/{kommunekode}/{kode}/naboer", s.vejstykkeNaboer)
	mux.HandleFunc("GET /vejstykker/{kommunekode}/{kode}", s.getVejstykke)
	// Vejnavne
	mux.HandleFunc("GET /vejnavne", s.listVejnavne)
	mux.HandleFunc("GET /vejnavne/{navn}", s.getVejnavn)
	// Navngivneveje (+ naboer: literal sub-path before the {id} wildcard)
	mux.HandleFunc("GET /navngivneveje", s.listNavngivneveje)
	mux.HandleFunc("GET /navngivneveje/{id}/naboer", s.navngivenvejNaboer)
	mux.HandleFunc("GET /navngivneveje/{id}", s.getNavngivenVej)
	// Vejnavnpostnummerrelationer (autocomplete before the composite wildcard)
	mux.HandleFunc("GET /vejnavnpostnummerrelationer", s.listVejnavnPostnummerRelationer)
	mux.HandleFunc("GET /vejnavnpostnummerrelationer/autocomplete", s.autocompleteVejnavnPostnummerRelationer)
	mux.HandleFunc("GET /vejnavnpostnummerrelationer/{postnr}/{vejnavn}", s.getVejnavnPostnummerRelation)
	// Stednavntyper (fixed catalog; DAWA ignores paging here)
	mux.HandleFunc("GET /stednavntyper", s.listStednavntyper)
	mux.HandleFunc("GET /stednavntyper/{hovedtype}", s.getStednavntype)
	// Supplerendebynavne. v2 keeps the rich {metadata,bbox,visueltcenter,dagi_id,
	// darstatus,kommune,postnumre} element; v1 (deprecated) is a DISTINCT, simpler
	// shape {href,navn,postnumre[],kommuner[]} ordered alphabetically by navn (its
	// own handler — NOT an alias of v2). The /supplerendebynavne2/{dagi_id} byKey +
	// /reverse sub-paths are registered below (reverse) and here (byKey, after the
	// literal collection paths).
	mux.HandleFunc("GET /supplerendebynavne2", s.listSupplerendebynavne)
	mux.HandleFunc("GET /supplerendebynavne", s.listSupplerendebynavneV1)
	mux.HandleFunc("GET /supplerendebynavne2/{dagi_id}", s.getSupplerendebynavn)
	// Ejerlav
	mux.HandleFunc("GET /ejerlav", s.listEjerlav)
	mux.HandleFunc("GET /ejerlav/{kode}", s.getEjerlav)
	// Jordstykker
	mux.HandleFunc("GET /jordstykker", s.listJordstykker)
	mux.HandleFunc("GET /jordstykker/{ejerlavkode}/{matrikelnr}", s.getJordstykke)
	// Steder (collection handler adds ?x&y ST_Covers + ?nærmeste KNN reverse-via-query)
	mux.HandleFunc("GET /steder", s.listSteder2)
	mux.HandleFunc("GET /steder/{id}", s.getSted)
	// Stednavne (legacy; autocomplete before the {id} wildcard) + stednavne2.
	mux.HandleFunc("GET /stednavne", s.listLegacyStednavne)
	mux.HandleFunc("GET /stednavne/autocomplete", s.autocompleteLegacyStednavne)
	mux.HandleFunc("GET /stednavne/{id}", s.getLegacyStednavn)
	// Stednavne2 (collection adds reverse-via-query like steder; + composite byKey)
	mux.HandleFunc("GET /stednavne2", s.listStednavne2WithReverse)
	mux.HandleFunc("GET /stednavne2/{sted_id}/{navn}", s.getStednavn2)
	// Bebyggelser
	mux.HandleFunc("GET /bebyggelser", s.listBebyggelser)
	mux.HandleFunc("GET /bebyggelser/{id}", s.getBebyggelse)
	// Adgangsadresser
	mux.HandleFunc("GET /adgangsadresser", s.listAdgangsadresser)
	mux.HandleFunc("GET /adgangsadresser/{id}", s.getAdgangsadresse)
	// Adresser
	mux.HandleFunc("GET /adresser", s.listAdresser)
	mux.HandleFunc("GET /adresser/{id}", s.getAdresse)
	mux.HandleFunc("GET /datavask/adgangsadresser", s.datavaskAdgangsadresser)
	mux.HandleFunc("GET /datavask/adresser", s.datavaskAdresser)
	// Afstemningsomraader — single path key is composite (kommunekode, nummer).
	mux.HandleFunc("GET /afstemningsomraader", s.listAfstemningsomraader)
	mux.HandleFunc("GET /afstemningsomraader/{kommunekode}/{nummer}", s.getAfstemningsomraade)
	// Menighedsrådsafstemningsområder — composite key; autocomplete+reverse before
	// the wildcard.
	mux.HandleFunc("GET /menighedsraadsafstemningsomraader", s.listMrafstemningsomraader)
	mux.HandleFunc("GET /menighedsraadsafstemningsomraader/autocomplete", s.autocompleteMrafstemningsomraader)
	mux.HandleFunc("GET /menighedsraadsafstemningsomraader/reverse", s.reverseMrafstemningsomraade)
	mux.HandleFunc("GET /menighedsraadsafstemningsomraader/{kommunekode}/{nummer}", s.getMrafstemningsomraade)

	// Autocomplete + reverse-geocoding. These literal sub-paths take precedence
	// over the {kode}/{nr}/... wildcards above (Go 1.22 ServeMux prefers the more
	// specific pattern), so e.g. /kommuner/reverse never hits getKommune.
	s.routesAutocomplete(mux)
	s.routesReverse(mux)

	// Tilknytninger — adgangsadresse↔area association resources (minimal rows).
	s.routesTilknytninger(mux)

	// Replikering API. udtraek + senestesekvensnummer are backed by real data;
	// haendelser + transaktioner + senestetransaktion return the documented DAWA
	// envelope with truthful (initial-load / empty) content only; datamodel serves
	// DAWA's static reference descriptor verbatim — see replikering.go.
	mux.HandleFunc("GET /replikering/senestesekvensnummer", s.senesteSekvensnummer)
	mux.HandleFunc("GET /replikering/senestetransaktion", s.senesteTransaktion)
	mux.HandleFunc("GET /replikering/transaktioner", s.transaktioner)
	mux.HandleFunc("GET /replikering/datamodel", s.datamodel)
	mux.HandleFunc("GET /replikering/{entitet}/udtraek", s.udtraek)
	mux.HandleFunc("GET /replikering/{entitet}/haendelser", s.haendelser)
}

// routesAutocomplete registers GET /{resource}/autocomplete for every resource
// that has a golden, plus the aggregate GET /autocomplete.
func (s *server) routesAutocomplete(mux *http.ServeMux) {
	mux.HandleFunc("GET /autocomplete", s.autocompleteAggregate)
	mux.HandleFunc("GET /kommuner/autocomplete", s.autocompleteKommuner)
	mux.HandleFunc("GET /regioner/autocomplete", s.autocompleteRegioner)
	mux.HandleFunc("GET /sogne/autocomplete", s.autocompleteSogne)
	mux.HandleFunc("GET /postnumre/autocomplete", s.autocompletePostnumre)
	mux.HandleFunc("GET /landsdele/autocomplete", s.autocompleteLandsdele)
	mux.HandleFunc("GET /storkredse/autocomplete", s.autocompleteStorkredse)
	mux.HandleFunc("GET /valglandsdele/autocomplete", s.autocompleteValglandsdele)
	mux.HandleFunc("GET /opstillingskredse/autocomplete", s.autocompleteOpstillingskredse)
	mux.HandleFunc("GET /retskredse/autocomplete", s.autocompleteRetskredse)
	mux.HandleFunc("GET /politikredse/autocomplete", s.autocompletePolitikredse)
	mux.HandleFunc("GET /vejstykker/autocomplete", s.autocompleteVejstykker)
	mux.HandleFunc("GET /vejnavne/autocomplete", s.autocompleteVejnavne)
	mux.HandleFunc("GET /navngivneveje/autocomplete", s.autocompleteNavngivneveje)
	mux.HandleFunc("GET /supplerendebynavne2/autocomplete", s.autocompleteSupplerendebynavne)
	mux.HandleFunc("GET /supplerendebynavne/autocomplete", s.autocompleteSupplerendebynavneV1)
	mux.HandleFunc("GET /ejerlav/autocomplete", s.autocompleteEjerlav)
	mux.HandleFunc("GET /jordstykker/autocomplete", s.autocompleteJordstykker)
	mux.HandleFunc("GET /afstemningsomraader/autocomplete", s.autocompleteAfstemningsomraader)
	mux.HandleFunc("GET /stednavne2/autocomplete", s.autocompleteStednavne2)
	mux.HandleFunc("GET /adgangsadresser/autocomplete", s.autocompleteAdgangsadresser)
	mux.HandleFunc("GET /adresser/autocomplete", s.autocompleteAdresser)
}

// routesReverse registers GET /{resource}/reverse for every resource with a
// reverse golden.
func (s *server) routesReverse(mux *http.ServeMux) {
	mux.HandleFunc("GET /kommuner/reverse", s.reverseKommune)
	mux.HandleFunc("GET /regioner/reverse", s.reverseRegion)
	mux.HandleFunc("GET /sogne/reverse", s.reverseSogn)
	mux.HandleFunc("GET /postnumre/reverse", s.reversePostnummer)
	mux.HandleFunc("GET /landsdele/reverse", s.reverseLandsdel)
	mux.HandleFunc("GET /storkredse/reverse", s.reverseStorkreds)
	mux.HandleFunc("GET /valglandsdele/reverse", s.reverseValglandsdel)
	mux.HandleFunc("GET /opstillingskredse/reverse", s.reverseOpstillingskreds)
	mux.HandleFunc("GET /retskredse/reverse", s.reverseRetskreds)
	mux.HandleFunc("GET /politikredse/reverse", s.reversePolitikreds)
	mux.HandleFunc("GET /afstemningsomraader/reverse", s.reverseAfstemningsomraade)
	mux.HandleFunc("GET /supplerendebynavne2/reverse", s.reverseSupplerendebynavn)
	mux.HandleFunc("GET /jordstykker/reverse", s.reverseJordstykke)
	mux.HandleFunc("GET /vejstykker/reverse", s.reverseVejstykke)
	mux.HandleFunc("GET /adgangsadresser/reverse", s.reverseAdgangsadresse)
	mux.HandleFunc("GET /adresser/reverse", s.reverseAdresse)
}

// ---- pagination ----

// paging holds the parsed side/per_side query parameters.
type paging struct {
	present bool // per_side was supplied → apply paging
	side    int  // 1-based page number (>= 1)
	perSide int  // page size (>= 0)
}

// limit returns the per_side window size, or 0 ("all") when paging is absent.
func (p paging) limit() int {
	if !p.present {
		return 0
	}
	return p.perSide
}

// offset returns (side-1)*per_side, or 0 when paging is absent.
func (p paging) offset() int {
	if !p.present {
		return 0
	}
	return (p.side - 1) * p.perSide
}

// parsePaging reads side/per_side. side defaults to 1. per_side absent → no
// paging. Returns ok=false (and writes a 400) on a malformed integer.
func parsePaging(w http.ResponseWriter, r *http.Request) (paging, bool) {
	q := r.URL.Query()
	p := paging{side: 1}
	if v := q.Get("per_side"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeBadRequest(w, "per_side", v)
			return paging{}, false
		}
		p.perSide = n
		p.present = true
	}
	if v := q.Get("side"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeBadRequest(w, "side", v)
			return paging{}, false
		}
		p.side = n
	}
	return p, true
}

// paginate slices items to the requested page. When perSide<=0 (no paging) it
// returns items unchanged. Out-of-range offsets yield an empty slice, never a
// panic.
func paginate[T any](items []T, side, perSide int) []T {
	if perSide <= 0 {
		return items
	}
	offset := (side - 1) * perSide
	if offset < 0 || offset >= len(items) {
		return items[:0]
	}
	end := offset + perSide
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}

// ---- shared error handling for single-resource lookups ----

// isNoRows reports whether err is pgx's no-rows sentinel.
func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

func (s *server) ctx(r *http.Request) context.Context { return r.Context() }

// ---- per-resource handlers ----

// Regioner — Get takes kode as string; validate numeric, pass verbatim.
func (s *server) listRegioner(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	items, err := dawa.ListRegions(s.ctx(r), s.pool, s.baseURL)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeCollection(w, cp, items)
}

func (s *server) getRegion(w http.ResponseWriter, r *http.Request) {
	kode, ok := numStr(w, r, "kode")
	if !ok {
		return
	}
	v, err := dawa.GetRegion(s.ctx(r), s.pool, kode, s.baseURL)
	finishGet(w, v, err, map[string]any{"kode": kode})
}

// Kommuner
func (s *server) listKommuner(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	items, err := dawa.ListKommuner(s.ctx(r), s.pool, s.baseURL)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeCollection(w, cp, items)
}

func (s *server) getKommune(w http.ResponseWriter, r *http.Request) {
	kode, ok := numStr(w, r, "kode")
	if !ok {
		return
	}
	v, err := dawa.GetKommune(s.ctx(r), s.pool, kode, s.baseURL)
	finishGet(w, v, err, map[string]any{"kode": kode})
}

// Sogne
func (s *server) listSogne(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	items, err := dawa.ListSogne(s.ctx(r), s.pool, s.baseURL)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeCollection(w, cp, items)
}

func (s *server) getSogn(w http.ResponseWriter, r *http.Request) {
	kode, ok := numStr(w, r, "kode")
	if !ok {
		return
	}
	v, err := dawa.GetSogn(s.ctx(r), s.pool, kode, s.baseURL)
	finishGet(w, v, err, map[string]any{"kode": kode})
}

// Postnumre
func (s *server) listPostnumre(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	items, err := dawa.ListPostnumre(s.ctx(r), s.pool, s.baseURL)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeCollection(w, cp, items)
}

func (s *server) getPostnummer(w http.ResponseWriter, r *http.Request) {
	nr, ok := numStr(w, r, "nr")
	if !ok {
		return
	}
	v, err := dawa.GetPostnummer(s.ctx(r), s.pool, nr, s.baseURL)
	finishGet(w, v, err, map[string]any{"nr": nr})
}

// Landsdele — nuts3 is a free string (e.g. DK011).
func (s *server) listLandsdele(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	items, err := dawa.ListLandsdele(s.ctx(r), s.pool, s.baseURL)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeCollection(w, cp, items)
}

func (s *server) getLandsdel(w http.ResponseWriter, r *http.Request) {
	nuts3 := r.PathValue("nuts3")
	v, err := dawa.GetLandsdel(s.ctx(r), s.pool, nuts3, s.baseURL)
	finishGet(w, v, err, map[string]any{"nuts3": nuts3})
}

// Storkredse — nummer is a string code.
func (s *server) listStorkredse(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	items, err := dawa.ListStorkredse(s.ctx(r), s.pool, s.baseURL)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeCollection(w, cp, items)
}

func (s *server) getStorkreds(w http.ResponseWriter, r *http.Request) {
	nummer := r.PathValue("nummer")
	v, err := dawa.GetStorkreds(s.ctx(r), s.pool, nummer, s.baseURL)
	finishGet(w, v, err, map[string]any{"nummer": nummer})
}

// Valglandsdele — bogstav is a free string (e.g. A).
func (s *server) listValglandsdele(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	items, err := dawa.ListValglandsdele(s.ctx(r), s.pool, s.baseURL)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeCollection(w, cp, items)
}

func (s *server) getValglandsdel(w http.ResponseWriter, r *http.Request) {
	bogstav := r.PathValue("bogstav")
	v, err := dawa.GetValglandsdel(s.ctx(r), s.pool, bogstav, s.baseURL)
	finishGet(w, v, err, map[string]any{"bogstav": bogstav})
}

// Opstillingskredse — kode is a string code.
func (s *server) listOpstillingskredse(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	items, err := dawa.ListOpstillingskredse(s.ctx(r), s.pool, s.baseURL)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeCollection(w, cp, items)
}

func (s *server) getOpstillingskreds(w http.ResponseWriter, r *http.Request) {
	kode := r.PathValue("kode")
	v, err := dawa.GetOpstillingskreds(s.ctx(r), s.pool, kode, s.baseURL)
	finishGet(w, v, err, map[string]any{"kode": kode})
}

// Retskredse (SimpleArea)
func (s *server) listRetskredse(w http.ResponseWriter, r *http.Request) {
	s.listSimpleArea(w, r, "dagi_retskredse", "retskredse")
}

func (s *server) getRetskreds(w http.ResponseWriter, r *http.Request) {
	s.getSimpleArea(w, r, "dagi_retskredse", "retskredse")
}

// Politikredse (SimpleArea)
func (s *server) listPolitikredse(w http.ResponseWriter, r *http.Request) {
	s.listSimpleArea(w, r, "dagi_politikredse", "politikredse")
}

func (s *server) getPolitikreds(w http.ResponseWriter, r *http.Request) {
	s.getSimpleArea(w, r, "dagi_politikredse", "politikredse")
}

func (s *server) listSimpleArea(w http.ResponseWriter, r *http.Request, table, pathSeg string) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	items, err := dawa.ListSimpleAreas(s.ctx(r), s.pool, table, pathSeg, s.baseURL)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeCollection(w, cp, items)
}

func (s *server) getSimpleArea(w http.ResponseWriter, r *http.Request, table, pathSeg string) {
	kode, ok := numStr(w, r, "kode")
	if !ok {
		return
	}
	v, err := dawa.GetSimpleArea(s.ctx(r), s.pool, table, pathSeg, kode, s.baseURL)
	finishGet(w, v, err, map[string]any{"kode": kode})
}

// Vejstykker — production-scale: pagination stays in SQL (limit-only List
// signature, slice the prefix to the requested page).
func (s *server) listVejstykker(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	// Per-field filters (kommunekode/kode), q, srid and spatial filters are pushed
	// into SQL (ListVejstykkerFiltered) so they filter over the whole table before
	// LIMIT/OFFSET; the page is then rendered with format/struktur.
	items, err := dawa.ListVejstykkerFiltered(s.ctx(r), s.pool, s.baseURL, cp.page.limit(), cp.page.offset(), cp.listFilter())
	if err != nil {
		writeServerError(w, err)
		return
	}
	writePagedCollection(w, cp, items)
}

func (s *server) getVejstykke(w http.ResponseWriter, r *http.Request) {
	kommunekode, ok := numStr(w, r, "kommunekode")
	if !ok {
		return
	}
	kode, ok := numStr(w, r, "kode")
	if !ok {
		return
	}
	v, err := dawa.GetVejstykke(s.ctx(r), s.pool, kommunekode, kode, s.baseURL)
	finishGet(w, v, err, map[string]any{"kommunekode": kommunekode, "kode": kode})
}

// Vejnavne — production-scale: pagination stays in SQL.
func (s *server) listVejnavne(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	// navn= and q are pushed into SQL (ListVejnavneFiltered) over the whole table.
	items, err := dawa.ListVejnavneFiltered(s.ctx(r), s.pool, s.baseURL, cp.page.limit(), cp.page.offset(), cp.listFilter())
	if err != nil {
		writeServerError(w, err)
		return
	}
	writePagedCollection(w, cp, items)
}

func (s *server) getVejnavn(w http.ResponseWriter, r *http.Request) {
	navn := r.PathValue("navn")
	v, err := dawa.GetVejnavn(s.ctx(r), s.pool, navn, s.baseURL)
	finishGet(w, v, err, map[string]any{"navn": navn})
}

// Navngivneveje — production-scale: pagination stays in SQL.
func (s *server) listNavngivneveje(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	// q, srid and spatial filters are pushed into SQL (ListNavngivnevejeFiltered).
	items, err := dawa.ListNavngivnevejeFiltered(s.ctx(r), s.pool, s.baseURL, cp.page.limit(), cp.page.offset(), cp.listFilter())
	if err != nil {
		writeServerError(w, err)
		return
	}
	writePagedCollection(w, cp, items)
}

func (s *server) getNavngivenVej(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := dawa.GetNavngivenVej(s.ctx(r), s.pool, id, s.baseURL)
	finishGet(w, v, err, map[string]any{"id": id})
}

// Supplerendebynavne — collection only, production-scale: pagination in SQL.
func (s *server) listSupplerendebynavne(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	// q, srid and spatial filters are pushed into SQL (ListSupplerendebynavneFiltered).
	items, err := dawa.ListSupplerendebynavneFiltered(s.ctx(r), s.pool, s.baseURL, cp.page.limit(), cp.page.offset(), cp.listFilter())
	if err != nil {
		writeServerError(w, err)
		return
	}
	writePagedCollection(w, cp, items)
}

// Ejerlav — small code-list; full pipeline (filter/q/struktur over all rows).
func (s *server) listEjerlav(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	items, err := dawa.ListEjerlav(s.ctx(r), s.pool, s.baseURL)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeCollection(w, cp, items)
}

func (s *server) getEjerlav(w http.ResponseWriter, r *http.Request) {
	kode, ok := pathInt(w, r, "kode")
	if !ok {
		return
	}
	v, err := dawa.GetEjerlav(s.ctx(r), s.pool, kode, s.baseURL)
	finishGet(w, v, err, map[string]any{"kode": r.PathValue("kode")})
}

// Jordstykker — production-scale: pagination stays in SQL (offset List
// signature). format/struktur applied to the page; filters/q deferred.
func (s *server) listJordstykker(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	// ejerlavkode, srid and spatial filters are pushed into SQL (ListJordstykkerFiltered).
	items, err := dawa.ListJordstykkerFiltered(s.ctx(r), s.pool, s.baseURL, cp.page.limit(), cp.page.offset(), cp.listFilter())
	if err != nil {
		writeServerError(w, err)
		return
	}
	writePagedCollection(w, cp, items)
}

func (s *server) getJordstykke(w http.ResponseWriter, r *http.Request) {
	ejerlavkode, ok := pathInt(w, r, "ejerlavkode")
	if !ok {
		return
	}
	matrikelnr := r.PathValue("matrikelnr")
	v, err := dawa.GetJordstykke(s.ctx(r), s.pool, ejerlavkode, matrikelnr, s.baseURL)
	finishGet(w, v, err, map[string]any{
		"ejerlavkode": r.PathValue("ejerlavkode"),
		"matrikelnr":  matrikelnr,
	})
}

// Steder — production-scale: pagination stays in SQL.
func (s *server) listSteder(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	// q, srid and spatial filters are pushed into SQL (ListStederFiltered).
	items, err := dawa.ListStederFiltered(s.ctx(r), s.pool, s.baseURL, cp.page.limit(), cp.page.offset(), cp.listFilter())
	if err != nil {
		writeServerError(w, err)
		return
	}
	writePagedCollection(w, cp, items)
}

func (s *server) getSted(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := dawa.GetSted(s.ctx(r), s.pool, id, s.baseURL)
	finishGet(w, v, err, map[string]any{"id": id})
}

// Stednavne2 — collection only, production-scale: pagination stays in SQL.
func (s *server) listStednavne2(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	// stedid=, q, srid and spatial filters are pushed into SQL
	// (ListStednavne2Filtered); the stedid filter is what makes ?stedid= return
	// only that place's name rows instead of the full 3M-row table.
	items, err := dawa.ListStednavne2Filtered(s.ctx(r), s.pool, s.baseURL, cp.page.limit(), cp.page.offset(), cp.listFilter())
	if err != nil {
		writeServerError(w, err)
		return
	}
	writePagedCollection(w, cp, items)
}

// Bebyggelser — production-scale: pagination stays in SQL.
func (s *server) listBebyggelser(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	// q is pushed into SQL (ListBebyggelserFiltered).
	items, err := dawa.ListBebyggelserFiltered(s.ctx(r), s.pool, s.baseURL, cp.page.limit(), cp.page.offset(), cp.listFilter())
	if err != nil {
		writeServerError(w, err)
		return
	}
	writePagedCollection(w, cp, items)
}

func (s *server) getBebyggelse(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := dawa.GetBebyggelse(s.ctx(r), s.pool, id, s.baseURL)
	finishGet(w, v, err, map[string]any{"id": id})
}

// Adgangsadresser — production-scale: pagination stays in SQL. The DB-paginated
// slice is rendered with format/struktur applied; per-field filters (postnr,
// vejkode, kommunekode, ...) and q are NOT applied in memory because that would
// only filter within the returned page.
// Per-field filters (postnr/vejkode/kommunekode), q, srid and spatial filters are
// pushed into SQL (ListAdgangsadresserFiltered) so they filter over the whole
// table before LIMIT/OFFSET; the page is rendered with format/struktur.
func (s *server) listAdgangsadresser(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	items, err := dawa.ListAdgangsadresserFiltered(s.ctx(r), s.pool, s.baseURL, cp.page.limit(), cp.page.offset(), cp.listFilter())
	if err != nil {
		writeServerError(w, err)
		return
	}
	writePagedCollection(w, cp, items)
}

func (s *server) getAdgangsadresse(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := dawa.GetAdgangsadresse(s.ctx(r), s.pool, id, s.baseURL)
	finishGet(w, v, err, map[string]any{"id": id})
}

// Adresser — production-scale: per-field filters (postnr/vejkode/kommunekode/
// etage/dør), q, srid and spatial filters are pushed into SQL
// (ListAdresserFiltered); the page is rendered with format/struktur.
func (s *server) listAdresser(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	items, err := dawa.ListAdresserFiltered(s.ctx(r), s.pool, s.baseURL, cp.page.limit(), cp.page.offset(), cp.listFilter())
	if err != nil {
		writeServerError(w, err)
		return
	}
	writePagedCollection(w, cp, items)
}

func (s *server) getAdresse(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := dawa.GetAdresse(s.ctx(r), s.pool, id, s.baseURL)
	finishGet(w, v, err, map[string]any{"id": id})
}

// datavaskAdgangsadresser washes a single ?betegnelse into {kategori,
// resultater}. A missing betegnelse yields DAWA's 400 QueryParameterFormatError.
// For a unique exact match the kategori-A response matches DAWA byte-for-byte
// except virkningstart (the original DAR virkningstid is not in the extract; see
// dawa.DatavaskAdgangsadresser). Ambiguous/corrected inputs are best-effort
// (kategori "C") and are NOT byte-exact vs DAWA's proprietary fuzzy scorer.
func (s *server) datavaskAdgangsadresser(w http.ResponseWriter, r *http.Request) {
	betegnelse := r.URL.Query().Get("betegnelse")
	if betegnelse == "" {
		writeBadRequest(w, "betegnelse", "")
		return
	}
	v, err := dawa.DatavaskAdgangsadresser(s.ctx(r), s.pool, betegnelse, s.baseURL)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeMarshalled(w, v)
}

// datavaskAdresser washes a single ?betegnelse for enhedsadresser (see
// datavaskAdgangsadresser).
func (s *server) datavaskAdresser(w http.ResponseWriter, r *http.Request) {
	betegnelse := r.URL.Query().Get("betegnelse")
	if betegnelse == "" {
		writeBadRequest(w, "betegnelse", "")
		return
	}
	v, err := dawa.DatavaskAdresser(s.ctx(r), s.pool, betegnelse, s.baseURL)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeMarshalled(w, v)
}

// Afstemningsomraader — List signature is (baseURL); single resource is keyed by
// the composite (kommunekode, nummer). Both path segments are numeric.
func (s *server) listAfstemningsomraader(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	items, err := dawa.ListAfstemningsomraader(s.ctx(r), s.pool, s.baseURL)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeCollection(w, cp, items)
}

func (s *server) getAfstemningsomraade(w http.ResponseWriter, r *http.Request) {
	kommunekode, ok := numStr(w, r, "kommunekode")
	if !ok {
		return
	}
	nummer, ok := numStr(w, r, "nummer")
	if !ok {
		return
	}
	v, err := dawa.GetAfstemningsomraadeByKommuneNummer(s.ctx(r), s.pool, kommunekode, nummer, s.baseURL)
	finishGet(w, v, err, map[string]any{"kommunekode": kommunekode, "nummer": nummer})
}

// ---- helpers ----

// finishGet renders a single-resource result: marshal on success, DAWA 404 on
// no-rows (carrying notFoundDetails), or 500 on any other error. v is typically a
// non-nil pointer on success; on error it is ignored.
func finishGet[T any](w http.ResponseWriter, v *T, err error, notFoundDetails map[string]any) {
	if err != nil {
		if isNoRows(err) {
			writeNotFound(w, notFoundDetails)
			return
		}
		writeServerError(w, err)
		return
	}
	writeMarshalled(w, v)
}

// numStr returns a numeric path segment verbatim (as a string, so leading zeros
// like kommunekode "0101" survive), writing a DAWA 400 and returning ok=false
// when the value is not a valid integer.
func numStr(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	raw := r.PathValue(name)
	if _, err := strconv.Atoi(raw); err != nil {
		writeBadRequest(w, name, raw)
		return "", false
	}
	return raw, true
}

// pathInt reads an int path segment, writing a DAWA 400 and returning ok=false
// on a parse failure (used where the dawa function takes an int key).
func pathInt(w http.ResponseWriter, r *http.Request, name string) (int, bool) {
	raw := r.PathValue(name)
	n, err := strconv.Atoi(raw)
	if err != nil {
		writeBadRequest(w, name, raw)
		return 0, false
	}
	return n, true
}

// ---- autocomplete + reverse helpers ----

// autoParams holds the parsed autocomplete query parameters. perSide defaults to
// 0 ("all") when absent; offset is derived from side (1-based). requestType and
// startfra are only meaningful for the aggregate /autocomplete escalation (they
// mirror DAWA's type/startfra params, defaulting to adresse/vejnavn); the
// per-resource autocompletes ignore them.
type autoParams struct {
	q           string
	perSide     int
	offset      int
	requestType string
	startfra    string
}

// parseAuto reads q/side/per_side for an autocomplete request. A malformed
// integer writes a DAWA 400 and returns ok=false. type and startfra default to
// DAWA's defaults (adresse / vejnavn) when absent.
//
// Unlike the generic collection paging, autocomplete defaults per_side to 20 when
// it is absent: DAWA's autocomplete endpoints cap every response at 20 elements.
// This default applies ONLY to the autocomplete code path (parseAuto is used by
// autocomplete handlers only); the collection/single GET handlers use parsePaging
// directly and keep their "no per_side → full collection" semantics.
func parseAuto(w http.ResponseWriter, r *http.Request) (autoParams, bool) {
	p, ok := parsePaging(w, r)
	if !ok {
		return autoParams{}, false
	}
	requestType := r.URL.Query().Get("type")
	if requestType == "" {
		requestType = "adresse"
	}
	startfra := r.URL.Query().Get("startfra")
	if startfra == "" {
		startfra = "vejnavn"
	}
	perSide := p.limit()
	if perSide <= 0 {
		perSide = 20 // DAWA autocomplete default cap
	}
	return autoParams{q: r.URL.Query().Get("q"), perSide: perSide, offset: p.offset(), requestType: requestType, startfra: startfra}, true
}

// parseXY reads the x (lon) and y (lat) query parameters as floats, writing a
// DAWA 400 and returning ok=false on a parse failure.
func parseXY(w http.ResponseWriter, r *http.Request) (x, y float64, ok bool) {
	q := r.URL.Query()
	xs, ys := q.Get("x"), q.Get("y")
	xv, err := strconv.ParseFloat(xs, 64)
	if err != nil {
		writeBadRequest(w, "x", xs)
		return 0, 0, false
	}
	yv, err := strconv.ParseFloat(ys, 64)
	if err != nil {
		writeBadRequest(w, "y", ys)
		return 0, 0, false
	}
	return xv, yv, true
}

// finishReverse renders a single reverse result: marshal on success, DAWA 404
// (empty details, matching the goldens) on no-match, or 500 otherwise.
func finishReverse[T any](w http.ResponseWriter, v *T, err error) {
	if err != nil {
		if isNoRows(err) {
			writeNotFound(w, nil)
			return
		}
		writeServerError(w, err)
		return
	}
	writeMarshalled(w, v)
}

// ---- autocomplete handlers ----

func (s *server) autocompleteKommuner(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteKommuner(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

func (s *server) autocompleteRegioner(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteRegioner(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

func (s *server) autocompleteSogne(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteSogne(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

func (s *server) autocompletePostnumre(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompletePostnumre(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

func (s *server) autocompleteLandsdele(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteLandsdele(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

func (s *server) autocompleteStorkredse(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteStorkredse(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

func (s *server) autocompleteValglandsdele(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteValglandsdele(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

func (s *server) autocompleteOpstillingskredse(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteOpstillingskredse(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

func (s *server) autocompleteRetskredse(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteSimpleAreas(s.ctx(r), s.pool, "dagi_retskredse", "retskredse", "retskreds", a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

func (s *server) autocompletePolitikredse(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteSimpleAreas(s.ctx(r), s.pool, "dagi_politikredse", "politikredse", "politikreds", a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

func (s *server) autocompleteVejstykker(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteVejstykker(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

func (s *server) autocompleteVejnavne(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteVejnavne(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

func (s *server) autocompleteNavngivneveje(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteNavngivneveje(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

func (s *server) autocompleteSupplerendebynavne(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteSupplerendebynavne(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

// autocompleteSupplerendebynavneV1 serves the DEPRECATED v1
// /supplerendebynavne/autocomplete (reduced {navn, href} element), distinct from
// the v2 /supplerendebynavne2/autocomplete (full object) above.
func (s *server) autocompleteSupplerendebynavneV1(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteSupplerendebynavneV1(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

func (s *server) autocompleteEjerlav(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteEjerlav(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

func (s *server) autocompleteJordstykker(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteJordstykker(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

func (s *server) autocompleteAfstemningsomraader(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteAfstemningsomraader(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

func (s *server) autocompleteStednavne2(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteStednavne2(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

func (s *server) autocompleteAdgangsadresser(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteAdgangsadresser(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

func (s *server) autocompleteAdresser(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteAdresser(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

// autocompleteAggregate is DAWA's combined /autocomplete?q=... (no resource
// prefix). It reproduces the vejnavn→adgangsadresse→adresse escalation
// (dawa_autocomplete.js sqlModel.processQuery) and the type/startfra-driven
// tekst/caretpos formatting, emitting the flat element shape.
func (s *server) autocompleteAggregate(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteAggregate(s.ctx(r), s.pool, a.q, a.requestType, a.startfra, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

// ---- reverse handlers ----

func (s *server) reverseKommune(w http.ResponseWriter, r *http.Request) {
	x, y, ok := parseXY(w, r)
	if !ok {
		return
	}
	v, err := dawa.ReverseKommune(s.ctx(r), s.pool, x, y, s.baseURL)
	finishReverse(w, v, err)
}

func (s *server) reverseRegion(w http.ResponseWriter, r *http.Request) {
	x, y, ok := parseXY(w, r)
	if !ok {
		return
	}
	v, err := dawa.ReverseRegion(s.ctx(r), s.pool, x, y, s.baseURL)
	finishReverse(w, v, err)
}

func (s *server) reverseSogn(w http.ResponseWriter, r *http.Request) {
	x, y, ok := parseXY(w, r)
	if !ok {
		return
	}
	v, err := dawa.ReverseSogn(s.ctx(r), s.pool, x, y, s.baseURL)
	finishReverse(w, v, err)
}

func (s *server) reversePostnummer(w http.ResponseWriter, r *http.Request) {
	x, y, ok := parseXY(w, r)
	if !ok {
		return
	}
	v, err := dawa.ReversePostnummer(s.ctx(r), s.pool, x, y, s.baseURL)
	finishReverse(w, v, err)
}

func (s *server) reverseLandsdel(w http.ResponseWriter, r *http.Request) {
	x, y, ok := parseXY(w, r)
	if !ok {
		return
	}
	v, err := dawa.ReverseLandsdel(s.ctx(r), s.pool, x, y, s.baseURL)
	finishReverse(w, v, err)
}

func (s *server) reverseStorkreds(w http.ResponseWriter, r *http.Request) {
	x, y, ok := parseXY(w, r)
	if !ok {
		return
	}
	v, err := dawa.ReverseStorkreds(s.ctx(r), s.pool, x, y, s.baseURL)
	finishReverse(w, v, err)
}

func (s *server) reverseValglandsdel(w http.ResponseWriter, r *http.Request) {
	x, y, ok := parseXY(w, r)
	if !ok {
		return
	}
	v, err := dawa.ReverseValglandsdel(s.ctx(r), s.pool, x, y, s.baseURL)
	finishReverse(w, v, err)
}

func (s *server) reverseOpstillingskreds(w http.ResponseWriter, r *http.Request) {
	x, y, ok := parseXY(w, r)
	if !ok {
		return
	}
	v, err := dawa.ReverseOpstillingskreds(s.ctx(r), s.pool, x, y, s.baseURL)
	finishReverse(w, v, err)
}

func (s *server) reverseRetskreds(w http.ResponseWriter, r *http.Request) {
	x, y, ok := parseXY(w, r)
	if !ok {
		return
	}
	v, err := dawa.ReverseSimpleArea(s.ctx(r), s.pool, "dagi_retskredse", "retskredse", x, y, s.baseURL)
	finishReverse(w, v, err)
}

func (s *server) reversePolitikreds(w http.ResponseWriter, r *http.Request) {
	x, y, ok := parseXY(w, r)
	if !ok {
		return
	}
	v, err := dawa.ReverseSimpleArea(s.ctx(r), s.pool, "dagi_politikredse", "politikredse", x, y, s.baseURL)
	finishReverse(w, v, err)
}

func (s *server) reverseAfstemningsomraade(w http.ResponseWriter, r *http.Request) {
	x, y, ok := parseXY(w, r)
	if !ok {
		return
	}
	v, err := dawa.ReverseAfstemningsomraade(s.ctx(r), s.pool, x, y, s.baseURL)
	finishReverse(w, v, err)
}

func (s *server) reverseJordstykke(w http.ResponseWriter, r *http.Request) {
	x, y, ok := parseXY(w, r)
	if !ok {
		return
	}
	v, err := dawa.ReverseJordstykke(s.ctx(r), s.pool, x, y, s.baseURL)
	finishReverse(w, v, err)
}

func (s *server) reverseVejstykke(w http.ResponseWriter, r *http.Request) {
	x, y, ok := parseXY(w, r)
	if !ok {
		return
	}
	v, err := dawa.ReverseVejstykke(s.ctx(r), s.pool, x, y, s.baseURL)
	finishReverse(w, v, err)
}

func (s *server) reverseAdgangsadresse(w http.ResponseWriter, r *http.Request) {
	x, y, ok := parseXY(w, r)
	if !ok {
		return
	}
	v, err := dawa.ReverseAdgangsadresse(s.ctx(r), s.pool, x, y, s.baseURL)
	finishReverse(w, v, err)
}

// reverseAdresse: DAWA has no /adresser/reverse route — it routes the request to
// /adresser/{id} with id="reverse", which fails the UUID path-pattern check and
// returns a 404 ResourcePathFormatError whose details is a nested array. We
// reproduce that envelope byte-for-byte (see writeAdresseReversePathError). The
// query is not parsed (DAWA rejects on the path before reading x/y).
func (s *server) reverseAdresse(w http.ResponseWriter, _ *http.Request) {
	writeAdresseReversePathError(w)
}

// ---- tilknytninger ----

// routesTilknytninger registers GET /<area>tilknytninger for every phantom
// tilknytning path. DAWA has NO standalone /<area>tilknytninger endpoints — the
// live Dataforsyningen gateway 404s every one with a static HTML page (verified
// 2026-05-31). We previously invented these as DB-backed collections; we now
// bind them to a handler that reproduces DAWA's gateway 404 byte-for-byte, so
// our server agrees with the upstream oracle (tests/compat_test.go).
func (s *server) routesTilknytninger(mux *http.ServeMux) {
	for _, path := range dawa.TilknytningPaths() {
		mux.HandleFunc("GET /"+path, s.tilknytninger)
	}
}

// tilknytninger emits the live DAWA API Gateway 404 (HTTP 404, text/html) for a
// phantom /<area>tilknytninger path. The body is the exact gateway page DAWA
// serves for these unknown collections (see writeGatewayNotFound).
func (s *server) tilknytninger(w http.ResponseWriter, r *http.Request) {
	_ = r
	writeGatewayNotFound(w)
}
