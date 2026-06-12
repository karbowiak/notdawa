// Package tests is notdawa's ONLY compatibility oracle: for every DAWA API
// endpoint it fetches the SAME path from our server AND from the live DAWA API
// (https://api.dataforsyningen.dk) and compares the two responses key-for-key.
// If they are not identical, the test FAILS and prints every differing JSON path
// with ours-vs-DAWA values.
//
// Why live-vs-live and nothing else: the previous suite compared our output to
// captured golden fixtures with a tolerance classifier WE controlled. That is
// self-grading — a frozen API that never updates would score 100% forever
// because it still matches its own fixtures. The only thing that cannot be gamed
// is the live upstream. DAWA shuts down 2026-07-01; until then it is the oracle.
//
// Tolerance policy: allowGeoDriftMeters starts at 0 (STRICT, byte-exact). Run it
// strict first to SEE every real divergence (timestamps, geo_version, bebyggelser
// membership, adgangspunkt.højde/tekstretning, ordering, …). Once a divergence is
// understood it is either fixed on our side or, for genuinely irreproducible GEOS
// reprojection drift, the geo dial is bumped — deliberately, in code review, not
// hidden behind a fixture.
//
//	Run:  DATABASE_URL=postgres://notdawa:notdawa@localhost:5432/notdawa?sslmode=disable \
//	        go test ./tests/ -run TestCompat -v
//
// Skips cleanly (exit 0, no tests) when the DB is unreachable; skips individual
// comparisons when live DAWA is unreachable.
package tests

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/karbowiak/notdawa/internal/api"
	"github.com/karbowiak/notdawa/internal/config"
	"github.com/karbowiak/notdawa/internal/db"
)

// liveDAWA is the upstream oracle. Our server is started with this exact base-url
// so that every `href` field is byte-comparable.
const liveDAWA = "https://api.dataforsyningen.dk"

// allowGeoDriftMeters is the ONLY tolerance dial. 0 = strict, byte-exact. Set to
// 10 (owner-authorised 2026-05-31) to tolerate GEOS pole-of-inaccessibility /
// reprojection drift on numeric geo leaves (visueltcenter/bbox/koordinater/
// adgangspunkt/vejpunkt): our PostGIS computation vs DAWA's differs by ~1–7 m on
// these (e.g. kommune visueltcenter ~1 m, ejerlav ~7 m) — not exactly reproducible
// and well within the project's "location within ~1%" tolerance. It is applied
// solely to numeric leaves whose JSON path is a geo path (see isGeoPath); it never
// relaxes a missing field, a null, or a non-geo value.
const allowGeoDriftMeters = 10.0

// maxDiffsPerEndpoint caps how many diffs we print per endpoint so a pure
// ordering mismatch on a big collection does not flood the log.
const maxDiffsPerEndpoint = 40

// userAgent is sent on every request; DAWA's gateway behaves better with a
// non-default UA.
const userAgent = "notdawa-compat-test/1.0"

var (
	ourBase string // httptest base URL of our in-process server
	// client forces HTTP/1.1 and a fresh connection per request: DAWA's gateway
	// resets pooled/HTTP-2 connections under rapid requests, which surfaces as
	// "unexpected EOF" mid-body. DisableKeepAlives + an empty TLSNextProto (which
	// disables HTTP/2 negotiation) makes the upstream comparison reliable.
	client = &http.Client{
		Timeout: 90 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives:   true,
			ForceAttemptHTTP2:   false,
			TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{},
			DialContext:         (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
			TLSHandshakeTimeout: 15 * time.Second,
		},
	}
)

func TestMain(m *testing.M) { os.Exit(run(m)) }

func run(m *testing.M) int {
	cfg := config.Load()
	pool, err := db.Connect(context.Background(), cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compat: no DB (%v) — skipping (set DATABASE_URL)\n", err)
		return 0
	}
	defer pool.Close()
	testPool = pool // direct DB access for sampling tests (historik_sample_test.go)
	// base-url MUST be the live DAWA host so our href fields match DAWA's.
	// The oracle runs against the HUMA server (the production front): this proves
	// Huma serves every route byte-exact vs live DAWA, with full param fidelity —
	// not just that a separate hand-rolled mux does.
	srv := httptest.NewServer(api.NewHumaServer(pool, liveDAWA))
	defer srv.Close()
	ourBase = srv.URL
	return m.Run()
}

// ---- endpoint catalogue: every DAWA documented family ----

type family struct {
	name    string
	path    string // collection base, e.g. "/kommuner"
	single  bool   // also compare one resource fetched by DAWA's own href
	autoQ   string // non-empty => compare /{path}/autocomplete?q=autoQ
	reverse bool   // compare /{path}/reverse?x=..&y=..
}

// revX, revY is a land point in central Copenhagen used for every reverse query.
const revX, revY = 12.5683, 55.6761

