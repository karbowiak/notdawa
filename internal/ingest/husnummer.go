package ingest

import (
	"context"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
)

// parsePointXY parses a WKT 2D point "POINT (x y)" into (*x, *y). Returns
// (nil, nil) for an empty/unparseable value so it lands as SQL NULL. Used for
// Husnummer.husnummerretning, the unit direction vector behind tekstretning.
func parsePointXY(wkt string) (*float64, *float64) {
	s := strings.TrimSpace(wkt)
	if s == "" {
		return nil, nil
	}
	i := strings.IndexByte(s, '(')
	j := strings.IndexByte(s, ')')
	if i < 0 || j < 0 || j <= i {
		return nil, nil
	}
	parts := strings.Fields(s[i+1 : j])
	if len(parts) < 2 {
		return nil, nil
	}
	x, err1 := strconv.ParseFloat(parts[0], 64)
	y, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil {
		return nil, nil
	}
	return &x, &y
}

// husnummerFeature is the subset of raw DAR Husnummer we load. The extract is
// ~620 MB JSON / ~3.7M rows, so it streams (matStreamLoadGeneric: DAR V3-pinned
// + NOTDAWA_INGEST_FILE offline bypass).
//
// The first block (navngivenVej/postnummer/kommuneinddeling/supplerendeBynavn/
// status) is the EXISTING link set that backs the vej/postnr endpoints —
// kommuneinddeling is a DAGI kommune id_lokalId (e.g. "389183", NOT a
// kommunekode), stored in the `kommune` column the vejstykker/navngivneveje
// queries join on (h.kommune = dagi_kommuner.dagi_id). DO NOT rename it.
//
// The second block is the full-attribute set the adgangsadresse needs:
// husnummertekst (-> husnr), vejmidte ("KKKK-VVVV" fast path for kommune+vejkode),
// the adgangspunkt/vejpunkt UUIDs (-> dar_adressepunkt), the DAGI sogn/afstemning
// inddeling UUIDs, the MAT jordstykke UUID, and the historik timestamps.
//
// ASSUMPTIONS (DAR Husnummer field names to confirm against the real extract):
//   - vejmidte: the "KKKK-VVVV" kommunekode-vejkode string (confirmed in spec).
//   - adgangspunkt / vejpunkt: the Adressepunkt UUIDs.
//   - sogneinddeling / afstemningsomraade: DAGI inddeling id_lokalId UUIDs.
//   - jordstykke: MAT Jordstykke id_lokalId.
//   - registreringFra / virkningFra / datafordelerOpdateringstid: historik trio
//     (oprettet / ikrafttrædelse / ændret) — best-effort DAWA-internal metadata.
type husnummerFeature struct {
	IDLokalId         string `json:"id_lokalId"`
	NavngivenVej      string `json:"navngivenVej"`      // -> dar_navngivenvej.id
	Postnummer        string `json:"postnummer"`        // -> dar_postnummer.id (uuid)
	Kommune           string `json:"kommuneinddeling"`  // DAGI kommune id_lokalId -> dagi_kommuner.dagi_id
	SupplerendeBynavn string `json:"supplerendeBynavn"` // -> dar SupplerendeBynavn UUID
	Status            string `json:"status"`

	Husnummertekst     string `json:"husnummertekst"`
	Husnummerretning   string `json:"husnummerretning"`   // WKT "POINT (dx dy)" unit vector -> adgangspunkt.tekstretning
	Vejmidte           string `json:"vejmidte"`           // "KKKK-VVVV"
	Adgangspunkt       string `json:"adgangspunkt"`       // -> dar_adressepunkt.id_lokalid
	Vejpunkt           string `json:"vejpunkt"`           // -> dar_adressepunkt.id_lokalid
	Sogneinddeling     string `json:"sogneinddeling"`     // DAGI Sogneinddeling id_lokalId
	Afstemningsomraade string `json:"afstemningsomraade"` // DAGI Afstemningsomraade id_lokalId
	Jordstykke         string `json:"jordstykke"`         // MAT Jordstykke id_lokalId
	RegistreringFra    string `json:"registreringFra"`    // -> oprettet
	VirkningFra        string `json:"virkningFra"`        // -> ikrafttraedelse
	Opdtid             string `json:"datafordelerOpdateringstid"`
}

// Husnummer streams DAR Husnummer into dar_husnummer, keeping only status-3
// ("Gældende") rows (the load-bearing DAWA filter, consistent with the road link
// tables). It populates BOTH the existing link columns (navngivenvej,
// postnummer_id, kommune, supplerende_bynavn, status) AND the full-attribute
// columns added in migration 022.
func Husnummer(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	return matStreamLoadGeneric(ctx, pool, client, "DAR", "Husnummer", "dar_husnummer",
		`INSERT INTO dar_husnummer
			(id, navngivenvej, postnummer_id, kommune, supplerende_bynavn, status, generation_number,
			 husnummertekst, vejmidte, adgangspunkt_id, vejpunkt_id, sogn_dagi_id, afstemning_dagi_id,
			 jordstykke_id, dar_status, oprettet, ikrafttraedelse, aendret,
			 husnummerretning_dx, husnummerretning_dy)
		 VALUES ($1, $2, $3, $4, $5, $6, $7,
			 $8, $9, $10, $11, $12, $13,
			 $14, $15, $16::timestamptz, $17::timestamptz, $18::timestamptz, $19, $20)
		 ON CONFLICT (id) DO NOTHING`,
		func(f husnummerFeature) bool { return f.Status == "3" },
		func(f husnummerFeature, gen int) []any {
			dx, dy := parsePointXY(f.Husnummerretning)
			return []any{
				f.IDLokalId, nullIfEmpty(f.NavngivenVej), nullIfEmpty(f.Postnummer),
				nullIfEmpty(f.Kommune), nullIfEmpty(f.SupplerendeBynavn), f.Status, gen,
				nullIfEmpty(f.Husnummertekst), nullIfEmpty(f.Vejmidte),
				nullIfEmpty(f.Adgangspunkt), nullIfEmpty(f.Vejpunkt),
				nullIfEmpty(f.Sogneinddeling), nullIfEmpty(f.Afstemningsomraade),
				nullIfEmpty(f.Jordstykke), nullIntStr(f.Status),
				nullIfEmpty(f.RegistreringFra), nullIfEmpty(f.VirkningFra), nullIfEmpty(f.Opdtid),
				dx, dy,
			}
		})
}
