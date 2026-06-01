package dawa

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// reverse.go implements /{resource}/reverse?x=LON&y=LAT. For polygon resources
// the feature whose geometry COVERS the (lon,lat) WGS84 point is returned; its
// body is byte-identical to that feature's single-GET representation (the reverse
// functions resolve the key spatially, then delegate to Get*). For address
// resources the nearest adgangspunkt is returned. When no feature covers the
// point (e.g. a sea coordinate) pgx.ErrNoRows is returned, which the HTTP layer
// renders as DAWA's 404 ResourceNotFoundError envelope.

// reverseKey resolves the single key-column value of the row in `table` whose
// geom covers the WGS84 point (x=lon, y=lat). The geometry is stored in
// EPSG:25832, so the point is reprojected before the ST_Covers test. Returns
// pgx.ErrNoRows when no row covers the point.
func reverseKey(ctx context.Context, pool *pgxpool.Pool, table, keyCol string, x, y float64) (string, error) {
	sql := fmt.Sprintf(
		"SELECT %s FROM %s WHERE ST_Covers(geom, ST_Transform(ST_SetSRID(ST_Point($1, $2), 4326), 25832)) LIMIT 1",
		keyCol, table)
	var key string
	if err := pool.QueryRow(ctx, sql, x, y).Scan(&key); err != nil {
		return "", err
	}
	return key, nil
}

// ReverseKommune returns the kommune covering (x,y), or pgx.ErrNoRows.
func ReverseKommune(ctx context.Context, pool *pgxpool.Pool, x, y float64, baseURL string) (*Kommune, error) {
	kode, err := reverseKey(ctx, pool, "dagi_kommuner", "kode", x, y)
	if err != nil {
		return nil, err
	}
	return GetKommune(ctx, pool, kode, baseURL)
}

// ReverseRegion returns the region covering (x,y), or pgx.ErrNoRows.
func ReverseRegion(ctx context.Context, pool *pgxpool.Pool, x, y float64, baseURL string) (*Region, error) {
	kode, err := reverseKey(ctx, pool, "dagi_regioner", "kode", x, y)
	if err != nil {
		return nil, err
	}
	return GetRegion(ctx, pool, kode, baseURL)
}

// ReverseSogn returns the sogn covering (x,y), or pgx.ErrNoRows.
func ReverseSogn(ctx context.Context, pool *pgxpool.Pool, x, y float64, baseURL string) (*Sogn, error) {
	kode, err := reverseKey(ctx, pool, "dagi_sogne", "kode", x, y)
	if err != nil {
		return nil, err
	}
	return GetSogn(ctx, pool, kode, baseURL)
}

// ReversePostnummer returns the postnummer covering (x,y), or pgx.ErrNoRows.
func ReversePostnummer(ctx context.Context, pool *pgxpool.Pool, x, y float64, baseURL string) (*Postnummer, error) {
	nr, err := reverseKey(ctx, pool, "dagi_postnumre", "nr", x, y)
	if err != nil {
		return nil, err
	}
	return GetPostnummer(ctx, pool, nr, baseURL)
}

// ReverseLandsdel returns the landsdel covering (x,y), or pgx.ErrNoRows.
func ReverseLandsdel(ctx context.Context, pool *pgxpool.Pool, x, y float64, baseURL string) (*Landsdel, error) {
	nuts3, err := reverseKey(ctx, pool, "dagi_landsdele", "nuts3", x, y)
	if err != nil {
		return nil, err
	}
	return GetLandsdel(ctx, pool, nuts3, baseURL)
}

// ReverseStorkreds returns the storkreds covering (x,y), or pgx.ErrNoRows.
func ReverseStorkreds(ctx context.Context, pool *pgxpool.Pool, x, y float64, baseURL string) (*Storkreds, error) {
	nummer, err := reverseKey(ctx, pool, "dagi_storkredse", "nummer", x, y)
	if err != nil {
		return nil, err
	}
	return GetStorkreds(ctx, pool, nummer, baseURL)
}

// ReverseValglandsdel returns the valglandsdel covering (x,y), or pgx.ErrNoRows.
func ReverseValglandsdel(ctx context.Context, pool *pgxpool.Pool, x, y float64, baseURL string) (*Valglandsdel, error) {
	bogstav, err := reverseKey(ctx, pool, "dagi_valglandsdele", "bogstav", x, y)
	if err != nil {
		return nil, err
	}
	return GetValglandsdel(ctx, pool, bogstav, baseURL)
}

// ReverseOpstillingskreds returns the opstillingskreds covering (x,y), or
// pgx.ErrNoRows. GetOpstillingskreds keys on the opstillingskreds nummer.
func ReverseOpstillingskreds(ctx context.Context, pool *pgxpool.Pool, x, y float64, baseURL string) (*Opstillingskreds, error) {
	nummer, err := reverseKey(ctx, pool, "dagi_opstillingskredse", "nummer", x, y)
	if err != nil {
		return nil, err
	}
	return GetOpstillingskreds(ctx, pool, nummer, baseURL)
}

// ReverseSimpleArea returns the SimpleArea (retskreds/politikreds) covering
// (x,y), or pgx.ErrNoRows.
func ReverseSimpleArea(ctx context.Context, pool *pgxpool.Pool, table, pathSeg string, x, y float64, baseURL string) (*SimpleArea, error) {
	kode, err := reverseKey(ctx, pool, table, "kode", x, y)
	if err != nil {
		return nil, err
	}
	return GetSimpleArea(ctx, pool, table, pathSeg, kode, baseURL)
}

