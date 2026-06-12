package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
)

// DS (Danske Stednavne) is a union of ~27 place-type extracts loaded into a
// single ds_steder table (one row per feature, MIXED geometry kinds) plus a
// separate Bitemporal names table (ds_stednavne) joined on the place's objectid.
//
// Each extract is a top-level JSON ARRAY of flat feature objects (NOT a GeoJSON
// FeatureCollection): the geometry is WKT in the "geometri" property, the keys
// objectid/id_lokalId/registreringFra sit at the top level, and the per-type
// subtype lives under a type-specific key (UndertypeField). Bebyggelse
// additionally carries bebyggelseskode + indbyggertal. The Bitemporal Stednavn
// export is also flat: its own key is "objectid" and it references its place via
// navngivetSted_objectid == ds_steder.objectid.

// dsPlaceType describes one DS place-type extract: its Datafordeler entity name
// (== the zip token, e.g. "Soe"), the DAWA display Hovedtype, and the JSON key
// holding its subtype value.
type dsPlaceType struct {
	Entity         string // Datafordeler entity / zip token, e.g. "Soe"
	Hovedtype      string // DAWA display type, e.g. "Sø"
	UndertypeField string // JSON key holding the undertype, e.g. "soetype"
}

// dsPlaceTypes is the full set of DS place types: the 23 with distinct undertype
// fields plus the three empty extracts (FaergeruteLinje, FaergerutePunkt, Rute —
// 0 rows is fine; included with a best-guess undertype field). Hovedtype display
// names are reasonable Danish values and may need alignment with live DAWA in
// the serving phase (see notes).
var dsPlaceTypes = []dsPlaceType{
	{"AndenTopografiFlade", "Anden topografi", "andenTopografiType"},
	{"AndenTopografiPunkt", "Anden topografi", "andenTopografiType"},
	{"Bebyggelse", "bebyggelse", "bebyggelsestype"},
	{"Begravelsesplads", "Begravelsesplads", "begravelsespladstype"},
	{"Bygning", "Bygning", "bygningstype"},
	{"Campingplads", "Campingplads", "campingpladstype"},
	{"Farvand", "Farvand", "farvandstype"},
	{"Fortidsminde", "Fortidsminde", "fortidsmindetype"},
	{"Friluftsbad", "Friluftsbad", "friluftsbadtype"},
	{"Havnebassin", "Havnebassin", "havnebassintype"},
	{"Idraetsanlaeg", "Idrætsanlæg", "idraetsanlaegstype"},
	{"Jernbane", "Jernbane", "jernbanetype"},
	{"Landskabsform", "Landskabsform", "landskabsformtype"},
	{"Lufthavn", "Lufthavn", "lufthavnstype"},
	{"Naturareal", "Naturareal", "naturarealtype"},
	{"Navigationsanlaeg", "Navigationsanlæg", "navigationsanlaegstype"},
	{"Restriktionsareal", "Restriktionsareal", "restriktionsarealtype"},
	{"Sevaerdighed", "Seværdighed", "sevaerdighedstype"},
	{"Soe", "Sø", "soetype"},
	{"Standsningssted", "Standsningssted", "standsningsstedtype"},
	{"Terraenkontur", "Terrænkontur", "terraenkonturtype"},
	{"UrentFarvand", "Urent farvand", "urentFarvandType"},
	{"Vandloeb", "Vandløb", "vandloebstype"},
	{"Vej", "Vej", "vejtype"},
	{"FaergeruteLinje", "Færgerute", "faergerutetype"},
	{"FaergerutePunkt", "Færgerute", "faergerutetype"},
	{"Rute", "Rute", "rutetype"},
}

