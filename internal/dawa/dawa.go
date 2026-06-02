// Package dawa builds DAWA-compatible JSON from the local PostGIS mirror. It
// holds the response shapes (exact key order matters) and the queries that
// derive DAWA's computed fields:
//
//   - bbox: the geometry's envelope is taken in the stored CRS (EPSG:25832),
//     then the projected rectangle is reprojected to WGS84 (4326) and its
//     corners bound the result. This reproduces DAWA byte-for-byte — DAWA's
//     bbox is wider than the WGS84 envelope of the geometry precisely because
//     it bounds a reprojected projected-CRS rectangle, not the geometry itself.
//   - visueltcenter: the pole of inaccessibility via ST_MaximumInscribedCircle
//     (computed in 25832, then reprojected). Matches DAWA to within metres; the
//     residual is GEOS's envelope-derived tolerance, which PostGIS does not
//     expose as a parameter.
//
// All coordinates are rounded to 8 decimals, matching DAWA, and marshalled with
// HTML escaping off so 'ø'/'æ'/'&' stay literal.
package dawa

import (
	"bytes"
	"encoding/json"
)

// DefaultBaseURL is the host DAWA uses in href fields (kept for fidelity).
const DefaultBaseURL = "https://api.dataforsyningen.dk"

// MarshalDAWA renders a single object the way DAWA does: 2-space indent, no
// HTML escaping. For arrays use MarshalDAWAList — DAWA's array layout is not
// Go's standard one (see below).
func MarshalDAWA(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// json.Encoder appends a trailing newline; DAWA bodies have none.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// MarshalDAWAList renders a JSON array the way DAWA's streaming serializer does,
// which is NOT Go's standard indentation. DAWA emits:
//
//	[
//	{
//	  "kode": "1081",
//	  ...
//	}, {
//	  "kode": "1082",
//	  ...
//	}
//	]
//
// i.e. "[\n", then each element marshalled as a top-level object (its "{" at
// column 0, members indented 2 spaces), elements separated by the literal
// ", ", then "\n]". Go's encoder would instead indent each element and use a
// "},\n  {" separator, so we assemble the array by hand from per-element bytes.
func MarshalDAWAList[T any](items []T) ([]byte, error) {
	if len(items) == 0 {
		return []byte("[]"), nil
	}
	parts := make([][]byte, len(items))
	for i, it := range items {
		b, err := MarshalDAWA(it)
		if err != nil {
			return nil, err
		}
		parts[i] = b
	}
	out := append([]byte("[\n"), bytes.Join(parts, []byte(", "))...)
	return append(out, "\n]"...), nil
}

// MarshalDAWAAutoList renders an AUTOCOMPLETE array the way DAWA's autocomplete
// endpoints do — the standard pretty-printer JSON.stringify(x, null, 2):
//
//	[
//	  {
//	    "type": "vejnavn",
//	    ...
//	  },
//	  {
//	    ...
//	  }
//	]
//
// i.e. each element indented one level and separated by ",\n". This is DELIBERATELY
// different from MarshalDAWAList (the collection streaming serializer, element "{"
// at column 0 with ", " separators): /autocomplete and /{resource}/autocomplete use
// the standard serializer, the collection endpoints use the streaming one — verified
// byte-for-byte against live DAWA. SetEscapeHTML(false) keeps æ/ø/å/& literal; any
// element type's own MarshalJSON is re-indented by the encoder.
func MarshalDAWAAutoList[T any](items []T) ([]byte, error) {
	if len(items) == 0 {
		return []byte("[]"), nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(items); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
