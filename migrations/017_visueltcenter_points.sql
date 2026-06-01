-- Precomputed visueltcenter (Mapbox polylabel) for the DAGI/MAT area entities
-- that previously computed it on the fly with ST_MaximumInscribedCircle. DAWA
-- uses @mapbox/polylabel, which differs from GEOS MIC by metres on these
-- geometries; the Go port in internal/geo makes it byte-exact. Backfilled via
-- `notdawa ingest polylabel-backfill` (geom is already loaded — no re-download).
ALTER TABLE dagi_regioner   ADD COLUMN IF NOT EXISTS visueltcenter geometry(Point, 25832);
ALTER TABLE dagi_kommuner   ADD COLUMN IF NOT EXISTS visueltcenter geometry(Point, 25832);
ALTER TABLE dagi_landsdele  ADD COLUMN IF NOT EXISTS visueltcenter geometry(Point, 25832);
ALTER TABLE dagi_sogne      ADD COLUMN IF NOT EXISTS visueltcenter geometry(Point, 25832);
ALTER TABLE mat_ejerlav     ADD COLUMN IF NOT EXISTS visueltcenter geometry(Point, 25832);
ALTER TABLE dagi_postnumre  ADD COLUMN IF NOT EXISTS visueltcenter geometry(Point, 25832);
