package ingest

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
)

// kommuneFeature is the subset of raw DAGI Kommuneinddeling grunddata we use.
type kommuneFeature struct {
	IDLokalId     string `json:"id_lokalId"`                 // -> dagi_id
	Kommunekode   string `json:"kommunekode"`                // -> kode
	Navn          string `json:"navn"`                       // -> navn
	RegionLokalId string `json:"regionLokalId"`              // -> dagi_regioner.dagi_id (region join)
	Udenfor       bool   `json:"udenforKommuneinddeling"`    // -> udenforkommuneinddeling
	Skala         string `json:"skala"`                      // scale filter
	Geometri      string `json:"geometri"`                   // WKT 25832
	Opdtid        string `json:"datafordelerOpdateringstid"` // best-effort -> aendret
}

// Kommuner ingests DAGI Kommuneinddeling into dagi_kommuner.
func Kommuner(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	return dagiLoad(ctx, pool, client, "DAGI", "Kommuneinddeling", "dagi_kommuner",
		`INSERT INTO dagi_kommuner (kode, navn, udenforkommuneinddeling, region_lokalid, dagi_id, aendret, geom, generation_number)
		 VALUES ($1, $2, $3, $4, $5, $6::timestamptz, `+geomExpr("$7")+`, $8)`,
		func(f kommuneFeature) bool { return f.Skala == fullResScale },
		func(f kommuneFeature, gen int) []any {
			return []any{f.Kommunekode, f.Navn, f.Udenfor, nullIfEmpty(f.RegionLokalId), nullIfEmpty(f.IDLokalId), nullIfEmpty(f.Opdtid), f.Geometri, gen}
		})
}
