package dawa

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/domain"
	"github.com/karbowiak/notdawa/internal/store"
)

// Region is the DAWA /regioner/{kode} response. Field order is significant:
// MarshalDAWA preserves struct order to match DAWA byte-for-byte. This is the
// dawa-mode RENDER target; the data is fetched once by the store package and
// projected here, so the api renderer can project the same domain.Region into
// the English representation without re-querying.
type Region struct {
	Aendret       *string    `json:"ændret"`        // DAWA-internal mtime; best-effort here
	GeoVersion    *int       `json:"geo_version"`   // DAWA-internal; not derivable from raw
	GeoAendret    *string    `json:"geo_ændret"`    // DAWA-internal; best-effort here
	Bbox          [4]float64 `json:"bbox"`          // envelope-in-25832 -> 4326
	Visueltcenter [2]float64 `json:"visueltcenter"` // pole of inaccessibility (MIC)
	DagiID        *string    `json:"dagi_id"`       // id_lokalId
	Kode          string     `json:"kode"`
	Navn          string     `json:"navn"`
	Nuts2         *string    `json:"nuts2"`
	Href          string     `json:"href"`
}

// renderRegion projects a neutral domain.Region into the byte-exact DAWA shape.
// It copies the DAWA-computed fields verbatim (so the bytes match the goldens)
// and ignores the api-only fields (TrueBbox/GeoJSON).
func renderRegion(d *domain.Region, baseURL string) *Region {
	return &Region{
		Aendret:       d.Aendret,
		GeoVersion:    d.GeoVersion,
		GeoAendret:    d.Aendret, // best-effort: DAWA tracks these separately
		Bbox:          d.DawaBbox,
		Visueltcenter: d.DawaVisueltcenter,
		DagiID:        d.DagiID,
		Kode:          d.Kode,
		Navn:          d.Navn,
		Nuts2:         d.Nuts2,
		Href:          fmt.Sprintf("%s/regioner/%s", baseURL, d.Kode),
	}
}

// GetRegion returns the region with the given kode, or (nil, pgx.ErrNoRows).
func GetRegion(ctx context.Context, pool *pgxpool.Pool, kode, baseURL string) (*Region, error) {
	d, err := store.GetRegion(ctx, pool, kode)
	if err != nil {
		return nil, err
	}
	return renderRegion(d, baseURL), nil
}

// ListRegions returns all regions ordered by kode (DAWA's default order).
func ListRegions(ctx context.Context, pool *pgxpool.Pool, baseURL string) ([]*Region, error) {
	ds, err := store.ListRegions(ctx, pool)
	if err != nil {
		return nil, err
	}
	out := make([]*Region, len(ds))
	for i, d := range ds {
		out[i] = renderRegion(d, baseURL)
	}
	return out, nil
}
