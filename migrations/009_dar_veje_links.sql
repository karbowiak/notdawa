-- DAR road link/lookup tables that turn dar_navngivenvej (migration 008) into
-- the /navngivneveje, /vejstykker and /vejnavne endpoints. All three are loaded
-- status-filtered to '3' at ingest (the load-bearing DAWA filter): every link
-- table also carries nedlagt/henlagt rows that DAWA drops.
--
--   * dar_navngivenvej_kommunedel — the vejstykke join table. One row per
--     (kommune, vejkode) belonging to a navngivenvej; backs vejstykker[] and the
--     /vejstykker entity. vejkode is already 4-char zero-padded in the raw data.
--   * dar_navngivenvej_postnummer — navngivenvej ↔ postnummer (UUID) membership;
--     resolved against dar_postnummer for nr/navn.
--   * dar_postnummer — the DAR postnr/navn lookup keyed by the postnummer UUID.

CREATE TABLE IF NOT EXISTS dar_navngivenvej_kommunedel (
    id                TEXT PRIMARY KEY, -- id_lokalId (the vejstykke id)
    navngivenvej      TEXT NOT NULL,    -- NavngivenVej.id_lokalId (uuid)
    kommune           TEXT NOT NULL,    -- 4-char zero-padded kommunekode
    vejkode           TEXT NOT NULL,    -- 4-char zero-padded vejkode
    status            TEXT NOT NULL,    -- raw DAR status code (always '3' after filter)
    generation_number INT  NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS dar_nvkommunedel_nv_idx   ON dar_navngivenvej_kommunedel (navngivenvej);
CREATE INDEX IF NOT EXISTS dar_nvkommunedel_kode_idx ON dar_navngivenvej_kommunedel (kommune, vejkode);

CREATE TABLE IF NOT EXISTS dar_navngivenvej_postnummer (
    id                TEXT PRIMARY KEY, -- id_lokalId (the link's own uuid)
    navngivenvej      TEXT NOT NULL,    -- NavngivenVej.id_lokalId (uuid)
    postnummer_id     TEXT NOT NULL,    -- dar_postnummer.id (uuid)
    status            TEXT NOT NULL,    -- raw DAR status code (always '3' after filter)
    generation_number INT  NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS dar_nvpostnummer_nv_idx ON dar_navngivenvej_postnummer (navngivenvej);

CREATE TABLE IF NOT EXISTS dar_postnummer (
    id                TEXT PRIMARY KEY, -- id_lokalId (uuid)
    postnr            TEXT NOT NULL,    -- 4-digit postnr string, e.g. "3080"
    navn              TEXT,             -- e.g. "Tikøb"
    status            TEXT NOT NULL,
    generation_number INT  NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
