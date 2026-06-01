package ingest

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
)

// regionFeature is the subset of the raw DAGI Regionsinddeling grunddata we use.
// The extract bundles every generalisation scale; we keep only skala 1:10.000.
type regionFeature struct {
	IDLokalId   string `json:"id_lokalId"`                 // -> dagi_id, e.g. "389099"
	Regionskode string `json:"regionskode"`                // -> kode, e.g. "1084"
	Navn        string `json:"navn"`                       // -> navn
	NUTS2       string `json:"NUTS2vaerdi"`                // -> nuts2, e.g. "DK01"
	Skala       string `json:"skala"`                      // generalisation scale filter
	Geometri    string `json:"geometri"`                   // WKT "MULTIPOLYGON Z(((... 25832)))"
	Opdtid      string `json:"datafordelerOpdateringstid"` // best-effort -> aendret
}

const fullResScale = "1:10.000"

// Regioner ingests DAGI Regionsinddeling into dagi_regioner.
func Regioner(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	return dagiLoad(ctx, pool, client, "DAGI", "Regionsinddeling", "dagi_regioner",
		`INSERT INTO dagi_regioner (kode, navn, nuts2, dagi_id, aendret, geom, generation_number)
		 VALUES ($1, $2, $3, $4, $5::timestamptz, `+geomExpr("$6")+`, $7)`,
		func(f regionFeature) bool { return f.Skala == fullResScale },
		func(f regionFeature, gen int) []any {
			return []any{f.Regionskode, f.Navn, nullIfEmpty(f.NUTS2), nullIfEmpty(f.IDLokalId), nullIfEmpty(f.Opdtid), f.Geometri, gen}
		})
}