// DS loads every place-type extract into ds_steder and the Bitemporal Stednavn
// extract into ds_stednavne. All ~28 downloads complete FIRST; only then does
// one transaction TRUNCATE + reload both tables and compute visueltcenter
// (point/line via SQL, polygon via the Go polylabel) — so readers either see
// last week's complete data or this week's complete data, never an empty or
// partial table. (The old shape truncated up front and then performed 28
// network round-trips against a server that stalls regularly: a mid-sequence
// failure left /steder, /stednavne — and via the brofast EXISTS every island
// address — silently wrong for a week.) NOTDAWA_INGEST_DIR (if set) serves the
// cached zips offline.
func DS(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	res := Result{Register: "DS", Entity: "steder+stednavne"}

	// Phase 1: download everything before touching the serving tables.
	type dsDownload struct {
		pt    dsPlaceType
		file  datafordeler.FileDownload
		path  string
		runID int64
	}
	var dls []dsDownload
	// pending tracks ledger rows not yet finished/failed so an error on any
	// later download (or the load tx) closes ALL open rows loudly.
	var pending []int64
	failPending := func(cause error) {
		for _, id := range pending {
			failRun(ctx, pool, id, cause)
		}
	}
	for _, pt := range dsPlaceTypes {
		file, path, runID, err := downloadEntity(ctx, pool, client, "DS", pt.Entity)
		if err != nil {
			failPending(fmt.Errorf("aborted: sibling DS download %s failed: %w", pt.Entity, err))
			return res, fmt.Errorf("%s: %w", pt.Entity, err)
		}
		defer os.Remove(path)
		dls = append(dls, dsDownload{pt, file, path, runID})
		pending = append(pending, runID)
		if file.GenerationNumber > res.GenerationNumber {
			res.GenerationNumber = file.GenerationNumber
		}
	}
	snFile, snPath, snRunID, err := downloadEntityT(ctx, pool, client, "DS", "Stednavn", "Bitemporal")
	if err != nil {
		failPending(fmt.Errorf("aborted: DS Stednavn download failed: %w", err))
		return res, fmt.Errorf("Stednavn: %w", err)
	}
	defer os.Remove(snPath)
	pending = append(pending, snRunID)

	// Phase 2: one crash-atomic transaction over both serving tables.
	tx, err := pool.Begin(ctx)
	if err != nil {
		failPending(err)
		return res, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "TRUNCATE ds_steder, ds_stednavne"); err != nil {
		failPending(err)
		return res, fmt.Errorf("truncate ds tables: %w", err)
	}

	insertSQL := `INSERT INTO ds_steder
		(id_lokalId, objectid, hovedtype, undertype, bebyggelseskode, indbyggertal, aendret, geom, generation_number)
	 VALUES ($1, $2, $3, $4, $5, $6, $7::timestamptz, ` + geomExprMixed("$8") + `, $9)
	 ON CONFLICT (id_lokalId) DO NOTHING`

	total := 0
	counts := make(map[int64]int, len(dls)+1)
	for _, dl := range dls {
		n, err := dsLoadPlaceTypeInto(ctx, tx, dl.path, dl.file, dl.pt, insertSQL)
		if err != nil {
			failPending(err)
			return res, fmt.Errorf("%s: %w", dl.pt.Entity, err)
		}
		counts[dl.runID] = n
		total += n
		fmt.Fprintf(os.Stderr, "notdawa: ds %s loaded %d rows\n", dl.pt.Entity, n)
	}
	res.RowsLoaded = total
	if total == 0 {
		err := fmt.Errorf("DS: all place-type extracts yielded 0 rows — refusing to commit empty ds_steder")
		failPending(err)
		return res, err
	}

	snN, err := dsLoadStednavneInto(ctx, tx, snPath, snFile)
	if err != nil {
		failPending(err)
		return res, fmt.Errorf("Stednavn: %w", err)
	}
	if snN == 0 {
		err := fmt.Errorf("DS: Stednavn extract yielded 0 rows — refusing to commit empty ds_stednavne")
		failPending(err)
		return res, err
	}
	counts[snRunID] = snN
	fmt.Fprintf(os.Stderr, "notdawa: ds Stednavn loaded %d rows\n", snN)

	// visueltcenter inside the same tx: committed rows are complete rows.
	if err := dsFillVisueltcenter(ctx, tx); err != nil {
		failPending(err)
		return res, fmt.Errorf("visueltcenter: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		failPending(err)
		return res, err
	}
	for _, id := range pending {
		if err := finishRun(ctx, pool, id, counts[id]); err != nil {
			fmt.Fprintf(os.Stderr, "notdawa: ingest committed but ledger update failed for run %d: %v\n", id, err)
		}
	}
	return res, nil
}

// dsLoadPlaceTypeInto streams one already-downloaded place-type extract
// feature-by-feature into ds_steder within the caller's transaction (the
// extracts are top-level JSON arrays; several run to tens of MB, so streaming
// keeps memory bounded). Returns rows inserted. Empty extracts
// (FaergeruteLinje/Punkt, Rute) yield 0 rows. Ledger bookkeeping is the
// caller's (DS drives one tx over all extracts).
func dsLoadPlaceTypeInto(ctx context.Context, tx pgx.Tx, path string, file datafordeler.FileDownload, pt dsPlaceType, insertSQL string) (int, error) {
	zr, rc, err := openZipMember(path)
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	defer rc.Close()

	dec := json.NewDecoder(rc)
	if _, err = dec.Token(); err != nil { // consume the opening '['
		return 0, fmt.Errorf("read array start in %s: %w", file.FileName, err)
	}

	batch := &pgx.Batch{}
	n := 0
	for dec.More() {
		var p map[string]any
		if err = dec.Decode(&p); err != nil {
			return 0, fmt.Errorf("decode feature in %s: %w", file.FileName, err)
		}
		geometri := asString(p["geometri"])
		idLokal := asString(p["id_lokalId"])
		objectid := asString(p["objectid"])
		if geometri == "" || idLokal == "" || objectid == "" {
			continue // geom is NOT NULL; the identity keys are mandatory.
		}
		batch.Queue(insertSQL,
			idLokal,
			objectid,
			pt.Hovedtype,
			nullIfEmpty(asString(p[pt.UndertypeField])),
			asIntPtr(p["bebyggelseskode"]),
			asIntPtr(p["indbyggertal"]),
			nullIfEmpty(asString(p["registreringFra"])),
			geometri,
			file.GenerationNumber,
		)
		n++
		if batch.Len() >= streamChunk {
			if err = drainBatch(ctx, tx, batch); err != nil {
				return 0, fmt.Errorf("insert into ds_steder: %w", err)
			}
			batch = &pgx.Batch{}
		}
	}
	if batch.Len() > 0 {
		if err = drainBatch(ctx, tx, batch); err != nil {
			return 0, fmt.Errorf("insert into ds_steder: %w", err)
		}
	}
	if err = drainZipMember(rc); err != nil {
		return 0, fmt.Errorf("%s: %w", file.FileName, err)
	}
	return n, nil
}

// dsLoadStednavneInto streams the already-downloaded Bitemporal Stednavn
// extract into ds_stednavne within the caller's transaction. The export is
// flat (no id_lokalId/registreringTil columns): the name's own key is
// "objectid" (-> ds_stednavne.id_lokalId) and it references its place via
// "navngivetSted_objectid" (-> place_objectid == ds_steder.objectid). Every row
// is the current version; rows are deduped defensively on the name's objectid.
func dsLoadStednavneInto(ctx context.Context, tx pgx.Tx, path string, file datafordeler.FileDownload) (int, error) {
	zr, rc, err := openZipMember(path)
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	defer rc.Close()

	dec := json.NewDecoder(rc)
	if _, err = dec.Token(); err != nil { // consume the opening '['
		return 0, fmt.Errorf("read array start in %s: %w", file.FileName, err)
	}

	insertSQL := `INSERT INTO ds_stednavne
		(id_lokalId, place_objectid, skrivemaade, navnestatus, brugsprioritet, navnefoelgenummer, generation_number)
	 VALUES ($1, $2, $3, $4, $5, $6, $7)
	 ON CONFLICT (id_lokalId) DO NOTHING`

	batch := &pgx.Batch{}
	n := 0
	seen := map[string]bool{}
	for dec.More() {
		var p map[string]any
		if err = dec.Decode(&p); err != nil {
			return 0, fmt.Errorf("decode feature in %s: %w", file.FileName, err)
		}
		idLokal := asString(p["objectid"])
		placeObj := asString(p["navngivetSted_objectid"])
		skriv := asString(p["skrivemaade"])
		if idLokal == "" || placeObj == "" || skriv == "" {
			continue
		}
		if seen[idLokal] {
			continue
		}
		seen[idLokal] = true
		batch.Queue(insertSQL,
			idLokal,
			placeObj,
			skriv,
			nullIfEmpty(asString(p["navnestatus"])),
			nullIfEmpty(asString(p["brugsprioritet"])),
			asIntPtr(p["navnefoelgenummer"]),
			file.GenerationNumber,
		)
		n++
		if batch.Len() >= streamChunk {
			if err = drainBatch(ctx, tx, batch); err != nil {
				return 0, fmt.Errorf("insert into ds_stednavne: %w", err)
			}
			batch = &pgx.Batch{}
		}
	}
	if batch.Len() > 0 {
		if err = drainBatch(ctx, tx, batch); err != nil {
			return 0, fmt.Errorf("insert into ds_stednavne: %w", err)
		}
	}
	if err = drainZipMember(rc); err != nil {
		return 0, fmt.Errorf("%s: %w", file.FileName, err)
	}
	return n, nil
}

// dsFillVisueltcenter sets the visueltcenter point for every ds_steder row:
// points use the point itself (ST_PointOnSurface), lines use the point on the
// line nearest its centroid (ST_ClosestPoint), and polygons use the Mapbox
// polylabel via fillPolylabelWhere (polylabelOfGeoJSON only handles polygons).
// Runs against the DS() load transaction so committed rows are complete rows.
func dsFillVisueltcenter(ctx context.Context, db pgExec) error {
	if _, err := db.Exec(ctx, `
		UPDATE ds_steder
		SET visueltcenter = ST_PointOnSurface(geom)
		WHERE GeometryType(geom) IN ('POINT', 'MULTIPOINT')`); err != nil {
		return fmt.Errorf("point visueltcenter: %w", err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE ds_steder
		SET visueltcenter = ST_ClosestPoint(geom, ST_Centroid(geom))
		WHERE GeometryType(geom) IN ('LINESTRING', 'MULTILINESTRING')`); err != nil {
		return fmt.Errorf("line visueltcenter: %w", err)
	}
	if err := fillPolylabelWhere(ctx, db, "ds_steder", "id_lokalId",
		"GeometryType(geom) IN ('POLYGON', 'MULTIPOLYGON')"); err != nil {
		return fmt.Errorf("polygon visueltcenter: %w", err)
	}
	return nil
}

// asString coerces a decoded JSON value to a string. DS properties are mostly
// strings, but numbers are tolerated (rendered without exponent loss).
func asString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// asIntPtr coerces a decoded JSON value to *int, returning nil for empty/absent
// or non-numeric values (Bebyggelse's bebyggelseskode/indbyggertal and
// Stednavn's navnefoelgenummer arrive as numbers or numeric strings).
func asIntPtr(v any) *int {
	s := asString(v)
	if s == "" {
		return nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		i := int(f)
		return &i
	}
	if i, err := strconv.Atoi(s); err == nil {
		return &i
	}
	return nil
}
