package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
)

func pickLatestTotalVersion(files []datafordeler.FileDownload, entity, format, typeOfData, nameSubstr string) (datafordeler.FileDownload, bool) {
	var best datafordeler.FileDownload
	found := false
	for _, f := range files {
		if f.EntityName != entity || f.ContainedFileFormat != format {
			continue
		}
		if f.TypeOfData != typeOfData || f.TypeOfDownload != "TotalDownload" || f.MunicipalityCode != nil {
			continue
		}
		if !strings.Contains(f.FileName, nameSubstr) {
			continue
		}
		if !found || f.GenerationNumber > best.GenerationNumber {
			best, found = f, true
		}
	}
	return best, found
}

// synthMATFile builds a FileDownload descriptor from a local extract path, for
// offline ingest (NOTDAWA_INGEST_FILE). The MAT listing is paginated and
// version-inconsistent — e.g. the national json Current Lodflade total is only
// listed as V1 even though the V3 file is served by direct name — so offline
// ingest must not depend on it. The generation number is parsed from the
// trailing "_NNN" of the filename (best-effort; 0 if absent).
func synthMATFile(path, entity string) datafordeler.FileDownload {
	name := filepath.Base(path)
	gen := 0
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if i := strings.LastIndex(stem, "_"); i >= 0 {
		if v, err := strconv.Atoi(stem[i+1:]); err == nil {
			gen = v
		}
	}
	var size int64
	if fi, err := os.Stat(path); err == nil {
		size = fi.Size()
	}
	return datafordeler.FileDownload{
		FileName:            name,
		Register:            "MAT",
		EntityName:          entity,
		TypeOfDownload:      "TotalDownload",
		TypeOfData:          "Current",
		ContainedFileFormat: "json",
		GenerationNumber:    gen,
		FileSizeInBytes:     size,
	}
}

// matResolveV3 picks the national json Current TotalDownload for a MAT entity,
// preferring the V3 schema (the one carrying the relational *LokalId fields) but
// falling back to the highest-generation any-version national total when V3 is
// not present in the listing (e.g. Lodflade is listed only as V1 nationally).
func matResolveV3(files []datafordeler.FileDownload, entity string) (datafordeler.FileDownload, bool) {
	if f, ok := pickLatestTotalVersion(files, entity, "json", "Current", "_V3_"); ok {
		return f, true
	}
	return pickLatestTotalVersion(files, entity, "json", "Current", "")
}

