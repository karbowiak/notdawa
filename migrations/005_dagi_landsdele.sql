-- Landsdel (landsdele). Keyed by NUTS3 (no numeric kode). Reproducible 1:1:
--   NUTS3vaerdi -> nuts3, navn, id_lokalId -> dagi_id, geom. The nested region{}
--   is derived by matching the region whose nuts2 = left(nuts3, 4).
CREATE TABLE IF NOT EXISTS dagi_landsdele (
    nuts3             TEXT PRIMARY KEY,                       -- e.g. "DK011"
    navn              TEXT NOT NULL,
    dagi_id           TEXT,                                   -- id_lokalId
    aendret           TIMESTAMPTZ,                            -- best-effort
    geom              geometry(MultiPolygon, 25832) NOT NULL, -- full-res (skala 1:10.000)
    generation_number INT  NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS dagi_landsdele_geom_gix ON dagi_landsdele USING GIST (geom);