// families enumerates the DAWA API surface. Sub-endpoints that a family does not
// expose are left off (e.g. ejerlav/vejnavne have no reverse). tilknytninger are
// listed separately below because the live gateway 404s them.
var families = []family{
	// DAGI administrative areas
	{"regioner", "/regioner", true, "Hoved", true},
	{"kommuner", "/kommuner", true, "København", true},
	{"sogne", "/sogne", true, "Vor", true},
	{"postnumre", "/postnumre", true, "21", true},
	{"landsdele", "/landsdele", true, "Køben", true},
	{"storkredse", "/storkredse", true, "København", true},
	{"valglandsdele", "/valglandsdele", true, "Hoved", true},
	{"opstillingskredse", "/opstillingskredse", true, "Indre", true},
	{"retskredse", "/retskredse", true, "København", true},
	{"politikredse", "/politikredse", true, "København", true},
	{"afstemningsomraader", "/afstemningsomraader", true, "Indre", true},
	{"menighedsraadsafstemningsomraader", "/menighedsraadsafstemningsomraader", true, "Helligaand", true},
	// Matrikel / ejerlav
	{"ejerlav", "/ejerlav", true, "København", false},
	{"jordstykker", "/jordstykker", true, "København", true},
	// Roads
	{"vejstykker", "/vejstykker", true, "Vester", true},
	{"vejnavne", "/vejnavne", true, "Vester", false},
	{"navngivneveje", "/navngivneveje", true, "Vester", false},
	{"vejnavnpostnummerrelationer", "/vejnavnpostnummerrelationer", true, "Rådhuspladsen", false},
	// Place names / settlements / supplementary town names
	{"supplerendebynavne", "/supplerendebynavne", true, "Strand", false}, // v1 (deprecated); single surfaces missing opslag
	{"supplerendebynavne2", "/supplerendebynavne2", true, "Strand", true},
	{"steder", "/steder", true, "", false},
	{"stednavne", "/stednavne", true, "Anneberg", false}, // legacy stednavne
	{"stednavne2", "/stednavne2", true, "København", false},
	{"stednavntyper", "/stednavntyper", true, "", false},
	{"bebyggelser", "/bebyggelser", true, "", false},
	// Address core
	{"adgangsadresser", "/adgangsadresser", true, "Rådhuspladsen 1", true},
	{"adresser", "/adresser", true, "Rådhuspladsen 1", true},
}

// tilknytningPaths: DAWA has NO standalone /<area>tilknytninger endpoints. Per
// the docs (docs/dawa_reference/replikering-forældet.html) tilknytninger exist
// ONLY as replication entities in the DEPRECATED replikering-forældet API
// (#regionstilknytninger-udtræk / -hændelser, …). Our server invented these 12
// standalone routes; the live gateway 404s every one (verified 2026-05-31 on
// api.dataforsyningen.dk + dawa.aws.dk, browser UA, with/without params).
// TestTilknytningerMatchLiveDAWA asserts our status matches the upstream (it does
// not: ours 200, DAWA 404) so the divergence stays visible until we remove them.
var tilknytningPaths = []string{
	"/regionstilknytninger", "/kommunetilknytninger", "/sognetilknytninger",
	"/politikredstilknytninger", "/retskredstilknytninger", "/opstillingskredstilknytninger",
	"/postnummertilknytninger", "/zonetilknytninger", "/valglandsdelstilknytninger",
	"/storkredstilknytninger", "/jordstykketilknytninger", "/stednavntilknytninger",
}

// ---- the suite ----

func TestCompat(t *testing.T) {
	requireDAWA(t)
	for _, f := range families {
		f := f
		t.Run(f.name, func(t *testing.T) {
			t.Run("collection", func(t *testing.T) {
				compareEndpoint(t, f.path+"?side=1&per_side=5")
			})
			if f.single {
				t.Run("single", func(t *testing.T) { compareSingle(t, f) })
			}
			if f.autoQ != "" {
				t.Run("autocomplete", func(t *testing.T) {
					compareEndpoint(t, f.path+"/autocomplete?q="+url.QueryEscape(f.autoQ))
				})
			}
			if f.reverse {
				t.Run("reverse", func(t *testing.T) {
					compareEndpoint(t, fmt.Sprintf("%s/reverse?x=%v&y=%v", f.path, revX, revY))
				})
			}
		})
	}

	// Aggregate /autocomplete (the primary search endpoint).
	t.Run("autocomplete-aggregate", func(t *testing.T) {
		compareEndpoint(t, "/autocomplete?q="+url.QueryEscape("Rådhuspladsen 1"))
	})

	// naboer (vejstykke + navngivenvej): derive the resource key from DAWA's own
	// collection href, then compare {href}/naboer on both sides.
	for _, base := range []string{"/vejstykker", "/navngivneveje"} {
		base := base
		t.Run("naboer/"+strings.Trim(base, "/"), func(t *testing.T) {
			_, dBody := fetch(t, liveDAWA+base+"?side=1&per_side=1")
			href := firstHref(dBody)
			if href == "" || !strings.HasPrefix(href, liveDAWA) {
				t.Skipf("no usable href in %s collection", base)
			}
			compareEndpoint(t, strings.TrimPrefix(href, liveDAWA)+"/naboer?afstand=50")
		})
	}

	// replikering — token-FREE endpoints. NOTE the real DAWA paths are the QUERY
	// form (/replikering/udtraek?entitet=…); the udtraek/haendelser variants are
	// token-gated and live in TestTokenGatedEndpoints. These four are open.
	for _, p := range []string{
		"/replikering/senestesekvensnummer",
		"/replikering/senestetransaktion",
		"/replikering/transaktioner?side=1&per_side=5",
		"/replikering/datamodel?entitet=vejstykke",
	} {
		p := p
		t.Run("replikering/"+strings.Trim(strings.SplitN(p[len("/replikering/"):], "?", 2)[0], "/"), func(t *testing.T) {
			compareEndpoint(t, p)
		})
	}
}

