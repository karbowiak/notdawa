package ingest

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
)

// The DAR road link/lookup tables that, together with dar_navngivenvej, back the
// /navngivneveje, /vejstykker and /vejnavne endpoints. All three filter to raw
// status "3" on load — the load-bearing DAWA filter (every link table also
// carries nedlagt/henlagt rows that DAWA drops from the served output).

// nvKommunedelFeature is the vejstykke join table (one row per kommune+vejkode of
// a navngivenvej). The 94 MB JSON / 114356 rows load via streamLoad.
type nvKommunedelFeature struct {
	IDLokalId    string `json:"id_lokalId"` // the vejstykke id
	Status       string `json:"status"`
	Kommune      string `json:"kommune"`      // 4-char zero-padded
	Vejkode      string `json:"vejkode"`      // 4-char zero-padded
	NavngivenVej string `json:"navngivenVej"` // parent NavngivenVej.id_lokalId
}

// NavngivenVejKommunedel streams DAR NavngivenVejKommunedel into
// dar_navngivenvej_kommunedel, keeping only status-3 rows.
func NavngivenVejKommunedel(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	return streamLoad(ctx, pool, client, "DAR", "NavngivenVejKommunedel", "dar_navngivenvej_kommunedel",
		`INSERT INTO dar_navngivenvej_kommunedel (id, navngivenvej, kommune, vejkode, status, generation_number)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		func(f *nvKommunedelFeature) bool { return f.Status == "3" },
		func(f *nvKommunedelFeature, gen int) []any {
			return []any{f.IDLokalId, f.NavngivenVej, f.Kommune, f.Vejkode, f.Status, gen}
		})
}

// nvPostnummerFeature is the navngivenvej ↔ postnummer (UUID) membership table.
// 100 MB JSON / 120798 rows → streamLoad.
type nvPostnummerFeature struct {
	IDLokalId    string `json:"id_lokalId"`
	Status       string `json:"status"`
	NavngivenVej string `json:"navngivenVej"`
	Postnummer   string `json:"postnummer"` // dar_postnummer.id (uuid)
}

// NavngivenVejPostnummer streams DAR NavngivenVejPostnummer into
// dar_navngivenvej_postnummer, keeping only status-3 rows.
func NavngivenVejPostnummer(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	return streamLoad(ctx, pool, client, "DAR", "NavngivenVejPostnummer", "dar_navngivenvej_postnummer",
		`INSERT INTO dar_navngivenvej_postnummer (id, navngivenvej, postnummer_id, status, generation_number)
		 VALUES ($1, $2, $3, $4, $5)`,
		func(f *nvPostnummerFeature) bool { return f.Status == "3" },
		func(f *nvPostnummerFeature, gen int) []any {
			return []any{f.IDLokalId, f.NavngivenVej, f.Postnummer, f.Status, gen}
		})
}

// darPostnummerFeature resolves a postnummer UUID to its nr/navn. Tiny (1089
// rows) so it loads whole via dagiLoad.
type darPostnummerFeature struct {
	IDLokalId string `json:"id_lokalId"`
	Status    string `json:"status"`
	Navn      string `json:"navn"`
	Postnr    string `json:"postnr"`
}

// DARPostnummer loads the DAR Postnummer lookup into dar_postnummer (status-3).
func DARPostnummer(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	return dagiLoad(ctx, pool, client, "DAR", "Postnummer", "dar_postnummer",
		`INSERT INTO dar_postnummer (id, postnr, navn, status, generation_number)
		 VALUES ($1, $2, $3, $4, $5)`,
		func(f darPostnummerFeature) bool { return f.Status == "3" },
		func(f darPostnummerFeature, gen int) []any {
			return []any{f.IDLokalId, f.Postnr, nullIfEmpty(f.Navn), f.Status, gen}
		})
}