func matAcquireV3(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client, entity string) (datafordeler.FileDownload, string, int64, error) {
	var file datafordeler.FileDownload
	if override := os.Getenv("NOTDAWA_INGEST_FILE"); override != "" {
		// Offline ingest: trust the local file, skip the network listing.
		file = synthMATFile(override, entity)
	} else {
		files, err := client.ListAvailable(ctx, "MAT")
		if err != nil {
			return datafordeler.FileDownload{}, "", 0, err
		}
		var ok bool
		file, ok = matResolveV3(files, entity)
		if !ok {
			return datafordeler.FileDownload{}, "", 0, fmt.Errorf("no Current json TotalDownload for MAT/%s", entity)
		}
	}
	runID, err := startRun(ctx, pool, "MAT", entity, file)
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

func matStreamLoad[F any](ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client,
	entity, table, insertSQL string, keep func(F) bool, args func(F, int) []any) (Result, error) {

	res := Result{Register: "MAT", Entity: entity}
	file, path, runID, err := matAcquireV3(ctx, pool, client, entity)
	if err != nil {
		return res, err
	}
	defer os.Remove(path)
	res.GenerationNumber = file.GenerationNumber

	zr, rc, err := openZipMember(path)
	if err != nil {
		failRun(ctx, pool, runID, err)
		return res, err
	}
	defer zr.Close()
	defer rc.Close()

	dec := json.NewDecoder(rc)
	if _, err = dec.Token(); err != nil {
		failRun(ctx, pool, runID, err)
		return res, fmt.Errorf("read array start in %s: %w", file.FileName, err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		failRun(ctx, pool, runID, err)
		return res, err
	}
	defer tx.Rollback(ctx)

	if _, err = tx.Exec(ctx, "TRUNCATE "+table); err != nil {
		failRun(ctx, pool, runID, err)
		return res, fmt.Errorf("truncate %s: %w", table, err)
	}

	batch := &pgx.Batch{}
	n := 0
	for dec.More() {
		var f F
		if err = dec.Decode(&f); err != nil {
			failRun(ctx, pool, runID, err)
			return res, fmt.Errorf("decode feature in %s: %w", file.FileName, err)
		}
		if keep != nil && !keep(f) {
			continue
		}
		batch.Queue(insertSQL, args(f, file.GenerationNumber)...)
		n++
		if batch.Len() >= streamChunk {
			if err = drainBatch(ctx, tx, batch); err != nil {
				failRun(ctx, pool, runID, err)
				return res, fmt.Errorf("insert into %s: %w", table, err)
			}
			batch = &pgx.Batch{}
		}
	}
	if batch.Len() > 0 {
		if err = drainBatch(ctx, tx, batch); err != nil {
			failRun(ctx, pool, runID, err)
			return res, fmt.Errorf("insert into %s: %w", table, err)
		}
	}
	if err = drainZipMember(rc); err != nil {
		failRun(ctx, pool, runID, err)
		return res, fmt.Errorf("%s: %w", file.FileName, err)
	}
	// Zero-row floor — see streamZipRows: never commit a TRUNCATE with 0 inserts.
	if n == 0 {
		err = fmt.Errorf("%s: extract %s yielded 0 rows for %s — refusing to commit empty table", res.Entity, file.FileName, table)
		failRun(ctx, pool, runID, err)
		return res, err
	}
	if err = tx.Commit(ctx); err != nil {
		failRun(ctx, pool, runID, err)
		return res, err
	}
	if err = finishRun(ctx, pool, runID, n); err != nil {
		fmt.Fprintf(os.Stderr, "notdawa: ingest committed but ledger update failed for run %d: %v\n", runID, err)
	}
	res.RowsLoaded = n
	return res, nil
}

type sfeFeature struct {
	IDLokalId string `json:"id_lokalId"`
	BFEnummer *int64 `json:"BFEnummer"`
}

func SamletFastEjendom(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	return matStreamLoad(ctx, pool, client, "SamletFastEjendom", "mat_sfe_bfe",
		`INSERT INTO mat_sfe_bfe (id_lokalid, bfenummer) VALUES ($1, $2)
		 ON CONFLICT (id_lokalid) DO NOTHING`,
		func(f sfeFeature) bool { return f.IDLokalId != "" },
		func(f sfeFeature, gen int) []any { return []any{f.IDLokalId, f.BFEnummer} })
}

type jordstykkeFeature struct {
	IDLokalId                   string `json:"id_lokalId"`
	Status                      string `json:"status"`
	Matrikelnummer              string `json:"matrikelnummer"`
	EjerlavLokalId              string `json:"ejerlavLokalId"`
	KommuneLokalId              string `json:"kommuneLokalId"`
	RegionLokalId               string `json:"regionLokalId"`
	SognLokalId                 string `json:"sognLokalId"`
	SamletFastEjendomLokalId    string `json:"samletFastEjendomLokalId"`
	RegistreretAreal            *int   `json:"registreretAreal"`
	Vejareal                    *int   `json:"vejareal"`
	Arealberegningsmetode       string `json:"arealberegningsmetode"`
	Faelleslod                  *bool  `json:"faelleslod"`
	StammerFraJordstykkeLokalId string `json:"stammerFraJordstykkeLokalId"`
	RegistreringFra             string `json:"registreringFra"`
}

func Jordstykke(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	res, err := matStreamLoad(ctx, pool, client, "Jordstykke", "mat_jordstykke",
		`INSERT INTO mat_jordstykke
			(id_lokalid, matrikelnr, ejerlav_kode, kommune_kode, region_kode, sogn_kode,
			 sfe_lokalid, registreretareal, vejareal, arealberegningsmetode_raw,
			 faelleslod, moder_lokalid, aendret, generation_number)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::timestamptz,$14)
		 ON CONFLICT (id_lokalid) DO NOTHING`,
		func(f jordstykkeFeature) bool {
			// DAWA serves only the current ("Gældende") parcel per
			// (ejerlav, matrikelnr); the extract also carries Historisk /
			// Foreløbig / Ikke gennemført variants, which is the sole source
			// of (ejerlav, matrikelnr) collisions. Filtering to Gældende both
			// matches DAWA and restores key uniqueness.
			if f.Status != "Gældende" {
				return false
			}
			if f.IDLokalId == "" || f.Matrikelnummer == "" {
				return false
			}
			_, err := strconv.Atoi(f.EjerlavLokalId)
			return err == nil
		},
		func(f jordstykkeFeature, gen int) []any {
			kode, _ := strconv.Atoi(f.EjerlavLokalId)
			return []any{
				f.IDLokalId, f.Matrikelnummer, kode,
				nullIfEmpty(f.KommuneLokalId), nullIfEmpty(f.RegionLokalId), nullIfEmpty(f.SognLokalId),
				nullIfEmpty(f.SamletFastEjendomLokalId), f.RegistreretAreal, f.Vejareal,
				nullIfEmpty(f.Arealberegningsmetode), f.Faelleslod,
				nullIfEmpty(f.StammerFraJordstykkeLokalId), nullIfEmpty(f.RegistreringFra), gen,
			}
		})
	if err != nil {
		return res, err
	}
	// The raw extract carries several registration variants (distinct id_lokalId)
	// per (ejerlav_kode, matrikelnr). DAWA serves exactly one current parcel per
	// key, so collapse to the most recently registered row (latest aendret; ties
	// broken by id_lokalid) — matching DAWA's "gældende" parcel.
	tag, derr := pool.Exec(ctx, `
		DELETE FROM mat_jordstykke
		WHERE id_lokalid IN (
			SELECT id_lokalid FROM (
				SELECT id_lokalid,
					row_number() OVER (
						PARTITION BY ejerlav_kode, matrikelnr
						ORDER BY aendret DESC NULLS LAST, id_lokalid DESC
					) AS rn
				FROM mat_jordstykke
			) t WHERE t.rn > 1
		)`)
	if derr != nil {
		return res, fmt.Errorf("dedup mat_jordstykke by (ejerlav,matrikelnr): %w", derr)
	}
	if removed := tag.RowsAffected(); removed > 0 {
		fmt.Fprintf(os.Stderr, "notdawa: mat jordstykke deduped %d non-current registration variants\n", removed)
		res.RowsLoaded -= int(removed)
	}
	return res, nil
}

type lodfladeFeature struct {
	IDLokalId         string `json:"id_lokalId"`
	JordstykkeLokalId string `json:"jordstykkeLokalId"`
	Status            string `json:"status"`
	Geometri          string `json:"geometri"`
}

func Lodflade(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	res, err := matStreamLoad(ctx, pool, client, "Lodflade", "mat_lodflade",
		`INSERT INTO mat_lodflade (id_lokalid, jordstykke_lokalid, geom, generation_number)
		 VALUES ($1, $2, `+geomExprMixed("$3")+`, $4)
		 ON CONFLICT (id_lokalid) DO NOTHING`,
		func(f lodfladeFeature) bool {
			// Only current faces contribute to the served parcel geometry.
			if f.Status != "" && f.Status != "Gældende" {
				return false
			}
			return f.Geometri != "" && f.JordstykkeLokalId != "" && f.IDLokalId != ""
		},
		func(f lodfladeFeature, gen int) []any {
			return []any{f.IDLokalId, f.JordstykkeLokalId, f.Geometri, gen}
		})
	if err != nil {
		return res, err
	}
	fmt.Fprintf(os.Stderr, "notdawa: mat lodflade staged %d faces; aggregating into mat_jordstykke.geom…\n", res.RowsLoaded)
	tag, err := pool.Exec(ctx, `
		UPDATE mat_jordstykke j
		SET geom = agg.g
		FROM (
			SELECT jordstykke_lokalid,
				ST_Multi(ST_CollectionExtract(ST_MakeValid(ST_Collect(geom)), 3)) AS g
			FROM mat_lodflade
			GROUP BY jordstykke_lokalid
		) agg
		WHERE agg.jordstykke_lokalid = j.id_lokalid`)
	if err != nil {
		return res, fmt.Errorf("aggregate lodflade into jordstykke geom: %w", err)
	}
	fmt.Fprintf(os.Stderr, "notdawa: mat jordstykke geom updated for %d parcels\n", tag.RowsAffected())
	return res, nil
}

func MAT(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	steps := []struct {
		name string
		run  func() (Result, error)
	}{
		{"SamletFastEjendom", func() (Result, error) { return SamletFastEjendom(ctx, pool, client) }},
		{"Jordstykke", func() (Result, error) { return Jordstykke(ctx, pool, client) }},
		{"Lodflade", func() (Result, error) { return Lodflade(ctx, pool, client) }},
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
