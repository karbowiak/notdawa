-- DAGI Retskreds + Politikreds → /retskredse, /politikredse. Both are simple
-- area entities (kode/navn/geom) sharing the sogne-like output shape but with
-- visueltcenter LAST and no dagi_id in the served output. visueltcenter is the
-- Mapbox polylabel, precomputed at ingest (byte-exact, unlike ST_MIC).
CREATE TABLE IF NOT EXISTS dagi_retskredse (
    kode              TEXT PRIMARY KEY,                       -- retskredsnummer
    navn              TEXT NOT NULL,
    dagi_id           TEXT,                                   -- id_lokalId (provenance; not served)
    aendret           TIMESTAMPTZ,
    geom              geometry(MultiPolygon, 25832) NOT NULL,
    visueltcenter     geometry(Point, 25832),
    generation_number INT NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS dagi_retskredse_geom_gix ON dagi_retskredse USING GIST (geom);

CREATE TABLE IF NOT EXISTS dagi_politikredse (
    kode              TEXT PRIMARY KEY,                       -- politikredsnummer
    navn              TEXT NOT NULL,
    dagi_id           TEXT,
    aendret           TIMESTAMPTZ,
    geom              geometry(MultiPolygon, 25832) NOT NULL,
    visueltcenter     geometry(Point, 25832),
    generation_number INT NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS dagi_politikredse_geom_gix ON dagi_politikredse USING GIST (geom);
