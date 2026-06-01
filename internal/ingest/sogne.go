package ingest

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
)

// sogneFeature is the subset of raw DAGI Sogneinddeling grunddata we use.
type sogneFeature struct {
	IDLokalId string `json:"id_lokalId"`                 // -> dagi_id
	Sognekode string `json:"sognekode"`                  // -> kode
	Navn      string `json:"navn"`                       // -> navn
	Skala     string `json:"skala"`                      // scale filter
	Geometri  string `json:"geometri"`                   // WKT 25832
	Opdtid    string `json:"datafordelerOpdateringstid"` // best-effort -> aendret
}

// Sogne ingests DAGI Sogneinddeling into dagi_sogne (~2097 full-res features).
func Sogne(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	return dagiLoad(ctx, pool, client, "DAGI", "Sogneinddeling", "dagi_sogne",
		`INSERT INTO dagi_sogne (kode, navn, dagi_id, aendret, geom, generation_number)
		 VALUES ($1, $2, $3, $4::timestamptz, `+geomExpr("$5")+`, $6)`,
		func(f sogneFeature) bool { return f.Skala == fullResScale },
		func(f sogneFeature, gen int) []any {
			return []any{f.Sognekode, f.Navn, nullIfEmpty(f.IDLokalId), nullIfEmpty(f.Opdtid), f.Geometri, gen}
		})
}
