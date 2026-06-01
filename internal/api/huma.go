// huma.go is an ADDITIVE Huma v2 harness that puts the DAWA-compatible API on
// the Huma framework to get an auto-generated OpenAPI document plus a Scalar
// docs UI, WITHOUT changing a single output byte.
//
// Design (proven by the regioner spike, now FANNED OUT to every route the
// net/http server registers): each Huma operation REPLAYS the existing *server
// handler from server.go against an httptest.ResponseRecorder built from a
// synthetic *http.Request, then returns the captured status / Content-Type /
// body verbatim via rawResponse. Huma writes a []byte Body without
// serialization (v2 huma.go: `if b, ok := body.([]byte); ok { ... }`), so the
// bytes that reach the client are exactly what the existing byte-exact handler
// produced — guaranteeing byte-for-byte equivalence with NewServer. The typed
// input structs exist ONLY to document params for Scalar; they never serialize
// the response.
//
// Why a synthetic request rather than humago.Unwrap: a handler registered via
// huma.Register receives only a plain context.Context, and humago v2.38.0 does
// NOT stash the *http.Request on it, so there is no supported way to recover the
// raw request inside the handler. We therefore rebuild a synthetic request from
// the typed input: path params via http.Request.SetPathValue, and the DAWA query
// string re-encoded from the declared fields. The recorder captures the exact
// bytes.
//
// QUERY-PARAM TRADEOFF (per the build brief): the raw request is unreachable, so
// only the fields DECLARED on the input struct can be forwarded to the replayed
// handler. We use a SMALL set of reusable input structs:
//
//   - collectionInput: the cross-cutting collection params (side, per_side, q,
//     struktur, format, callback, srid, polygon, cirkel, bbox) PLUS the ~common
//     per-field filter names used across DAWA resources (kode, kommunekode,
//     regionskode, postnr, nr, navn, nuts2, nuts3, vejkode, etage, doer, stedid,
//     ejerlavkode, adgangsadresseid). One struct, reused for every list endpoint.
//   - autocompleteInput (q, side, per_side), reverseInput (x, y, srid), and the
//     single-key path inputs (oneKeyInput, twoKeyInput) — also reused everywhere.
//   - replikeringInput / sekvensInput for the /replikering/* operations.
//
// Consequence: a list endpoint's RARE per-field filters that are not in the
// shared declared set will not forward through the Huma layer (they would simply
// be absent from the synthetic request, exactly as if the caller had not sent
// them). The byte-exact NewServer remains the production oracle for those; the
// goldens/tests exercise only declared params, so the EQUIVALENCE gate stays
// green. This is the deliberate "one generic collection input" middle ground the
// brief asked us to decide on: maximal docs + correctness for the common params,
// at the cost of not documenting/forwarding the long tail of rare filters.
package api

import (
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/dawa"
)

// logResponseWriter wraps http.ResponseWriter to capture the status code and the
// number of bytes written for access logging. Status defaults to 200 (Go's
// implicit status when WriteHeader is never called).
type logResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *logResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *logResponseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// accessLog wraps an http.Handler and logs one Apache-ish access line per request
// to the standard logger (stderr/console): remote-ip, method, URI, status,
// response size, and duration. Applied to the whole Huma handler so it covers
// every route plus /docs and /openapi.*.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lw := &logResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(lw, r)
		uri := r.URL.RequestURI()
		log.Printf("%s %s %s %d %dB %s", clientIP(r), r.Method, uri, lw.status, lw.bytes, time.Since(start).Round(time.Microsecond))
	})
}

// clientIP returns the best-effort client address: the X-Forwarded-For/
// X-Real-IP header when present (behind a proxy), else the raw RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := indexByte(xff, ','); i >= 0 {
			return xff[:i]
		}
		return xff
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return xr
	}
	return r.RemoteAddr
}

// indexByte is strings.IndexByte without importing strings just for this.
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// rawResponse is the Huma output struct for every bridged operation. Because
// Body is a []byte, Huma writes it verbatim with no serialization, and the
// Content-Type header field overrides Huma's negotiated content type — so the
// emitted bytes and headers are exactly those captured from the existing
// byte-exact handler.
type rawResponse struct {
	Status      int
	ContentType string `header:"Content-Type"`
	Body        []byte
}

// rawRequestKey is the context key under which the Huma middleware stashes the
// real *http.Request (recovered via humago.Unwrap). replay() reads it so the
// byte-exact handler runs against the ACTUAL request — every query param, filter,
// and path value the client sent — not a synthetic reconstruction. This is what
// makes Huma the real front: full request fidelity, no dropped params.
type rawRequestKey struct{}

// replay runs the existing byte-exact net/http handler and captures its response.
//
// It prefers the REAL *http.Request stashed on ctx by the Huma middleware (so all
// query params/filters/path values flow through verbatim). The path / rawQuery /
// pathValues args are a FALLBACK used only when no real request is present (e.g. a
// direct unit-test call): they rebuild a synthetic request from the typed input.
// In normal serving the fallback is never taken, so the long-tail filter params the
// old synthetic design dropped now forward automatically.
func replay(ctx context.Context, h http.HandlerFunc, path string, rawQuery string, pathValues map[string]string) *rawResponse {
	var r *http.Request
	if real, ok := ctx.Value(rawRequestKey{}).(*http.Request); ok && real != nil {
		// Use the real request verbatim (it already carries the query string and
		// Go 1.22 path values set by the ServeMux pattern). Rebind the context so
		// cancellation/deadlines still propagate.
		r = real.WithContext(ctx)
	} else {
		target := path
		if rawQuery != "" {
			target += "?" + rawQuery
		}
		r = httptest.NewRequest(http.MethodGet, target, nil)
		for k, v := range pathValues {
			r.SetPathValue(k, v)
		}
		r = r.WithContext(ctx)
	}

	rec := httptest.NewRecorder()
	h(rec, r)

	return &rawResponse{
		Status:      rec.Code,
		ContentType: rec.Header().Get("Content-Type"),
		Body:        rec.Body.Bytes(),
	}
}

// ---- reusable input structs (document the real DAWA params for Scalar) ----

