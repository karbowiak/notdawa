// query.go implements DAWA's cross-cutting collection query parameters as a
// reusable pipeline layered on top of the existing per-resource List handlers.
//
// The pipeline operates on the already-DAWA-marshalled per-item bytes so the
// default json/nestet output stays byte-for-byte identical to the goldens. Each
// item is parsed into an *ordered* JSON value (ordObj preserves key order, which
// json.Unmarshal into a map would lose), the requested transforms are applied,
// and the result is re-emitted either as a DAWA array (json), one object per
// line (ndjson), wrapped in a callback (jsonp), compact (noformat) or flattened
// (csv).
//
// Supported here (collection handlers):
//   - per-field filters: equality on top-level scalar representation fields
//     (e.g. kommunekode, regionskode, kode, nr, navn, nuts3, nummer, bogstav).
//     Code-like values are compared with leading zeros normalised so
//     ?kommunekode=101 matches "0101".
//   - q=: free-text, case- and diacritic-insensitive substring match across the
//     item's scalar string fields (reuses dawa.MatchQ/foldString semantics).
//   - struktur=nestet|flad|mini: nestet is the default (unchanged bytes); mini
//     reduces to {href? , kode/nr/id, navn}; flad drops nested ref objects/arrays
//     and inlines their scalars as dotted columns.
//   - format=json|ndjson|jsonp|csv ; callback=NAME (implies jsonp);
//     noformat (compact, single line).
//
// srid= reprojection and the spatial filters (polygon/cirkel/bbox) are parsed
// here (parseSpatial/listFilter) and threaded, together with the per-field
// filters and q=, into the dawa-layer List*Filtered SQL queries for the large
// tables (vejstykker, vejnavne, navngivneveje, adgangsadresser, adresser,
// jordstykker, steder, stednavne2, bebyggelser, supplerendebynavne). The small
// code-list resources keep using the in-memory writeCollection pipeline.
package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/karbowiak/notdawa/internal/dawa"
)

// reservedParams are the query parameters the pipeline interprets itself; every
// other parameter is treated as a per-field equality filter.
var reservedParams = map[string]bool{
	"side":     true,
	"per_side": true,
	"format":   true,
	"struktur": true,
	"callback": true,
	"noformat": true,
	"q":        true,
	"fuzzy":    true,
	"srid":     true,
	"polygon":  true,
	"cirkel":   true,
	"bbox":     true,
	"x":        true,
	"y":        true,
}

// collectionParams holds the parsed cross-cutting parameters for a collection
// request.
type collectionParams struct {
	page     paging
	filters  map[string]string // per-field equality filters (name → value)
	q        string
	struktur string // "nestet" (default), "flad", "mini"
	format   string // "json" (default), "ndjson", "jsonp", "csv"
	callback string // jsonp callback name (non-empty ⇒ format jsonp)
	noformat bool
	srid     int                 // output CRS (0 = DAWA default 4326)
	spatial  *dawa.SpatialFilter // polygon/cirkel/bbox filter (nil = none)
}