// ReverseAfstemningsomraade returns the afstemningsområde covering (x,y), or
// pgx.ErrNoRows. The dagi_id is the stable key delegated to Get.
func ReverseAfstemningsomraade(ctx context.Context, pool *pgxpool.Pool, x, y float64, baseURL string) (*Afstemningsomraade, error) {
	dagiID, err := reverseKey(ctx, pool, "dagi_afstemningsomraader", "dagi_id", x, y)
	if err != nil {
		return nil, err
	}
	return GetAfstemningsomraade(ctx, pool, dagiID, baseURL)
}

// ReverseMrafstemningsomraade returns the SINGLE menighedsråd afstemningsområde
// covering (x,y), or pgx.ErrNoRows. DAWA's /menighedsraadsafstemningsomraader/
// reverse returns ONE bare object (the area containing the point), exactly like
// the other DAGI families' reverse — not an array. The dagi_id resolved spatially
// is delegated to GetMrafstemningsomraade so the body is byte-identical to the
// single-GET shape.
func ReverseMrafstemningsomraade(ctx context.Context, pool *pgxpool.Pool, x, y float64, baseURL string) (*Menighedsraadsafstemningsomraade, error) {
	dagiID, err := reverseKey(ctx, pool, "dagi_mrafstemningsomraader", "dagi_id", x, y)
	if err != nil {
		return nil, err
	}
	return GetMrafstemningsomraade(ctx, pool, dagiID, baseURL)
}

// ReverseJordstykke returns the jordstykke covering (x,y), or pgx.ErrNoRows. The
// jordstykke key is the composite (ejerlav_kode, matrikelnr).
func ReverseJordstykke(ctx context.Context, pool *pgxpool.Pool, x, y float64, baseURL string) (*Jordstykke, error) {
	sql := "SELECT ejerlav_kode, matrikelnr FROM mat_jordstykke" +
		" WHERE ST_Covers(geom, ST_Transform(ST_SetSRID(ST_Point($1, $2), 4326), 25832)) LIMIT 1"
	var ejerlavKode int
	var matrikelnr string
	if err := pool.QueryRow(ctx, sql, x, y).Scan(&ejerlavKode, &matrikelnr); err != nil {
		return nil, err
	}
	return GetJordstykke(ctx, pool, ejerlavKode, matrikelnr, baseURL)
}

// ReverseVejstykke returns the FULL vejstykke representation for the vejstykke of
// the adgangsadresse nearest to (x,y), or pgx.ErrNoRows. DAWA's
// /vejstykker/reverse resolves the nearest ADGANGSADRESSE (adgangspunkt) and
// returns that address's vejstykke (the full GET-by-id shape) — NOT the nearest
// road centerline — so the selection mirrors ReverseAdgangsadresse's KNN on the
// adgangspunkt geom, resolving the kommunedel exactly as the full adgangsadresse
// representation does (kd on navngivenvej+status, kommune via kom.dagi_id).
func ReverseVejstykke(ctx context.Context, pool *pgxpool.Pool, x, y float64, baseURL string) (*Vejstykke, error) {
	sql := `SELECT kd.kommune, kd.vejkode
		FROM dar_husnummer h
		JOIN dar_adressepunkt ap ON ap.id_lokalid = h.adgangspunkt_id
		LEFT JOIN dagi_kommuner kom ON kom.dagi_id = h.kommune
		JOIN dar_navngivenvej_kommunedel kd ON kd.navngivenvej = h.navngivenvej AND kd.status = '3' AND kd.kommune = kom.kode
		WHERE h.status = '3' AND ap.geom IS NOT NULL
		ORDER BY ap.geom <-> ST_Transform(ST_SetSRID(ST_Point($1, $2), 4326), 25832)
		LIMIT 1`
	var kommunekode, vejkode string
	if err := pool.QueryRow(ctx, sql, x, y).Scan(&kommunekode, &vejkode); err != nil {
		return nil, err
	}
	return GetVejstykke(ctx, pool, kommunekode, vejkode, baseURL)
}

// ReverseAdgangsadresse returns the FULL adgangsadresse representation whose
// adgangspunkt is nearest to (x,y), or pgx.ErrNoRows. Uses a KNN (<->) order on
// the adgangspunkt geom — the same selection DAWA's reverse makes, and the body
// is the full GET-by-id shape.
func ReverseAdgangsadresse(ctx context.Context, pool *pgxpool.Pool, x, y float64, baseURL string) (*Adgangsadresse, error) {
	sql := "SELECT " + adgangsadresseCols + adgangsadresseFrom +
		" WHERE h.status = '3' AND ap.geom IS NOT NULL" +
		" ORDER BY ap.geom <-> ST_Transform(ST_SetSRID(ST_Point($1, $2), 4326), 25832)" +
		" LIMIT 1"
	return scanAdgangsadresse(pool.QueryRow(ctx, sql, x, y), baseURL)
}

// reverseNoRows is the sentinel returned for an empty spatial result; it aliases
// pgx.ErrNoRows so the HTTP layer's existing isNoRows check renders the DAWA 404.
var reverseNoRows = pgx.ErrNoRows
