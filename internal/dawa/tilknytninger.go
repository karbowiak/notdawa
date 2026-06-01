// Package dawa: tilknytninger path catalogue.
//
// DAWA has NO standalone /<area>tilknytninger collection endpoints — the live
// Dataforsyningen gateway 404s every one of them (verified 2026-05-31). This
// file therefore carries ONLY the canonical list of the 12 phantom paths so the
// API layer can register them and reply with DAWA's gateway 404 byte-for-byte.
// The former DB-backed handler and its row/area helpers were removed once the
// endpoints were confirmed to be invented (they had been returning an empty
// 200 collection where DAWA returns 404).
package dawa

// tilknytningPaths is the canonical list of the 12 phantom /<area>tilknytninger
// URL segments. They exist as a group only because our old API surface invented
// them; we keep the list to register the matching gateway-404 routes.
var tilknytningPaths = []string{
	"regionstilknytninger",
	"kommunetilknytninger",
	"sognetilknytninger",
	"politikredstilknytninger",
	"retskredstilknytninger",
	"opstillingskredstilknytninger",
	"postnummertilknytninger",
	"zonetilknytninger",
	"valglandsdelstilknytninger",
	"storkredstilknytninger",
	"jordstykketilknytninger",
	"stednavntilknytninger",
}

// TilknytningPaths returns the URL path segments for every phantom tilknytning
// collection (each of which DAWA's gateway 404s).
func TilknytningPaths() []string {
	out := make([]string, len(tilknytningPaths))
	copy(out, tilknytningPaths)
	return out
}