// TestDocumentedCoverage probes documented DAWA endpoints that notdawa does
// NOT implement (or implements at a different path). A case FAILS with a
// status mismatch (DAWA serves it, ours 404s) — that is the honest "not
// implemented" signal, not a bug in the test. See docs/dawa_reference/ENDPOINTS.md.
//
// /bygninger (GeoDanmark building polygons) is deliberately OUT OF SCOPE
// (owner decision 2026-06-12): it is not address data, the upstream GeoDanmark
// sources survive DAWA's shutdown, the extract needs a separate Datafordeler
// subscription, and the production access log (access_paths) shows zero
// demand. Revisit only if real /bygninger traffic ever appears in the log.
func TestDocumentedCoverage(t *testing.T) {
	requireDAWA(t)
	// id-bearing endpoints derive their key from a sibling collection so they
	// don't hardcode unstable UUIDs.
	adgId := firstID(t, "/adgangsadresser")
	adrId := firstID(t, "/adresser")
	cases := map[string]string{
		"historik-adgangsadresser": "/historik/adgangsadresser?id=" + adgId,
		"historik-adresser":        "/historik/adresser?id=" + adrId,
	}
	for name, path := range cases {
		if strings.HasSuffix(path, "/") { // missing derived id
			continue
		}
		name, path := name, path
		t.Run(name, func(t *testing.T) { compareEndpoint(t, path) })
	}
}

// TestTokenGatedEndpoints covers DAWA endpoints the public gateway gates behind a
// (free) Dataforsyningen token: datavask, replikering udtraek/haendelser, BBR-light,
// darhistorik. Without DATAFORSYNINGEN_TOKEN the comparison is skipped; with it,
// the token is appended to the DAWA request (our server ignores it). These also
// surface our replikering PATH-SHAPE divergence (we serve /replikering/{entitet}/…,
// DAWA uses /replikering/…?entitet=).
func TestTokenGatedEndpoints(t *testing.T) {
	requireDAWA(t)
	token := os.Getenv("DATAFORSYNINGEN_TOKEN")
	if token == "" {
		t.Skip("set DATAFORSYNINGEN_TOKEN to compare token-gated datavask / replikering-bulk / BBR-light / darhistorik")
	}
	addr := url.QueryEscape("Rentemestervej 8, 2400 København NV")
	cases := map[string]string{
		"datavask-adresser":        "/datavask/adresser?betegnelse=" + addr,
		"datavask-adgangsadresser": "/datavask/adgangsadresser?betegnelse=" + addr,
		"datavask-vejnavne":        "/datavask/vejnavne?betegnelse=" + url.QueryEscape("Rentemestervej"),
		"replikering-udtraek":      "/replikering/udtraek?entitet=region",
		"replikering-haendelser":   "/replikering/haendelser?entitet=region&sekvensnummerfra=1&sekvensnummertil=1",
		"bbrlight-bygninger":       "/bbrlight/bygninger?side=1&per_side=5",
		"darhistorik":              "/darhistorik?id=" + firstID(t, "/adresser"),
	}
	for name, path := range cases {
		name, path := name, path
		t.Run(name, func(t *testing.T) {
			sep := "?"
			if strings.Contains(path, "?") {
				sep = "&"
			}
			// token only on the DAWA side (ours has no concept of it)
			compareEndpointDawaURL(t, path, path+sep+"token="+url.QueryEscape(token))
		})
	}
}

// firstID returns the first element's id (or its href's last path segment) from a
// collection, or "" if unavailable.
func firstID(t *testing.T, collPath string) string {
	t.Helper()
	_, body := fetch(t, liveDAWA+collPath+"?side=1&per_side=1")
	v, err := decodeJSON(body)
	if err != nil {
		return ""
	}
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return ""
	}
	obj, ok := arr[0].(map[string]any)
	if !ok {
		return ""
	}
	if id, ok := obj["id"].(string); ok && id != "" {
		return id
	}
	if h, ok := obj["href"].(string); ok {
		if i := strings.LastIndex(h, "/"); i >= 0 {
			return h[i+1:]
		}
	}
	return ""
}

// TestTilknytningerMatchLiveDAWA documents that our server must agree with the
// live upstream on the tilknytninger paths. DAWA currently 404s them; if our
// server returns 200 this FAILS — surfacing that we expose endpoints the live
// API does not (decide: remove them, or confirm the real DAWA path + re-derive).
func TestTilknytningerMatchLiveDAWA(t *testing.T) {
	requireDAWA(t)
	for _, p := range tilknytningPaths {
		p := p
		t.Run(strings.Trim(p, "/"), func(t *testing.T) {
			compareEndpoint(t, p+"?side=1&per_side=5")
		})
	}
}

// ---- comparison machinery ----

// compareEndpoint fetches the same path from our server and from live DAWA and
// asserts the status codes AND the full JSON bodies are identical (modulo geo).
func compareEndpoint(t *testing.T, path string) {
	t.Helper()
	oStatus, oBody := fetch(t, ourBase+path)
	dStatus, dBody := fetch(t, liveDAWA+path)
	compareStatusBody(t, path, oStatus, oBody, dStatus, dBody)
}

// compareEndpointDawaURL is compareEndpoint with distinct paths for ours vs DAWA
// (used when the DAWA request needs extra query params, e.g. a gateway token).
func compareEndpointDawaURL(t *testing.T, oursPath, dawaPath string) {
	t.Helper()
	oStatus, oBody := fetch(t, ourBase+oursPath)
	dStatus, dBody := fetch(t, liveDAWA+dawaPath)
	compareStatusBody(t, oursPath, oStatus, oBody, dStatus, dBody)
}

