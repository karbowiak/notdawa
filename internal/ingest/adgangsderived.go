package ingest

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AdgangsadresseDerive rebuilds adgangsadresse_derived: the per-address spatial
// memberships (afstemningsområde / landsdel / retskreds / politikreds via
// ST_Covers of the adgangspunkt, bebyggelse membership + brofast via
// ST_Contains) that the serving queries previously evaluated as per-row
// laterals (~34ms/row). Built set-wise — polygon-outer joins let PostGIS
// prepare each polygon once and stream the point index past it — in one
// crash-atomic TRUNCATE+INSERT transaction.
//
// The DISTINCT ON picks for boundary points are arbitrary exactly like the
// serve-time `LIMIT 1` laterals they replace (no ORDER BY there either).
// A derived step: runs after husnummer/adressepunkt (DAR), the DAGI loads,
// DS (bebyggelser) and the brofasthed seed.
func AdgangsadresseDerive(ctx context.Context, pool *pgxpool.Pool) (Result, error) {
	res := Result{Register: "derived", Entity: "adgangsadresse-derived"}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "TRUNCATE adgangsadresse_derived"); err != nil {
		return res, err
	}
	ct, err := tx.Exec(ctx, `
		INSERT INTO adgangsadresse_derived
			(id, afstem_dagi_id, landsdel_nuts3, retskreds_kode, politikreds_kode, bebyggelser, brofast)
		SELECT h.id, af.dagi_id, ld.nuts3, rk.kode, pk.kode, beb.j, (bro.pid IS NULL)
		FROM dar_husnummer h
		LEFT JOIN (
			SELECT DISTINCT ON (ap.id_lokalid) ap.id_lokalid AS pid, t.dagi_id
			FROM dagi_afstemningsomraader t
			JOIN dar_adressepunkt ap ON ap.geom IS NOT NULL AND ST_Covers(t.geom, ap.geom)
		) af ON af.pid = h.adgangspunkt_id
		LEFT JOIN (
			SELECT DISTINCT ON (ap.id_lokalid) ap.id_lokalid AS pid, t.nuts3
			FROM dagi_landsdele t
			JOIN dar_adressepunkt ap ON ap.geom IS NOT NULL AND ST_Covers(t.geom, ap.geom)
		) ld ON ld.pid = h.adgangspunkt_id
		LEFT JOIN (
			SELECT DISTINCT ON (ap.id_lokalid) ap.id_lokalid AS pid, t.kode
			FROM dagi_retskredse t
			JOIN dar_adressepunkt ap ON ap.geom IS NOT NULL AND ST_Covers(t.geom, ap.geom)
		) rk ON rk.pid = h.adgangspunkt_id
		LEFT JOIN (
			SELECT DISTINCT ON (ap.id_lokalid) ap.id_lokalid AS pid, t.kode
			FROM dagi_politikredse t
			JOIN dar_adressepunkt ap ON ap.geom IS NOT NULL AND ST_Covers(t.geom, ap.geom)
		) pk ON pk.pid = h.adgangspunkt_id
		LEFT JOIN (
			SELECT ap.id_lokalid AS pid,
				jsonb_agg(jsonb_build_object(
					'id', beb.id_lokalId,
					'kode', beb.bebyggelseskode,
					'type', beb.undertype,
					'navn', bn.navn
				) ORDER BY beb.id_lokalId) AS j
			FROM ds_steder beb
			JOIN dar_adressepunkt ap ON ap.geom IS NOT NULL AND ST_Contains(beb.geom, ap.geom)
			LEFT JOIN LATERAL (
				SELECT (array_agg(sn2.skrivemaade ORDER BY sn2.navnefoelgenummer NULLS LAST, sn2.skrivemaade)
					FILTER (WHERE sn2.brugsprioritet = 'primær'))[1] AS navn
				FROM ds_stednavne sn2 WHERE sn2.place_objectid = beb.objectid
			) bn ON true
			WHERE beb.hovedtype = 'bebyggelse'
			GROUP BY ap.id_lokalid
		) beb ON beb.pid = h.adgangspunkt_id
		LEFT JOIN (
			SELECT DISTINCT ap.id_lokalid AS pid
			FROM brofasthed bf
			JOIN ds_steder bs ON bs.id_lokalId = bf.stedid
			JOIN dar_adressepunkt ap ON ap.geom IS NOT NULL AND ST_Contains(bs.geom, ap.geom)
			WHERE bf.brofast = false
		) bro ON bro.pid = h.adgangspunkt_id`)
	if err != nil {
		return res, fmt.Errorf("build adgangsadresse_derived: %w", err)
	}
	n := int(ct.RowsAffected())
	if n == 0 {
		return res, fmt.Errorf("build adgangsadresse_derived: 0 rows (dar_husnummer empty?)")
	}
	if _, err := tx.Exec(ctx, "ANALYZE adgangsadresse_derived"); err != nil {
		return res, err
	}
	if err := tx.Commit(ctx); err != nil {
		return res, err
	}
	res.RowsLoaded = n
	return res, nil
}
