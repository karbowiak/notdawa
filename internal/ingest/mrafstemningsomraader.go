package ingest

// CLI: case "menighedsraadsafstemningsomraader": res, err = ingest.MRAfstemningsomraader(cmd.Context(), pool, client)

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
)

// mrAfstemningsomraadeFeature is the subset of raw DAGI MRafstemningsområde
// (menighedsråd voting districts) we use. Verified against the live extract
// (DAGI MRafstemningsomraade Current TotalDownload json gen 645, 2128 full-res
// features): identity is id_lokalId (unique per row); navn is the district name;
// kommuneLokalId is the owning kommune; sognLokalId the parish; geometri the WKT
// MultiPolygon; skala the resolution; datafordelerOpdateringstid the change time.
// NOTE: MRafstemningsomraadenummer is present in the schema but is null on every
// feature in the current extract, so (kommune, nummer) is NOT a viable key — the
// table is keyed on dagi_id (id_lokalId) instead (see migration 026).
type mrAfstemningsomraadeFeature struct {
	IDLokalId      string `json:"id_lokalId"`
	Nummer         string `json:"MRafstemningsomraadenummer"`
	Navn           string `json:"navn"`
	KommuneLokalId string `json:"kommuneLokalId"`
	SognLokalId    string `json:"sognLokalId"`
	Skala          string `json:"skala"`
	Geometri       string `json:"geometri"`
	Opdtid         string `json:"datafordelerOpdateringstid"`
}

// MRAfstemningsomraader ingests DAGI MRafstemningsområde into
// dagi_mrafstemningsomraader (full-res rows). Backs
// /menighedsraadsafstemningsomraader and supplies the menighedsråd voting
// districts. visueltcenter is precomputed (polylabel) in a follow-up step.
func MRAfstemningsomraader(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	res, err := dagiLoad(ctx, pool, client, "DAGI", "MRafstemningsomraade", "dagi_mrafstemningsomraader",
		`INSERT INTO dagi_mrafstemningsomraader
			(dagi_id, nummer, navn, kommune_lokalid, sogn_lokalid, aendret, geom, generation_number)
		 VALUES ($1, $2, $3, $4, $5, $6::timestamptz, `+geomExpr("$7")+`, $8)`,
		func(f mrAfstemningsomraadeFeature) bool { return f.Skala == fullResScale },
		func(f mrAfstemningsomraadeFeature, gen int) []any {
			return []any{
				f.IDLokalId, nullIfEmpty(f.Nummer), nullIfEmpty(f.Navn), nullIfEmpty(f.KommuneLokalId),
				nullIfEmpty(f.SognLokalId), nullIfEmpty(f.Opdtid), f.Geometri, gen,
			}
		})
	if err != nil {
		return res, err
	}
	if err := fillPolylabel(ctx, pool, "dagi_mrafstemningsomraader", "dagi_id"); err != nil {
		return res, fmt.Errorf("mrafstemningsomraade polylabel: %w", err)
	}
	return res, nil
}
