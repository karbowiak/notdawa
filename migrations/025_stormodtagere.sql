-- DAWA-seed Stormodtagere (large-volume postal recipients / "firmapostnumre").
-- This is NOT a Datafordeler register: it is a hand-curated CSV that DAWA ships
-- in its own repo (packages/server/data/stormodtagere.csv) and loads verbatim.
-- We vendor + //go:embed that exact CSV, so the ingest needs no network.
--
-- PRIMARY KEY = adgangsadresseid, mirroring DAWA's own table
-- (packages/server/psql/schema/tables/stormodtagere.sql):
--     nr integer NOT NULL, navn VARCHAR(20) NOT NULL,
--     adgangsadresseid UUID NOT NULL PRIMARY KEY
-- Firmapostnr (nr) is NOT unique: a single firmapostnummer covers many access
-- addresses (e.g. nr "1092" appears on 4 rows in stormodtagere.csv, "1780" on 18
-- rows in stormodtager-opdateret.csv). The unique key is the access address.
-- adgangsadresseid is unique in both vendored CSVs (verified), so it is the PK.
--
-- We keep nr as TEXT to preserve leading zeros ("0800", "0999"); DAWA serves the
-- firmapostnr as a 4-char zero-padded string. The extra source columns
-- (gadeadresse/postnr/bynavn) are retained for the /postnumre stormodtager view.
-- The one stormodtagere.csv row with an empty Adgangsadresseid (Nordea,
-- Torvegade 2) is skipped by the loader, matching DAWA's NOT NULL PK constraint.
CREATE TABLE IF NOT EXISTS stormodtagere (
    adgangsadresseid text PRIMARY KEY,   -- Adgangsadresseid (UUID) -> dar_husnummer.id
    nr               text NOT NULL,      -- Firmapostnr, e.g. "0800" (leading zeros preserved; NOT unique)
    navn             text NOT NULL,      -- Firma (recipient name)
    gadeadresse      text,               -- Gadeadresse (street address)
    postnr           text,               -- the underlying real postnr (e.g. "2630")
    bynavn           text                -- Bynavn (postal town)
);

-- Many addresses share one firmapostnummer; index nr for the postnr-side lookup.
CREATE INDEX IF NOT EXISTS stormodtagere_nr_idx ON stormodtagere (nr);
