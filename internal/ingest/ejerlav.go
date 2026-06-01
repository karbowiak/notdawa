package ingest

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
)

// ejerlavFeature is the subset of raw MAT Ejerlav grunddata we use. MAT is not
// multi-scale, so there is no skala filter — we load every row with geometry.
type ejerlavFeature struct {
	Ejerlavskode int    `json:"ejerlavskode"` // -> kode (already an int in raw)
	Ejerlavsnavn string `json:"ejerlavsnavn"` // -> navn
	Geometri     string `json:"geometri"`     // WKT MULTIPOLYGON 25832 (2D)
}

// Ejerlav ingests MAT Ejerlav (national extract, ~9033 rows) into mat_ejerlav.
func Ejerlav(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	return dagiLoad(ctx, pool, client, "MAT", "Ejerlav", "mat_ejerlav",
		`INSERT INTO mat_ejerlav (kode, navn, geom, generation_number)
		 VALUES ($1, $2, `+geomExpr("$3")+`, $4)`,
		func(f ejerlavFeature) bool { return f.Geometri != "" },
		func(f ejerlavFeature, gen int) []any {
			return []any{f.Ejerlavskode, f.Ejerlavsnavn, f.Geometri, gen}
		})
}
