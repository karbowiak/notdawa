package api

// vejnavnpostnummerrelationer_handlers.go adds three DAWA resources as *server
// methods (Go allows methods on *server across files in the same package):
//
//   - /vejnavnpostnummerrelationer           (list + autocomplete)
//   - /vejnavnpostnummerrelationer/{postnr}/{vejnavn}  (single, composite key)
//   - /vejstykker/{kommunekode}/{kode}/naboer  (road neighbours)
//   - /navngivneveje/{id}/naboer               (road neighbours)
//   - /stednavntyper (+ /stednavntyper/{hovedtype})  (type catalog)
//
// They reuse the server's existing helpers (parseCollectionParams,
// writePagedCollection / writeList / writeMarshalled / finishGet, parseAuto,
// numStr, error envelopes). The mux.HandleFunc route lines for these handlers are
// listed in the build report for the orchestrator to wire into server.go's
// routes() — server.go is intentionally NOT edited here.

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/karbowiak/notdawa/internal/dawa"
)

// ---- vejnavnpostnummerrelationer ----

// listVejnavnPostnummerRelationer serves the paged collection. postnr=/vejnavn=
// per-field filters, q= and srid are pushed into SQL (List*Filtered) so they
// filter over the whole grouped set before LIMIT/OFFSET; the page is rendered
// with format/struktur via the shared pipeline.
func (s *server) listVejnavnPostnummerRelationer(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	items, err := dawa.ListVejnavnPostnummerRelationerFiltered(s.ctx(r), s.pool, s.baseURL, cp.page.limit(), cp.page.offset(), cp.listFilter())
	if err != nil {
		writeServerError(w, err)
		return
	}
	writePagedCollection(w, cp, items)
}

// getVejnavnPostnummerRelation serves the single relation keyed by the composite
// (postnr, vejnavn). postnr must be numeric; vejnavn is the URL-decoded path
// segment (Go's ServeMux already decodes %20 etc. into PathValue).
func (s *server) getVejnavnPostnummerRelation(w http.ResponseWriter, r *http.Request) {
	postnr, ok := numStr(w, r, "postnr")
	if !ok {
		return
	}
	vejnavn := r.PathValue("vejnavn")
	v, err := dawa.GetVejnavnPostnummerRelation(s.ctx(r), s.pool, postnr, vejnavn, s.baseURL)
	finishGet(w, v, err, map[string]any{"postnr": postnr, "vejnavn": vejnavn})
}

// autocompleteVejnavnPostnummerRelationer serves the autocomplete list. DAWA's
// element is {tekst, vejnavnpostnummerrelation:{...}}; the order follows the
// resource's default (postnr, vejnavn) — DAWA's proprietary relevance rank may
// pick a different top-N (documented non-substantive ordering class).
func (s *server) autocompleteVejnavnPostnummerRelationer(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteVejnavnPostnummerRelationer(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

// ---- road naboer ----

// parseAfstand reads the optional ?afstand= (metres) for the naboer endpoints.
// DAWA's default is 0 (roads touching the target). A malformed value writes the
// DAWA 400 envelope and returns ok=false.
func parseAfstand(w http.ResponseWriter, r *http.Request) (float64, bool) {
	v := r.URL.Query().Get("afstand")
	if v == "" {
		return 0, true
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		writeBadRequest(w, "afstand", v)
		return 0, false
	}
	return f, true
}

// vejstykkeNaboer serves /vejstykker/{kommunekode}/{kode}/naboer — the vejstykker
// within ?afstand= metres of the target road, excluding itself, each as the full
// vejstykke list element. srid/struktur/format are honoured via the shared
// pipeline; paging is pushed into SQL.
func (s *server) vejstykkeNaboer(w http.ResponseWriter, r *http.Request) {
	kommunekode, ok := numStr(w, r, "kommunekode")
	if !ok {
		return
	}
	kode, ok := numStr(w, r, "kode")
	if !ok {
		return
	}
	afstand, ok := parseAfstand(w, r)
	if !ok {
		return
	}
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	items, err := dawa.VejstykkeNaboer(s.ctx(r), s.pool, kommunekode, kode, s.baseURL, afstand, cp.page.limit(), cp.page.offset(), cp.listFilter())
	if err != nil {
		writeServerError(w, err)
		return
	}
	writePagedCollection(w, cp, items)
}

// navngivenvejNaboer serves /navngivneveje/{id}/naboer — the navngivneveje within
// ?afstand= metres of the target road, excluding itself, each as the full
// navngivenvej list element.
func (s *server) navngivenvejNaboer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	afstand, ok := parseAfstand(w, r)
	if !ok {
		return
	}
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	items, err := dawa.NavngivenvejNaboer(s.ctx(r), s.pool, id, s.baseURL, afstand, cp.page.limit(), cp.page.offset(), cp.listFilter())
	if err != nil {
		writeServerError(w, err)
		return
	}
	writePagedCollection(w, cp, items)
}

// ---- stednavntyper ----

// listStednavntyper serves /stednavntyper — the full hovedtype→undertyper
// catalog. DAWA DOES honour side/per_side here (verified live: ?side=1&per_side=5
// returns exactly 5 entries), so this paginates via the shared pipeline like every
// other collection; struktur/format are honoured too.
func (s *server) listStednavntyper(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	items, err := dawa.ListStednavntyper(s.ctx(r), s.pool, s.baseURL)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeCollection(w, cp, items)
}

// getStednavntype serves /stednavntyper/{hovedtype} — one catalog entry. The
// hovedtype path segment is URL-decoded by ServeMux (e.g. "Vandl%C3%B8b" →
// "Vandløb"); an unknown hovedtype yields DAWA's 404.
func (s *server) getStednavntype(w http.ResponseWriter, r *http.Request) {
	hovedtype := r.PathValue("hovedtype")
	// Defensive: if a client sent an already-encoded value that ServeMux left
	// encoded, decode it so the catalog lookup matches the display string.
	if dec, err := url.PathUnescape(hovedtype); err == nil {
		hovedtype = dec
	}
	v, err := dawa.GetStednavntype(s.ctx(r), s.pool, hovedtype, s.baseURL)
	finishGet(w, v, err, map[string]any{"hovedtype": hovedtype})
}
