package api

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/karbowiak/notdawa/internal/dawa"
)

// stednavne_handlers.go adds the server methods for the resources built in this
// wave, keeping internal/api/server.go untouched:
//
//   - legacy /stednavne (list + /{id} single + /autocomplete): DAWA's OLDER
//     stednavn resource (flat {id,hovedtype,undertype,navn,navnestatus,ændret,
//     geo_ændret,geo_version,href,egenskaber,visueltcenter,kommuner}), distinct
//     from the already-served /stednavne2.
//   - /stednavne2/{sted_id}/{navn} byKey (composite key).
//   - /supplerendebynavne2/{dagi_id} byKey + /supplerendebynavne2/reverse (ST_Covers).
//   - /steder and /stednavne2 reverse-via-QUERY on the collection: ?x=&y=
//     (ST_Covers containment) and ?nærmeste=true / ?naermeste=true (KNN nearest,
//     LIMIT 1). polygon/cirkel/bbox geomWithin already flow through the existing
//     list handlers' cp.listFilter() pipeline, so the new collection handlers only
//     add the point-containment and nearest branches and otherwise delegate to the
//     existing listSteder / listStednavne2.
//
// These are wired by adding/repointing routes in server.go's routes(); see the
// final build message for the exact mux.HandleFunc lines.

// ---- legacy /stednavne ----

// listLegacyStednavne serves the deprecated /stednavne collection (legacy flat
// shape) over ds_steder + ds_stednavne, ordered by id_lokalId. The cross-cutting
// collection pipeline (q/filter/struktur/format/paging) applies via
// writePagedCollection.
func (s *server) listLegacyStednavne(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	items, err := dawa.ListLegacyStednavne(s.ctx(r), s.pool, s.baseURL, cp.page.limit(), cp.page.offset())
	if err != nil {
		writeServerError(w, err)
		return
	}
	writePagedCollection(w, cp, items)
}

// getLegacyStednavn serves /stednavne/{id} (legacy single), keyed by the place's
// id_lokalId.
func (s *server) getLegacyStednavn(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := dawa.GetLegacyStednavn(s.ctx(r), s.pool, id, s.baseURL)
	finishGet(w, v, err, map[string]any{"id": id})
}

// autocompleteLegacyStednavne serves /stednavne/autocomplete (legacy
// {tekst, stednavn} elements).
func (s *server) autocompleteLegacyStednavne(w http.ResponseWriter, r *http.Request) {
	a, ok := parseAuto(w, r)
	if !ok {
		return
	}
	items, err := dawa.AutocompleteLegacyStednavne(s.ctx(r), s.pool, a.q, s.baseURL, a.perSide, a.offset)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeList(w, items)
}

// ---- stednavne2 byKey ----

// getStednavn2 serves /stednavne2/{sted_id}/{navn} (composite key). The {navn}
// path segment is percent-decoded before the lookup, so e.g. "Anneberg%20Skov"
// resolves "Anneberg Skov". The body matches the /stednavne2 list element.
func (s *server) getStednavn2(w http.ResponseWriter, r *http.Request) {
	stedID := r.PathValue("sted_id")
	navnRaw := r.PathValue("navn")
	navn, err := url.PathUnescape(navnRaw)
	if err != nil {
		navn = navnRaw
	}
	v, err := dawa.GetStednavn2(s.ctx(r), s.pool, stedID, navn, s.baseURL)
	finishGet(w, v, err, map[string]any{"sted_id": stedID, "navn": navn})
}

// ---- supplerendebynavne v1 (deprecated) collection ----

// listSupplerendebynavneV1 serves the deprecated /supplerendebynavne (v1)
// collection — a SIMPLER shape than /supplerendebynavne2: {href, navn,
// postnumre[], kommuner[]}, ordered alphabetically by navn, with href pointing at
// the v2 resource (matching the live API). It has no bbox/dagi_id/visueltcenter/
// metadata v2 fields. The cross-cutting paging/struktur/format pipeline applies
// via writePagedCollection; the v1 SQL does not support the per-field/q/spatial
// filters (DAWA's v1 surface is effectively read-only), so those are not pushed.
func (s *server) listSupplerendebynavneV1(w http.ResponseWriter, r *http.Request) {
	cp, ok := parseCollectionParams(w, r)
	if !ok {
		return
	}
	items, err := dawa.ListSupplerendebynavneV1(s.ctx(r), s.pool, s.baseURL, cp.page.limit(), cp.page.offset())
	if err != nil {
		writeServerError(w, err)
		return
	}
	writePagedCollection(w, cp, items)
}

// ---- supplerendebynavne2 byKey + reverse ----

