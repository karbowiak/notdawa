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

// DS truncates ds_steder + ds_stednavne, then loads every place-type extract
// into ds_steder and the Bitemporal Stednavn extract into ds_stednavne, and
// finally computes visueltcenter (point/line via SQL, polygon via the Go
// polylabel). NOTDAWA_INGEST_DIR (if set) serves the cached zips offline.
func DS(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	res := Result{Register: "DS", Entity: "steder+stednavne"}

	if _, err := pool.Exec(ctx, "TRUNCATE ds_steder, ds_stednavne"); err != nil {
		return res, fmt.Errorf("truncate ds tables: %w", err)
	}

	insertSQL := `INSERT INTO ds_steder
		(id_lokalId, objectid, hovedtype, undertype, bebyggelseskode, indbyggertal, aendret, geom, generation_number)
	 VALUES ($1, $2, $3, $4, $5, $6, $7::timestamptz, ` + geomExprMixed("$8") + `, $9)
	 ON CONFLICT (id_lokalId) DO NOTHING`

	total := 0
	for _, pt := range dsPlaceTypes {
		n, gen, err := dsLoadPlaceType(ctx, pool, client, pt, insertSQL)
		if err != nil {
			return res, fmt.Errorf("%s: %w", pt.Entity, err)
		}
		if gen > res.GenerationNumber {
			res.GenerationNumber = gen
		}
		total += n
		fmt.Fprintf(os.Stderr, "notdawa: ds %s loaded %d rows\n", pt.Entity, n)
	}
	res.RowsLoaded = total

	snN, err := dsLoadStednavne(ctx, pool, client)
	if err != nil {
		return res, fmt.Errorf("Stednavn: %w", err)
	}
	fmt.Fprintf(os.Stderr, "notdawa: ds Stednavn loaded %d rows\n", snN)

	if err := dsFillVisueltcenter(ctx, pool); err != nil {
		return res, fmt.Errorf("visueltcenter: %w", err)
	}
	return res, nil
}

// dsLoadPlaceType streams one place-type extract feature-by-feature into
// ds_steder (the extracts are top-level JSON arrays; several run to tens of MB,
// so streaming keeps memory bounded). Returns rows inserted and the extract's
// generation number. Empty extracts (FaergeruteLinje/Punkt, Rute) yield 0 rows.
func dsLoadPlaceType(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client, pt dsPlaceType, insertSQL string) (int, int, error) {
	file, path, runID, err := downloadEntity(ctx, pool, client, "DS", pt.Entity)
	if err != nil {
		return 0, 0, err
	}
	defer os.Remove(path)

	zr, rc, err := openZipMember(path)
	if err != nil {
		failRun(ctx, pool, runID, err)
		return 0, file.GenerationNumber, err
	}
	defer zr.Close()
	defer rc.Close()

	dec := json.NewDecoder(rc)
	if _, err = dec.Token(); err != nil { // consume the opening '['
		failRun(ctx, pool, runID, err)
		return 0, file.GenerationNumber, fmt.Errorf("read array start in %s: %w", file.FileName, err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		failRun(ctx, pool, runID, err)
		return 0, file.GenerationNumber, err
	}
	defer tx.Rollback(ctx)

	batch := &pgx.Batch{}
	n := 0
	for dec.More() {
		var p map[string]any
		if err = dec.Decode(&p); err != nil {
			failRun(ctx, pool, runID, err)
			return 0, file.GenerationNumber, fmt.Errorf("decode feature in %s: %w", file.FileName, err)
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
				failRun(ctx, pool, runID, err)
				return 0, file.GenerationNumber, fmt.Errorf("insert into ds_steder: %w", err)
			}
			batch = &pgx.Batch{}
		}
	}
	if batch.Len() > 0 {
		if err = drainBatch(ctx, tx, batch); err != nil {
			failRun(ctx, pool, runID, err)
			return 0, file.GenerationNumber, fmt.Errorf("insert into ds_steder: %w", err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		failRun(ctx, pool, runID, err)
		return 0, file.GenerationNumber, err
	}
	if err = finishRun(ctx, pool, runID, n); err != nil {
		fmt.Fprintf(os.Stderr, "notdawa: ingest committed but ledger update failed for run %d: %v\n", runID, err)
	}
	return n, file.GenerationNumber, nil
}

// dsLoadStednavne streams the Bitemporal Stednavn extract into ds_stednavne.
// The export is flat (no id_lokalId/registreringTil columns): the name's own key
// is "objectid" (-> ds_stednavne.id_lokalId) and it references its place via
// "navngivetSted_objectid" (-> place_objectid == ds_steder.objectid). Every row
// is the current version; rows are deduped defensively on the name's objectid.
func dsLoadStednavne(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (int, error) {
	file, path, runID, err := downloadEntityT(ctx, pool, client, "DS", "Stednavn", "Bitemporal")
	if err != nil {
		return 0, err
	}
	defer os.Remove(path)

	zr, rc, err := openZipMember(path)
	if err != nil {
		failRun(ctx, pool, runID, err)
		return 0, err
	}
	defer zr.Close()
	defer rc.Close()

	dec := json.NewDecoder(rc)
	if _, err = dec.Token(); err != nil { // consume the opening '['
		failRun(ctx, pool, runID, err)
		return 0, fmt.Errorf("read array start in %s: %w", file.FileName, err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		failRun(ctx, pool, runID, err)
		return 0, err
	}
	defer tx.Rollback(ctx)

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
			failRun(ctx, pool, runID, err)
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
				failRun(ctx, pool, runID, err)
				return 0, fmt.Errorf("insert into ds_stednavne: %w", err)
			}
			batch = &pgx.Batch{}
		}
	}
	if batch.Len() > 0 {
		if err = drainBatch(ctx, tx, batch); err != nil {
			failRun(ctx, pool, runID, err)
			return 0, fmt.Errorf("insert into ds_stednavne: %w", err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		failRun(ctx, pool, runID, err)
		return 0, err
	}
	if err = finishRun(ctx, pool, runID, n); err != nil {
		fmt.Fprintf(os.Stderr, "notdawa: ingest committed but ledger update failed for run %d: %v\n", runID, err)
	}
	return n, nil
}

// dsFillVisueltcenter sets the visueltcenter point for every ds_steder row:
// points use the point itself (ST_PointOnSurface), lines use the point on the
// line nearest its centroid (ST_ClosestPoint), and polygons use the Mapbox
// polylabel via fillPolylabelWhere (polylabelOfGeoJSON only handles polygons).
func dsFillVisueltcenter(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
		UPDATE ds_steder
		SET visueltcenter = ST_PointOnSurface(geom)
		WHERE GeometryType(geom) IN ('POINT', 'MULTIPOINT')`); err != nil {
		return fmt.Errorf("point visueltcenter: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE ds_steder
		SET visueltcenter = ST_ClosestPoint(geom, ST_Centroid(geom))
		WHERE GeometryType(geom) IN ('LINESTRING', 'MULTILINESTRING')`); err != nil {
		return fmt.Errorf("line visueltcenter: %w", err)
	}
	if err := fillPolylabelWhere(ctx, pool, "ds_steder", "id_lokalId",
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