// compareStatusBody asserts status parity then full-body key-for-key equality.
func compareStatusBody(t *testing.T, label string, oStatus int, oBody []byte, dStatus int, dBody []byte) {
	t.Helper()
	if oStatus != dStatus {
		t.Errorf("STATUS mismatch %s\n  ours=%d dawa=%d\n  ours body: %s\n  dawa body: %s",
			label, oStatus, dStatus, snippet(oBody), snippet(dBody))
		return
	}
	ov, oerr := decodeJSON(oBody)
	dv, derr := decodeJSON(dBody)
	if oerr != nil || derr != nil {
		// Non-JSON on either side (e.g. NDJSON udtraek, or a gateway HTML error).
		// If the raw bytes match it is fine; otherwise report it.
		if string(oBody) != string(dBody) {
			t.Errorf("NON-JSON body differs %s\n  ours(%v): %s\n  dawa(%v): %s",
				label, oerr, snippet(oBody), derr, snippet(dBody))
		}
		return
	}
	// Autocomplete endpoints are compared by RESPONSE SHAPE, not row-for-row.
	// DAWA ranks autocomplete results by an internal tsvector/matview relevance
	// score that is not reproducible from our schema, so WHICH 20 rows it returns
	// and in WHAT order legitimately differs from ours — that is the documented,
	// owner-accepted "close enough" class (a client typing an address gets relevant
	// suggestions, not DAWA's exact ranking). We therefore tolerate row identity,
	// order, and scalar values, but STILL enforce the element SHAPE: the set of JSON
	// key-paths we emit must equal DAWA's (so an extra/missing key — a real
	// projection bug a client would choke on — still fails). See autocompleteShapeDiff.
	if isAutocompletePath(label) {
		compareAutocompleteShape(t, label, ov, dv)
		return
	}

	// naboer endpoints are compared as an unordered SET of elements. DAWA's
	// /vejstykker/{kk}/{kode}/naboer returns its neighbour rows in the physical
	// scan order of its internal navngivenvejkommunedel_mat matview (verified: not
	// by distance, kode, or id) — an order not reproducible from our schema. The
	// element BODIES are byte-exact (the full vejstykker/navngivneveje list shape)
	// and the SET (membership) is reproducible, so we compare the two as multisets
	// of canonicalised elements: same members required, ROW ORDER tolerated. This is
	// stricter than the autocomplete shape check (full element equality, not just key
	// presence) — only the ordering is relaxed. See compareNaboerSet.
	if isNaboerPath(label) {
		compareNaboerSet(t, label, ov, dv)
		return
	}

	var all []diff
	compareJSON("", ov, dv, &all)

	// Partition diffs into REAL (must fix) and TOLERATED (proven not derivable
	// from the Datafordeler bulk extract — see toleratedDiff). Tolerated diffs are
	// still PRINTED below so they stay visible and a regression cannot hide behind
	// the allow-list; only real diffs fail the endpoint.
	var diffs, tolerated, decayed []diff
	for _, d := range all {
		switch {
		case toleratedDiff(label, d.path):
			tolerated = append(tolerated, d)
		case dawaDecayedDiff(d):
			decayed = append(decayed, d)
		default:
			diffs = append(diffs, d)
		}
	}
	if len(tolerated) > 0 {
		var tb strings.Builder
		fmt.Fprintf(&tb, "%d tolerated source-data diff(s) %s (not failing — see toleratedDiff)", len(tolerated), label)
		for i, d := range tolerated {
			if i >= maxDiffsPerEndpoint {
				fmt.Fprintf(&tb, "\n  …+%d more", len(tolerated)-maxDiffsPerEndpoint)
				break
			}
			fmt.Fprintf(&tb, "\n  %s: ours=%s dawa=%s", d.path, d.ours, d.dawa)
		}
		t.Log(tb.String())
	}
	if len(decayed) > 0 {
		var db strings.Builder
		fmt.Fprintf(&db, "%d tolerated DAWA-DECAY diff(s) %s (live DAWA degrading toward the 2026-07-01 shutdown; ours serves the real values — see dawaDecayedDiff)", len(decayed), label)
		for i, d := range decayed {
			if i >= maxDiffsPerEndpoint {
				fmt.Fprintf(&db, "\n  …+%d more", len(decayed)-maxDiffsPerEndpoint)
				break
			}
			fmt.Fprintf(&db, "\n  %s: ours=%s dawa=%s", d.path, d.ours, d.dawa)
		}
		t.Log(db.String())
	}
	if len(diffs) == 0 {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d diff(s) %s", len(diffs), label)
	for i, d := range diffs {
		if i >= maxDiffsPerEndpoint {
			fmt.Fprintf(&b, "\n  …+%d more", len(diffs)-maxDiffsPerEndpoint)
			break
		}
		fmt.Fprintf(&b, "\n  %s: ours=%s dawa=%s", d.path, d.ours, d.dawa)
	}
	t.Error(b.String())
}

// isAutocompletePath reports whether a label is an autocomplete request (the
// per-resource /{resource}/autocomplete forms and the aggregate /autocomplete).
func isAutocompletePath(label string) bool {
	return strings.HasPrefix(label, "/autocomplete") || strings.Contains(label, "/autocomplete?") || strings.Contains(label, "/autocomplete/")
}

// keyPathSet walks a decoded JSON value and returns the SET of structural
// key-paths it contains, with array indices collapsed to "[]" so that row order
// and row count do not matter — only which keys exist anywhere in the structure.
// A leaf contributes its own path; this captures the response's SHAPE vocabulary.
func keyPathSet(v any, prefix string, out map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			out[p] = true
			keyPathSet(val, p, out)
		}
	case []any:
		p := prefix + "[]"
		for _, e := range x {
			keyPathSet(e, p, out)
		}
	}
}

