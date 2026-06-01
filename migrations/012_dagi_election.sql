-- DAGI election chain: opstillingskredse (the geometry source) → storkredse +
-- valglandsdele (geometry DERIVED by unioning opstillingskredse). DAWA does NOT
-- use the raw Storkreds/Valglandsdel polygons (they are clipped); it unions the
-- constituent opstillingskredse. visueltcenter is precomputed at ingest with a
-- Mapbox-polylabel port (ST_MaximumInscribedCircle is km off here) and stored as
-- a 25832 point; bbox stays the reprojected-envelope rule.

-- Opstillingskredse: persisted (also the future /opstillingskredse endpoint).
CREATE TABLE IF NOT EXISTS dagi_opstillingskredse (
    nummer               TEXT PRIMARY KEY,                       -- opstillingskredsnummer (e.g. "56")
    navn                 TEXT NOT NULL,
    dagi_id              TEXT,                                   -- id_lokalId
    storkreds_lokalid    TEXT,                                   -- storkredsLokalId (join key)
    storkredsnummer      TEXT,
    kredskommunekode     TEXT,
    valglandsdelsbogstav TEXT,
    aendret              TIMESTAMPTZ,
    geom                 geometry(MultiPolygon, 25832) NOT NULL,
    generation_number    INT NOT NULL,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS dagi_opstillingskredse_geom_gix ON dagi_opstillingskredse USING GIST (geom);
CREATE INDEX IF NOT EXISTS dagi_opstillingskredse_stork_idx ON dagi_opstillingskredse (storkreds_lokalid);

CREATE TABLE IF NOT EXISTS dagi_storkredse (
    nummer               TEXT PRIMARY KEY,                       -- storkredsnummer "1".."10" (unpadded)
    navn                 TEXT NOT NULL,
    regionskode          TEXT,                                   -- STATIC map (not in any DAGI extract)
    valglandsdelsbogstav TEXT,
    dagi_id              TEXT,                                   -- provenance only (not in DAWA output)
    aendret              TIMESTAMPTZ,
    geom                 geometry(MultiPolygon, 25832) NOT NULL, -- ST_Union of opstillingskredse
    visueltcenter        geometry(Point, 25832),                 -- polylabel, precomputed
    generation_number    INT NOT NULL,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS dagi_storkredse_geom_gix ON dagi_storkredse USING GIST (geom);

CREATE TABLE IF NOT EXISTS dagi_valglandsdele (
    bogstav           TEXT PRIMARY KEY,                       -- A/B/C
    navn              TEXT NOT NULL,
    dagi_id           TEXT,                                   -- provenance only
    aendret           TIMESTAMPTZ,
    geom              geometry(MultiPolygon, 25832) NOT NULL, -- ST_Union of constituent opstillingskredse
    visueltcenter     geometry(Point, 25832),                 -- polylabel, precomputed
    generation_number INT NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS dagi_valglandsdele_geom_gix ON dagi_valglandsdele USING GIST (geom);