// collectionInput documents the DAWA cross-cutting collection params plus the
// per-field filters shared across resources. RejectUnknownQueryParameters stays
// off (huma default). Only the declared fields forward to the replayed handler
// (the raw request is unreachable in humago v2.38.0); see the file header for
// the tradeoff.
type collectionInput struct {
	Side             int      `query:"side" doc:"1-based page number (used with per_side)"`
	PerSide          int      `query:"per_side" doc:"Page size; absent returns the full collection"`
	Q                string   `query:"q" doc:"Free-text autocomplete-style search"`
	Struktur         string   `query:"struktur" enum:"nestet,flad,mini" doc:"Output structure"`
	Format           string   `query:"format" enum:"json,ndjson,csv,jsonp,geojson,geojsonz" doc:"Output format"`
	Callback         string   `query:"callback" doc:"JSONP callback name (implies format=jsonp)"`
	SRID             int      `query:"srid" doc:"Output coordinate system (e.g. 25832)"`
	Polygon          string   `query:"polygon" doc:"GeoJSON polygon spatial filter"`
	Cirkel           string   `query:"cirkel" doc:"x,y,radius circle spatial filter"`
	Bbox             string   `query:"bbox" doc:"Bounding-box spatial filter"`
	Kode             string   `query:"kode" doc:"Filter by code (resource-dependent)"`
	Kommunekode      string   `query:"kommunekode" doc:"Filter by kommunekode"`
	Regionskode      string   `query:"regionskode" doc:"Filter by regionskode"`
	Postnr           string   `query:"postnr" doc:"Filter by postnummer"`
	Nr               string   `query:"nr" doc:"Filter by number"`
	Navn             string   `query:"navn" doc:"Filter by name"`
	Nuts2            string   `query:"nuts2" doc:"Filter by NUTS2 code"`
	Nuts3            string   `query:"nuts3" doc:"Filter by NUTS3 code"`
	Vejkode          string   `query:"vejkode" doc:"Filter by vejkode"`
	Etage            string   `query:"etage" doc:"Filter by etage (floor)"`
	Doer             string   `query:"dør" doc:"Filter by dør (door)"`
	Stedid           string   `query:"stedid" doc:"Filter by sted id (stednavne2)"`
	Ejerlavkode      string   `query:"ejerlavkode" doc:"Filter by ejerlavkode (jordstykker)"`
	Adgangsadresseid []string `query:"adgangsadresseid" doc:"Filter by adgangsadresse id (repeatable; tilknytninger)"`
	X                string   `query:"x" doc:"Longitude/easting (steder/stednavne2 reverse-via-query)"`
	Y                string   `query:"y" doc:"Latitude/northing (steder/stednavne2 reverse-via-query)"`
	Betegnelse       string   `query:"betegnelse" doc:"Free-text address to wash (datavask)"`
}

// oneKeyInput is a single-resource path input with one {key} segment. The huma
// path-param name is set per-operation via the Operation.Path placeholder, but
// the struct field must carry the same tag, so we register a small typed input
// per distinct placeholder name below instead of one generic struct (Huma reads
// the path tag literally). To keep it to a few structs we group by placeholder.

// kodeInput — {kode}
type kodeInput struct {
	Kode string `path:"kode" doc:"Resource code"`
}

// nrInput — {nr}
type nrInput struct {
	Nr string `path:"nr" doc:"Postnummer"`
}

// nuts3Input — {nuts3}
type nuts3Input struct {
	Nuts3 string `path:"nuts3" doc:"NUTS3 code (e.g. DK011)"`
}

// nummerInput — {nummer}
type nummerInput struct {
	Nummer string `path:"nummer" doc:"Number/code"`
}

// bogstavInput — {bogstav}
type bogstavInput struct {
	Bogstav string `path:"bogstav" doc:"Letter code (e.g. A)"`
}

// navnInput — {navn}
type navnInput struct {
	Navn string `path:"navn" doc:"Name"`
}

// idInput — {id}
type idInput struct {
	ID string `path:"id" doc:"Resource id (UUID or code)"`
}

// hovedtypeInput — {hovedtype}
type hovedtypeInput struct {
	Hovedtype string `path:"hovedtype" doc:"Stednavn hovedtype"`
}

// dagiIDInput — {dagi_id}
type dagiIDInput struct {
	DagiID string `path:"dagi_id" doc:"DAGI id"`
}

// kommuneKodeKodeInput — {kommunekode}/{kode}
type kommuneKodeKodeInput struct {
	Kommunekode string `path:"kommunekode" doc:"Kommunekode"`
	Kode        string `path:"kode" doc:"Resource code within the kommune"`
}

// ejerlavMatrikelInput — {ejerlavkode}/{matrikelnr}
type ejerlavMatrikelInput struct {
	Ejerlavkode string `path:"ejerlavkode" doc:"Ejerlavkode"`
	Matrikelnr  string `path:"matrikelnr" doc:"Matrikelnr"`
}

// stedNavnInput — {sted_id}/{navn}
type stedNavnInput struct {
	StedID string `path:"sted_id" doc:"Sted id"`
	Navn   string `path:"navn" doc:"Stednavn"`
}

// kommuneNummerInput — {kommunekode}/{nummer}
type kommuneNummerInput struct {
	Kommunekode string `path:"kommunekode" doc:"Kommunekode"`
	Nummer      string `path:"nummer" doc:"Nummer within the kommune"`
}

// postnrVejnavnInput — {postnr}/{vejnavn}
type postnrVejnavnInput struct {
	Postnr  string `path:"postnr" doc:"Postnummer"`
	Vejnavn string `path:"vejnavn" doc:"Vejnavn"`
}

// autocompleteInput documents the autocomplete params.
type autocompleteInput struct {
	Q       string `query:"q" doc:"Search string"`
	Side    int    `query:"side" doc:"1-based page number"`
	PerSide int    `query:"per_side" doc:"Page size"`
}

// reverseInput documents the reverse-geocode params.
type reverseInput struct {
	X    float64 `query:"x" doc:"Longitude (or easting when srid is projected)"`
	Y    float64 `query:"y" doc:"Latitude (or northing when srid is projected)"`
	SRID int     `query:"srid" doc:"Coordinate system of x/y (e.g. 25832)"`
}

