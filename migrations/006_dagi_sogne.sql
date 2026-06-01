-- Sogneinddeling (sogne). Standalone (no nested refs). Reproducible 1:1:
--   sognekode -> kode, navn, id_lokalId -> dagi_id, geom.
CREATE TABLE IF NOT EXISTS dagi_sogne (
    kode              TEXT PRIMARY KEY,                       -- sognekode, e.g. "7002"
    navn              TEXT NOT NULL,
    dagi_id           TEXT,                                   -- id_lokalId
    aendret           TIMESTAMPTZ,                            -- best-effort
    geom              geometry(MultiPolygon, 25832) NOT NULL, -- full-res (skala 1:10.000)
    generation_number INT  NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS dagi_sogne_geom_gix ON dagi_sogne USING GIST (geom);
