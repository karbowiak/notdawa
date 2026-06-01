package ingest

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
)

// landsdelFeature is the subset of raw DAGI Landsdel grunddata we use.
type landsdelFeature struct {
	IDLokalId string `json:"id_lokalId"`                 // -> dagi_id
	NUTS3     string `json:"NUTS3vaerdi"`                // -> nuts3 (primary key)
	Navn      string `json:"navn"`                       // -> navn
	Skala     string `json:"skala"`                      // scale filter
	Geometri  string `json:"geometri"`                   // WKT 25832
	Opdtid    string `json:"datafordelerOpdateringstid"` // best-effort -> aendret
}

// Landsdele ingests DAGI Landsdel into dagi_landsdele.
func Landsdele(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	return dagiLoad(ctx, pool, client, "DAGI", "Landsdel", "dagi_landsdele",
		`INSERT INTO dagi_landsdele (nuts3, navn, dagi_id, aendret, geom, generation_number)
		 VALUES ($1, $2, $3, $4::timestamptz, `+geomExpr("$5")+`, $6)`,
		func(f landsdelFeature) bool { return f.Skala == fullResScale },
		func(f landsdelFeature, gen int) []any {
			return []any{f.NUTS3, f.Navn, nullIfEmpty(f.IDLokalId), nullIfEmpty(f.Opdtid), f.Geometri, gen}
		})
}
