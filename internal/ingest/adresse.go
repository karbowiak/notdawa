package ingest

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
)

// adresseFeature is the subset of raw DAR Adresse (the enhedsadresse / unit
// address) we load. The extract is ~436 MB JSON / ~4.5M rows, so it streams
// (matStreamLoadGeneric: DAR V3-pinned + NOTDAWA_INGEST_FILE offline bypass).
//
// ASSUMPTIONS (DAR Adresse field names to confirm against the real extract):
//   - husnummer: the Husnummer id_lokalId (the embedded adgangsadresse link).
//   - etagebetegnelse / doerbetegnelse: the etage/dør strings (golden etage "2",
//     dør "tv"). doerbetegnelse is the raw camelCase ("dørbetegnelse" in some
//     transforms) — both spellings are tagged below.
//   - registreringFra / virkningFra / datafordelerOpdateringstid: historik trio.
type adresseFeature struct {
	IDLokalId       string `json:"id_lokalId"`
	Husnummer       string `json:"husnummer"`       // -> dar_husnummer.id
	Etagebetegnelse string `json:"etagebetegnelse"` // -> etage
	Doerbetegnelse  string `json:"doerbetegnelse"`  // -> dør
	DoerbetegnDansk string `json:"dørbetegnelse"`   // alternate spelling
	Status          string `json:"status"`
	RegistreringFra string `json:"registreringFra"` // -> oprettet
	VirkningFra     string `json:"virkningFra"`     // -> ikrafttraedelse
	Opdtid          string `json:"datafordelerOpdateringstid"`
}

// Adresse streams DAR Adresse into dar_adresse, keeping only status-3
// ("Gældende") rows. Each row links to a Husnummer (the embedded adgangsadresse)
// and carries the etage/dør betegnelser.
func Adresse(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	return matStreamLoadGeneric(ctx, pool, client, "DAR", "Adresse", "dar_adresse",
		`INSERT INTO dar_adresse
			(id_lokalid, husnummer_id, etagebetegnelse, doerbetegnelse, status, dar_status,
			 oprettet, ikrafttraedelse, aendret, generation_number)
		 VALUES ($1, $2, $3, $4, $5, $6, $7::timestamptz, $8::timestamptz, $9::timestamptz, $10)
		 ON CONFLICT (id_lokalid) DO NOTHING`,
		func(f adresseFeature) bool { return f.Status == "3" && f.IDLokalId != "" },
		func(f adresseFeature, gen int) []any {
			return []any{
				f.IDLokalId, nullIfEmpty(f.Husnummer),
				nullIfEmpty(f.Etagebetegnelse), nullIfEmpty(firstNonEmpty(f.Doerbetegnelse, f.DoerbetegnDansk)),
				f.Status, nullIntStr(f.Status),
				nullIfEmpty(f.RegistreringFra), nullIfEmpty(f.VirkningFra), nullIfEmpty(f.Opdtid),
				gen,
			}
		})
}

// AdresserCore runs the address-core ingest in dependency order:
// Adressepunkt (geometry) → Husnummer (adgangsadresse) → Adresse (enhedsadresse).
func AdresserCore(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	steps := []struct {
		name string
		run  func() (Result, error)
	}{
		{"Adressepunkt", func() (Result, error) { return Adressepunkt(ctx, pool, client) }},
		{"Husnummer", func() (Result, error) { return Husnummer(ctx, pool, client) }},
		{"Adresse", func() (Result, error) { return Adresse(ctx, pool, client) }},
	}
	var last Result
	for _, s := range steps {
		r, e := s.run()
		if e != nil {
			return last, fmt.Errorf("%s: %w", s.name, e)
		}
		fmt.Println(r)
		last = r
	}
	return last, nil
}
