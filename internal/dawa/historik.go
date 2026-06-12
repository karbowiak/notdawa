package dawa

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// /historik/adgangsadresser and /historik/adresser: the DAR virkning history,
// one row per version, sourced from dar_husnummer_hist / dar_adresse_hist
// (the Bitemporal extracts filtered to the current registrering — see
// migrations/032_dar_historik.sql). Names and the adgangspunkt status resolve
// against the CURRENT mirror tables (dar_navngivenvej, dar_postnummer,
// dar_supplerendebynavn, dar_adressepunkt) — DAWA's historical responses agree
// with current-name lookups on every sampled row; revisit if a renamed road
// ever sample-diverges.

// HistorikAdgangsadresse is one virkning version of an adgangsadresse. Key
// order matches live DAWA byte-for-byte (frozen reference:
// tests/golden/r/historik_adgangsadresser_*).
type HistorikAdgangsadresse struct {
	ID                 string  `json:"id"`
	Status             int     `json:"status"`
	Adgangspunktstatus *int    `json:"adgangspunktstatus"`
	Kommunekode        *string `json:"kommunekode"`
	Vejkode            *string `json:"vejkode"`
	Vejnavn            *string `json:"vejnavn"`
	Husnr              *string `json:"husnr"`
	Supplerendebynavn  *string `json:"supplerendebynavn"`
	Postnr             *string `json:"postnr"`
	Postnrnavn         *string `json:"postnrnavn"`
	Virkningstart      string  `json:"virkningstart"`
	Virkningslut       *string `json:"virkningslut"`
}

// HistorikAdresse is one virkning version of an adresse: the husnummer
// attribution at that time plus the unit fields, trailing the interval like
// live DAWA does.
type HistorikAdresse struct {
	HistorikAdgangsadresse
	AdgangsadresseID *string `json:"adgangsadresseid"`
	Etage            *string `json:"etage"`
	Dør              *string `json:"dør"`
}

// histTS renders a stored timestamptz as DAWA's historik wire format
// (UTC, milliseconds, trailing Z).
const histTS = `YYYY-MM-DD"T"HH24:MI:SS.MS"Z"`

// husnummerHistProjection is the shared select list resolving one
// dar_husnummer_hist_seg row (aliased hh) to the response fields. The
// segment's adgangspunkt_status carries the punkt status in force DURING the
// segment (see ingest.HistorikSegments). The version's own vejmidte-derived
// kommunekode/vejkode wins; versions predating the field fall back to the
// current husnummer row's attribution.
const husnummerHistProjection = `
	hh.id,
	COALESCE(hh.dar_status, 0),
	hh.adgangspunkt_status,
	COALESCE(hh.kommunekode, split_part(cur.vejmidte, '-', 1)),
	COALESCE(hh.vejkode, split_part(cur.vejmidte, '-', 2)),
	nv.navn,
	hh.husnr,
	sb.navn,
	pn.postnr,
	pn.navn,
	to_char(hh.virkning_start AT TIME ZONE 'UTC', '` + histTS + `'),
	to_char(hh.virkning_slut  AT TIME ZONE 'UTC', '` + histTS + `')`

const husnummerHistJoins = `
	LEFT JOIN dar_navngivenvej     nv  ON nv.id = hh.navngivenvej
	LEFT JOIN dar_postnummer       pn  ON pn.id = hh.postnummer
	LEFT JOIN dar_supplerendebynavn sb ON sb.dar_uuid = hh.supplerendebynavn
	LEFT JOIN dar_husnummer        cur ON cur.id = hh.id`

// histFilter builds the WHERE clause for the shared id/postnr/kommunekode
// params against the husnummer-history attribution. Empty params don't filter.
func histFilter(idCol string, id, postnr, kommunekode string) (string, []any) {
	where := "TRUE"
	var args []any
	add := func(cond, v string) {
		if v != "" {
			args = append(args, v)
			where += fmt.Sprintf(" AND %s = $%d", cond, len(args))
		}
	}
	add(idCol, id)
	add("pn.postnr", postnr)
	add("COALESCE(hh.kommunekode, split_part(cur.vejmidte, '-', 1))", kommunekode)
	return where, args
}

