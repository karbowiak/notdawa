package api

import (
	"net/http"

	"github.com/karbowiak/notdawa/internal/dawa"
)

// /historik/{adgangsadresser,adresser}: the DAR virkning history, one row per
// version (see internal/dawa/historik.go). Params: id (single chain), postnr /
// kommunekode filters, side/per_side paging; with no per_side the full dump
// streams, like live DAWA. Unknown id → an empty array, HTTP 200 — DAWA
// historik never 404s.

func (s *server) historikAdgangsadresser(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePaging(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	items, err := dawa.ListHistorikAdgangsadresser(s.ctx(r), s.pool,
		q.Get("id"), q.Get("postnr"), q.Get("kommunekode"), page.limit(), page.offset())
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeHistorikList(w, items)
}

func (s *server) historikAdresser(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePaging(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	items, err := dawa.ListHistorikAdresser(s.ctx(r), s.pool,
		q.Get("id"), q.Get("postnr"), q.Get("kommunekode"), page.limit(), page.offset())
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeHistorikList(w, items)
}

// writeHistorikList renders with the collection streaming serializer (live
// historik bodies use it, incl. the "[\n\n]" empty form).
func writeHistorikList[T any](w http.ResponseWriter, items []T) {
	b, err := dawa.MarshalDAWAList(items)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}
