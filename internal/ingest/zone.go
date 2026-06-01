package ingest

// Plandata.dk planning-zone ingest (byzone / sommerhusområde / landzone) into the
// `zone` table, later used to derive /zonetilknytninger (address→zone point-in-polygon).
//
// Unlike the DAGI/DAR/MAT loaders this does NOT use the Datafordeler Fildownload
// client — the data lives on a different host (Plandata's GeoServer WFS), so we hit
// the GetFeature GeoJSON endpoint directly over net/http. The client argument is
// kept only to match the uniform ingest signature wired from the CLI.
//
// CLI: case "zoner": res, err = ingest.Zoner(cmd.Context(), pool, client)

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
)

// zoneWFSBase is the Plandata open WFS GetFeature endpoint. theme_pdk_zonekort_v
// holds 6126 polygon features nationally in EPSG:25832 — well under GeoServer's
// 10000/page cap, so a single GetFeature request returns the whole collection.
const zoneWFSBase = "https://geoserver.plandata.dk/geoserver/wfs"

// zoneGenerationNumber is a constant stamp: the WFS exposes no generationNumber,
// but the column is useful as a "loaded by this importer version" marker.
const zoneGenerationNumber = 1

// zoneFeature is one feature of the WFS FeatureCollection. We keep the geometry as
// raw JSON so it can be handed straight to Postgres ST_GeomFromGeoJSON, and read
// the `zonestatus` label from the properties (the authoritative DAWA-code source).
//
// Observed properties (count=1 sample):
//
//	{"planid":12185187,"komnr":813,"objektkode":40,"zone":1,
//	 "datooprt":"...","datoopdt":"...","kommunenavn":"Frederikshavn",
//	 "zonestatus":"Byzone"}
type zoneFeature struct {
	Geometry   json.RawMessage `json:"geometry"`
	Properties struct {
		ZoneStatus string `json:"zonestatus"`
	} `json:"properties"`
}

type zoneFeatureCollection struct {
	Features      []zoneFeature `json:"features"`
	NumberMatched int           `json:"numberMatched"`
}

// zoneLabelToCode maps the Danish zonestatus label to DAWA's integer zone code
// (1 byzone, 2 sommerhusområde, 3 landzone — DAWA temaModels.js). The label
// (zonestatus) is authoritative: the source's own integer `zone` field uses a
// DIFFERENT coding (it labels Sommerhusområde as 3, which is DAWA's Landzone),
// so we deliberately ignore it and derive the DAWA code from the label only.
// Landzone is not an explicit feature in this layer — it is the implicit
// complement of the byzone/sommerhus polygons, which DAWA derives downstream.
var zoneLabelToCode = map[string]int{
	"Byzone":          1,
	"Sommerhusområde": 2,
	"Landzone":        3,
}

// Zoner fetches the Plandata zonekort WFS layer as GeoJSON and loads it into the
// `zone` table: TRUNCATE then insert every feature, parsing the GeoJSON geometry
// in-database. The label→code mapping is strict: an unknown zonestatus aborts the
// load so a new upstream category is never silently dropped.
func Zoner(ctx context.Context, pool *pgxpool.Pool, _ *datafordeler.Client) (Result, error) {
	res := Result{Register: "PLANDATA", Entity: "Zone", GenerationNumber: zoneGenerationNumber}

	fc, err := fetchZones(ctx)
	if err != nil {
		return res, err
	}
	if len(fc.Features) == 0 {
		return res, fmt.Errorf("zoner: WFS returned no features")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)

	if _, err = tx.Exec(ctx, "TRUNCATE zone RESTART IDENTITY"); err != nil {
		return res, fmt.Errorf("truncate zone: %w", err)
	}

	batch := &pgx.Batch{}
	for i, f := range fc.Features {
		code, ok := zoneLabelToCode[f.Properties.ZoneStatus]
		if !ok {
			return res, fmt.Errorf("zoner: unknown zonestatus %q in feature %d", f.Properties.ZoneStatus, i)
		}
		if len(f.Geometry) == 0 {
			return res, fmt.Errorf("zoner: feature %d (%s) has no geometry", i, f.Properties.ZoneStatus)
		}
		// ST_GeomFromGeoJSON ignores any CRS member; the WFS returns 25832 metre
		// coordinates (x≈500k–900k, y≈6.0M–6.4M), so SetSRID stamps the true SRID
		// without reprojecting. ST_Multi normalises Polygon → MultiPolygon for the
		// MultiPolygon column.
		batch.Queue(
			`INSERT INTO zone (zone, zone_navn, geom, generation_number)
			 VALUES ($1, $2, ST_Multi(ST_SetSRID(ST_GeomFromGeoJSON($3), 25832)), $4)`,
			code, nullIfEmpty(f.Properties.ZoneStatus), string(f.Geometry), zoneGenerationNumber,
		)
		res.RowsLoaded++
	}

	br := tx.SendBatch(ctx, batch)
	for i := 0; i < res.RowsLoaded; i++ {
		if _, err = br.Exec(); err != nil {
			br.Close()
			return res, fmt.Errorf("insert into zone: %w", err)
		}
	}
	if err = br.Close(); err != nil {
		return res, err
	}
	if err = tx.Commit(ctx); err != nil {
		return res, err
	}
	return res, nil
}

// fetchZones issues the single WFS GetFeature request and decodes the GeoJSON
// FeatureCollection. It logs a warning (but does not fail) if the server's
// numberMatched exceeds the rows returned, which would signal silent paging.
func fetchZones(ctx context.Context) (*zoneFeatureCollection, error) {
	q := url.Values{}
	q.Set("service", "WFS")
	q.Set("version", "2.0.0")
	q.Set("request", "GetFeature")
	q.Set("typeNames", "pdk:theme_pdk_zonekort_v")
	q.Set("outputFormat", "application/json")
	q.Set("srsName", "urn:ogc:def:crs:EPSG::25832")
	reqURL := zoneWFSBase + "?" + q.Encode()

	hc := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("zoner: GET %s: %w", zoneWFSBase, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("zoner: WFS returned HTTP %d", resp.StatusCode)
	}
	// GeoServer signals exceptions with an XML/text body and a 200; guard against
	// decoding that as an (empty) FeatureCollection.
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "json") {
		return nil, fmt.Errorf("zoner: unexpected WFS content-type %q (likely a service exception)", ct)
	}

	var fc zoneFeatureCollection
	if err := json.NewDecoder(resp.Body).Decode(&fc); err != nil {
		return nil, fmt.Errorf("zoner: decode FeatureCollection: %w", err)
	}
	if fc.NumberMatched > 0 && fc.NumberMatched > len(fc.Features) {
		fmt.Fprintf(os.Stderr, "notdawa: zoner WFS matched %d features but returned %d — response may be paged\n",
			fc.NumberMatched, len(fc.Features))
	}
	return &fc, nil
}