// compareAutocompleteShape fails only when the SET of key-paths differs between
// ours and DAWA — i.e. we emit a key DAWA never does (projection bug) or omit one
// DAWA emits. Row identity/order/values are intentionally ignored (DAWA's
// proprietary relevance rank). It also enforces that both sides return a non-empty
// JSON array (or both empty) so an endpoint silently returning [] can't pass.
func compareAutocompleteShape(t *testing.T, label string, ov, dv any) {
	t.Helper()
	oArr, ook := ov.([]any)
	dArr, dok := dv.([]any)
	if !ook || !dok {
		t.Errorf("autocomplete %s: expected JSON arrays (ours array=%v dawa array=%v)", label, ook, dok)
		return
	}
	if (len(oArr) == 0) != (len(dArr) == 0) {
		t.Errorf("autocomplete %s: emptiness mismatch — ours len=%d dawa len=%d", label, len(oArr), len(dArr))
		return
	}
	ours := map[string]bool{}
	dawa := map[string]bool{}
	keyPathSet(ov, "", ours)
	keyPathSet(dv, "", dawa)

	var extra, missing []string
	for p := range ours {
		if !dawa[p] {
			extra = append(extra, p)
		}
	}
	for p := range dawa {
		if !ours[p] {
			missing = append(missing, p)
		}
	}
	if len(extra) == 0 && len(missing) == 0 {
		t.Logf("autocomplete %s: shape matches (ours %d rows / dawa %d rows; row set+order+values tolerated)", label, len(oArr), len(dArr))
		return
	}
	sort.Strings(extra)
	sort.Strings(missing)
	var b strings.Builder
	fmt.Fprintf(&b, "autocomplete SHAPE mismatch %s (row set/order/values are tolerated; these are KEY diffs)", label)
	for _, p := range extra {
		fmt.Fprintf(&b, "\n  EXTRA key we emit (dawa lacks): %s", p)
	}
	for _, p := range missing {
		fmt.Fprintf(&b, "\n  MISSING key dawa emits (we lack): %s", p)
	}
	t.Error(b.String())
}

// isNaboerPath reports whether a label is a road-neighbour request.
func isNaboerPath(label string) bool {
	return strings.Contains(label, "/naboer")
}

// naboerKey returns a stable identity for a naboer element so ours/dawa rows can be
// PAIRED regardless of order: the road's href (unique per vejstykke/navngivenvej).
func naboerKey(e any) string {
	m, ok := e.(map[string]any)
	if !ok {
		return ""
	}
	if h, ok := m["href"].(string); ok {
		return h
	}
	return ""
}

// compareNaboerSet compares ours/dawa naboer rows as a SET keyed by href: the
// membership must match exactly, but ROW ORDER is tolerated (DAWA returns naboer in
// the physical scan order of its internal navngivenvejkommunedel_mat matview, not
// reproducible from our schema). Each MATCHED pair is then compared with the SAME
// tolerant field logic as every other endpoint (compareJSON + toleratedDiff + the
// geo dial), so a wrong/missing field on a neighbour still fails and only the
// ordering is relaxed. A neighbour present on one side only is a real SET error.
func compareNaboerSet(t *testing.T, label string, ov, dv any) {
	t.Helper()
	oArr, ook := ov.([]any)
	dArr, dok := dv.([]any)
	if !ook || !dok {
		t.Errorf("naboer %s: expected JSON arrays (ours array=%v dawa array=%v)", label, ook, dok)
		return
	}
	oByKey := map[string]any{}
	dByKey := map[string]any{}
	for _, e := range oArr {
		oByKey[naboerKey(e)] = e
	}
	for _, e := range dArr {
		dByKey[naboerKey(e)] = e
	}
	var onlyOurs, onlyDawa []string
	for k := range oByKey {
		if _, ok := dByKey[k]; !ok {
			onlyOurs = append(onlyOurs, k)
		}
	}
	for k := range dByKey {
		if _, ok := oByKey[k]; !ok {
			onlyDawa = append(onlyDawa, k)
		}
	}

	// Field-level compare of each matched pair, reusing the tolerant pipeline.
	var diffs, tolerated []diff
	for k, ov := range oByKey {
		dv, ok := dByKey[k]
		if !ok {
			continue
		}
		var pair []diff
		compareJSON(k, ov, dv, &pair)
		for _, d := range pair {
			if toleratedDiff(label, d.path) || dawaDecayedDiff(d) {
				tolerated = append(tolerated, d)
			} else {
				diffs = append(diffs, d)
			}
		}
	}
	if len(tolerated) > 0 {
		t.Logf("naboer %s: %d tolerated source-data diff(s) across matched neighbours (not failing)", label, len(tolerated))
	}
	if len(onlyOurs) == 0 && len(onlyDawa) == 0 && len(diffs) == 0 {
		t.Logf("naboer %s: set matches (%d elements; row order tolerated)", label, len(dArr))
		return
	}
	sort.Strings(onlyOurs)
	sort.Strings(onlyDawa)
	var b strings.Builder
	fmt.Fprintf(&b, "naboer mismatch %s (row order tolerated; ours=%d dawa=%d; %d ours-only, %d dawa-only, %d field-diffs)",
		label, len(oArr), len(dArr), len(onlyOurs), len(onlyDawa), len(diffs))
	for i, s := range onlyOurs {
		if i >= 8 {
			fmt.Fprintf(&b, "\n  …+%d more ours-only", len(onlyOurs)-8)
			break
		}
		fmt.Fprintf(&b, "\n  OURS-ONLY neighbour: %s", s)
	}
	for i, s := range onlyDawa {
		if i >= 8 {
			fmt.Fprintf(&b, "\n  …+%d more dawa-only", len(onlyDawa)-8)
			break
		}
		fmt.Fprintf(&b, "\n  DAWA-ONLY neighbour: %s", s)
	}
	for i, d := range diffs {
		if i >= maxDiffsPerEndpoint {
			fmt.Fprintf(&b, "\n  …+%d more field-diffs", len(diffs)-maxDiffsPerEndpoint)
			break
		}
		fmt.Fprintf(&b, "\n  %s: ours=%s dawa=%s", d.path, d.ours, d.dawa)
	}
	t.Error(b.String())
}

