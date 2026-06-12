package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
	"github.com/karbowiak/notdawa/internal/geo"
)

// The DAGI election chain. Opstillingskredse carry the geometry; storkredse and
// valglandsdele DERIVE their geometry by unioning opstillingskredse (DAWA does
// not use the clipped raw Storkreds/Valglandsdel polygons). visueltcenter is the
// Mapbox polylabel of the union's largest sub-polygon, precomputed at ingest.

// opstillingskredsFeature is the subset of raw DAGI Opstillingskreds we use. The
// grouping key for a storkreds is storkredsnummer (storkredsLokalId is unique
// per opstillingskreds despite its name — verified, do NOT group on it).
type opstillingskredsFeature struct {
	IDLokalId        string `json:"id_lokalId"`
	Nummer           string `json:"opstillingskredsnummer"`
	Navn             string `json:"navn"`
	StorkredsLokalId string `json:"storkredsLokalId"`
	Storkredsnummer  string `json:"storkredsnummer"`
	Valgkredsnummer  string `json:"valgkredsnummer"` // served field; non-monotonic vs nummer
	Kredskommunekode string `json:"kredskommunekode"`
	Valglandsdel     string `json:"valglandsdelsbogstav"` // raw is unreliable; bogstav comes from Storkreds
	Skala            string `json:"skala"`
	Geometri         string `json:"geometri"`
	Opdtid           string `json:"datafordelerOpdateringstid"`
}

// Opstillingskredse ingests DAGI Opstillingskreds into dagi_opstillingskredse
// (the geometry source for the whole election chain; also the future
// /opstillingskredse endpoint).
func Opstillingskredse(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	return dagiLoad(ctx, pool, client, "DAGI", "Opstillingskreds", "dagi_opstillingskredse",
		`INSERT INTO dagi_opstillingskredse
			(nummer, navn, dagi_id, storkreds_lokalid, storkredsnummer, valgkredsnummer, kredskommunekode,
			 valglandsdelsbogstav, aendret, geom, generation_number)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::timestamptz, `+geomExpr("$10")+`, $11)`,
		func(f opstillingskredsFeature) bool { return f.Skala == fullResScale },
		func(f opstillingskredsFeature, gen int) []any {
			return []any{
				f.Nummer, f.Navn, nullIfEmpty(f.IDLokalId), nullIfEmpty(f.StorkredsLokalId),
				nullIfEmpty(f.Storkredsnummer), nullIfEmpty(f.Valgkredsnummer), nullIfEmpty(f.Kredskommunekode),
				nullIfEmpty(f.Valglandsdel), nullIfEmpty(f.Opdtid), f.Geometri, gen,
			}
		})
}

// storkredsAttrFeature reads the attributes (not geometry) of a Storkreds.
type storkredsAttrFeature struct {
	IDLokalId           string `json:"id_lokalId"`
	Nummer              string `json:"storkredsnummer"`
	Navn                string `json:"navn"`
	ValglandsdelLokalId string `json:"valglandsdelLokalId"` // bare 6-digit, e.g. "218522"
	Skala               string `json:"skala"`
	Opdtid              string `json:"datafordelerOpdateringstid"`
}

// valglandsdelAttrFeature reads the attributes of a Valglandsdel (any scale —
// the entity has no 1:10.000 scale; dedupe by bogstav).
type valglandsdelAttrFeature struct {
	IDLokalId string `json:"id_lokalId"`
	Bogstav   string `json:"valglandsdelsbogstav"`
	Navn      string `json:"navn"`
	Opdtid    string `json:"datafordelerOpdateringstid"`
}

// storkredsRegionskode is DAWA's hardcoded storkreds→region map (absent from
// every DAGI extract; from DAWA's packages/server/data/storkredse.json).
var storkredsRegionskode = map[string]string{
	"1": "1084", "2": "1084", "3": "1084", "4": "1084",
	"5": "1085", "6": "1083", "7": "1083",
	"8": "1082", "9": "1082", "10": "1081",
}