// replikeringInput — per-entitet udtraek/haendelser.
type replikeringInput struct {
	Entitet          string `path:"entitet" doc:"Entity name (e.g. adgangsadresse, vejstykke)"`
	Sekvensnummer    string `query:"sekvensnummer" doc:"Sequence number bound"`
	SekvensnummerFra string `query:"sekvensnummerfra" doc:"Sequence number lower bound (haendelser)"`
	SekvensnummerTil string `query:"sekvensnummertil" doc:"Sequence number upper bound (haendelser)"`
	Format           string `query:"format" enum:"json,ndjson,csv" doc:"Output format"`
}

// sekvensInput — /replikering/senestesekvensnummer + /replikering/transaktioner
// + /replikering/senestetransaktion + /replikering/datamodel.
type sekvensInput struct {
	Txid    string `query:"txid" doc:"Transaction id (transaktioner)"`
	Side    string `query:"side" doc:"Page number (transaktioner)"`
	PerSide string `query:"per_side" doc:"Page size (transaktioner)"`
	Entitet string `query:"entitet" doc:"Entity name (datamodel)"`
	Format  string `query:"format" enum:"json,ndjson,csv" doc:"Output format"`
}

// ---- NewHumaServer ----

// NewHumaServer builds the Huma-framed DAWA-compatible handler covering EVERY
// route the net/http NewServer registers, serving /openapi.json + /openapi.yaml
// (Huma) and a Scalar docs UI at /docs. Output bytes are identical to NewServer
// for every route because the existing byte-exact handlers are replayed verbatim.
func NewHumaServer(pool *pgxpool.Pool, baseURL string) http.Handler {
	s := &server{pool: pool, baseURL: baseURL}

	mux := http.NewServeMux()

	config := huma.DefaultConfig("DAWA-compatible API (notdawa)", "1.0.0")
	// Drop Huma's body-mutating hooks ($schema injection, Link header). We never
	// touch the body anyway (it is replayed verbatim), but disabling them keeps
	// the OpenAPI clean and avoids any surprise.
	config.CreateHooks = nil
	// Disable Huma's built-in (Stoplight) docs; we serve Scalar ourselves below.
	config.DocsPath = ""
	// OpenAPIPath stays at its default "/openapi" → /openapi.json + /openapi.yaml.

	api := humago.New(mux, config)

	// Middleware: recover the REAL *http.Request (humago gives Huma the actual
	// request, with Go 1.22 path values already set by the ServeMux pattern) and
	// stash it on the context. Every operation's replay() then forwards that real
	// request to the byte-exact handler — so ALL query params, filters and path
	// values flow through, not just the fields declared on the typed input structs.
	// The typed inputs remain solely for OpenAPI/Scalar documentation.
	api.UseMiddleware(func(hctx huma.Context, next func(huma.Context)) {
		if r, _ := humago.Unwrap(hctx); r != nil {
			hctx = huma.WithValue(hctx, rawRequestKey{}, r)
		}
		next(hctx)
	})

	reg := api.OpenAPI().Components.Schemas

	// schemaResponses builds a 200 response carrying a registered body schema so
	// Scalar documents the wire shape (Huma can't infer it from a []byte).
	schemaResponses := func(t reflect.Type, hint, desc string) map[string]*huma.Response {
		sch := reg.Schema(t, true, hint)
		return map[string]*huma.Response{
			"200": {
				Description: desc,
				Content:     map[string]*huma.MediaType{"application/json": {Schema: sch}},
			},
		}
	}

	// ---- helper closures to register the four operation shapes compactly ----

	// list registers a collection GET. The shared collectionInput forwards the
	// cross-cutting params + common filters to the replayed handler.
	list := func(tag, path string, h http.HandlerFunc, summary string) {
		huma.Register(api, huma.Operation{
			OperationID: "list-" + opID(path),
			Method:      http.MethodGet,
			Path:        path,
			Summary:     summary,
			Tags:        []string{tag},
		}, func(ctx context.Context, in *collectionInput) (*rawResponse, error) {
			return replay(ctx, h, path, encodeCollection(in), nil), nil
		})
	}

	// auto registers an autocomplete GET.
	auto := func(tag, path string, h http.HandlerFunc, summary string) {
		huma.Register(api, huma.Operation{
			OperationID: "autocomplete-" + opID(path),
			Method:      http.MethodGet,
			Path:        path,
			Summary:     summary,
			Tags:        []string{tag},
		}, func(ctx context.Context, in *autocompleteInput) (*rawResponse, error) {
			return replay(ctx, h, path, encodeAutocomplete(in), nil), nil
		})
	}

	// rev registers a reverse-geocode GET. resp may be nil.
	rev := func(tag, path string, h http.HandlerFunc, summary string, resp map[string]*huma.Response) {
		huma.Register(api, huma.Operation{
			OperationID: "reverse-" + opID(path),
			Method:      http.MethodGet,
			Path:        path,
			Summary:     summary,
			Tags:        []string{tag},
			Responses:   resp,
		}, func(ctx context.Context, in *reverseInput) (*rawResponse, error) {
			return replay(ctx, h, path, encodeReverse(in), nil), nil
		})
	}

	// ===================== Regioner =====================
	list("Regioner", "/regioner", s.listRegioner, "List regioner")
	huma.Register(api, huma.Operation{
		OperationID: "get-region", Method: http.MethodGet, Path: "/regioner/{kode}",
		Summary: "Get a single region", Tags: []string{"Regioner"},
		Responses: schemaResponses(reflect.TypeOf(dawa.Region{}), "Region", "A region in DAWA's exact wire format."),
	}, func(ctx context.Context, in *kodeInput) (*rawResponse, error) {
		return replay(ctx, s.getRegion, "/regioner/"+in.Kode, "", map[string]string{"kode": in.Kode}), nil
	})
	auto("Regioner", "/regioner/autocomplete", s.autocompleteRegioner, "Autocomplete regioner")
	rev("Regioner", "/regioner/reverse", s.reverseRegion, "Reverse-geocode a region",
		schemaResponses(reflect.TypeOf(dawa.Region{}), "Region", "The region containing the point."))

	// ===================== Kommuner =====================
	list("Kommuner", "/kommuner", s.listKommuner, "List kommuner")
	registerKode(api, "Kommuner", "/kommuner/{kode}", "get-kommune", "Get a single kommune", s.getKommune,
		schemaResponses(reflect.TypeOf(dawa.Kommune{}), "Kommune", "A kommune in DAWA's exact wire format."))
	auto("Kommuner", "/kommuner/autocomplete", s.autocompleteKommuner, "Autocomplete kommuner")
	rev("Kommuner", "/kommuner/reverse", s.reverseKommune, "Reverse-geocode a kommune", nil)

	// ===================== Sogne =====================
	list("Sogne", "/sogne", s.listSogne, "List sogne")
	registerKode(api, "Sogne", "/sogne/{kode}", "get-sogn", "Get a single sogn", s.getSogn,
		schemaResponses(reflect.TypeOf(dawa.Sogn{}), "Sogn", "A sogn in DAWA's exact wire format."))
	auto("Sogne", "/sogne/autocomplete", s.autocompleteSogne, "Autocomplete sogne")
	rev("Sogne", "/sogne/reverse", s.reverseSogn, "Reverse-geocode a sogn", nil)

	// ===================== Postnumre =====================
	list("Postnumre", "/postnumre", s.listPostnumre, "List postnumre")
	huma.Register(api, huma.Operation{
		OperationID: "get-postnummer", Method: http.MethodGet, Path: "/postnumre/{nr}",
		Summary: "Get a single postnummer", Tags: []string{"Postnumre"},
		Responses: schemaResponses(reflect.TypeOf(dawa.Postnummer{}), "Postnummer", "A postnummer in DAWA's exact wire format."),
	}, func(ctx context.Context, in *nrInput) (*rawResponse, error) {
		return replay(ctx, s.getPostnummer, "/postnumre/"+in.Nr, "", map[string]string{"nr": in.Nr}), nil
	})
	auto("Postnumre", "/postnumre/autocomplete", s.autocompletePostnumre, "Autocomplete postnumre")
	rev("Postnumre", "/postnumre/reverse", s.reversePostnummer, "Reverse-geocode a postnummer", nil)

	// ===================== Landsdele =====================
	list("Landsdele", "/landsdele", s.listLandsdele, "List landsdele")
	huma.Register(api, huma.Operation{
		OperationID: "get-landsdel", Method: http.MethodGet, Path: "/landsdele/{nuts3}",
		Summary: "Get a single landsdel", Tags: []string{"Landsdele"},
	}, func(ctx context.Context, in *nuts3Input) (*rawResponse, error) {
		return replay(ctx, s.getLandsdel, "/landsdele/"+in.Nuts3, "", map[string]string{"nuts3": in.Nuts3}), nil
	})
	auto("Landsdele", "/landsdele/autocomplete", s.autocompleteLandsdele, "Autocomplete landsdele")
	rev("Landsdele", "/landsdele/reverse", s.reverseLandsdel, "Reverse-geocode a landsdel", nil)

	// ===================== Storkredse =====================
	list("Storkredse", "/storkredse", s.listStorkredse, "List storkredse")
	huma.Register(api, huma.Operation{
		OperationID: "get-storkreds", Method: http.MethodGet, Path: "/storkredse/{nummer}",
		Summary: "Get a single storkreds", Tags: []string{"Storkredse"},
	}, func(ctx context.Context, in *nummerInput) (*rawResponse, error) {
		return replay(ctx, s.getStorkreds, "/storkredse/"+in.Nummer, "", map[string]string{"nummer": in.Nummer}), nil
	})
	auto("Storkredse", "/storkredse/autocomplete", s.autocompleteStorkredse, "Autocomplete storkredse")
	rev("Storkredse", "/storkredse/reverse", s.reverseStorkreds, "Reverse-geocode a storkreds", nil)

	// ===================== Valglandsdele =====================
	list("Valglandsdele", "/valglandsdele", s.listValglandsdele, "List valglandsdele")
	huma.Register(api, huma.Operation{
		OperationID: "get-valglandsdel", Method: http.MethodGet, Path: "/valglandsdele/{bogstav}",
		Summary: "Get a single valglandsdel", Tags: []string{"Valglandsdele"},
	}, func(ctx context.Context, in *bogstavInput) (*rawResponse, error) {
		return replay(ctx, s.getValglandsdel, "/valglandsdele/"+in.Bogstav, "", map[string]string{"bogstav": in.Bogstav}), nil
	})
	auto("Valglandsdele", "/valglandsdele/autocomplete", s.autocompleteValglandsdele, "Autocomplete valglandsdele")
	rev("Valglandsdele", "/valglandsdele/reverse", s.reverseValglandsdel, "Reverse-geocode a valglandsdel", nil)

	// ===================== Opstillingskredse =====================
	list("Opstillingskredse", "/opstillingskredse", s.listOpstillingskredse, "List opstillingskredse")
	registerKode(api, "Opstillingskredse", "/opstillingskredse/{kode}", "get-opstillingskreds", "Get a single opstillingskreds", s.getOpstillingskreds, nil)
	auto("Opstillingskredse", "/opstillingskredse/autocomplete", s.autocompleteOpstillingskredse, "Autocomplete opstillingskredse")
	rev("Opstillingskredse", "/opstillingskredse/reverse", s.reverseOpstillingskreds, "Reverse-geocode an opstillingskreds", nil)

	// ===================== Retskredse =====================
	list("Retskredse", "/retskredse", s.listRetskredse, "List retskredse")
	registerKode(api, "Retskredse", "/retskredse/{kode}", "get-retskreds", "Get a single retskreds", s.getRetskreds,
		schemaResponses(reflect.TypeOf(dawa.SimpleArea{}), "SimpleArea", "A simple area (retskreds) in DAWA's exact wire format."))
	auto("Retskredse", "/retskredse/autocomplete", s.autocompleteRetskredse, "Autocomplete retskredse")
	rev("Retskredse", "/retskredse/reverse", s.reverseRetskreds, "Reverse-geocode a retskreds", nil)

	// ===================== Politikredse =====================
	list("Politikredse", "/politikredse", s.listPolitikredse, "List politikredse")
	registerKode(api, "Politikredse", "/politikredse/{kode}", "get-politikreds", "Get a single politikreds", s.getPolitikreds, nil)
	auto("Politikredse", "/politikredse/autocomplete", s.autocompletePolitikredse, "Autocomplete politikredse")
	rev("Politikredse", "/politikredse/reverse", s.reversePolitikreds, "Reverse-geocode a politikreds", nil)

	// ===================== Vejstykker =====================
	list("Vejstykker", "/vejstykker", s.listVejstykker, "List vejstykker")
	huma.Register(api, huma.Operation{
		OperationID: "naboer-vejstykke", Method: http.MethodGet, Path: "/vejstykker/{kommunekode}/{kode}/naboer",
		Summary: "Neighbours of a vejstykke", Tags: []string{"Vejstykker"},
	}, func(ctx context.Context, in *kommuneKodeKodeInput) (*rawResponse, error) {
		return replay(ctx, s.vejstykkeNaboer, "/vejstykker/"+in.Kommunekode+"/"+in.Kode+"/naboer", "",
			map[string]string{"kommunekode": in.Kommunekode, "kode": in.Kode}), nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "get-vejstykke", Method: http.MethodGet, Path: "/vejstykker/{kommunekode}/{kode}",
		Summary: "Get a single vejstykke", Tags: []string{"Vejstykker"},
		Responses: schemaResponses(reflect.TypeOf(dawa.Vejstykke{}), "Vejstykke", "A vejstykke in DAWA's exact wire format."),
	}, func(ctx context.Context, in *kommuneKodeKodeInput) (*rawResponse, error) {
		return replay(ctx, s.getVejstykke, "/vejstykker/"+in.Kommunekode+"/"+in.Kode, "",
			map[string]string{"kommunekode": in.Kommunekode, "kode": in.Kode}), nil
	})
	auto("Vejstykker", "/vejstykker/autocomplete", s.autocompleteVejstykker, "Autocomplete vejstykker")
	rev("Vejstykker", "/vejstykker/reverse", s.reverseVejstykke, "Reverse-geocode a vejstykke", nil)

	// ===================== Vejnavne =====================
	list("Vejnavne", "/vejnavne", s.listVejnavne, "List vejnavne")
	huma.Register(api, huma.Operation{
		OperationID: "get-vejnavn", Method: http.MethodGet, Path: "/vejnavne/{navn}",
		Summary: "Get a single vejnavn", Tags: []string{"Vejnavne"},
	}, func(ctx context.Context, in *navnInput) (*rawResponse, error) {
		return replay(ctx, s.getVejnavn, "/vejnavne/"+in.Navn, "", map[string]string{"navn": in.Navn}), nil
	})
	auto("Vejnavne", "/vejnavne/autocomplete", s.autocompleteVejnavne, "Autocomplete vejnavne")

	// ===================== Navngivneveje =====================
	list("Navngivneveje", "/navngivneveje", s.listNavngivneveje, "List navngivneveje")
	huma.Register(api, huma.Operation{
		OperationID: "naboer-navngivenvej", Method: http.MethodGet, Path: "/navngivneveje/{id}/naboer",
		Summary: "Neighbours of a navngiven vej", Tags: []string{"Navngivneveje"},
	}, func(ctx context.Context, in *idInput) (*rawResponse, error) {
		return replay(ctx, s.navngivenvejNaboer, "/navngivneveje/"+in.ID+"/naboer", "", map[string]string{"id": in.ID}), nil
	})
	registerID(api, "Navngivneveje", "/navngivneveje/{id}", "get-navngivenvej", "Get a single navngiven vej", s.getNavngivenVej,
		schemaResponses(reflect.TypeOf(dawa.NavngivenVej{}), "NavngivenVej", "A navngiven vej in DAWA's exact wire format."))
	auto("Navngivneveje", "/navngivneveje/autocomplete", s.autocompleteNavngivneveje, "Autocomplete navngivneveje")

	// ===================== Vejnavnpostnummerrelationer =====================
	list("Vejnavnpostnummerrelationer", "/vejnavnpostnummerrelationer", s.listVejnavnPostnummerRelationer, "List vejnavn-postnummer-relationer")
	auto("Vejnavnpostnummerrelationer", "/vejnavnpostnummerrelationer/autocomplete", s.autocompleteVejnavnPostnummerRelationer, "Autocomplete vejnavn-postnummer-relationer")
	huma.Register(api, huma.Operation{
		OperationID: "get-vejnavnpostnummerrelation", Method: http.MethodGet, Path: "/vejnavnpostnummerrelationer/{postnr}/{vejnavn}",
		Summary: "Get a single vejnavn-postnummer-relation", Tags: []string{"Vejnavnpostnummerrelationer"},
	}, func(ctx context.Context, in *postnrVejnavnInput) (*rawResponse, error) {
		return replay(ctx, s.getVejnavnPostnummerRelation, "/vejnavnpostnummerrelationer/"+in.Postnr+"/"+in.Vejnavn, "",
			map[string]string{"postnr": in.Postnr, "vejnavn": in.Vejnavn}), nil
	})

	// ===================== Stednavntyper =====================
	list("Stednavntyper", "/stednavntyper", s.listStednavntyper, "List stednavntyper")
	huma.Register(api, huma.Operation{
		OperationID: "get-stednavntype", Method: http.MethodGet, Path: "/stednavntyper/{hovedtype}",
		Summary: "Get a single stednavntype", Tags: []string{"Stednavntyper"},
	}, func(ctx context.Context, in *hovedtypeInput) (*rawResponse, error) {
		return replay(ctx, s.getStednavntype, "/stednavntyper/"+in.Hovedtype, "", map[string]string{"hovedtype": in.Hovedtype}), nil
	})

	// ===================== Supplerendebynavne =====================
	list("Supplerendebynavne", "/supplerendebynavne2", s.listSupplerendebynavne, "List supplerendebynavne (v2)")
	list("Supplerendebynavne", "/supplerendebynavne", s.listSupplerendebynavneV1, "List supplerendebynavne (legacy)")
	huma.Register(api, huma.Operation{
		OperationID: "get-supplerendebynavn", Method: http.MethodGet, Path: "/supplerendebynavne2/{dagi_id}",
		Summary: "Get a single supplerende bynavn", Tags: []string{"Supplerendebynavne"},
	}, func(ctx context.Context, in *dagiIDInput) (*rawResponse, error) {
		return replay(ctx, s.getSupplerendebynavn, "/supplerendebynavne2/"+in.DagiID, "", map[string]string{"dagi_id": in.DagiID}), nil
	})
	auto("Supplerendebynavne", "/supplerendebynavne2/autocomplete", s.autocompleteSupplerendebynavne, "Autocomplete supplerendebynavne (v2)")
	auto("Supplerendebynavne", "/supplerendebynavne/autocomplete", s.autocompleteSupplerendebynavneV1, "Autocomplete supplerendebynavne (legacy)")
	rev("Supplerendebynavne", "/supplerendebynavne2/reverse", s.reverseSupplerendebynavn, "Reverse-geocode a supplerende bynavn", nil)

	// ===================== Ejerlav =====================
	list("Ejerlav", "/ejerlav", s.listEjerlav, "List ejerlav")
	registerKode(api, "Ejerlav", "/ejerlav/{kode}", "get-ejerlav", "Get a single ejerlav", s.getEjerlav, nil)
	auto("Ejerlav", "/ejerlav/autocomplete", s.autocompleteEjerlav, "Autocomplete ejerlav")

	// ===================== Jordstykker =====================
	list("Jordstykker", "/jordstykker", s.listJordstykker, "List jordstykker")
	huma.Register(api, huma.Operation{
		OperationID: "get-jordstykke", Method: http.MethodGet, Path: "/jordstykker/{ejerlavkode}/{matrikelnr}",
		Summary: "Get a single jordstykke", Tags: []string{"Jordstykker"},
		Responses: schemaResponses(reflect.TypeOf(dawa.Jordstykke{}), "Jordstykke", "A jordstykke in DAWA's exact wire format."),
	}, func(ctx context.Context, in *ejerlavMatrikelInput) (*rawResponse, error) {
		return replay(ctx, s.getJordstykke, "/jordstykker/"+in.Ejerlavkode+"/"+in.Matrikelnr, "",
			map[string]string{"ejerlavkode": in.Ejerlavkode, "matrikelnr": in.Matrikelnr}), nil
	})
	auto("Jordstykker", "/jordstykker/autocomplete", s.autocompleteJordstykker, "Autocomplete jordstykker")
	rev("Jordstykker", "/jordstykker/reverse", s.reverseJordstykke, "Reverse-geocode a jordstykke", nil)

	// ===================== Steder =====================
	list("Steder", "/steder", s.listSteder2, "List steder (supports ?x&y and ?nærmeste reverse-via-query)")
	registerID(api, "Steder", "/steder/{id}", "get-sted", "Get a single sted", s.getSted,
		schemaResponses(reflect.TypeOf(dawa.Sted{}), "Sted", "A sted in DAWA's exact wire format."))

	// ===================== Stednavne (legacy) =====================
	list("Stednavne", "/stednavne", s.listLegacyStednavne, "List stednavne (legacy)")
	auto("Stednavne", "/stednavne/autocomplete", s.autocompleteLegacyStednavne, "Autocomplete stednavne (legacy)")
	registerID(api, "Stednavne", "/stednavne/{id}", "get-legacy-stednavn", "Get a single legacy stednavn", s.getLegacyStednavn, nil)

	// ===================== Stednavne2 =====================
	list("Stednavne2", "/stednavne2", s.listStednavne2WithReverse, "List stednavne2 (supports reverse-via-query)")
	huma.Register(api, huma.Operation{
		OperationID: "get-stednavn2", Method: http.MethodGet, Path: "/stednavne2/{sted_id}/{navn}",
		Summary: "Get a single stednavn2", Tags: []string{"Stednavne2"},
	}, func(ctx context.Context, in *stedNavnInput) (*rawResponse, error) {
		return replay(ctx, s.getStednavn2, "/stednavne2/"+in.StedID+"/"+in.Navn, "",
			map[string]string{"sted_id": in.StedID, "navn": in.Navn}), nil
	})
	auto("Stednavne2", "/stednavne2/autocomplete", s.autocompleteStednavne2, "Autocomplete stednavne2")

	// ===================== Bebyggelser =====================
	list("Bebyggelser", "/bebyggelser", s.listBebyggelser, "List bebyggelser")
	registerID(api, "Bebyggelser", "/bebyggelser/{id}", "get-bebyggelse", "Get a single bebyggelse", s.getBebyggelse,
		schemaResponses(reflect.TypeOf(dawa.Bebyggelse{}), "Bebyggelse", "A bebyggelse in DAWA's exact wire format."))

	// ===================== Adgangsadresser =====================
	list("Adgangsadresser", "/adgangsadresser", s.listAdgangsadresser, "List adgangsadresser")
	registerID(api, "Adgangsadresser", "/adgangsadresser/{id}", "get-adgangsadresse", "Get a single adgangsadresse", s.getAdgangsadresse,
		schemaResponses(reflect.TypeOf(dawa.Adgangsadresse{}), "Adgangsadresse", "An adgangsadresse in DAWA's exact wire format."))
	auto("Adgangsadresser", "/adgangsadresser/autocomplete", s.autocompleteAdgangsadresser, "Autocomplete adgangsadresser")
	rev("Adgangsadresser", "/adgangsadresser/reverse", s.reverseAdgangsadresse, "Reverse-geocode an adgangsadresse", nil)

	// ===================== Adresser =====================
	list("Adresser", "/adresser", s.listAdresser, "List adresser")
	registerID(api, "Adresser", "/adresser/{id}", "get-adresse", "Get a single adresse", s.getAdresse,
		schemaResponses(reflect.TypeOf(dawa.Adresse{}), "Adresse", "An adresse in DAWA's exact wire format."))
	auto("Adresser", "/adresser/autocomplete", s.autocompleteAdresser, "Autocomplete adresser")
	rev("Adresser", "/adresser/reverse", s.reverseAdresse, "Reverse-geocode an adresse (DAWA path-error envelope)", nil)

	// ===================== Datavask =====================
	huma.Register(api, huma.Operation{
		OperationID: "datavask-adgangsadresser", Method: http.MethodGet, Path: "/datavask/adgangsadresser",
		Summary: "Wash an adgangsadresse betegnelse", Tags: []string{"Datavask"},
	}, func(ctx context.Context, in *collectionInput) (*rawResponse, error) {
		return replay(ctx, s.datavaskAdgangsadresser, "/datavask/adgangsadresser", encodeCollection(in), nil), nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "datavask-adresser", Method: http.MethodGet, Path: "/datavask/adresser",
		Summary: "Wash an adresse betegnelse", Tags: []string{"Datavask"},
	}, func(ctx context.Context, in *collectionInput) (*rawResponse, error) {
		return replay(ctx, s.datavaskAdresser, "/datavask/adresser", encodeCollection(in), nil), nil
	})

	// ===================== Afstemningsomraader =====================
	list("Afstemningsomraader", "/afstemningsomraader", s.listAfstemningsomraader, "List afstemningsområder")
	huma.Register(api, huma.Operation{
		OperationID: "get-afstemningsomraade", Method: http.MethodGet, Path: "/afstemningsomraader/{kommunekode}/{nummer}",
		Summary: "Get a single afstemningsområde", Tags: []string{"Afstemningsomraader"},
	}, func(ctx context.Context, in *kommuneNummerInput) (*rawResponse, error) {
		return replay(ctx, s.getAfstemningsomraade, "/afstemningsomraader/"+in.Kommunekode+"/"+in.Nummer, "",
			map[string]string{"kommunekode": in.Kommunekode, "nummer": in.Nummer}), nil
	})
	auto("Afstemningsomraader", "/afstemningsomraader/autocomplete", s.autocompleteAfstemningsomraader, "Autocomplete afstemningsområder")
	rev("Afstemningsomraader", "/afstemningsomraader/reverse", s.reverseAfstemningsomraade, "Reverse-geocode an afstemningsområde", nil)

	// ===================== Menighedsrådsafstemningsområder =====================
	list("Menighedsraadsafstemningsomraader", "/menighedsraadsafstemningsomraader", s.listMrafstemningsomraader, "List menighedsråds-afstemningsområder")
	auto("Menighedsraadsafstemningsomraader", "/menighedsraadsafstemningsomraader/autocomplete", s.autocompleteMrafstemningsomraader, "Autocomplete menighedsråds-afstemningsområder")
	rev("Menighedsraadsafstemningsomraader", "/menighedsraadsafstemningsomraader/reverse", s.reverseMrafstemningsomraade, "Reverse-geocode a menighedsråds-afstemningsområde", nil)
	huma.Register(api, huma.Operation{
		OperationID: "get-mrafstemningsomraade", Method: http.MethodGet, Path: "/menighedsraadsafstemningsomraader/{kommunekode}/{nummer}",
		Summary: "Get a single menighedsråds-afstemningsområde", Tags: []string{"Menighedsraadsafstemningsomraader"},
	}, func(ctx context.Context, in *kommuneNummerInput) (*rawResponse, error) {
		return replay(ctx, s.getMrafstemningsomraade, "/menighedsraadsafstemningsomraader/"+in.Kommunekode+"/"+in.Nummer, "",
			map[string]string{"kommunekode": in.Kommunekode, "nummer": in.Nummer}), nil
	})

	// ===================== Aggregate autocomplete =====================
	auto("Autocomplete", "/autocomplete", s.autocompleteAggregate, "Aggregate autocomplete (address escalation)")

	// ===================== Tilknytninger =====================
	// DAWA has NO standalone /<area>tilknytninger endpoints; the live gateway
	// 404s every one (HTML body). They are intentionally NOT advertised in the
	// OpenAPI spec, but they ARE bound on the Huma mux to s.tilknytninger so the
	// Huma server reproduces DAWA's gateway 404 verbatim (humago's default 404
	// would not match). Registered directly on the mux (not via huma.Register) to
	// keep them out of the documented surface.
	for _, p := range dawa.TilknytningPaths() {
		mux.HandleFunc("GET /"+p, s.tilknytninger)
	}

	// ===================== Replikering =====================
	huma.Register(api, huma.Operation{
		OperationID: "replikering-senestesekvensnummer", Method: http.MethodGet, Path: "/replikering/senestesekvensnummer",
		Summary: "Latest sequence number", Tags: []string{"Replikering"},
	}, func(ctx context.Context, in *sekvensInput) (*rawResponse, error) {
		return replay(ctx, s.senesteSekvensnummer, "/replikering/senestesekvensnummer", encodeSekvens(in), nil), nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "replikering-transaktioner", Method: http.MethodGet, Path: "/replikering/transaktioner",
		Summary: "Replication transactions", Tags: []string{"Replikering"},
	}, func(ctx context.Context, in *sekvensInput) (*rawResponse, error) {
		return replay(ctx, s.transaktioner, "/replikering/transaktioner", encodeSekvens(in), nil), nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "replikering-senestetransaktion", Method: http.MethodGet, Path: "/replikering/senestetransaktion",
		Summary: "Latest transaction", Tags: []string{"Replikering"},
	}, func(ctx context.Context, in *sekvensInput) (*rawResponse, error) {
		return replay(ctx, s.senesteTransaktion, "/replikering/senestetransaktion", encodeSekvens(in), nil), nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "replikering-datamodel", Method: http.MethodGet, Path: "/replikering/datamodel",
		Summary: "Replication datamodel", Tags: []string{"Replikering"},
	}, func(ctx context.Context, in *sekvensInput) (*rawResponse, error) {
		return replay(ctx, s.datamodel, "/replikering/datamodel", encodeSekvens(in), nil), nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "replikering-udtraek", Method: http.MethodGet, Path: "/replikering/{entitet}/udtraek",
		Summary: "Initial-load extract for an entity", Tags: []string{"Replikering"},
	}, func(ctx context.Context, in *replikeringInput) (*rawResponse, error) {
		return replay(ctx, s.udtraek, "/replikering/"+in.Entitet+"/udtraek", encodeReplikering(in),
			map[string]string{"entitet": in.Entitet}), nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "replikering-haendelser", Method: http.MethodGet, Path: "/replikering/{entitet}/haendelser",
		Summary: "Change events for an entity", Tags: []string{"Replikering"},
	}, func(ctx context.Context, in *replikeringInput) (*rawResponse, error) {
		return replay(ctx, s.haendelser, "/replikering/"+in.Entitet+"/haendelser", encodeReplikering(in),
			map[string]string{"entitet": in.Entitet}), nil
	})

	// Scalar docs UI. Served as a plain mux route per the Huma docs.
	mux.HandleFunc("GET /docs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(scalarHTML))
	})

	// Wrap the whole server in access logging so every request (incl. /docs and
	// /openapi.*) is logged to the console as it comes in.
	return accessLog(mux)
}