// compareSingle asks DAWA for the collection, takes the first element's href,
// and compares that one resource by key on both sides (ordering-independent).
func compareSingle(t *testing.T, f family) {
	t.Helper()
	_, dBody := fetch(t, liveDAWA+f.path+"?side=1&per_side=1")
	href := firstHref(dBody)
	if href == "" {
		t.Skipf("no href in %s collection element — cannot derive a single-resource path", f.path)
		return
	}
	rel := strings.TrimPrefix(href, liveDAWA)
	if rel == href { // href pointed elsewhere; bail rather than compare the wrong thing
		t.Skipf("href %q is not under %s", href, liveDAWA)
		return
	}
	compareEndpoint(t, rel)
}

type diff struct{ path, ours, dawa string }

// compareJSON walks ours/dawa in parallel and records every leaf/shape diff.
func compareJSON(path string, ours, dawa any, out *[]diff) {
	switch o := ours.(type) {
	case map[string]any:
		d, ok := dawa.(map[string]any)
		if !ok {
			*out = append(*out, diff{path, kind(ours), kind(dawa)})
			return
		}
		// Proportional visueltcenter tolerance: the pole-of-inaccessibility label
		// point drifts with polygon size (PostGIS vs DAWA polylabel). When it
		// agrees to within 0.5% of this object's own bbox diagonal, drop it from
		// both sides so the generic walk does not flag it. See visueltcenterPropOK.
		if visueltcenterPropOK(o, d) {
			delete(o, "visueltcenter")
			delete(d, "visueltcenter")
		}
		for _, k := range unionKeys(o, d) {
			ov, ook := o[k]
			dv, dok := d[k]
			switch {
			case ook && !dok:
				*out = append(*out, diff{joinPath(path, k), trunc(ov), "<absent>"})
			case !ook && dok:
				*out = append(*out, diff{joinPath(path, k), "<absent>", trunc(dv)})
			default:
				compareJSON(joinPath(path, k), ov, dv, out)
			}
		}
	case []any:
		d, ok := dawa.([]any)
		if !ok {
			*out = append(*out, diff{path, kind(ours), kind(dawa)})
			return
		}
		if len(o) != len(d) {
			*out = append(*out, diff{path + ".length", fmt.Sprint(len(o)), fmt.Sprint(len(d))})
		}
		n := len(o)
		if len(d) < n {
			n = len(d)
		}
		for i := 0; i < n; i++ {
			compareJSON(fmt.Sprintf("%s[%d]", path, i), o[i], d[i], out)
		}
	default:
		if !leafEqual(path, ours, dawa) {
			*out = append(*out, diff{path, trunc(ours), trunc(dawa)})
		}
	}
}

// geoFloats extracts an n-element coordinate slice from a decoded JSON value
// (an []any of json.Number). Returns false if it is not such a slice of length n.
func geoFloats(v any, n int) ([]float64, bool) {
	a, ok := v.([]any)
	if !ok || len(a) != n {
		return nil, false
	}
	out := make([]float64, n)
	for i, e := range a {
		f, ok := toFloat(e)
		if !ok {
			return nil, false
		}
		out[i] = f
	}
	return out, true
}

// visueltcenterPropOK reports whether ours/dawa agree on the visueltcenter label
// point to within 0.5% of this object's OWN bbox diagonal. The pole-of-
// inaccessibility (PostGIS ST_PointOnSurface/polylabel vs DAWA's polylabel)
// drifts in absolute metres proportionally to polygon size — ~1 m on a kommune
// but up to ~210 m on a region — so a flat metre dial cannot fit both. A
// proportional bound (owner-authorised 2026-06-01) keeps tiny address geometry
// effectively exact while tolerating the large-area label drift, all well within
// the project's "~1%" intent. Applies ONLY to the visueltcenter array when bbox
// is present on the same object; everything else stays on the flat geo dial. When
// it returns true the caller drops visueltcenter from both sides before the walk.
func visueltcenterPropOK(o, d map[string]any) bool {
	ovc, ok1 := geoFloats(o["visueltcenter"], 2)
	dvc, ok2 := geoFloats(d["visueltcenter"], 2)
	if !ok1 || !ok2 || (ovc[0] == dvc[0] && ovc[1] == dvc[1]) {
		return false // missing/exact → let the normal walk handle it
	}
	bb, ok := geoFloats(d["bbox"], 4)
	if !ok {
		return false
	}
	const lonScale = metresPerDegLat * 0.5592 // cos(56°)
	w := math.Abs(bb[2]-bb[0]) * lonScale
	h := math.Abs(bb[3]-bb[1]) * metresPerDegLat
	diag := math.Sqrt(w*w + h*h)
	if diag == 0 {
		return false
	}
	dx := math.Abs(ovc[0]-dvc[0]) * lonScale
	dy := math.Abs(ovc[1]-dvc[1]) * metresPerDegLat
	return math.Sqrt(dx*dx+dy*dy) <= 0.005*diag
}

