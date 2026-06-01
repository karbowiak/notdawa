-- DAGI administrative-geography registers. Geometry is stored in the source CRS
-- EPSG:25832 (what the raw Fildownload extracts ship) and reprojected to WGS84
-- (4326) only at the API edge, matching how DAWA computes bbox/visueltcenter.
-- Codes are zero-paddable identifiers and are therefore TEXT, never integers.

-- Regionsinddeling (regioner). Reproducible 1:1 from the raw extract:
--   regionskode -> kode, navn, NUTS2vaerdi -> nuts2, id_lokalId -> dagi_id, geom.
-- DAWA-internal (not derivable from raw): geo_version, ændret, geo_ændret.
CREATE TABLE IF NOT EXISTS dagi_regioner (
    kode              TEXT PRIMARY KEY,                       -- regionskode, e.g. "1084"
    navn              TEXT NOT NULL,
    nuts2             TEXT,                                   -- NUTS2vaerdi, e.g. "DK01"
    dagi_id           TEXT,                                   -- id_lokalId, e.g. "389099"
    aendret           TIMESTAMPTZ,                            -- best-effort (datafordelerOpdateringstid)
    geom              geometry(MultiPolygon, 25832) NOT NULL, -- full-res (skala 1:10.000)
    generation_number INT  NOT NULL,                          -- source Fildownload generation
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS dagi_regioner_geom_gix ON dagi_regioner USING GIST (geom);
