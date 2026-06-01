package ingest

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
)

// postnummerFeature is the subset of raw DAGI Postnummerinddeling we use.
type postnummerFeature struct {
	IDLokalId  string `json:"id_lokalId"`                 // -> dagi_id ("192100")
	Postnummer string `json:"postnummer"`                 // -> nr ("2100")
	Navn       string `json:"navn"`                       // -> navn
	Skala      string `json:"skala"`                      // scale filter
	Geometri   string `json:"geometri"`                   // WKT 25832
	Opdtid     string `json:"datafordelerOpdateringstid"` // best-effort -> aendret
}

// Postnumre ingests DAGI Postnummerinddeling into dagi_postnumre (1089 full-res
// features). Mirrors the sogne recipe; keyed by nr instead of kode.
func Postnumre(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	return dagiLoad(ctx, pool, client, "DAGI", "Postnummerinddeling", "dagi_postnumre",
		`INSERT INTO dagi_postnumre (nr, navn, dagi_id, aendret, geom, generation_number)
		 VALUES ($1, $2, $3, $4::timestamptz, `+geomExpr("$5")+`, $6)`,
		func(f postnummerFeature) bool { return f.Skala == fullResScale },
		func(f postnummerFeature, gen int) []any {
			return []any{f.Postnummer, f.Navn, nullIfEmpty(f.IDLokalId), nullIfEmpty(f.Opdtid), f.Geometri, gen}
		})
}

// PostnumreKommuner (re)builds the address-based postnr↔kommune relation from
// already-loaded tables (no download): DISTINCT(dar_postnummer.postnr,
// dagi_kommuner.kode) over status-3 dar_husnummer rows. This is DAWA's
// definition — NOT a spatial intersection (which over-includes via slivers).
// Requires dar_husnummer + dar_postnummer + dagi_kommuner to be loaded first.
func PostnumreKommuner(ctx context.Context, pool *pgxpool.Pool) (Result, error) {
	res := Result{Register: "DERIVED", Entity: "postnumre_kommuner"}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)

	if _, err = tx.Exec(ctx, "TRUNCATE postnumre_kommuner"); err != nil {
		return res, fmt.Errorf("truncate postnumre_kommuner: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO postnumre_kommuner (nr, kommunekode)
		SELECT DISTINCT p.postnr, k.kode
		FROM dar_husnummer h
		JOIN dar_postnummer p ON p.id = h.postnummer_id
		JOIN dagi_kommuner  k ON k.dagi_id = h.kommune
		WHERE h.postnummer_id IS NOT NULL AND h.kommune IS NOT NULL`)
	if err != nil {
		return res, fmt.Errorf("build postnumre_kommuner: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return res, err
	}
	res.RowsLoaded = int(tag.RowsAffected())
	return res, nil
}