// StorkredseValglandsdele builds dagi_storkredse and dagi_valglandsdele from the
// already-loaded dagi_opstillingskredse plus the Storkreds/Valglandsdel attribute
// extracts. Requires Opstillingskredse to have run first.
func StorkredseValglandsdele(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	res := Result{Register: "DAGI", Entity: "Storkreds+Valglandsdel"}

	var stork []storkredsAttrFeature
	sf, sfRun, err := downloadAndDecode(ctx, pool, client, "DAGI", "Storkreds", &stork)
	if err != nil {
		return res, err
	}
	res.GenerationNumber = sf.GenerationNumber

	var vld []valglandsdelAttrFeature
	_, vldRun, err := downloadAndDecode(ctx, pool, client, "DAGI", "Valglandsdel", &vld)
	if err != nil {
		failRun(ctx, pool, sfRun, fmt.Errorf("aborted: Valglandsdel download failed: %w", err))
		return res, err
	}
	// Both ledger rows stay open until the load tx commits (they used to be
	// abandoned at 'downloaded' forever).
	fail := func(cause error) {
		failRun(ctx, pool, sfRun, cause)
		failRun(ctx, pool, vldRun, cause)
	}
	// bogstav -> navn (dedupe; all scales carry the same navn) and the bare
	// 6-digit suffix of valglandsdel.id_lokalId -> bogstav, used to resolve a
	// storkreds's bogstav from its valglandsdelLokalId (storkreds's own
	// valglandsdelsbogstav field is null at 1:10.000).
	vldNavn := map[string]string{}
	bareToBogstav := map[string]string{}
	for _, v := range vld {
		if v.Bogstav == "" {
			continue
		}
		vldNavn[v.Bogstav] = v.Navn
		if len(v.IDLokalId) >= 6 {
			bareToBogstav[v.IDLokalId[len(v.IDLokalId)-6:]] = v.Bogstav
		}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		fail(err)
		return res, err
	}
	defer tx.Rollback(ctx)

	if _, err = tx.Exec(ctx, "TRUNCATE dagi_storkredse"); err != nil {
		fail(err)
		return res, err
	}
	if _, err = tx.Exec(ctx, "TRUNCATE dagi_valglandsdele"); err != nil {
		fail(err)
		return res, err
	}

	// Storkredse: geom = union of the opstillingskredse whose storkreds_lokalid
	// matches this storkreds's id_lokalId ($5). NOTE: opstillingskreds.storkredsnummer
	// is null/unreliable in the raw data — storkreds_lokalid (10 distinct values) is
	// the real grouping key. Match it to Storkreds.id_lokalId.
	seen := map[string]bool{}
	for _, s := range stork {
		if s.Skala != fullResScale || seen[s.Nummer] {
			continue
		}
		seen[s.Nummer] = true
		bogstav := bareToBogstav[s.ValglandsdelLokalId]
		_, err = tx.Exec(ctx, `
			INSERT INTO dagi_storkredse
				(nummer, navn, regionskode, valglandsdelsbogstav, dagi_id, aendret, geom, generation_number)
			SELECT $1, $2, $3, $4, $5, $6::timestamptz,
				ST_Multi(ST_Union(o.geom)), $7
			FROM dagi_opstillingskredse o
			WHERE o.storkreds_lokalid = $5`,
			s.Nummer, s.Navn, nullIfEmpty(storkredsRegionskode[s.Nummer]), nullIfEmpty(bogstav),
			nullIfEmpty(s.IDLokalId), nullIfEmpty(s.Opdtid), sf.GenerationNumber)
		if err != nil {
			fail(err)
			return res, fmt.Errorf("insert storkreds %s: %w", s.Nummer, err)
		}
		res.RowsLoaded++
	}
	// Zero-row floor: an attribute extract published without the 1:10.000 scale
	// would otherwise silently empty both election tables.
	if res.RowsLoaded == 0 {
		err = fmt.Errorf("Storkreds extract yielded 0 rows at scale %q — refusing to commit empty election tables", fullResScale)
		fail(err)
		return res, err
	}

	// Valglandsdele: geom = union of the storkredse with that bogstav.
	for bogstav, navn := range vldNavn {
		_, err = tx.Exec(ctx, `
			INSERT INTO dagi_valglandsdele (bogstav, navn, aendret, geom, generation_number)
			SELECT $1, $2, NULL,
				ST_Multi(ST_Union(s.geom)), $3
			FROM dagi_storkredse s
			WHERE s.valglandsdelsbogstav = $1`,
			bogstav, navn, sf.GenerationNumber)
		if err != nil {
			fail(err)
			return res, fmt.Errorf("insert valglandsdel %s: %w", bogstav, err)
		}
	}

	if err = tx.Commit(ctx); err != nil {
		fail(err)
		return res, err
	}
	for _, id := range []int64{sfRun, vldRun} {
		if err := finishRun(ctx, pool, id, res.RowsLoaded); err != nil {
			fmt.Fprintf(os.Stderr, "notdawa: ingest committed but ledger update failed for run %d: %v\n", id, err)
		}
	}

	// Precompute visueltcenter (Mapbox polylabel) for both tables.
	if err = computeElectionVisualCenters(ctx, pool); err != nil {
		return res, fmt.Errorf("polylabel: %w", err)
	}
	return res, nil
}

