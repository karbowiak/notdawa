-- 020_mat_jordstykke.sql — Matriklen jordstykker (cadastral parcels) → /jordstykker.
-- Jordstykke JSON carries attributes + *LokalId references but NO geometry; the
-- polygon comes from the separate MAT Lodflade extract (1..N faces per parcel,
-- keyed by jordstykkeLokalId) aggregated into geom. bfenummer comes from the
-- SamletFastEjendom extract (mat_sfe_bfe). kommune/region/sogn LokalId equal the
-- DAGI kode directly, so they are stored as codes and the navn is joined from the
-- existing DAGI tables at serve time. Several DAWA fields are effectively constant
-- in DAWA's old data model (esrejendomsnr/udvidet '0', vejarealberegningsmetode
-- 'e', vandarealberegningsmetode 'ukendt', retskreds null) and are emitted in Go.
CREATE TABLE IF NOT EXISTS mat_jordstykke (
    id_lokalid          text PRIMARY KEY,          -- == featureid output; lodflade join key
    matrikelnr          text NOT NULL,
    ejerlav_kode        integer NOT NULL,          -- ejerlavLokalId parsed to int (== mat_ejerlav.kode)
    kommune_kode        text,                      -- kommuneLokalId (e.g. '0147' == dagi_kommuner.kode)
    region_kode         text,                      -- regionLokalId (e.g. '1084')
    sogn_kode           text,                      -- sognLokalId  (e.g. '7103')
    sfe_lokalid         text,                      -- samletFastEjendomLokalId -> sfeejendomsnr + bfe join
    registreretareal    integer,
    vejareal            integer,
    arealberegningsmetode_raw text,                -- 'Areal beregnet efter kortet - k' -> 'k'
    faelleslod          boolean,
    moder_lokalid       text,                      -- stammerFraJordstykkeLokalId -> moderjordstykke (int|null)
    aendret             timestamptz,               -- best-effort (DAWA-internal metadata)
    geom                geometry(MultiPolygon, 25832),
    generation_number   integer
);
CREATE UNIQUE INDEX IF NOT EXISTS mat_jordstykke_ejerlav_matr_uq ON mat_jordstykke (ejerlav_kode, matrikelnr);
CREATE INDEX IF NOT EXISTS mat_jordstykke_geom_gix ON mat_jordstykke USING gist (geom);

-- SamletFastEjendom BFE lookup: id_lokalId -> BFEnummer (for jordstykke.bfenummer).
CREATE TABLE IF NOT EXISTS mat_sfe_bfe (
    id_lokalid text PRIMARY KEY,
    bfenummer  bigint
);

-- Lodflade staging: raw parcel faces keyed by jordstykke. Populated by the
-- lodflade stream, aggregated into mat_jordstykke.geom, then left for delta runs.
CREATE TABLE IF NOT EXISTS mat_lodflade (
    id_lokalid          text PRIMARY KEY,
    jordstykke_lokalid  text NOT NULL,
    geom                geometry(Geometry, 25832),
    generation_number   integer
);
CREATE INDEX IF NOT EXISTS mat_lodflade_jordstykke_idx ON mat_lodflade (jordstykke_lokalid);
