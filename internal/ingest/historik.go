package ingest

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
)

// DAR virkning history (backs /historik/adgangsadresser and /historik/adresser).
//
// Source: the DAR *Bitemporal* TotalDownloads — every registrering+virkning
// version of every row, not just the current one. Keeping only rows whose
// registreringTil is null (the current registrering) yields exactly the
// virkning chain DAWA's historik endpoints emit (verified against the live
// API and the Postnummer bitemporal extract, 2026-06-11). The extracts are big
// (Husnummer ~3.6 GB zipped), so both loaders stream via streamZipRows.

// acquireBitempV3 resolves and downloads the latest national json Bitemporal
// TotalDownload for an entity, preferring the V3 schema (the relational
// *LokalId fields) like acquireV3 does for Current. ledgerEntity is the name
// recorded in ingest_runs — it must differ from the Current ingest's entity
// (e.g. "Husnummer/bitemporal" vs "Husnummer") because the ledger's uniqueness
// is (register, entity, type_of_download, generation) and both temporalities
// share TotalDownload + generation numbers.
func acquireBitempV3(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client, register, entity, ledgerEntity string) (datafordeler.FileDownload, string, int64, error) {
	files, err := client.ListAvailable(ctx, register)
	if err != nil {
		return datafordeler.FileDownload{}, "", 0, err
	}
	file, ok := pickLatestTotalVersion(files, entity, "json", "Bitemporal", "_V3_")
	if !ok {
		file, ok = pickLatestTotalVersion(files, entity, "json", "Bitemporal", "")
	}
	if !ok {
		return datafordeler.FileDownload{}, "", 0, fmt.Errorf("no Bitemporal json TotalDownload for %s/%s", register, entity)
	}
	runID, err := startRun(ctx, pool, register, ledgerEntity, file)
	if err != nil {
		return file, "", 0, err
	}
	path, err := acquireExtract(ctx, client, file)
	if err != nil {
		failRun(ctx, pool, runID, err)
		return file, "", runID, err
	}
	markDownloaded(ctx, pool, runID)
	return file, path, runID, nil
}

// splitVejmidte splits the "KKKK-VVVV" kommunekode-vejkode fast path. Older
// versions can predate the field; both parts land NULL then and the serving
// query falls back to the current husnummer row's attribution.
func splitVejmidte(v string) (*string, *string) {
	kk, vv, ok := strings.Cut(v, "-")
	if !ok || kk == "" || vv == "" {
		return nil, nil
	}
	return &kk, &vv
}

// husnummerHistFeature is the bitemporal DAR Husnummer row subset: the Current
// husnummerFeature fields the historik projection needs, plus the bitemporal
// interval ends.
type husnummerHistFeature struct {
	IDLokalId         string `json:"id_lokalId"`
	Status            string `json:"status"`
	Husnummertekst    string `json:"husnummertekst"`
	Vejmidte          string `json:"vejmidte"` // "KKKK-VVVV"
	NavngivenVej      string `json:"navngivenVej"`
	Postnummer        string `json:"postnummer"`
	SupplerendeBynavn string `json:"supplerendeBynavn"`
	Adgangspunkt      string `json:"adgangspunkt"`
	RegistreringTil   string `json:"registreringTil"`
	VirkningFra       string `json:"virkningFra"`
	VirkningTil       string `json:"virkningTil"`
}