// parseCollectionParams parses pagination plus all cross-cutting parameters.
// On a malformed value it writes the DAWA 400 envelope and returns ok=false.
func parseCollectionParams(w http.ResponseWriter, r *http.Request) (collectionParams, bool) {
	page, ok := parsePaging(w, r)
	if !ok {
		return collectionParams{}, false
	}
	q := r.URL.Query()

	cp := collectionParams{
		page:     page,
		filters:  map[string]string{},
		q:        q.Get("q"),
		struktur: q.Get("struktur"),
		callback: q.Get("callback"),
	}
	if cp.q == "" {
		cp.q = q.Get("fuzzy") // fuzzy is treated as q here (both free-text)
	}

	switch cp.struktur {
	case "", "nestet", "flad", "mini":
	default:
		writeBadRequest(w, "struktur", cp.struktur)
		return collectionParams{}, false
	}
	if cp.struktur == "" {
		cp.struktur = "nestet"
	}

	cp.format = q.Get("format")
	switch cp.format {
	case "", "json", "ndjson", "jsonp", "csv":
	default:
		writeBadRequest(w, "format", cp.format)
		return collectionParams{}, false
	}
	if cp.format == "" {
		cp.format = "json"
	}
	if cp.callback != "" {
		cp.format = "jsonp"
	}
	if _, ok := q["noformat"]; ok {
		cp.noformat = true
	}

	// srid: output CRS for coordinate arrays/bbox. Empty means DAWA default (4326).
	if v := q.Get("srid"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeBadRequest(w, "srid", v)
			return collectionParams{}, false
		}
		cp.srid = n
	}

	// Spatial filter: at most one of bbox/cirkel/polygon. The filter coordinates
	// are interpreted in the requested srid (DAWA default 4326).
	sf, sok := parseSpatial(w, q, cp.srid)
	if !sok {
		return collectionParams{}, false
	}
	cp.spatial = sf

	for name, vals := range q {
		if reservedParams[name] || len(vals) == 0 {
			continue
		}
		cp.filters[name] = vals[0]
	}
	return cp, true
}

// parseSpatial reads at most one of bbox=/cirkel=/polygon= into a
// *dawa.SpatialFilter. Returns (nil, true) when none are present, and writes the
// DAWA 400 envelope + (nil, false) on a malformed value. srid is the requested
// output srid, used as the filter-coordinate CRS (DAWA convention).
func parseSpatial(w http.ResponseWriter, q url.Values, srid int) (*dawa.SpatialFilter, bool) {
	if v := q.Get("bbox"); v != "" {
		parts := strings.Split(v, ",")
		if len(parts) != 4 {
			writeBadRequest(w, "bbox", v)
			return nil, false
		}
		var bb [4]float64
		for i, p := range parts {
			f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
			if err != nil {
				writeBadRequest(w, "bbox", v)
				return nil, false
			}
			bb[i] = f
		}
		return &dawa.SpatialFilter{Kind: "bbox", SRID: srid, Bbox: bb}, true
	}
	if v := q.Get("cirkel"); v != "" {
		parts := strings.Split(v, ",")
		if len(parts) != 3 {
			writeBadRequest(w, "cirkel", v)
			return nil, false
		}
		var c [3]float64
		for i, p := range parts {
			f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
			if err != nil {
				writeBadRequest(w, "cirkel", v)
				return nil, false
			}
			c[i] = f
		}
		return &dawa.SpatialFilter{Kind: "cirkel", SRID: srid, Cirkel: c}, true
	}
	if v := q.Get("polygon"); v != "" {
		ring, err := parsePolygon(v)
		if err != nil {
			writeBadRequest(w, "polygon", v)
			return nil, false
		}
		return &dawa.SpatialFilter{Kind: "polygon", SRID: srid, Polygon: ring}, true
	}
	return nil, true
}

// parsePolygon decodes DAWA's polygon= JSON: [[[x,y],[x,y],...]] (an array of
// rings) where only the FIRST (outer) ring is used. A bare single ring
// [[x,y],...] is also accepted. The ring is closed (first==last) when not already.
func parsePolygon(v string) ([][2]float64, error) {
	var rings [][][2]float64
	if err := json.Unmarshal([]byte(v), &rings); err == nil && len(rings) > 0 {
		if len(rings[0]) < 3 {
			return nil, errBadPolygon
		}
		return closeRing(rings[0]), nil
	}
	var ring [][2]float64
	if err := json.Unmarshal([]byte(v), &ring); err != nil {
		return nil, err
	}
	if len(ring) < 3 {
		return nil, errBadPolygon
	}
	return closeRing(ring), nil
}

// errBadPolygon is returned for a polygon ring with too few points.
var errBadPolygon = errPolygon{}

type errPolygon struct{}

func (errPolygon) Error() string { return "polygon ring too short" }