// registerKode registers a single-resource GET keyed by {kode}.
func registerKode(api huma.API, tag, path, opID, summary string, h http.HandlerFunc, resp map[string]*huma.Response) {
	huma.Register(api, huma.Operation{
		OperationID: opID, Method: http.MethodGet, Path: path, Summary: summary,
		Tags: []string{tag}, Responses: resp,
	}, func(ctx context.Context, in *kodeInput) (*rawResponse, error) {
		base := path[:len(path)-len("{kode}")]
		return replay(ctx, h, base+in.Kode, "", map[string]string{"kode": in.Kode}), nil
	})
}

// registerID registers a single-resource GET keyed by {id}.
func registerID(api huma.API, tag, path, opID, summary string, h http.HandlerFunc, resp map[string]*huma.Response) {
	huma.Register(api, huma.Operation{
		OperationID: opID, Method: http.MethodGet, Path: path, Summary: summary,
		Tags: []string{tag}, Responses: resp,
	}, func(ctx context.Context, in *idInput) (*rawResponse, error) {
		base := path[:len(path)-len("{id}")]
		return replay(ctx, h, base+in.ID, "", map[string]string{"id": in.ID}), nil
	})
}

// opID turns a route path into a stable, slug-like operation-id fragment so
// every operation gets a unique OperationID (Huma requires uniqueness).
func opID(path string) string {
	out := make([]byte, 0, len(path))
	prevDash := false
	for i := 0; i < len(path); i++ {
		c := path[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			out = append(out, c)
			prevDash = false
		default:
			if !prevDash && len(out) > 0 {
				out = append(out, '-')
				prevDash = true
			}
		}
	}
	// trim trailing dash
	for len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	return string(out)
}

