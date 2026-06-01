-- DAR SupplerendeBynavn names → the /supplerendebynavne (v1, deprecated) name set.
--
-- The v1 collection's element set is "every distinct supplerende bynavn NAME".
-- DAGI SupplerendeBynavn only carries the ~6503 names that have a polygon at full
-- resolution; DAWA's v1 list has 6552, the extra ~49 being address-less names that
-- exist only in the DAR register (no DAGI geometry, no addresses). The DAR
-- SupplerendeBynavn feed DOES carry the `navn` text for ALL of them (across all DAR
-- statuses its distinct navn set == DAWA's 6552 exactly), but migration 018's
-- ingest dropped it, keeping only status/uuid keyed by dagi_id.
--
-- This table persists the DAR feed's (uuid, dagi_id, navn, status) so the v1 query
-- can source its DISTINCT navn set from here (full 6552) instead of from
-- dagi_supplerendebynavne (6503). Per-navn postnumre[]/kommuner[] still come from
-- the address graph via the existing dar_uuid join; address-less names get [].
CREATE TABLE IF NOT EXISTS dar_supplerendebynavn (
    dar_uuid          TEXT PRIMARY KEY,   -- DAR SupplerendeBynavn.id_lokalId (husnummer join key)
    dagi_id           TEXT,               -- DAR SupplerendeBynavn.supplerendeBynavn (== DAGI id_lokalId)
    navn              TEXT NOT NULL,
    status            INT,
    generation_number INT NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS dar_supplerendebynavn_navn_idx ON dar_supplerendebynavn (navn);
