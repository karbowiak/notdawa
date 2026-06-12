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
	"bytes"
	"encoding/json"
	"fmt"
	"math"
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
	// struktur=flad: not in the captured client traffic, but the user requested it
	// alongside mini. flad is a wide flat projection of the (byte-verified) nestet
	// object; the only diffs vs DAWA are the documented source-data limitations
	// (frozen DAR historik timestamps, null tekstretning/højde, sub-mm etrs89 float
	// drift). compareFlad enforces identical key set+order and value equality on
	// every OTHER field — including brofast, now computed from the brofasthed seed.
	// Bornholm is included so the island brofast=false path is asserted byte-exact.
	t.Run("flad", func(t *testing.T) {
		for _, path := range []string{
			"/adgangsadresser/00000667-2566-47c9-9ba0-f5ec6b8ce50f?struktur=flad",   // Odense, mainland
			"/adgangsadresser/00038978-be22-4dec-bb57-34e022021549?struktur=flad",   // Bornholm (island)
			"/adresser/000021c5-e9ee-411d-b2d8-ec9161780ccd?struktur=flad",          // Stubbekøbing, full etage/dør
			"/adresser/0a3f50c3-6f6d-32b8-e044-0003ba298018?struktur=flad",          // the captured address
		} {
			path := path
			t.Run(label(path), func(t *testing.T) { compareFladSingle(t, path) })
		}
		for _, path := range []string{
			"/adgangsadresser?per_side=5&struktur=flad",
			"/adresser?per_side=5&struktur=flad",
		} {
			path := path
			t.Run(label(path), func(t *testing.T) { compareFladCollection(t, path) })
		}
	})
}

// fladTolerated holds the flad keys whose value may differ from DAWA without
// being a bug. Since the DAR bitemporal ingest (2026-06-12) the oprettet/
// ikrafttrædelse timestamps DERIVE from the virkning chain and are STRICT;
// only ændret remains tolerated (live's value is DAWA's own event clock —
// proven 2 s off DAR on the same event), plus the elevation/bearing we cannot
// derive (served null). etrs89 coords are handled separately (sub-mm float
// tolerance). Every other key must match exactly.
var fladTolerated = map[string]bool{
	"ændret": true, "adgangsadresse_ændret": true,
	"tekstretning": true, "højde": true,
}

// fladDecayed holds the flat-projection twins of the jordstykke relation that
// live DAWA has degraded to null on its way to the 2026-07-01 shutdown (first
// observed 2026-06-11; the fields served real values on 2026-06-01). Same
// asymmetric rule as dawaDecayedDiff in compat_test.go: tolerated ONLY when the
// DAWA side is null and ours is not — if WE go null too the pair compares equal
// here, which is why TestDecayedFieldsStillServed pins our values separately.
var fladDecayed = map[string]bool{
	"ejerlavkode": true, "ejerlavnavn": true, "esrejendomsnr": true, "matrikelnr": true,
	"jordstykke_ejerlavkode": true, "jordstykke_ejerlavnavn": true,
	"jordstykke_esrejendomsnr": true, "jordstykke_matrikelnr": true,
}

// compareFladObject asserts ours/dawa flad objects have the SAME keys in the SAME
// order and equal values on every non-tolerated field. etrs89koordinat_* may drift
// sub-mm (shared-source float last-bits). Returns the list of real diffs plus the
// number of DAWA-decay skips (dawa-side null on a fladDecayed key).
func compareFladObject(prefix string, ov, dv any) ([]diff, int) {
	om, ook := ov.(map[string]any)
	dm, dok := dv.(map[string]any)
	if !ook || !dok {
		return []diff{{prefix, kind(ov), kind(dv)}}, 0
	}
	var diffs []diff
	decayed := 0
	ok, dk := sortedKeys(om), sortedKeys(dm)
	if strings.Join(ok, ",") != strings.Join(dk, ",") {
		diffs = append(diffs, diff{prefix + " <keyset>", strings.Join(ok, ","), strings.Join(dk, ",")})
	}
	for _, k := range dk {
		if fladTolerated[k] {
			continue
		}
		ovv, dvv := om[k], dm[k]
		if fladDecayed[k] && dvv == nil && ovv != nil {
			decayed++
			continue
		}
		if strings.HasPrefix(k, "etrs89koordinat_") {
			of, ofok := toFloat(ovv)
			df, dfok := toFloat(dvv)
			if ofok && dfok && math.Abs(of-df) < 0.001 {
				continue
			}
		}
		var sub []diff
		compareJSON(prefix+k, ovv, dvv, &sub)
		diffs = append(diffs, sub...)
	}
	return diffs, decayed
}

