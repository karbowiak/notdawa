package ingest

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
)

// adressepunktFeature is the subset of DAR Adressepunkt we load. The extract is
// ~496 MB JSON / ~3.7M rows, so it streams. position is WKT 'POINT(oest nord)'
// in EPSG:25832; the origin (oprindelse_*) attributes describe how the point was
// captured. Field names are camelCase Fildownload grunddata names — confirm the
// exact spellings of the origin attributes against the real extract (see
// ASSUMPTIONS below).
//
// ASSUMPTIONS (DAR field names to confirm against the real Adressepunkt extract):
//   - position: WKT POINT in 25832 (confirmed pattern; the grunddata geometry
//     field is "position").
//   - hoejde / tekstretning: stored as numbers (golden højde=22.1/8,
//     tekstretning=200/178.35). Likely raw camelCase "højde" / "tekstretning"
//     — JSON tags here use both the likely Danish spelling and ASCII fallbacks.
//   - oprindelseNoejagtighedsklasse, oprindelseTekniskStandard, oprindelseKilde,
//     oprindelseRegistrering: the origin block. adgangspunkt.kilde is an int code
//     (golden 5/1) while vejpunkt.kilde is a string ("Adressemyn"/"Ekstern") —
//     both stored here as text; the serving layer renders adgangspunkt.kilde as
//     an int when numeric.
type adressepunktFeature struct {
	IDLokalId    string `json:"id_lokalId"`
	Position     string `json:"position"`
	Hoejde       string `json:"højde"`
	HoejdeASCII  string `json:"hoejde"`
	Tekstretning string `json:"tekstretning"`

	OprNoejagtighed string `json:"oprindelse_nøjagtighedsklasse"`
	OprTeknisk      string `json:"oprindelse_tekniskStandard"`
	OprKilde        string `json:"oprindelse_kilde"`
	OprRegistrering string `json:"oprindelse_registrering"`

	// Alternate (camelCase / current-schema) spellings of the origin block.
	Noejagtighedsklasse string `json:"noejagtighedsklasse"`
	TekniskStandard     string `json:"tekniskStandard"`
	Kilde               string `json:"kilde"`
	Registrering        string `json:"registrering"`

	Status          string `json:"status"`
	RegistreringFra string `json:"registreringFra"`
}

// firstNonEmpty returns the first non-empty argument (or "").
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Adressepunkt streams DAR Adressepunkt into dar_adressepunkt, keeping current
// ("Gældende") points with a parseable position. V3-pinned + offline bypass via
// acquireV3 (same as the MAT loaders).
func Adressepunkt(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	return matStreamLoadGeneric(ctx, pool, client, "DAR", "Adressepunkt", "dar_adressepunkt",
		`INSERT INTO dar_adressepunkt
			(id_lokalid, geom, hoejde, tekstretning, noejagtighedsklasse,
			 tekniskstandard, kilde, aendret, status, generation_number)
		 VALUES ($1, `+geomExprMixed("$2")+`, $3, $4, $5, $6, $7, $8::timestamptz, $9, $10)
		 ON CONFLICT (id_lokalid) DO NOTHING`,
		func(f adressepunktFeature) bool {
			// Do NOT filter by status. DAR Adressepunkt status codes (8 ≈ 5.23M,
			// 9 ≈ 172k, 6 ≈ 592; all registreringTil=null) are point-lifecycle
			// states, not temporal markers — and Husnummer references both an
			// adgangspunkt and a vejpunkt by exact id_lokalId, so ~2× the current
			// husnummer count of points must be loadable. Every point has a
			// unique id_lokalId (PK), so loading all of them keeps the
			// adgangspunkt/vejpunkt joins 1:1 with whatever status the referenced
			// point happens to carry.
			return f.IDLokalId != "" && f.Position != ""
		},
		func(f adressepunktFeature, gen int) []any {
			return []any{
				f.IDLokalId, f.Position,
				nullFloatStr(firstNonEmpty(f.Hoejde, f.HoejdeASCII)),
				nullFloatStr(f.Tekstretning),
				nullIfEmpty(firstNonEmpty(f.OprNoejagtighed, f.Noejagtighedsklasse)),
				nullIfEmpty(firstNonEmpty(f.OprTeknisk, f.TekniskStandard)),
				nullIfEmpty(firstNonEmpty(f.OprKilde, f.Kilde)),
				nullIfEmpty(firstNonEmpty(f.OprRegistrering, f.Registrering)),
				nullIfEmpty(f.Status), gen,
			}
		})
}
