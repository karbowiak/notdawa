// Package domain holds the neutral, render-agnostic representations of every
// register entity. A domain struct carries EVERY field both serving modes need:
// the DAWA-byte-exact computed values (e.g. DAWA's reprojected-rectangle bbox and
// the pole-of-inaccessibility visueltcenter) AND the "proper" WGS84 geometry
// (true bbox + raw GeoJSON) the English api mode renders. The store package fills
// these from Postgres; the dawa and api renderers each project the subset they
// emit. This is the single data-fetch path shared by both modes.
package domain

// Region is the neutral representation of a DAGI region (dagi_regioner).
type Region struct {
	Aendret    *string // formatted UTC mtime ("…Z"); DAWA-internal, best-effort
	GeoVersion *int    // DAWA-internal version counter; not derivable (nil)
	DagiID     *string // id_lokalId
	Kode       string
	Navn       string
	Nuts2      *string

	// DAWA-quirk geometry summary, for the byte-exact dawa renderer:
	// bbox bounds the EPSG:25832 envelope reprojected to 4326 (wider than the
	// geometry's true WGS84 envelope), visueltcenter is the pole of
	// inaccessibility (ST_MaximumInscribedCircle on the largest sub-polygon).
	DawaBbox          [4]float64
	DawaVisueltcenter [2]float64

	// True WGS84 geometry, for the api renderer.
	TrueBbox [4]float64 // ST_Transform(geom,4326) extent (the correct bbox)
	GeoJSON  string     // raw geometry as GeoJSON (EPSG:4326)
}
