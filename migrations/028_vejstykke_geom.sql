-- 028_vejstykke_geom.sql — precomputed per-vejstykke centerline geometry.
--
-- DAWA's per-vejstykke geometry is the named-road centerline clipped to ONE
-- kommune (a named road can span several kommuner; each vejstykke is the road's
-- segment inside a single kommune). The bulk DAR extract carries only the parent
-- dar_navngivenvej.geom (the whole road across all kommuner), so the
-- /vejstykker/{kommunekode}/{kode}/naboer endpoint was clipping nothing and used
-- the whole-road line for both target and candidates — wildly over-counting
-- neighbours on cross-kommune roads (e.g. 0173/3104 returned ~167 vs DAWA's 13).
--
-- The faithful per-vejstykke geometry is ST_Intersection(parent road line,
-- kommune polygon). Clipping all ~114k status-3 vejstykker per request is
-- minutes-slow, so we materialise the result once here. Both geom inputs are
-- SRID 25832 (metric), so the stored geom is metric and ST_DWithin/ST_Distance
-- on it are in metres. Join keys are both 4-char zero-padded
-- (dar_navngivenvej_kommunedel.kommune = dagi_kommuner.kode, e.g. '0173').
--
-- The column is geometry(Geometry,25832) because a line∩polygon clip can yield
-- LineString / MultiLineString / GeometryCollection / Point depending on how the
-- road meets the border. Empty/NULL clips are filtered out (WHERE ST_Intersects
-- guards most; the ST_IsEmpty / IS NOT NULL guards catch the rest), so absent
-- rows simply have no neighbours — matching DAWA's behaviour for such roads.

CREATE TABLE IF NOT EXISTS vejstykke_geom (
    kommune TEXT NOT NULL,                       -- 4-char kommunekode, e.g. '0173'
    vejkode TEXT NOT NULL,                       -- 4-char vejkode,      e.g. '3104'
    geom    geometry(Geometry, 25832) NOT NULL,  -- road centerline clipped to the kommune
    PRIMARY KEY (kommune, vejkode)
);

CREATE INDEX IF NOT EXISTS vejstykke_geom_geom_gix ON vejstykke_geom USING GIST (geom);

-- Populate only when empty so the migration is idempotent (the runner re-applies
-- every file on each `migrate`). ON CONFLICT DO NOTHING is a belt-and-suspenders
-- guard against the (kommune,vejkode) PK in case of any duplicate kommunedel rows.
INSERT INTO vejstykke_geom (kommune, vejkode, geom)
SELECT kd.kommune, kd.vejkode, ST_Intersection(nv.geom, k.geom)
FROM dar_navngivenvej_kommunedel kd
JOIN dar_navngivenvej nv ON nv.id = kd.navngivenvej AND nv.status = '3'
JOIN dagi_kommuner    k  ON k.kode = kd.kommune
WHERE kd.status = '3'
  AND nv.geom IS NOT NULL
  AND ST_Intersects(nv.geom, k.geom)
  AND NOT ST_IsEmpty(ST_Intersection(nv.geom, k.geom))
  AND NOT EXISTS (SELECT 1 FROM vejstykke_geom)
ON CONFLICT (kommune, vejkode) DO NOTHING;