// computeElectionVisualCenters fills the visueltcenter point of every storkreds
// and valglandsdel with the polylabel of the union's largest sub-polygon
// (computed in 25832, matching DAWA's @mapbox/polylabel at 1 m precision).
func computeElectionVisualCenters(ctx context.Context, pool *pgxpool.Pool) error {
	for _, t := range []struct{ table, key string }{
		{"dagi_storkredse", "nummer"},
		{"dagi_valglandsdele", "bogstav"},
	} {
		rows, err := pool.Query(ctx, fmt.Sprintf(
			"SELECT %s, ST_AsGeoJSON(geom) FROM %s", t.key, t.table))
		if err != nil {
			return err
		}
		type job struct{ key, gj string }
		var jobs []job
		for rows.Next() {
			var k, gj string
			if err := rows.Scan(&k, &gj); err != nil {
				rows.Close()
				return err
			}
			jobs = append(jobs, job{k, gj})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, j := range jobs {
			pt, err := polylabelOfGeoJSON(j.gj)
			if err != nil {
				return fmt.Errorf("%s %s: %w", t.table, j.key, err)
			}
			if _, err := pool.Exec(ctx, fmt.Sprintf(
				"UPDATE %s SET visueltcenter = ST_SetSRID(ST_MakePoint($1,$2),25832) WHERE %s = $3",
				t.table, t.key), pt[0], pt[1], j.key); err != nil {
				return err
			}
		}
	}
	return nil
}

// polylabelOfGeoJSON parses a (Multi)Polygon GeoJSON and returns the polylabel
// of its largest sub-polygon (coords in the GeoJSON's CRS, i.e. 25832 here).
func polylabelOfGeoJSON(gj string) (geo.Point, error) {
	var g struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(gj), &g); err != nil {
		return geo.Point{}, err
	}
	var polys []geo.Polygon
	switch g.Type {
	case "Polygon":
		var poly geo.Polygon
		if err := json.Unmarshal(g.Coordinates, &poly); err != nil {
			return geo.Point{}, err
		}
		polys = []geo.Polygon{poly}
	case "MultiPolygon":
		if err := json.Unmarshal(g.Coordinates, &polys); err != nil {
			return geo.Point{}, err
		}
	default:
		return geo.Point{}, fmt.Errorf("unexpected geometry type %q", g.Type)
	}
	largest := geo.LargestPolygon(polys)
	if len(largest) == 0 {
		return geo.Point{}, fmt.Errorf("no polygon")
	}
	return geo.Polylabel(largest, 1.0), nil
}