// sortedKeys returns a decoded object's member keys sorted — a SET check (Go maps
// lose order). KEY ORDER is checked separately on the raw bytes (rawObjectKeys).
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// compareFladSingle fetches a single flad resource from both sides and asserts the
// key SET + ORDER are identical and all non-tolerated values match.
func compareFladSingle(t *testing.T, path string) {
	t.Helper()
	oStatus, oBody := fetch(t, ourBase+path)
	dStatus, dBody := fetch(t, liveDAWA+path)
	if oStatus != dStatus {
		t.Errorf("STATUS mismatch %s ours=%d dawa=%d", path, oStatus, dStatus)
		return
	}
	if oks, dks := rawObjectKeys(oBody), rawObjectKeys(dBody); strings.Join(oks, ",") != strings.Join(dks, ",") {
		t.Errorf("flad %s: KEY ORDER/SET mismatch\n  ours=%v\n  dawa=%v", path, oks, dks)
		return
	}
	ov, _ := decodeJSON(oBody)
	dv, _ := decodeJSON(dBody)
	diffs, decayed := compareFladObject("", ov, dv)
	reportFladDiffs(t, path, diffs, decayed)
}

// compareFladCollection compares a flad collection page: same length, same id
// order, and each element compared with compareFladObject.
func compareFladCollection(t *testing.T, path string) {
	t.Helper()
	_, oBody := fetch(t, ourBase+path)
	_, dBody := fetch(t, liveDAWA+path)
	ov, _ := decodeJSON(oBody)
	dv, _ := decodeJSON(dBody)
	oArr, ook := ov.([]any)
	dArr, dok := dv.([]any)
	if !ook || !dok {
		t.Errorf("flad %s: expected arrays (ours=%v dawa=%v)", path, ook, dok)
		return
	}
	if len(oArr) != len(dArr) {
		t.Errorf("flad %s: COUNT mismatch ours=%d dawa=%d", path, len(oArr), len(dArr))
		return
	}
	var diffs []diff
	decayed := 0
	for i := range dArr {
		d, dec := compareFladObject(fmt.Sprintf("[%d].", i), oArr[i], dArr[i])
		diffs = append(diffs, d...)
		decayed += dec
	}
	reportFladDiffs(t, path, diffs, decayed)
}

func reportFladDiffs(t *testing.T, path string, diffs []diff, decayed int) {
	t.Helper()
	if decayed > 0 {
		t.Logf("flad %s: %d tolerated DAWA-DECAY field(s) (upstream nulled the jordstykke relation while dying; ours serves the real values — see fladDecayed)", path, decayed)
	}
	if len(diffs) == 0 {
		t.Logf("flad %s: matches DAWA (key set+order identical; tolerated: historik ts, tekstretning/højde null, etrs89 sub-mm)", path)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "flad %s: %d real diff(s) (beyond tolerated source-data fields)", path, len(diffs))
	for i, d := range diffs {
		if i >= maxDiffsPerEndpoint {
			break
		}
		fmt.Fprintf(&b, "\n  %s: ours=%s dawa=%s", d.path, d.ours, d.dawa)
	}
	t.Error(b.String())
}

// rawObjectKeys returns a JSON object's member keys in source (emit) order.
func rawObjectKeys(body []byte) []string {
	dec := json.NewDecoder(bytes.NewReader(body))
	if _, err := dec.Token(); err != nil { // opening {
		return nil
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return keys
		}
		if k, ok := tok.(string); ok {
			keys = append(keys, k)
			var skip json.RawMessage
			_ = dec.Decode(&skip)
		}
	}
	return keys
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

// TestDecayedFieldsStillServed pins OUR values for the fields live DAWA has
// degraded to null (the decay tolerated by dawaDecayedDiff/fladDecayed). The
// decay tolerance is asymmetric, but once our own jordstykke join broke too the
// two nulls would compare EQUAL and no diff would ever surface — this pin is
// the regression guard that keeps our side honest. The expected values were
// byte-verified against HEALTHY live DAWA on 2026-06-01 (suite fully green).
// Purely ours-vs-pin: no live DAWA involved. If the pinned address is ever
// decommissioned in DAR, update the pin from another address with a jordstykke.
func TestDecayedFieldsStillServed(t *testing.T) {
	if ourBase == "" {
		t.Skip("no DB — server not started (set DATABASE_URL)")
	}
	status, body := fetch(t, ourBase+"/adgangsadresser/00000667-2566-47c9-9ba0-f5ec6b8ce50f?struktur=flad")
	if status != 200 {
		t.Fatalf("pinned address fetch: status %d: %s", status, snippet(body))
	}
	v, err := decodeJSON(body)
	if err != nil {
		t.Fatalf("pinned address: %v", err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("pinned address: not an object")
	}
	want := map[string]string{
		"ejerlavkode":              "350455",
		"ejerlavnavn":              "Hollufgård Hgd., Fraugde",
		"esrejendomsnr":            "0",
		"matrikelnr":               "1f",
		"jordstykke_ejerlavkode":   "350455",
		"jordstykke_ejerlavnavn":   "Hollufgård Hgd., Fraugde",
		"jordstykke_esrejendomsnr": "0",
		"jordstykke_matrikelnr":    "1f",
	}
	for k, w := range want {
		got, present := m[k]
		if !present || got == nil || fmt.Sprint(got) != w {
			t.Errorf("decayed-field pin %s: ours=%v want=%s (our jordstykke join regressed?)", k, got, w)
		}
	}
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
