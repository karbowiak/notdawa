// realqueries_test.go replays REAL autocomplete/lookup requests captured from a
// live client (an address-search-as-you-type box) against our server AND live
// DAWA, and compares them as closely as each endpoint allows. It is SEPARATE from
// compat_test.go on purpose: compat_test sweeps the API surface with synthetic
// inputs and only checks autocomplete SHAPE (key-paths), whereas this file pins
// the exact byte stream a real client receives and goes row-by-row, field-by-field
// on the captured queries — the level at which client-visible bugs actually live.
//
// Comparison ladder (per query):
//  1. BYTE-IDENTICAL — the strong bar; most fully/partly specified queries hit it.
//  2. Else decode and compare structurally:
//       - result TYPE set must match (vejnavn vs adgangsadresse vs adresse) — a
//         mismatch is an escalation bug, FAIL.
//       - rows paired by identity (data.id, else type+forslagstekst); every matched
//         pair must be field-for-field equal (modulo toleratedDiff) — FAIL otherwise.
//       - element COUNT must match — a mismatch means we return too few/many
//         (e.g. the aa↔å folding gap), FAIL.
//       - if counts+types+fields all agree but the row SET/ORDER differs AND the
//         page is saturated (len == per_side), that is DAWA's proprietary relevance
//         rank at the truncation boundary — the documented-unreproducible class, so
//         it is TOLERATED (logged, not failed). An unsaturated set with a membership
//         diff is a real miss and FAILS.
//
//	Run:  DATABASE_URL=postgres://notdawa:notdawa@localhost:5432/notdawa?sslmode=disable \
//	        go test ./tests/ -run TestRealQueries -v
package tests

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// capturedAutocomplete is the verbatim list of /autocomplete request targets seen
// in the client access log (URL-encoded exactly as the client sent them). The
// OPTIONS preflight and duplicate lines are dropped.
var capturedAutocomplete = []string{
	"/autocomplete?per_side=10&q=ko&type=adresse&caretpos=2&supplerendebynavn=true&stormodtagerpostnumre=true&multilinje=true&fuzzy=",
	"/autocomplete?q=ko",
	"/autocomplete?per_side=10&q=kongs&type=adresse&caretpos=5&supplerendebynavn=true&stormodtagerpostnumre=true&multilinje=true&fuzzy=",
	"/autocomplete?per_side=10&q=kongsk&type=adresse&caretpos=6&supplerendebynavn=true&stormodtagerpostnumre=true&multilinje=true&fuzzy=",
	"/autocomplete?per_side=10&q=kongsg&type=adresse&caretpos=6&supplerendebynavn=true&stormodtagerpostnumre=true&multilinje=true&fuzzy=",
	"/autocomplete?per_side=10&q=Kongsg%C3%A5rdsvej%20&type=adresse&caretpos=14&supplerendebynavn=true&stormodtagerpostnumre=true&multilinje=true&fuzzy=&startfra=adgangsadresse",
	"/autocomplete?per_side=10&q=Kongsg%C3%A5rdsvej%202&type=adresse&caretpos=15&supplerendebynavn=true&stormodtagerpostnumre=true&multilinje=true&fuzzy=",
	"/autocomplete?per_side=10&q=Kongsg%C3%A5rdsvej%2024&type=adresse&caretpos=16&supplerendebynavn=true&stormodtagerpostnumre=true&multilinje=true&fuzzy=",
	"/autocomplete?per_side=10&q=Kongsg%C3%A5rdsvej%2024%2C&type=adresse&caretpos=17&supplerendebynavn=true&stormodtagerpostnumre=true&multilinje=true&fuzzy=",
	"/autocomplete?per_side=10&q=Kongsg%C3%A5rdsvej%2024%2C%20s&type=adresse&caretpos=19&supplerendebynavn=true&stormodtagerpostnumre=true&multilinje=true&fuzzy=",
	"/autocomplete?per_side=10&q=Kongsg%C3%A5rdsvej%2024%2C%20st&type=adresse&caretpos=20&supplerendebynavn=true&stormodtagerpostnumre=true&multilinje=true&fuzzy=",
}

// capturedSingle is the verbatim list of single-resource lookups from the log: the
// same address fetched with struktur=mini and struktur=nestet.
var capturedSingle = []string{
	"/adresser/0a3f50c3-6f6d-32b8-e044-0003ba298018?struktur=nestet",
	"/adresser/0a3f50c3-6f6d-32b8-e044-0003ba298018?struktur=mini",
}

func TestRealQueries(t *testing.T) {
	requireDAWA(t)
	t.Run("autocomplete", func(t *testing.T) {
		for _, path := range capturedAutocomplete {
			path := path
			t.Run(label(path), func(t *testing.T) { compareCapturedAutocomplete(t, path) })
		}
	})
	t.Run("single", func(t *testing.T) {
		for _, path := range capturedSingle {
			path := path
			t.Run(label(path), func(t *testing.T) {
				oStatus, oBody := fetch(t, ourBase+path)
				dStatus, dBody := fetch(t, liveDAWA+path)
				compareStatusBody(t, path, oStatus, oBody, dStatus, dBody)
			})
		}
	})
}