// toleratedDiff reports whether a single diff falls on a field PROVEN not
// derivable from the Datafordeler bulk extract — a documented source-data
// limitation, not a bug we can fix on our side. It is, besides allowGeoDriftMeters,
// the only relaxation of the strict oracle, and it is deliberately NARROW: keyed by
// the exact JSON leaf path, never a whole object, and every tolerated diff is still
// PRINTED by compareStatusBody (t.Log) so a regression cannot hide behind it. Any
// field NOT listed here still fails the endpoint. Owner-authorised 2026-05-31.
//
// Evidence (memory: unreproducible-dawa-fields; docs/IMPLEMENTATION_HANDOFF.md §1):
//   - DAGI ændret / geo_ændret: we already store the per-object
//     datafordelerOpdateringstid; DAWA emits its OWN importer run-time (same date,
//     different time-of-day) which exists only in DAWA's internal import log.
//     Scoped to the object's own field — NOT historik.ændret (a separate value we
//     may yet fix) and NOT adgangspunkt/vejpunkt.ændret (already byte-matched).
//   - geo_version: a DAWA-internal counter bumped on each DAWA geometry re-import;
//     no per-object version ships in the Datafordeler source (we hold only the
//     per-file generation number).
//   - adgangspunkt.højde: needs the DHM elevation model (a separate Datafordeler
//     service-user product, absent from the address extract).
//   - adgangspunkt.tekstretning: not byte-derivable from the DAR husnummerretning
//     vector (its bearing disagrees with DAWA on most sampled rows).
//   - replikering sekvensnummer / txid / tidspunkt and the transaktioner count:
//     DAWA's global event-log sequence; no event log ships in the bulk extract.
func toleratedDiff(label, path string) bool {
	leaf := path
	if i := strings.LastIndex(path, "."); i >= 0 {
		leaf = path[i+1:]
	}
	// historik.*: since the DAR bitemporal ingests (2026-06-12), oprettet and
	// ikrafttrædelse DERIVE from the virkning chains and are STRICT everywhere
	// (byte-verified vs live incl. foreløbig-start addresses and 1900-sentinel
	// roads). ændret on ROADS is also derived exact (= the latest virkningFra);
	// on ADDRESSES it stays tolerated — live's value is DAWA's OWN event clock
	// (proven 2 s off DAR on the same event), not in any extract. nedlagt stays
	// tolerated (event-log state for discontinued entities; both sides null on
	// everything the suite samples).
	if strings.Contains(path, "historik") {
		onRoads := strings.Contains(label, "vejstykker") || strings.Contains(label, "navngivneveje") ||
			strings.Contains(label, "vejnavne") || strings.Contains(label, "naboer")
		switch leaf {
		case "nedlagt":
			return true
		case "ændret":
			return !onRoads
		}
	}
	switch leaf {
	case "geo_version", "geo_ændret":
		return true
	case "ændret":
		// DAGI object ændret = DAWA's importer clock (see header). adgangspunkt/
		// vejpunkt.ændret are already byte-matched — keep them strict.
		return !strings.Contains(path, "adgangspunkt") &&
			!strings.Contains(path, "vejpunkt")
	case "højde", "tekstretning":
		return strings.Contains(path, "adgangspunkt")
	}
	if strings.Contains(label, "replikering") {
		switch leaf {
		case "sekvensnummer", "txid", "tidspunkt":
			return true
		}
		if strings.HasSuffix(path, ".length") {
			return true
		}
	}
	return false
}

// dawaDecayedDiff reports whether a diff is live-DAWA DECAY: a field the dying
// upstream has degraded to null/absent on its way to the 2026-07-01 shutdown
// while WE still serve the real value. First observed 2026-06-11: the
// address→jordstykke relation (the ejerlav/jordstykke objects and the
// matrikelnr/esrejendomsnr leaves) returns null on live /adgangsadresser —
// it served real values on 2026-06-01 when the whole suite was green (zone
// "Udfaset" was the same phase-out pattern, earlier).
//
// The tolerance is deliberately ASYMMETRIC: only the DAWA side may be
// null/absent. If OUR side loses the value the diff still fails — that would be
// a regression in our join, not upstream decay. And because a decayed field is
// null on BOTH sides once our join breaks (which compares equal and produces no
// diff at all), TestDecayedFieldsStillServed separately pins our values for
// these fields. Tolerated decay is still printed by compareStatusBody so the
// decay's spread stays visible run-over-run.
func dawaDecayedDiff(d diff) bool {
	leaf := d.path
	if i := strings.LastIndex(d.path, "."); i >= 0 {
		leaf = d.path[i+1:]
	}
	switch leaf {
	case "ejerlav", "jordstykke", "matrikelnr", "esrejendomsnr":
	default:
		return false
	}
	dawaNull := d.dawa == "null" || d.dawa == "<nil>" || d.dawa == "<absent>"
	oursNull := d.ours == "null" || d.ours == "<nil>" || d.ours == "<absent>"
	return dawaNull && !oursNull
}