// closeRing appends the first point to the end when the ring is not closed, so
// ST_GeomFromText('POLYGON((...))') accepts it.
func closeRing(ring [][2]float64) [][2]float64 {
	if len(ring) == 0 {
		return ring
	}
	first, last := ring[0], ring[len(ring)-1]
	if first != last {
		ring = append(ring, first)
	}
	return ring
}

// listFilter builds the dawa.ListFilter the SQL-side List*Filtered functions
// consume from the parsed collection parameters.
func (cp collectionParams) listFilter() dawa.ListFilter {
	return dawa.ListFilter{
		Filters: cp.filters,
		Q:       cp.q,
		SRID:    cp.srid,
		Spatial: cp.spatial,
	}
}

// writeCollection runs the full cross-cutting pipeline over items (a typed
// slice of *T) and writes the response. items are the FULL result for the
// resource (pre-pagination); the pipeline filters, applies q, projects the
// struktur, paginates and renders the requested format.
func writeCollection[T any](w http.ResponseWriter, cp collectionParams, items []T) {
	// Marshal each item to its DAWA bytes, then parse to an ordered value so the
	// default nestet/json path round-trips byte-for-byte.
	objs := make([]*ordObj, 0, len(items))
	for _, it := range items {
		b, err := dawa.MarshalDAWA(it)
		if err != nil {
			writeServerError(w, err)
			return
		}
		o, err := parseOrdObj(b)
		if err != nil {
			writeServerError(w, err)
			return
		}
		objs = append(objs, o)
	}

	// Per-field filters.
	if len(cp.filters) > 0 {
		filtered := objs[:0:0]
		for _, o := range objs {
			if o.matchesFilters(cp.filters) {
				filtered = append(filtered, o)
			}
		}
		objs = filtered
	}

	// Free-text q.
	if cp.q != "" {
		matched := objs[:0:0]
		for _, o := range objs {
			if o.matchesQ(cp.q) {
				matched = append(matched, o)
			}
		}
		objs = matched
	}

	// Struktur projection.
	if cp.struktur != "nestet" {
		for _, o := range objs {
			o.project(cp.struktur)
		}
	}

	// Pagination (slice the projected/filtered list).
	objs = paginate(objs, cp.page.side, cp.page.perSide)

	renderCollection(w, cp, objs)
}

// writePagedCollection renders an already-DB-paginated slice. It applies
// struktur and format (pure presentation transforms that are safe on a page)
// but deliberately skips per-field filters, q and re-pagination, which for a
// production-scale table must be pushed into the SQL query instead. Used by the
// adgangsadresser/adresser collection handlers.
func writePagedCollection[T any](w http.ResponseWriter, cp collectionParams, items []T) {
	objs := make([]*ordObj, 0, len(items))
	for _, it := range items {
		b, err := dawa.MarshalDAWA(it)
		if err != nil {
			writeServerError(w, err)
			return
		}
		o, err := parseOrdObj(b)
		if err != nil {
			writeServerError(w, err)
			return
		}
		objs = append(objs, o)
	}
	if cp.struktur != "nestet" {
		for _, o := range objs {
			o.project(cp.struktur)
		}
	}
	renderCollection(w, cp, objs)
}

