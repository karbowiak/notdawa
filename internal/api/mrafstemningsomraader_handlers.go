package api

import (
	"net/http"

	"github.com/karbowiak/notdawa/internal/dawa"
)

// mrafstemningsomraader_handlers.go serves DAWA's
// /menighedsraadsafstemningsomraader resource family (list, single by composite
// (kommunekode, nummer), autocomplete, reverse). The handlers mirror the
// afstemningsomraader handlers in server.go: collection params + writeCollection
// for the list, numStr + finishGet for the single, parseAuto + writeList for
// autocomplete, parseXY + finishReverse for reverse. The dawa representation
// (internal/dawa/mrafstemningsomraader.go) controls the byte-exact shape.

// listMrafstemningsomraader serves GET /menighedsraadsafstemningsomraader.
func (s *server) listMrafstemningsomraader(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	items, err := dawa.ListMrafstemningsomraader(s.ctx(r), s.pool, s.baseURL)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeCollection(w, cp, items)
}

// getMrafstemningsomraade serves GET
// /menighedsraadsafstemningsomraader/{kommunekode}/{nummer}. The single path key
// is the composite (kommunekode, nummer): kommunekode is validated numeric and
// nummer is matched as an integer, then delegated to the composite getter.
func (s *server) getMrafstemningsomraade(w http.ResponseWriter, r *http.Request) {
	kommunekode, ok := numStr(w, r, "kommunekode")
	if !ok {
		return
	}
	nummer, ok := numStr(w, r, "nummer")
	if !ok {
		return
	}
	v, err := dawa.GetMrafstemningsomraadeByKommuneNummer(s.ctx(r), s.pool, kommunekode, nummer, s.baseURL)
	finishGet(w, v, err, map[string]any{"kommunekode": kommunekode, "nummer": nummer})
}

// autocompleteMrafstemningsomraader serves GET
// /menighedsraadsafstemningsomraader/autocomplete.
func (s *server) autocompleteMrafstemningsomraader(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteMrafstemningsomraader(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

// reverseMrafstemningsomraade serves GET
// /menighedsraadsafstemningsomraader/reverse?x=&y=. DAWA returns the SINGLE area
// containing the point as one bare object (like the other DAGI families' reverse),
// so this delegates to the singular ReverseMrafstemningsomraade + finishReverse
// (which renders the object on success and DAWA's 404 on pgx.ErrNoRows).
func (s *server) reverseMrafstemningsomraade(w http.ResponseWriter, r *http.Request) {
	x, y, ok := parseXY(w, r)
	if !ok {
		return
	}
	v, err := dawa.ReverseMrafstemningsomraade(s.ctx(r), s.pool, x, y, s.baseURL)
	finishReverse(w, v, err)
}