// leafEqual compares two scalar leaves. Numbers compare by value; geo numeric
// leaves are allowed to drift by up to allowGeoDriftMeters (0 by default = exact).
func leafEqual(path string, ours, dawa any) bool {
	of, ook := toFloat(ours)
	df, dok := toFloat(dawa)
	if ook && dok {
		if of == df {
			return true
		}
		if allowGeoDriftMeters > 0 && isGeoPath(path) {
			return metresApart(path, of, df) <= allowGeoDriftMeters
		}
		return false
	}
	// Non-numeric (string/bool/null): exact match required.
	return ours == dawa
}

// isGeoPath reports whether a JSON path addresses a coordinate-derived leaf.
func isGeoPath(path string) bool {
	for _, s := range []string{"bbox", "visueltcenter", "koordinater", "adgangspunkt", "vejpunkt", "tekstretning", "højde"} {
		if strings.Contains(path, s) {
			return true
		}
	}
	return false
}

const metresPerDegLat = 111_320.0

// metresApart converts a lat/lon degree delta to metres. Even array indices are
// longitude (scaled by cos(lat≈56°)), odd/scalar leaves use the latitude factor.
func metresApart(path string, a, b float64) float64 {
	scale := metresPerDegLat
	if i := strings.LastIndex(path, "["); i >= 0 && strings.HasSuffix(path, "]") {
		if idx, err := atoiSafe(path[i+1 : len(path)-1]); err == nil && idx%2 == 0 {
			scale = metresPerDegLat * 0.5592 // cos(56°)
		}
	}
	return math.Abs(a-b) * scale
}

// ---- small helpers ----

func requireDAWA(t *testing.T) {
	t.Helper()
	if ourBase == "" {
		t.Skip("no DB — server not started (set DATABASE_URL)")
	}
	status, _ := fetch(t, liveDAWA+"/kommuner?per_side=1")
	if status != http.StatusOK {
		t.Skipf("live DAWA unreachable / returned %d for the reachability probe", status)
	}
}

// fetch GETs full and returns (status, body). DAWA-side requests are routed
// through the oracle (see oracle_test.go): in snapshot mode they are served
// from the frozen tests/golden/ capture instead of the network; in capture
// mode every live response is additionally saved verbatim. Requests to OUR
// in-process server always hit the network path.
func fetch(t *testing.T, full string) (int, []byte) {
	t.Helper()
	if rel, ok := strings.CutPrefix(full, liveDAWA); ok {
		switch oracle() {
		case oracleSnapshot:
			return snapshotFetch(t, rel)
		case oracleCapture:
			status, body := liveFetch(t, full)
			captureSave(rel, status, body)
			return status, body
		}
	}
	return liveFetch(t, full)
}

// liveFetch GETs over the network, retrying on transport/truncated-body errors
// (DAWA's gateway intermittently resets connections) AND on HTTP 429 / 5xx
// gateway throttling. The suite fires hundreds of live requests, so DAWA's rate
// limiter occasionally returns 429 ("Too Many Requests") or a transient 502/503 —
// those are upstream throttling, not a real status divergence, so we back off and
// retry with longer waits. A status that PERSISTS past all retries is returned
// as-is and fails honestly (we never substitute or hide it).
func liveFetch(t *testing.T, full string) (int, []byte) {
	t.Helper()
	var lastErr error
	lastStatus := 0
	var lastBody []byte
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			// Linear-ish backoff; longer for throttling than for resets.
			time.Sleep(time.Duration(attempt) * 600 * time.Millisecond)
		}
		req, _ := http.NewRequest(http.MethodGet, full, nil)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent)
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		// Retry transient upstream throttling/gateway errors rather than treating
		// them as the endpoint's real status.
		if resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusBadGateway ||
			resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusGatewayTimeout {
			lastStatus, lastBody, lastErr = resp.StatusCode, body, nil
			continue
		}
		return resp.StatusCode, body
	}
	// Exhausted retries. If the last attempt got a (throttle) status, return it so
	// the comparison fails visibly rather than aborting the whole test run.
	if lastStatus != 0 {
		return lastStatus, lastBody
	}
	t.Fatalf("GET %s failed after retries: %v", full, lastErr)
	return 0, nil
}

func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// firstHref returns the href of the first element of a JSON-array body, or "".
func firstHref(body []byte) string {
	v, err := decodeJSON(body)
	if err != nil {
		return ""
	}
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return ""
	}
	obj, ok := arr[0].(map[string]any)
	if !ok {
		return ""
	}
	if h, ok := obj["href"].(string); ok {
		return h
	}
	return ""
}

func unionKeys(a, b map[string]any) []string {
	seen := map[string]bool{}
	var ks []string
	for k := range a {
		if !seen[k] {
			seen[k] = true
			ks = append(ks, k)
		}
	}
	for k := range b {
		if !seen[k] {
			seen[k] = true
			ks = append(ks, k)
		}
	}
	sort.Strings(ks)
	return ks
}

func joinPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	}
	return 0, false
}

func atoiSafe(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("nan")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

func kind(v any) string {
	switch v.(type) {
	case map[string]any:
		return "<object>"
	case []any:
		return "<array>"
	case nil:
		return "null"
	default:
		return trunc(v)
	}
}

func trunc(v any) string {
	s := fmt.Sprintf("%v", v)
	if len(s) > 70 {
		return s[:70] + "…"
	}
	return s
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
