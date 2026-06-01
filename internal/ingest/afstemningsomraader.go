package ingest

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
)

// afstemningsomraadeFeature is the subset of raw DAGI Afstemningsområde we use.
type afstemningsomraadeFeature struct {
	IDLokalId               string `json:"id_lokalId"`
	Nummer                  string `json:"afstemningsomraadenummer"`
	Navn                    string `json:"navn"`
	KommuneLokalId          string `json:"kommuneLokalId"`
	OpstillingskredsLokalId string `json:"opstillingskredsLokalId"`
	AfstemningsstedNavn     string `json:"afstemningsstedNavn"`
	AfstemningsstedAdresse  string `json:"afstemningsstedAdresseLokalId"`
	Skala                   string `json:"skala"`
	Geometri                string `json:"geometri"`
	Opdtid                  string `json:"datafordelerOpdateringstid"`
}

// Afstemningsomraader ingests DAGI Afstemningsområde into dagi_afstemningsomraader
// (1314 full-res rows). Backs /afstemningsomraader and supplies opstillingskredse'
// kommuner[]. visueltcenter is precomputed (polylabel) in a follow-up step.
func Afstemningsomraader(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	res, err := dagiLoad(ctx, pool, client, "DAGI", "Afstemningsomraade", "dagi_afstemningsomraader",
		`INSERT INTO dagi_afstemningsomraader
			(dagi_id, nummer, navn, kommune_lokalid, opstillingskreds_lokalid,
			 afstemningssted_navn, afstemningssted_adresse_lokalid, aendret, geom, generation_number)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8::timestamptz, `+geomExpr("$9")+`, $10)`,
		func(f afstemningsomraadeFeature) bool { return f.Skala == fullResScale },
		func(f afstemningsomraadeFeature, gen int) []any {
			return []any{
				f.IDLokalId, f.Nummer, nullIfEmpty(f.Navn), nullIfEmpty(f.KommuneLokalId),
				nullIfEmpty(f.OpstillingskredsLokalId), nullIfEmpty(f.AfstemningsstedNavn),
				nullIfEmpty(f.AfstemningsstedAdresse), nullIfEmpty(f.Opdtid), f.Geometri, gen,
			}
		})
	if err != nil {
		return res, err
	}
	if err := computeAfstemPolylabel(ctx, pool); err != nil {
		return res, fmt.Errorf("afstemningsomraade polylabel: %w", err)
	}
	return res, nil
}

// computeAfstemPolylabel fills each afstemningsområde's visueltcenter (polylabel).
func computeAfstemPolylabel(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, "SELECT dagi_id, ST_AsGeoJSON(geom) FROM dagi_afstemningsomraader")
	if err != nil {
		return err
	}
	type job struct{ id, gj string }
	var jobs []job
	for rows.Next() {
		var id, gj string
		if err := rows.Scan(&id, &gj); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, job{id, gj})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, j := range jobs {
		pt, err := polylabelOfGeoJSON(j.gj)
		if err != nil {
			return fmt.Errorf("afstem %s: %w", j.id, err)
		}
		if _, err := pool.Exec(ctx,
			"UPDATE dagi_afstemningsomraader SET visueltcenter = ST_SetSRID(ST_MakePoint($1,$2),25832) WHERE dagi_id = $3",
			pt[0], pt[1], j.id); err != nil {
			return err
		}
	}
	return nil
}

// OpstillingskredseFinish precomputes the opstillingskredse visueltcenter
// (polylabel) and rebuilds the opstillingskreds_kommuner relation from the
// afstemningsområder. Requires dagi_opstillingskredse + dagi_afstemningsomraader
// + opstillingskreds_kredskommune to be loaded. DAWA orders kommuner[] with the
// kredskommune first (ord 0), then the remaining kommuner ascending by kode.
func OpstillingskredseFinish(ctx context.Context, pool *pgxpool.Pool) (Result, error) {
	res := Result{Register: "DERIVED", Entity: "opstillingskreds_finish"}

	// visueltcenter (polylabel on each opk's own geometry).
	rows, err := pool.Query(ctx, "SELECT nummer, ST_AsGeoJSON(geom) FROM dagi_opstillingskredse")
	if err != nil {
		return res, err
	}
	type job struct{ nummer, gj string }
	var jobs []job
	for rows.Next() {
		var n, gj string
		if err := rows.Scan(&n, &gj); err != nil {
			rows.Close()
			return res, err
		}
		jobs = append(jobs, job{n, gj})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	for _, j := range jobs {
		pt, err := polylabelOfGeoJSON(j.gj)
		if err != nil {
			return res, fmt.Errorf("opk %s polylabel: %w", j.nummer, err)
		}
		if _, err := pool.Exec(ctx,
			"UPDATE dagi_opstillingskredse SET visueltcenter = ST_SetSRID(ST_MakePoint($1,$2),25832) WHERE nummer = $3",
			pt[0], pt[1], j.nummer); err != nil {
			return res, err
		}
	}

	// kommuner[] relation: kredskommune first (ord 0), then the other kommuner
	// the opstillingskreds spans (from its afstemningsområder) ascending by kode.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "TRUNCATE opstillingskreds_kommuner"); err != nil {
		return res, err
	}
	tag, err := tx.Exec(ctx, `
		WITH spanned AS (
			SELECT DISTINCT o.nummer AS opk, k.kode AS kommunekode
			FROM dagi_opstillingskredse o
			JOIN dagi_afstemningsomraader a ON a.opstillingskreds_lokalid = o.dagi_id
			JOIN dagi_kommuner k ON k.dagi_id = a.kommune_lokalid
		),
		withkreds AS (
			-- ensure the kredskommune is present even if no afstemningsområde row carries it
			SELECT opk, kommunekode FROM spanned
			UNION
			SELECT o.nummer, kk.kommunekode
			FROM dagi_opstillingskredse o
			JOIN opstillingskreds_kredskommune kk ON kk.nummer = o.nummer
		)
		INSERT INTO opstillingskreds_kommuner (opstillingskredsnummer, kommunekode, ord)
		SELECT w.opk, w.kommunekode,
			CASE WHEN w.kommunekode = kk.kommunekode THEN 0 ELSE 1 END
		FROM withkreds w
		LEFT JOIN opstillingskreds_kredskommune kk ON kk.nummer = w.opk`)
	if err != nil {
		return res, fmt.Errorf("build opstillingskreds_kommuner: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return res, err
	}
	res.RowsLoaded = int(tag.RowsAffected())
	return res, nil
}
