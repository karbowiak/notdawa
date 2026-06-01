package ingest

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
)

// supplerendeBynavnFeature is the DAGI SupplerendeBynavn (geometry + identity).
type supplerendeBynavnFeature struct {
	IDLokalId      string `json:"id_lokalId"` // -> dagi_id (e.g. "654870")
	Navn           string `json:"navn"`
	KommuneLokalId string `json:"kommuneLokalId"` // -> dagi_kommuner.dagi_id
	Skala          string `json:"skala"`
	Geometri       string `json:"geometri"`
	Opdtid         string `json:"datafordelerOpdateringstid"`
}

// darSupplerendeBynavnFeature is the DAR SupplerendeBynavn (darstatus + the UUID
// husnummer joins on). supplerendeBynavn == DAGI SupplerendeBynavn.id_lokalId.
// Navn carries the bynavn text: across all DAR statuses its distinct set is the
// FULL v1 name universe (6552), a superset of the DAGI geometry-bearing names
// (6503) — the ~49 extra are address-less names with no DAGI polygon.
type darSupplerendeBynavnFeature struct {
	IDLokalId         string `json:"id_lokalId"`        // DAR UUID (husnummer.supplerende_bynavn)
	SupplerendeBynavn string `json:"supplerendeBynavn"` // == DAGI id_lokalId
	Navn              string `json:"navn"`
	Status            string `json:"status"`
}

// Supplerendebynavne ingests DAGI SupplerendeBynavn into dagi_supplerendebynavne,
// enriching each row with darstatus + the DAR join UUID from DAR SupplerendeBynavn
// (loaded into a map first), then precomputes the polylabel visueltcenter.
func Supplerendebynavne(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	res := Result{Register: "DAGI", Entity: "SupplerendeBynavn"}

	// DAR SupplerendeBynavn → map[dagi_id] = {dar_uuid, status}.
	var darRows []darSupplerendeBynavnFeature
	if file, runID, err := downloadAndDecode(ctx, pool, client, "DAR", "SupplerendeBynavn", &darRows); err != nil {
		return res, err
	} else if err := loadDarSupplerendeBynavnNames(ctx, pool, darRows, file.GenerationNumber); err != nil {
		failRun(ctx, pool, runID, err)
		return res, err
	} else if err := finishRun(ctx, pool, runID, len(darRows)); err != nil {
		fmt.Fprintf(os.Stderr, "notdawa: DAR SupplerendeBynavn names committed but ledger update failed for run %d: %v\n", runID, err)
	}
	type darInfo struct {
		uuid   string
		status int
	}
	darByDagi := map[string]darInfo{}
	for _, r := range darRows {
		st, _ := strconv.Atoi(r.Status)
		darByDagi[r.SupplerendeBynavn] = darInfo{uuid: r.IDLokalId, status: st}
	}

	r, err := dagiLoad(ctx, pool, client, "DAGI", "SupplerendeBynavn", "dagi_supplerendebynavne",
		`INSERT INTO dagi_supplerendebynavne
			(dagi_id, navn, kommune_lokalid, darstatus, dar_uuid, aendret, geom, generation_number)
		 VALUES ($1, $2, $3, $4, $5, $6::timestamptz, `+geomExpr("$7")+`, $8)`,
		func(f supplerendeBynavnFeature) bool { return f.Skala == fullResScale },
		func(f supplerendeBynavnFeature, gen int) []any {
			di := darByDagi[f.IDLokalId]
			var darstatus *int
			var darUUID *string
			if di.uuid != "" {
				darstatus = &di.status
				darUUID = &di.uuid
			}
			return []any{
				f.IDLokalId, f.Navn, nullIfEmpty(f.KommuneLokalId),
				darstatus, darUUID, nullIfEmpty(f.Opdtid), f.Geometri, gen,
			}
		})
	if err != nil {
		return res, err
	}
	res = r
	if err := fillPolylabel(ctx, pool, "dagi_supplerendebynavne", "dagi_id"); err != nil {
		return res, fmt.Errorf("supplerendebynavne polylabel: %w", err)
	}
	return res, nil
}

// loadDarSupplerendeBynavnNames replaces dar_supplerendebynavn with the DAR
// SupplerendeBynavn feed's (uuid, dagi_id, navn, status). The v1 /supplerendebynavne
// collection sources its DISTINCT navn set from here so it covers the full 6552
// names (including the ~49 address-less ones with no DAGI geometry). Rows with an
// empty navn or uuid are skipped; on a duplicate uuid the last row wins.
func loadDarSupplerendeBynavnNames(ctx context.Context, pool *pgxpool.Pool, rows []darSupplerendeBynavnFeature, gen int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "TRUNCATE dar_supplerendebynavn"); err != nil {
		return fmt.Errorf("truncate dar_supplerendebynavn: %w", err)
	}

	batch := &pgx.Batch{}
	queued := 0
	for _, r := range rows {
		if r.IDLokalId == "" || r.Navn == "" {
			continue
		}
		batch.Queue(`INSERT INTO dar_supplerendebynavn (dar_uuid, dagi_id, navn, status, generation_number)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (dar_uuid) DO UPDATE
				SET dagi_id = EXCLUDED.dagi_id, navn = EXCLUDED.navn,
				    status = EXCLUDED.status, generation_number = EXCLUDED.generation_number,
				    updated_at = now()`,
			r.IDLokalId, nullIfEmpty(r.SupplerendeBynavn), r.Navn, nullIntStr(r.Status), gen)
		queued++
	}
	if queued > 0 {
		br := tx.SendBatch(ctx, batch)
		for i := 0; i < queued; i++ {
			if _, err := br.Exec(); err != nil {
				br.Close()
				return fmt.Errorf("insert into dar_supplerendebynavn: %w", err)
			}
		}
		if err := br.Close(); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