// getSupplerendebynavn serves /supplerendebynavne2/{dagi_id} (byKey). The
// dagi_id path segment is an integer in DAWA; a non-integer (e.g. a navn, which
// the v1 collection's href produces) fails the path-pattern check upstream and
// returns a 404 ResourcePathFormatError with details [["dagi_id","notInteger"]].
// We reproduce that envelope before touching the DB so the byte-output matches.
func (s *server) getSupplerendebynavn(w http.ResponseWriter, r *http.Request) {
	dagiID := r.PathValue("dagi_id")
	if !isAllDigits(dagiID) {
		writePathFormatError(w, "dagi_id", "notInteger")
		return
	}
	v, err := dawa.GetSupplerendeBynavn(s.ctx(r), s.pool, dagiID, s.baseURL)
	finishGet(w, v, err, map[string]any{"dagi_id": dagiID})
}

// isAllDigits reports whether s is non-empty and made up solely of ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// reverseSupplerendebynavn serves /supplerendebynavne2/reverse?x=&y= (ST_Covers
// point-in-polygon; full GET-by-id body, DAWA 404 when no bynavn covers the
// point).
func (s *server) reverseSupplerendebynavn(w http.ResponseWriter, r *http.Request) {
	x, y, ok := parseXY(w, r)
	if !ok {
		return
	}
	v, err := dawa.ReverseSupplerendeBynavn(s.ctx(r), s.pool, x, y, s.baseURL)
	finishReverse(w, v, err)
}

// ---- steder / stednavne2 reverse-via-query ----

// nearestRequested reports whether the request asks for the nearest place: the
// nærmeste/naermeste query param present with a truthy value ("", "true" or "1"),
// matching DAWA's ?nærmeste=true semantics (Danish key + ASCII alias).
func nearestRequested(r *http.Request) bool {
	q := r.URL.Query()
	for _, key := range []string{"nærmeste", "naermeste"} {
		if v, present := q[key]; present {
			val := ""
			if len(v) > 0 {
				val = strings.ToLower(strings.TrimSpace(v[0]))
			}
			if val == "" || val == "true" || val == "1" {
				return true
			}
		}
	}
	return false
}

// listSteder2 is the reverse-via-query-aware /steder collection handler. With
// ?nærmeste=true&x=&y= it returns the single nearest place (KNN LIMIT 1); with
// ?x=&y= (no nærmeste) it returns the places COVERING the point (ST_Covers);
// otherwise it delegates to the standard listSteder (paging + filter/q/struktur/
// format + polygon/cirkel/bbox geomWithin).
func (s *server) listSteder2(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	hasXY := q.Get("x") != "" && q.Get("y") != ""
	if nearestRequested(r) || hasXY {
		x, y, ok := parseXY(w, r)
		if !ok {
			return
		}
		cp, ok := parseCollectionParams(w, r)
		if !ok {
			return
		}
		var (
			items []*dawa.Sted
			err   error
		)
		if nearestRequested(r) {
			items, err = dawa.NearestSted(s.ctx(r), s.pool, x, y, s.baseURL)
		} else {
			items, err = dawa.ListStederCoveringPoint(s.ctx(r), s.pool, x, y, s.baseURL)
		}
		if err != nil {
			writeServerError(w, err)
			return
		}
		// writePagedCollection (not writeCollection): the spatial result is the
		// final set, so it must NOT be re-filtered by the in-memory per-field filter
		// pipeline — which would treat nærmeste/x/y as unknown fields and drop every
		// row. struktur/format still apply.
		writePagedCollection(w, cp, items)
		return
	}
	s.listSteder(w, r)
}

// listStednavne2WithReverse is the reverse-via-query-aware /stednavne2 collection
// handler, mirroring listSteder2 over the stednavn2 representation.
func (s *server) listStednavne2WithReverse(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	hasXY := q.Get("x") != "" && q.Get("y") != ""
	if nearestRequested(r) || hasXY {
		x, y, ok := parseXY(w, r)
		if !ok {
			return
		}
		cp, ok := parseCollectionParams(w, r)
		if !ok {
			return
		}
		var (
			items []*dawa.Stednavn2
			err   error
		)
		if nearestRequested(r) {
			items, err = dawa.NearestStednavne2(s.ctx(r), s.pool, x, y, s.baseURL)
		} else {
			items, err = dawa.ListStednavne2CoveringPoint(s.ctx(r), s.pool, x, y, s.baseURL)
		}
		if err != nil {
			writeServerError(w, err)
			return
		}
		// writePagedCollection (not writeCollection): the spatial result is final and
		// must not be re-filtered by the per-field pipeline (which would drop every
		// row on the unknown nærmeste/x/y "fields"). struktur/format still apply.
		writePagedCollection(w, cp, items)
		return
	}
	s.listStednavne2(w, r)
}
