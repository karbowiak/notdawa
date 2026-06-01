package ingest

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
)

// retsPolitiFeature is the subset of raw DAGI Retskreds / Politikreds we use.
// DAWA's `kode` is the raw `myndighedskode` (e.g. 1101 Københavns Byret, 1460
// Nordjyllands Politi) — NOT retskredsnummer/politikredsnummer (1..24/1..12).
type retsPolitiFeature struct {
	IDLokalId      string `json:"id_lokalId"`
	Navn           string `json:"navn"`
	Skala          string `json:"skala"`
	Geometri       string `json:"geometri"`
	Opdtid         string `json:"datafordelerOpdateringstid"`
	Myndighedskode string `json:"myndighedskode"`
}

// Retskredse ingests DAGI Retskreds into dagi_retskredse (14 full-res areas) and
// precomputes the polylabel visueltcenter.
func Retskredse(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	res, err := dagiLoad(ctx, pool, client, "DAGI", "Retskreds", "dagi_retskredse",
		`INSERT INTO dagi_retskredse (kode, navn, dagi_id, aendret, geom, generation_number)
		 VALUES ($1, $2, $3, $4::timestamptz, `+geomExpr("$5")+`, $6)`,
		func(f retsPolitiFeature) bool { return f.Skala == fullResScale },
		func(f retsPolitiFeature, gen int) []any {
			return []any{f.Myndighedskode, f.Navn, nullIfEmpty(f.IDLokalId), nullIfEmpty(f.Opdtid), f.Geometri, gen}
		})
	if err != nil {
		return res, err
	}
	if err := fillPolylabel(ctx, pool, "dagi_retskredse", "kode"); err != nil {
		return res, fmt.Errorf("retskredse polylabel: %w", err)
	}
	return res, nil
}

// Politikredse ingests DAGI Politikreds into dagi_politikredse (14 full-res
// areas) and precomputes the polylabel visueltcenter.
func Politikredse(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	res, err := dagiLoad(ctx, pool, client, "DAGI", "Politikreds", "dagi_politikredse",
		`INSERT INTO dagi_politikredse (kode, navn, dagi_id, aendret, geom, generation_number)
		 VALUES ($1, $2, $3, $4::timestamptz, `+geomExpr("$5")+`, $6)`,
		func(f retsPolitiFeature) bool { return f.Skala == fullResScale },
		func(f retsPolitiFeature, gen int) []any {
			return []any{f.Myndighedskode, f.Navn, nullIfEmpty(f.IDLokalId), nullIfEmpty(f.Opdtid), f.Geometri, gen}
		})
	if err != nil {
		return res, err
	}
	if err := fillPolylabel(ctx, pool, "dagi_politikredse", "kode"); err != nil {
		return res, fmt.Errorf("politikredse polylabel: %w", err)
	}
	return res, nil
}
