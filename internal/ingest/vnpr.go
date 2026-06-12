package ingest

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/dawa"
)

// VNPRDerive rebuilds the precomputed /vejnavnpostnummerrelationer tables
// (vnpr_perrel + vnpr_agg). A derived step — runs after the DAR road/address
// and DAGI postnumre loads it reads from. The SQL lives in internal/dawa next
// to the serving expressions so the two cannot drift.
func VNPRDerive(ctx context.Context, pool *pgxpool.Pool) (Result, error) {
	res := Result{Register: "derived", Entity: "vnpr"}
	n, err := dawa.BuildVNPRTables(ctx, pool)
	res.RowsLoaded = n
	return res, err
}

// VejstykkePostnumreDerive rebuilds vejstykke_postnumre (the per-vejstykke
// postnumre[] precompute). Same lane as VNPRDerive.
func VejstykkePostnumreDerive(ctx context.Context, pool *pgxpool.Pool) (Result, error) {
	res := Result{Register: "derived", Entity: "vejstykke-postnumre"}
	n, err := dawa.BuildVejstykkePostnumre(ctx, pool)
	res.RowsLoaded = n
	return res, err
}