const scalarHTML = `<!doctype html><html><head><meta charset="utf-8"><title>DAWA API Reference</title>
<meta name="viewport" content="width=device-width, initial-scale=1"></head><body>
<script id="api-reference" data-url="/openapi.json"></script>
<script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
</body></html>`

// ---- query-string encoders ----
//
// These rebuild the DAWA query string from the typed input struct, emitting a
// param only when the client supplied it (so absent params behave exactly as on
// NewServer, where Query().Get returns ""). Integers are emitted only when
// non-zero; strings only when non-empty. url.Values.Encode is order-stable, and
// each param is read independently by the existing handler via Query().Get(name),
// so ordering is irrelevant to the output bytes.

func encodeCollection(in *collectionInput) string {
	v := url.Values{}
	setInt(v, "side", in.Side)
	setInt(v, "per_side", in.PerSide)
	setStr(v, "q", in.Q)
	setStr(v, "struktur", in.Struktur)
	setStr(v, "format", in.Format)
	setStr(v, "callback", in.Callback)
	setInt(v, "srid", in.SRID)
	setStr(v, "polygon", in.Polygon)
	setStr(v, "cirkel", in.Cirkel)
	setStr(v, "bbox", in.Bbox)
	setStr(v, "kode", in.Kode)
	setStr(v, "kommunekode", in.Kommunekode)
	setStr(v, "regionskode", in.Regionskode)
	setStr(v, "postnr", in.Postnr)
	setStr(v, "nr", in.Nr)
	setStr(v, "navn", in.Navn)
	setStr(v, "nuts2", in.Nuts2)
	setStr(v, "nuts3", in.Nuts3)
	setStr(v, "vejkode", in.Vejkode)
	setStr(v, "etage", in.Etage)
	setStr(v, "dør", in.Doer)
	setStr(v, "stedid", in.Stedid)
	setStr(v, "ejerlavkode", in.Ejerlavkode)
	setStr(v, "x", in.X)
	setStr(v, "y", in.Y)
	setStr(v, "betegnelse", in.Betegnelse)
	for _, id := range in.Adgangsadresseid {
		if id != "" {
			v.Add("adgangsadresseid", id)
		}
	}
	return v.Encode()
}

