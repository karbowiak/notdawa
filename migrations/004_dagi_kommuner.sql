-- Kommuneinddeling (kommuner). Reproducible 1:1 from the raw extract:
--   kommunekode -> kode, navn, udenforKommuneinddeling -> udenforkommuneinddeling,
--   id_lokalId -> dagi_id, regionLokalId joins to dagi_regioner.dagi_id for the
--   nested region{} reference (and regionskode), geom.
CREATE TABLE IF NOT EXISTS dagi_kommuner (
    kode                    TEXT PRIMARY KEY,                       -- kommunekode, e.g. "0101"
    navn                    TEXT NOT NULL,
    udenforkommuneinddeling BOOLEAN NOT NULL,
    region_lokalid          TEXT,                                   -- regionLokalId -> dagi_regioner.dagi_id
    dagi_id                 TEXT,                                   -- id_lokalId, e.g. "389103"
    aendret                 TIMESTAMPTZ,                            -- best-effort (datafordelerOpdateringstid)
    geom                    geometry(MultiPolygon, 25832) NOT NULL, -- full-res (skala 1:10.000)
    generation_number       INT  NOT NULL,
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS dagi_kommuner_geom_gix   ON dagi_kommuner USING GIST (geom);
CREATE INDEX IF NOT EXISTS dagi_kommuner_region_idx ON dagi_kommuner (region_lokalid);