// renderCollection emits objs in the requested format.
func renderCollection(w http.ResponseWriter, cp collectionParams, objs []*ordObj) {
	switch cp.format {
	case "ndjson":
		var buf bytes.Buffer
		for _, o := range objs {
			buf.Write(o.marshal(false))
			buf.WriteByte('\n')
		}
		w.Header().Set("Content-Type", "application/x-ndjson; charset=UTF-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf.Bytes())
	case "csv":
		body := marshalCSV(objs)
		w.Header().Set("Content-Type", "text/csv; charset=UTF-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	case "jsonp":
		body := marshalArray(objs, !cp.noformat)
		name := cp.callback
		if name == "" {
			name = "callback"
		}
		out := append([]byte(name+"("), body...)
		out = append(out, ");"...)
		w.Header().Set("Content-Type", "application/javascript; charset=UTF-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
	default: // json
		body := marshalArray(objs, !cp.noformat)
		writeJSON(w, http.StatusOK, body)
	}
}

// marshalArray emits objs as a DAWA array. When pretty is true the DAWA array
// layout ("[\n", per-object 2-space indent, ", " separators, "\n]") is used,
// matching MarshalDAWAList; when false a compact single-line array is emitted
// (format=...&noformat).
func marshalArray(objs []*ordObj, pretty bool) []byte {
	if len(objs) == 0 {
		return []byte("[]")
	}
	parts := make([][]byte, len(objs))
	for i, o := range objs {
		parts[i] = o.marshal(pretty)
	}
	if !pretty {
		return append(append([]byte("["), bytes.Join(parts, []byte(","))...), ']')
	}
	out := append([]byte("[\n"), bytes.Join(parts, []byte(", "))...)
	return append(out, "\n]"...)
}

// ---- ordered JSON value ----

// ordObj is an order-preserving JSON object: keys holds the member order and
// vals the (raw, already-DAWA-marshalled) member values keyed by name. Nested
// objects/arrays are kept as raw json.RawMessage so the default path re-emits
// them verbatim. orig holds the exact source bytes the object was parsed from
// (trimmed of surrounding whitespace); it is returned verbatim by the pretty
// marshaller whenever the object has NOT been projected, guaranteeing the
// default/nestet path is byte-identical to MarshalDAWA(it). project() clears
// orig so a transformed object is re-serialised from keys/vals instead.
type ordObj struct {
	keys []string
	vals map[string]json.RawMessage
	orig []byte
}

// parseOrdObj decodes a single JSON object preserving member order.
func parseOrdObj(b []byte) (*ordObj, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	// opening "{"
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	o := &ordObj{vals: map[string]json.RawMessage{}, orig: bytes.TrimSpace(b)}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key := keyTok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		o.keys = append(o.keys, key)
		o.vals[key] = raw
	}
	return o, nil
}

// scalarString returns the value of key as a plain string when it is a JSON
// string or number (the DAWA scalar representation), and ok=false otherwise
// (object/array/bool/null).
func (o *ordObj) scalarString(key string) (string, bool) {
	raw, present := o.vals[key]
	if !present {
		return "", false
	}
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return "", false
	}
	switch t[0] {
	case '"':
		var s string
		if err := json.Unmarshal(t, &s); err != nil {
			return "", false
		}
		return s, true
	case '{', '[':
		return "", false
	default:
		s := string(t)
		if s == "null" || s == "true" || s == "false" {
			return "", false
		}
		return s, true
	}
}

// matchesFilters reports whether every filter (name=value) matches a scalar
// top-level field. Missing fields never match. Code-like numeric values are
// compared with leading zeros normalised (so kommunekode=101 matches "0101").
func (o *ordObj) matchesFilters(filters map[string]string) bool {
	for name, want := range filters {
		got, ok := o.scalarString(name)
		if !ok {
			return false
		}
		if !scalarEqual(got, want) {
			return false
		}
	}
	return true
}

// scalarEqual compares two scalar values for a per-field filter. When both look
// like integers they are compared numerically (normalising leading zeros);
// otherwise the comparison is exact.
func scalarEqual(got, want string) bool {
	if got == want {
		return true
	}
	gi, ge := strconv.Atoi(got)
	wi, we := strconv.Atoi(want)
	if ge == nil && we == nil {
		return gi == wi
	}
	return false
}

// matchesQ reports whether q (folded) is a substring of any scalar string field
// of the object (DAWA's free-text behaviour over the representation).
func (o *ordObj) matchesQ(q string) bool {
	for _, k := range o.keys {
		if s, ok := o.scalarString(k); ok {
			if dawa.MatchQ(s, q) {
				return true
			}
		}
	}
	return false
}

// project reduces the object to the requested struktur.
//
//	mini → href (if present), the identity field (kode|nr|id|nummer|bogstav|nuts3)
//	       and navn (if present), in that order.
//	flad → drop nested object/array members and inline a nested ref's scalar
//	       members as dotted keys ("region.kode", ...). Top-level scalars are
//	       kept in place.
func (o *ordObj) project(struktur string) {
	switch struktur {
	case "mini":
		o.projectMini()
	case "flad":
		o.projectFlad()
	}
	// The object's bytes no longer match the source, so the pretty marshaller
	// must rebuild from keys/vals rather than echo orig.
	o.orig = nil
}

var miniIdentityFields = []string{"kode", "nr", "id", "nummer", "bogstav", "nuts3"}

func (o *ordObj) projectMini() {
	keep := []string{}
	if _, ok := o.vals["href"]; ok {
		keep = append(keep, "href")
	}
	for _, idf := range miniIdentityFields {
		if _, ok := o.vals[idf]; ok {
			keep = append(keep, idf)
			break
		}
	}
	if _, ok := o.vals["navn"]; ok {
		keep = append(keep, "navn")
	}
	newVals := map[string]json.RawMessage{}
	newKeys := keep[:0:0]
	for _, k := range keep {
		newKeys = append(newKeys, k)
		newVals[k] = o.vals[k]
	}
	o.keys = newKeys
	o.vals = newVals
}

func (o *ordObj) projectFlad() {
	newKeys := []string{}
	newVals := map[string]json.RawMessage{}
	for _, k := range o.keys {
		raw := bytes.TrimSpace(o.vals[k])
		if len(raw) > 0 && raw[0] == '{' {
			// Inline a nested object's scalar members as dotted keys.
			child, err := parseOrdObj(raw)
			if err != nil {
				continue
			}
			for _, ck := range child.keys {
				if s, ok := child.scalarString(ck); ok {
					dk := k + "." + ck
					newKeys = append(newKeys, dk)
					b, _ := json.Marshal(s)
					newVals[dk] = b
				}
			}
			continue
		}
		if len(raw) > 0 && raw[0] == '[' {
			// Drop nested arrays in flad (DAWA replaces them with dotted scalars
			// of the first element where applicable; we conservatively drop them).
			continue
		}
		newKeys = append(newKeys, k)
		newVals[k] = o.vals[k]
	}
	o.keys = newKeys
	o.vals = newVals
}

// marshal re-emits the object. When pretty is true it uses DAWA's 2-space
// indentation (object members on their own lines); when false it is compact.
// For an unprojected object (orig still set) the pretty form is the source
// bytes verbatim, so the default/nestet/filter path stays byte-identical to
// MarshalDAWA.
func (o *ordObj) marshal(pretty bool) []byte {
	if pretty && o.orig != nil {
		return o.orig
	}
	if len(o.keys) == 0 {
		return []byte("{}")
	}
	var buf bytes.Buffer
	if pretty {
		buf.WriteString("{\n")
	} else {
		buf.WriteByte('{')
	}
	for i, k := range o.keys {
		kb, _ := json.Marshal(k)
		if pretty {
			buf.WriteString("  ")
			buf.Write(kb)
			buf.WriteString(": ")
			buf.Write(indentValue(o.vals[k]))
		} else {
			buf.Write(kb)
			buf.WriteByte(':')
			buf.Write(compactValue(o.vals[k]))
		}
		if i < len(o.keys)-1 {
			buf.WriteByte(',')
		}
		if pretty {
			buf.WriteByte('\n')
		}
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

// indentValue renders a raw member value with DAWA's 2-space indentation,
// matching MarshalDAWA so nested objects keep their byte-exact layout.
func indentValue(raw json.RawMessage) []byte {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return []byte("null")
	}
	if t[0] != '{' && t[0] != '[' {
		return t // scalar
	}
	var v any
	if err := json.Unmarshal(t, &v); err != nil {
		return t
	}
	b, err := dawa.MarshalDAWA(v)
	if err != nil {
		return t
	}
	// MarshalDAWA indents from column 0; re-indent every line after the first by
	// 2 spaces so it nests under the member key.
	lines := bytes.Split(b, []byte("\n"))
	for i := 1; i < len(lines); i++ {
		lines[i] = append([]byte("  "), lines[i]...)
	}
	return bytes.Join(lines, []byte("\n"))
}

// compactValue renders a raw member value compactly (no whitespace).
func compactValue(raw json.RawMessage) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return bytes.TrimSpace(raw)
	}
	return buf.Bytes()
}