// consolidateHist collapses each id's consecutive versions whose projected
// columns are all equal into one row spanning the run (classic gaps-and-
// islands). DAWA's historik emits a new version ONLY when a field of its
// projection changes — raw DAR splits versions on plenty of fields the
// projection drops (verified live 2026-06-11: a 12-version DAR chain with
// nothing visibly changing is ONE row on live DAWA, while a chain whose
// adgangspunktstatus flipped keeps its split). Merging at ingest also shrinks
// the table ~3× and keeps serve-time paging simple. cols are the projected
// (code-controlled) column names; adjacent means prev.virkning_slut ==
// next.virkning_start — a gap always splits.
func consolidateHist(ctx context.Context, pool *pgxpool.Pool, table string, cols []string) (int, error) {
	colList := strings.Join(cols, ", ")
	rowExpr := "(" + colList + ")"
	sql := fmt.Sprintf(`
		CREATE TEMP TABLE consolidated ON COMMIT DROP AS
		WITH ordered AS (
			SELECT *,
				CASE WHEN LAG(%s) OVER w IS DISTINCT FROM %s
					OR LAG(virkning_slut) OVER w IS DISTINCT FROM virkning_start
				THEN 1 ELSE 0 END AS brk
			FROM %s
			WINDOW w AS (PARTITION BY id ORDER BY virkning_start)
		), grouped AS (
			SELECT *, SUM(brk) OVER (PARTITION BY id ORDER BY virkning_start ROWS UNBOUNDED PRECEDING) AS grp
			FROM ordered
		)
		SELECT id, %s,
			MIN(virkning_start) AS virkning_start,
			CASE WHEN COUNT(*) FILTER (WHERE virkning_slut IS NULL) > 0 THEN NULL
				ELSE MAX(virkning_slut) END AS virkning_slut,
			MAX(generation_number) AS generation_number
		FROM grouped
		GROUP BY id, grp, %s`,
		rowExpr, rowExpr, table, colList, colList)

	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, sql); err != nil {
		return 0, fmt.Errorf("consolidate %s: %w", table, err)
	}
	if _, err := tx.Exec(ctx, "TRUNCATE "+table); err != nil {
		return 0, err
	}
	ct, err := tx.Exec(ctx, fmt.Sprintf(
		"INSERT INTO %s (id, %s, virkning_start, virkning_slut, generation_number) SELECT * FROM consolidated", table, colList))
	if err != nil {
		return 0, fmt.Errorf("consolidate insert %s: %w", table, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int(ct.RowsAffected()), nil
}

// HusnummerHistorik streams the DAR Husnummer Bitemporal total into
// dar_husnummer_hist, keeping the current registrering's virkning versions,
// then consolidates equal-projection runs. RowsLoaded is the consolidated count.
func HusnummerHistorik(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	res := Result{Register: "DAR", Entity: "Husnummer/bitemporal"}
	file, path, runID, err := acquireBitempV3(ctx, pool, client, "DAR", "Husnummer", "Husnummer/bitemporal")
	if err != nil {
		return res, err
	}
	res, err = streamZipRows(ctx, pool, res, file, path, runID, "dar_husnummer_hist",
		`INSERT INTO dar_husnummer_hist
			(id, dar_status, husnr, kommunekode, vejkode, navngivenvej, postnummer,
			 supplerendebynavn, adgangspunkt, virkning_start, virkning_slut, generation_number)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::timestamptz,$11::timestamptz,$12)`,
		func(f husnummerHistFeature) bool {
			return f.RegistreringTil == "" && f.IDLokalId != "" && f.VirkningFra != ""
		},
		func(f husnummerHistFeature, gen int) []any {
			kk, vv := splitVejmidte(f.Vejmidte)
			return []any{
				f.IDLokalId, nullIntStr(f.Status), nullIfEmpty(f.Husnummertekst), kk, vv,
				nullIfEmpty(f.NavngivenVej), nullIfEmpty(f.Postnummer),
				nullIfEmpty(f.SupplerendeBynavn), nullIfEmpty(f.Adgangspunkt),
				f.VirkningFra, nullIfEmpty(f.VirkningTil), gen,
			}
		})
	if err != nil {
		return res, err
	}
	res.RowsLoaded, err = consolidateHist(ctx, pool, "dar_husnummer_hist",
		[]string{"dar_status", "husnr", "kommunekode", "vejkode", "navngivenvej", "postnummer", "supplerendebynavn", "adgangspunkt"})
	return res, err
}

// adressepunktHistFeature is the bitemporal DAR Adressepunkt row subset: just
// the status lifecycle — live DAWA's historik versions adgangspunktstatus on
// the punkt's own virkning chain.
type adressepunktHistFeature struct {
	IDLokalId       string `json:"id_lokalId"`
	Status          string `json:"status"`
	RegistreringTil string `json:"registreringTil"`
	VirkningFra     string `json:"virkningFra"`
	VirkningTil     string `json:"virkningTil"`
}

// AdressepunktHistorik streams the DAR Adressepunkt Bitemporal total into
// dar_adressepunkt_hist, keeping the current registrering's virkning versions,
// then consolidates equal-status runs.
func AdressepunktHistorik(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	res := Result{Register: "DAR", Entity: "Adressepunkt/bitemporal"}
	file, path, runID, err := acquireBitempV3(ctx, pool, client, "DAR", "Adressepunkt", "Adressepunkt/bitemporal")
	if err != nil {
		return res, err
	}
	res, err = streamZipRows(ctx, pool, res, file, path, runID, "dar_adressepunkt_hist",
		`INSERT INTO dar_adressepunkt_hist (id, status, virkning_start, virkning_slut, generation_number)
		 VALUES ($1,$2,$3::timestamptz,$4::timestamptz,$5)`,
		func(f adressepunktHistFeature) bool {
			return f.RegistreringTil == "" && f.IDLokalId != "" && f.VirkningFra != ""
		},
		func(f adressepunktHistFeature, gen int) []any {
			return []any{f.IDLokalId, nullIfEmpty(f.Status), f.VirkningFra, nullIfEmpty(f.VirkningTil), gen}
		})
	if err != nil {
		return res, err
	}
	res.RowsLoaded, err = consolidateHist(ctx, pool, "dar_adressepunkt_hist", []string{"status"})
	return res, err
}

// HistorikSegments rebuilds dar_husnummer_hist_seg: each husnummer-history
// version is cut at the boundaries of its punkt's status versions, every
// segment labelled with the punkt status in force (null where the punkt did
// not exist yet / is unknown), then equal-projection runs are consolidated.
// /historik serves straight from this table, so paging stays byte-stable. A
// derived step — runs after the three bitemporal loads it reads from.
func HistorikSegments(ctx context.Context, pool *pgxpool.Pool) (Result, error) {
	res := Result{Register: "derived", Entity: "historik-segments"}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "TRUNCATE dar_husnummer_hist_seg"); err != nil {
		return res, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO dar_husnummer_hist_seg
			(id, dar_status, husnr, kommunekode, vejkode, navngivenvej, postnummer,
			 supplerendebynavn, adgangspunkt_status, kommunekode_resolved,
			 virkning_start, virkning_slut, generation_number)
		SELECT h.id, h.dar_status, h.husnr, h.kommunekode, h.vejkode, h.navngivenvej, h.postnummer,
			h.supplerendebynavn, p.status,
			-- the serve-time kommunekode filter expression, resolved here so the
			-- filter can use an index (dar_husnummer is fresh: it loads earlier
			-- in the same plan run)
			COALESCE(h.kommunekode, split_part(cur.vejmidte, '-', 1)),
			seg.s, NULLIF(seg.e, 'infinity'::timestamptz), h.generation_number
		FROM dar_husnummer_hist h
		LEFT JOIN dar_husnummer cur ON cur.id = h.id
		CROSS JOIN LATERAL (
			SELECT s, LEAD(s) OVER (ORDER BY s) AS e FROM (
				SELECT h.virkning_start AS s
				UNION
				SELECT COALESCE(h.virkning_slut, 'infinity'::timestamptz)
				UNION
				SELECT aph.virkning_start FROM dar_adressepunkt_hist aph
				WHERE aph.id = h.adgangspunkt
					AND aph.virkning_start > h.virkning_start
					AND aph.virkning_start < COALESCE(h.virkning_slut, 'infinity'::timestamptz)
				UNION
				SELECT aph.virkning_slut FROM dar_adressepunkt_hist aph
				WHERE aph.id = h.adgangspunkt AND aph.virkning_slut IS NOT NULL
					AND aph.virkning_slut > h.virkning_start
					AND aph.virkning_slut < COALESCE(h.virkning_slut, 'infinity'::timestamptz)
			) x
		) seg
		LEFT JOIN dar_adressepunkt_hist p
			ON p.id = h.adgangspunkt
			AND p.virkning_start <= seg.s AND seg.s < COALESCE(p.virkning_slut, 'infinity'::timestamptz)
		WHERE seg.e IS NOT NULL`); err != nil {
		return res, fmt.Errorf("segment dar_husnummer_hist: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return res, err
	}

	n, err := consolidateHist(ctx, pool, "dar_husnummer_hist_seg",
		[]string{"dar_status", "husnr", "kommunekode", "vejkode", "navngivenvej", "postnummer", "supplerendebynavn", "adgangspunkt_status", "kommunekode_resolved"})
	res.RowsLoaded = n
	return res, err
}

// vejHistFeature is the lean bitemporal row for the road entities: just the
// status lifecycle and the virkning interval (historik.oprettet/ændret/
// ikrafttrædelse derive from MIN/MAX over the raw chain — these tables are
// deliberately NOT consolidated, a projection-merge would destroy MAX).
type vejHistFeature struct {
	IDLokalId       string `json:"id_lokalId"`
	Status          string `json:"status"`
	RegistreringTil string `json:"registreringTil"`
	VirkningFra     string `json:"virkningFra"`
	VirkningTil     string `json:"virkningTil"`
}

// vejHistLoad is the shared loader for the two road-history entities.
func vejHistLoad(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client, entity, table string) (Result, error) {
	res := Result{Register: "DAR", Entity: entity + "/bitemporal"}
	file, path, runID, err := acquireBitempV3(ctx, pool, client, "DAR", entity, entity+"/bitemporal")
	if err != nil {
		return res, err
	}
	return streamZipRows(ctx, pool, res, file, path, runID, table,
		`INSERT INTO `+table+` (id, dar_status, virkning_start, virkning_slut, generation_number)
		 VALUES ($1,$2,$3::timestamptz,$4::timestamptz,$5)`,
		func(f vejHistFeature) bool {
			return f.RegistreringTil == "" && f.IDLokalId != "" && f.VirkningFra != ""
		},
		func(f vejHistFeature, gen int) []any {
			return []any{f.IDLokalId, nullIntStr(f.Status), f.VirkningFra, nullIfEmpty(f.VirkningTil), gen}
		})
}

// NavngivenVejHistorik streams the DAR NavngivenVej Bitemporal total into
// dar_navngivenvej_hist (raw reg-current versions).
func NavngivenVejHistorik(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	return vejHistLoad(ctx, pool, client, "NavngivenVej", "dar_navngivenvej_hist")
}

// NavngivenVejKommunedelHistorik streams the DAR NavngivenVejKommunedel
// Bitemporal total into dar_nvkommunedel_hist (the vejstykke history).
func NavngivenVejKommunedelHistorik(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	return vejHistLoad(ctx, pool, client, "NavngivenVejKommunedel", "dar_nvkommunedel_hist")
}

// adresseHistFeature is the bitemporal DAR Adresse row subset.
type adresseHistFeature struct {
	IDLokalId       string `json:"id_lokalId"`
	Status          string `json:"status"`
	Husnummer       string `json:"husnummer"`
	Etagebetegnelse string `json:"etagebetegnelse"`
	Doerbetegnelse  string `json:"doerbetegnelse"` // both spellings appear across DAR feeds
	DoerbetegnDansk string `json:"dørbetegnelse"`
	RegistreringTil string `json:"registreringTil"`
	VirkningFra     string `json:"virkningFra"`
	VirkningTil     string `json:"virkningTil"`
}

// doer returns whichever dørbetegnelse spelling the extract carried.
func (f adresseHistFeature) doer() string {
	if f.Doerbetegnelse != "" {
		return f.Doerbetegnelse
	}
	return f.DoerbetegnDansk
}

// AdresseHistorik streams the DAR Adresse Bitemporal total into
// dar_adresse_hist, keeping the current registrering's virkning versions,
// then consolidates equal-projection runs. RowsLoaded is the consolidated count.
func AdresseHistorik(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	res := Result{Register: "DAR", Entity: "Adresse/bitemporal"}
	file, path, runID, err := acquireBitempV3(ctx, pool, client, "DAR", "Adresse", "Adresse/bitemporal")
	if err != nil {
		return res, err
	}
	res, err = streamZipRows(ctx, pool, res, file, path, runID, "dar_adresse_hist",
		`INSERT INTO dar_adresse_hist
			(id, dar_status, husnummer, etage, doer, virkning_start, virkning_slut, generation_number)
		 VALUES ($1,$2,$3,$4,$5,$6::timestamptz,$7::timestamptz,$8)`,
		func(f adresseHistFeature) bool {
			return f.RegistreringTil == "" && f.IDLokalId != "" && f.VirkningFra != ""
		},
		func(f adresseHistFeature, gen int) []any {
			return []any{
				f.IDLokalId, nullIntStr(f.Status), nullIfEmpty(f.Husnummer),
				nullIfEmpty(f.Etagebetegnelse), nullIfEmpty(f.doer()),
				f.VirkningFra, nullIfEmpty(f.VirkningTil), gen,
			}
		})
	if err != nil {
		return res, err
	}
	res.RowsLoaded, err = consolidateHist(ctx, pool, "dar_adresse_hist",
		[]string{"dar_status", "husnummer", "etage", "doer"})
	return res, err
}