// compareCapturedAutocomplete applies the comparison ladder described in the file
// header to one captured /autocomplete request.
func compareCapturedAutocomplete(t *testing.T, path string) {
	t.Helper()
	oStatus, oBody := fetch(t, ourBase+path)
	dStatus, dBody := fetch(t, liveDAWA+path)
	if oStatus != dStatus {
		t.Errorf("STATUS mismatch %s ours=%d dawa=%d", path, oStatus, dStatus)
		return
	}
	if string(oBody) == string(dBody) {
		return // byte-identical — the strong bar
	}
	ov, oerr := decodeJSON(oBody)
	dv, derr := decodeJSON(dBody)
	if oerr != nil || derr != nil {
		t.Errorf("non-JSON autocomplete body %s (ours=%v dawa=%v)", path, oerr, derr)
		return
	}
	oArr, ook := ov.([]any)
	dArr, dok := dv.([]any)
	if !ook || !dok {
		t.Errorf("autocomplete %s: expected arrays (ours=%v dawa=%v)", path, ook, dok)
		return
	}

	// Result TYPE set: vejnavn vs adgangsadresse vs adresse. A divergence is an
	// escalation bug (e.g. we descend to adresse where DAWA stops at adgangsadresse).
	oTypes, dTypes := typeSet(oArr), typeSet(dArr)
	if !strings.EqualFold(strings.Join(oTypes, ","), strings.Join(dTypes, ",")) {
		t.Errorf("autocomplete %s: TYPE-set mismatch — ours=%v dawa=%v (escalation divergence)", path, oTypes, dTypes)
		return
	}

	// Field-level: every row both sides return (paired by identity) must be equal.
	om := byKey(oArr)
	dm := byKey(dArr)
	var fieldDiffs []diff
	for k, oe := range om {
		de, ok := dm[k]
		if !ok {
			continue
		}
		var pair []diff
		compareJSON(k, oe, de, &pair)
		for _, d := range pair {
			if !toleratedDiff(path, d.path) {
				fieldDiffs = append(fieldDiffs, d)
			}
		}
	}
	if len(fieldDiffs) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "autocomplete %s: %d field diff(s) on matched rows", path, len(fieldDiffs))
		for i, d := range fieldDiffs {
			if i >= maxDiffsPerEndpoint {
				break
			}
			fmt.Fprintf(&b, "\n  %s: ours=%s dawa=%s", d.path, d.ours, d.dawa)
		}
		t.Error(b.String())
		return
	}

	// Count: a mismatch means we return too few/many rows (e.g. the aa↔å folding
	// gap drops the alternative-spelling matches).
	if len(oArr) != len(dArr) {
		t.Errorf("autocomplete %s: COUNT mismatch — ours=%d dawa=%d (missing/extra rows; matched rows agree)", path, len(oArr), len(dArr))
		return
	}

	// Same count, types, and field values, but a different row SET/ORDER. On a
	// saturated page (len == per_side) this is DAWA's proprietary relevance rank at
	// the truncation boundary — unreproducible from our schema, so it is tolerated.
	onlyOurs, onlyDawa := setDiff(om, dm)
	saturated := len(dArr) >= perSideOf(path)
	if len(onlyOurs) == 0 && len(onlyDawa) == 0 {
		t.Logf("autocomplete %s: rows match, only order differs (proprietary rank, tolerated)", path)
		return
	}
	if saturated {
		t.Logf("autocomplete %s: saturated page (%d rows); boundary set differs by %d/%d rows — proprietary rank, tolerated",
			path, len(dArr), len(onlyOurs), len(onlyDawa))
		return
	}
	t.Errorf("autocomplete %s: unsaturated SET mismatch (%d ours-only, %d dawa-only) — a real miss, not rank",
		path, len(onlyOurs), len(onlyDawa))
}

// typeSet returns the sorted distinct "type" values present in an element array.
func typeSet(arr []any) []string {
	seen := map[string]bool{}
	for _, e := range arr {
		if m, ok := e.(map[string]any); ok {
			if s, ok := m["type"].(string); ok {
				seen[s] = true
			}
		}
	}
	var out []string
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// autoRowKey is a stable identity for an autocomplete element so ours/dawa rows can
// be paired regardless of rank order: the address id when present (adgangsadresse/
// adresse), else type+forslagstekst (vejnavn rows carry no id).
func autoRowKey(e any) string {
	m, ok := e.(map[string]any)
	if !ok {
		return ""
	}
	typ, _ := m["type"].(string)
	if data, ok := m["data"].(map[string]any); ok {
		if id, ok := data["id"].(string); ok && id != "" {
			return "id:" + id
		}
	}
	fs, _ := m["forslagstekst"].(string)
	return typ + ":" + fs
}

func byKey(arr []any) map[string]any {
	out := make(map[string]any, len(arr))
	for _, e := range arr {
		out[autoRowKey(e)] = e
	}
	return out
}

func setDiff(om, dm map[string]any) (onlyOurs, onlyDawa []string) {
	for k := range om {
		if _, ok := dm[k]; !ok {
			onlyOurs = append(onlyOurs, k)
		}
	}
	for k := range dm {
		if _, ok := om[k]; !ok {
			onlyDawa = append(onlyDawa, k)
		}
	}
	return
}

// perSideOf extracts per_side from a request path, defaulting to 20 (DAWA's
// autocomplete cap, which parseAuto also uses when per_side is absent).
func perSideOf(path string) int {
	for _, kv := range strings.Split(pathQuery(path), "&") {
		if v, ok := strings.CutPrefix(kv, "per_side="); ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				return n
			}
		}
	}
	return 20
}

func pathQuery(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		return path[i+1:]
	}
	return ""
}

// label shortens a request path to a readable subtest name (q + the distinguishing
// params), so -run output is legible.
func label(path string) string {
	q := pathQuery(path)
	parts := []string{}
	for _, kv := range strings.Split(q, "&") {
		for _, keep := range []string{"q=", "type=", "struktur=", "startfra=", "per_side="} {
			if strings.HasPrefix(kv, keep) {
				parts = append(parts, kv)
			}
		}
	}
	s := strings.Join(parts, "&")
	if s == "" {
		s = path
	}
	return strings.NewReplacer("/", "_", "%", "", " ", "_").Replace(s)
}