// pageSQL appends LIMIT/OFFSET. limit<=0 means no paging (DAWA's no-per_side
// behaviour is the full dump).
func pageSQL(limit, offset int) string {
	if limit <= 0 {
		return ""
	}
	return fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
}

// punktStatusToDawa maps the DAR Adressepunkt status domain to DAWA's enum
// (1=gældende, 2=nedlagt, 3=foreløbig, 4=henlagt). The punkt domain differs
// from Husnummer's — the data holds 6/8/9: 8 = in-use (live shows 1), 9 =
// taken out of use (live shows 4 on chains whose punkt went 8→9), 6 = the
// foreløbig analogue. Verified/corrected by the live sampling sweep
// (tests/historik_sample_test.go). nil (no punkt resolved — historic punkts
// are absent from the Current mirror) stays nil, matching live's nulls.
func punktStatusToDawa(raw *string) *int {
	if raw == nil {
		return nil
	}
	var v int
	switch *raw {
	case "6":
		v = 3
	case "8":
		v = 1
	case "9":
		v = 4
	default:
		if _, err := fmt.Sscanf(*raw, "%d", &v); err != nil {
			return nil
		}
		v = darStatusToDawa(v)
	}
	return &v
}

// histProjEqual reports whether two historik rows agree on every projected
// field (NOT the interval). Used by mergeHistorikRuns.
func histProjEqual(a, b HistorikAdgangsadresse) bool {
	return a.ID == b.ID && a.Status == b.Status &&
		eqIntPtr(a.Adgangspunktstatus, b.Adgangspunktstatus) &&
		eqStrPtr(a.Kommunekode, b.Kommunekode) && eqStrPtr(a.Vejkode, b.Vejkode) &&
		eqStrPtr(a.Vejnavn, b.Vejnavn) && eqStrPtr(a.Husnr, b.Husnr) &&
		eqStrPtr(a.Supplerendebynavn, b.Supplerendebynavn) &&
		eqStrPtr(a.Postnr, b.Postnr) && eqStrPtr(a.Postnrnavn, b.Postnrnavn)
}

func eqStrPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func eqIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ListHistorikAdgangsadresser returns adgangsadresse virkning versions ordered
// by id, then chronologically — live DAWA's order.
func ListHistorikAdgangsadresser(ctx context.Context, pool *pgxpool.Pool, id, postnr, kommunekode string, limit, offset int) ([]HistorikAdgangsadresse, error) {
	where, args := histFilter("hh.id", id, postnr, kommunekode)
	rows, err := pool.Query(ctx, `
		SELECT `+husnummerHistProjection+`
		FROM dar_husnummer_hist_seg hh`+husnummerHistJoins+`
		WHERE `+where+`
		ORDER BY hh.id, hh.virkning_start`+pageSQL(limit, offset), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []HistorikAdgangsadresse{}
	for rows.Next() {
		var h HistorikAdgangsadresse
		var darStatus int
		var apStatus *string
		if err := rows.Scan(&h.ID, &darStatus, &apStatus, &h.Kommunekode, &h.Vejkode, &h.Vejnavn,
			&h.Husnr, &h.Supplerendebynavn, &h.Postnr, &h.Postnrnavn, &h.Virkningstart, &h.Virkningslut); err != nil {
			return nil, err
		}
		h.Status = darStatusToDawa(darStatus)
		h.Adgangspunktstatus = punktStatusToDawa(apStatus)
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Final RESOLVED-value merge: the ingest consolidates on stored refs, but a
	// ref can change without the resolved projection changing (punkt swapped,
	// same status) — live DAWA merges those too.
	merged := out[:0]
	for _, h := range out {
		if n := len(merged); n > 0 && histProjEqual(merged[n-1], h) &&
			merged[n-1].Virkningslut != nil && *merged[n-1].Virkningslut == h.Virkningstart {
			merged[n-1].Virkningslut = h.Virkningslut
			continue
		}
		merged = append(merged, h)
	}
	return merged, nil
}

// ListHistorikAdresser returns adresse virkning versions as SEGMENTS: each
// adresse version is intersected with the overlapping husnummer versions, so a
// husnummer-side change (new postnr, punkt status flip) mid-adresse-version
// splits the row exactly like live DAWA's flattened history does. Segment
// bounds are the interval intersection; equal-projection adjacent segments are
// merged afterwards.
func ListHistorikAdresser(ctx context.Context, pool *pgxpool.Pool, id, postnr, kommunekode string, limit, offset int) ([]HistorikAdresse, error) {
	where, args := histFilter("ah.id", id, postnr, kommunekode)
	rows, err := pool.Query(ctx, `
		SELECT `+husnummerHistProjection+`,
			ah.id, COALESCE(ah.dar_status, 0), ah.husnummer, ah.etage, ah.doer,
			to_char(GREATEST(ah.virkning_start, COALESCE(hh.virkning_start, ah.virkning_start)) AT TIME ZONE 'UTC', '`+histTS+`'),
			to_char(LEAST(COALESCE(ah.virkning_slut, 'infinity'), COALESCE(hh.virkning_slut, 'infinity')) AT TIME ZONE 'UTC', '`+histTS+`')
		FROM dar_adresse_hist ah
		LEFT JOIN dar_husnummer_hist_seg hh
			ON hh.id = ah.husnummer
			AND hh.virkning_start < COALESCE(ah.virkning_slut, 'infinity')
			AND ah.virkning_start < COALESCE(hh.virkning_slut, 'infinity')`+
		husnummerHistJoins+`
		WHERE `+where+`
		ORDER BY ah.id, GREATEST(ah.virkning_start, COALESCE(hh.virkning_start, ah.virkning_start))`+pageSQL(limit, offset), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []HistorikAdresse{}
	for rows.Next() {
		var h HistorikAdresse
		// The husnummer-side identity/interval are discarded (the segment's
		// identity is the ADRESSE version's; the bounds come pre-intersected
		// from the SELECT) — pointers because the LEFT JOIN can miss.
		var hhID, hhStart, hhSlut *string
		var darStatus, aDarStatus int
		var apStatus *string
		var aID, aStart string
		var aSlut *string
		if err := rows.Scan(&hhID, &darStatus, &apStatus, &h.Kommunekode, &h.Vejkode, &h.Vejnavn,
			&h.Husnr, &h.Supplerendebynavn, &h.Postnr, &h.Postnrnavn, &hhStart, &hhSlut,
			&aID, &aDarStatus, &h.AdgangsadresseID, &h.Etage, &h.Dør, &aStart, &aSlut); err != nil {
			return nil, err
		}
		h.ID = aID
		h.Status = darStatusToDawa(aDarStatus)
		h.Adgangspunktstatus = punktStatusToDawa(apStatus)
		h.Virkningstart = aStart
		if aSlut != nil && *aSlut == "infinity" {
			aSlut = nil
		}
		h.Virkningslut = aSlut
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	merged := out[:0]
	for _, h := range out {
		if n := len(merged); n > 0 && histProjEqual(merged[n-1].HistorikAdgangsadresse, h.HistorikAdgangsadresse) &&
			eqStrPtr(merged[n-1].AdgangsadresseID, h.AdgangsadresseID) &&
			eqStrPtr(merged[n-1].Etage, h.Etage) && eqStrPtr(merged[n-1].Dør, h.Dør) &&
			merged[n-1].Virkningslut != nil && *merged[n-1].Virkningslut == h.Virkningstart {
			merged[n-1].Virkningslut = h.Virkningslut
			continue
		}
		merged = append(merged, h)
	}
	return merged, nil
}