// ---- CSV ----

// marshalCSV renders objs as a flattened CSV. Columns are the union of all
// flattened (dotted) scalar keys across the objects, ordered by first
// appearance (the representation's field order); nested objects contribute
// dotted columns and nested arrays are dropped. The first row is the header.
//
// NOTE: DAWA's exact CSV column set for each resource is resource-specific and
// not fully reproduced here; the column order is deterministic (first-seen) and
// the cell encoding follows RFC 4180 quoting.
func marshalCSV(objs []*ordObj) []byte {
	type cell map[string]string
	var columns []string
	seen := map[string]bool{}
	rows := make([]cell, 0, len(objs))
	for _, o := range objs {
		flat := o.flatten()
		row := cell{}
		for _, kv := range flat {
			if !seen[kv.key] {
				seen[kv.key] = true
				columns = append(columns, kv.key)
			}
			row[kv.key] = kv.val
		}
		rows = append(rows, row)
	}
	// Keep deterministic order: stable across runs even if first-seen order
	// already deterministic; sort only as a tie-break safety net is unnecessary.
	_ = sort.Strings
	var buf bytes.Buffer
	writeCSVRow(&buf, columns)
	for _, row := range rows {
		vals := make([]string, len(columns))
		for i, c := range columns {
			vals[i] = row[c]
		}
		writeCSVRow(&buf, vals)
	}
	return buf.Bytes()
}