func encodeAutocomplete(in *autocompleteInput) string {
	v := url.Values{}
	setStr(v, "q", in.Q)
	setInt(v, "side", in.Side)
	setInt(v, "per_side", in.PerSide)
	return v.Encode()
}

func encodeReverse(in *reverseInput) string {
	v := url.Values{}
	// x/y are required by the reverse handler (parseXY 400s when absent). Emit via
	// strconv.FormatFloat 'g'/-1, which round-trips the float64 to its shortest
	// exact decimal, preserving the caller's value through the synthetic request.
	v.Set("x", strconv.FormatFloat(in.X, 'g', -1, 64))
	v.Set("y", strconv.FormatFloat(in.Y, 'g', -1, 64))
	setInt(v, "srid", in.SRID)
	return v.Encode()
}

func encodeReplikering(in *replikeringInput) string {
	v := url.Values{}
	setStr(v, "sekvensnummer", in.Sekvensnummer)
	setStr(v, "sekvensnummerfra", in.SekvensnummerFra)
	setStr(v, "sekvensnummertil", in.SekvensnummerTil)
	setStr(v, "format", in.Format)
	return v.Encode()
}

func encodeSekvens(in *sekvensInput) string {
	v := url.Values{}
	setStr(v, "txid", in.Txid)
	setStr(v, "side", in.Side)
	setStr(v, "per_side", in.PerSide)
	setStr(v, "entitet", in.Entitet)
	setStr(v, "format", in.Format)
	return v.Encode()
}

func setStr(v url.Values, k, val string) {
	if val != "" {
		v.Set(k, val)
	}
}

func setInt(v url.Values, k string, val int) {
	if val != 0 {
		v.Set(k, strconv.Itoa(val))
	}
}
