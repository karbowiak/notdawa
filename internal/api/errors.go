package api

import (
	_ "embed"
	"log"
	"net/http"
	"regexp"

	"github.com/karbowiak/notdawa/internal/dawa"
)

// contentTypeJSON is the exact Content-Type DAWA serves.
const contentTypeJSON = "application/json; charset=UTF-8"

// gateway404Body is the exact body the live Dataforsyningen API Gateway returns
// (HTTP 404, Content-Type text/html) for the phantom /<area>tilknytninger paths.
// Those collections do not exist in DAWA; the gateway serves this static HTML
// page rather than the application-level JSON error. The bytes are embedded
// verbatim (captured from the live API) so our response byte-matches DAWA's.
//
//go:embed gateway_404.html
var gateway404Body []byte

// writeGatewayNotFound emits the DAWA API Gateway 404 page byte-for-byte (HTTP
// 404, Content-Type text/html). Used for paths the live gateway 404s with HTML
// rather than the JSON ResourcePathFormatError our application catch-all serves.
func writeGatewayNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write(gateway404Body)
}

// writeJSON writes pre-marshalled DAWA bytes with the DAWA Content-Type and the
// given status. No trailing newline is added — the body is written verbatim, so
// callers must pass exactly what dawa.MarshalDAWA / MarshalDAWAList produced.
func writeJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeMarshalled marshals v the DAWA way and writes it, or 500s on a marshal
// error (which should never happen for our value types).
func writeMarshalled(w http.ResponseWriter, v any) {
	b, err := dawa.MarshalDAWA(v)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// writeList writes an AUTOCOMPLETE array (its only callers are the /autocomplete
// + /{resource}/autocomplete handlers). DAWA serves these with the standard
// pretty-printer, NOT the collection streaming serializer, so it uses
// MarshalDAWAAutoList. Collection endpoints render via the query pipeline
// (MarshalDAWAList) instead.
func writeList[T any](w http.ResponseWriter, items []T) {
	b, err := dawa.MarshalDAWAAutoList(items)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// dawaError is the DAWA error envelope shape.
type dawaError struct {
	Type    string         `json:"type"`
	Title   string         `json:"title"`
	Details map[string]any `json:"details"`
}

// writeNotFound emits DAWA's ResourceNotFoundError (HTTP 404). details carries
// the lookup parameters that did not resolve.
func writeNotFound(w http.ResponseWriter, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	body, err := dawa.MarshalDAWA(dawaError{
		Type:    "ResourceNotFoundError",
		Title:   "The resource was not found",
		Details: details,
	})
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusNotFound, body)
}

// writeBadRequest emits DAWA's QueryParameterFormatError (HTTP 400) for a
// malformed path/query parameter.
func writeBadRequest(w http.ResponseWriter, param, value string) {
	body, err := dawa.MarshalDAWA(dawaError{
		Type:  "QueryParameterFormatError",
		Title: "One or more query parameters was ill-formed",
		Details: map[string]any{
			"parameter": param,
			"value":     value,
		},
	})
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusBadRequest, body)
}

// pathFormatError is the DAWA ResourcePathFormatError envelope. Unlike
// dawaError, its details is an ARRAY of [field, message] pairs (DAWA emits the
// failing path segment and the regex it violated), so it needs its own type.
type pathFormatError struct {
	Type    string     `json:"type"`
	Title   string     `json:"title"`
	Details [][]string `json:"details"`
}

// jsonpCallbackRE is DAWA's JSONP callback-name pattern. The pattern source
// also appears verbatim in the error message below, so keep them in sync.
var jsonpCallbackRE = regexp.MustCompile(`^[\$_a-zA-Z0-9\.]+$`)

// writeCallbackFormatError reproduces DAWA's 400 for an invalid JSONP callback
// name byte-exactly (captured live 2026-06-12): QueryParameterFormatError with
// the ARRAY details form and a trailing period in the title — distinct from
// writeBadRequest's map-details form.
func writeCallbackFormatError(w http.ResponseWriter, value string) {
	body, err := dawa.MarshalDAWA(pathFormatError{
		Type:    "QueryParameterFormatError",
		Title:   "One or more query parameters was ill-formed.",
		Details: [][]string{{"callback", `String does not match pattern ^[\$_a-zA-Z0-9\.]+$: ` + value}},
	})
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusBadRequest, body)
}

// writeAdresseReversePathError reproduces DAWA's response to /adresser/reverse:
// there is no reverse route on /adresser, so DAWA matches /adresser/{id} with
// id="reverse", which fails the UUID pattern and yields a 404
// ResourcePathFormatError. The body is byte-identical to the captured golden.
func writeAdresseReversePathError(w http.ResponseWriter) {
	body, err := dawa.MarshalDAWA(pathFormatError{
		Type:  "ResourcePathFormatError",
		Title: "The URI path was ill-formed.",
		Details: [][]string{{
			"id",
			`String does not match pattern ^([0-9a-fA-F]{8}\-[0-9a-fA-F]{4}\-[0-9a-fA-F]{4}\-[0-9a-fA-F]{4}\-[0-9a-fA-F]{12})$: reverse`,
		}},
	})
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusNotFound, body)
}

// writePathFormatError emits DAWA's ResourcePathFormatError (HTTP 404) with a
// single [field, message] details pair. DAWA uses this when a path parameter
// fails its type check — e.g. /supplerendebynavne2/{dagi_id} with a non-integer
// dagi_id yields details [["dagi_id","notInteger"]].
func writePathFormatError(w http.ResponseWriter, field, message string) {
	body, err := dawa.MarshalDAWA(pathFormatError{
		Type:    "ResourcePathFormatError",
		Title:   "The URI path was ill-formed.",
		Details: [][]string{{field, message}},
	})
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusNotFound, body)
}

// writeServerError emits a plain HTTP 500 for unexpected DB/marshal errors.
// The error detail is logged server-side only: raw pgx errors carry SQLSTATE
// codes, table/column names and (on connect failures) DB host/user — internal
// details a public API must not disclose.
func writeServerError(w http.ResponseWriter, err error) {
	log.Printf("500: %v", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}