type flatKV struct{ key, val string }

// flatten returns the object's scalar fields as ordered dotted key/value pairs.
func (o *ordObj) flatten() []flatKV {
	var out []flatKV
	for _, k := range o.keys {
		raw := bytes.TrimSpace(o.vals[k])
		if len(raw) == 0 {
			out = append(out, flatKV{k, ""})
			continue
		}
		switch raw[0] {
		case '{':
			child, err := parseOrdObj(raw)
			if err == nil {
				for _, ck := range child.flatten() {
					out = append(out, flatKV{k + "." + ck.key, ck.val})
				}
			}
		case '[':
			// Nested arrays are not expanded into CSV columns.
		default:
			out = append(out, flatKV{k, csvScalar(raw)})
		}
	}
	return out
}

// csvScalar converts a raw scalar JSON value to its CSV cell text. Strings are
// unquoted to their literal value; null becomes empty; numbers/booleans pass
// through verbatim.
func csvScalar(raw json.RawMessage) string {
	t := bytes.TrimSpace(raw)
	if string(t) == "null" {
		return ""
	}
	if len(t) > 0 && t[0] == '"' {
		var s string
		if err := json.Unmarshal(t, &s); err == nil {
			return s
		}
	}
	return string(t)
}

// writeCSVRow writes one RFC 4180 row (CRLF terminated, quoting cells that
// contain comma, quote or newline).
func writeCSVRow(buf *bytes.Buffer, fields []string) {
	for i, f := range fields {
		if i > 0 {
			buf.WriteByte(',')
		}
		if strings.ContainsAny(f, ",\"\n\r") {
			buf.WriteByte('"')
			buf.WriteString(strings.ReplaceAll(f, "\"", "\"\""))
			buf.WriteByte('"')
		} else {
			buf.WriteString(f)
		}
	}
	buf.WriteString("\r\n")
}
