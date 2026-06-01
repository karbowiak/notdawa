-- DAR Husnummer (address access points). 620 MB JSON / ~2.4M rows, loaded via
-- the STREAMING loader. This is the address layer that makes the road entities'
-- postnumre[] byte-exact: DAWA's navngivenvej/vejstykke/vejnavn postnumre is NOT
-- the raw DAR NavngivenVejPostnummer link table (a superset), but the subset of
-- postnumre actually backed by addresses — i.e. DISTINCT(darNavngivenVej,
-- postnummer) over the status-3 Husnummer rows (proven: Abel Cathrines Gade's
-- link set {1650,1654,1655,1700} vs DAWA's {1654}).
--
-- Husnummer is also the keystone for postnumre.kommuner[], supplerendebynavne2,
-- and (ultimately) adgangsadresser/adresser. For now we store only the columns
-- the road postnumre[] fix needs; the adgangspunkt geometry + richer refs are
-- added when adgangsadresser is built.
CREATE TABLE IF NOT EXISTS dar_husnummer (
    id                TEXT PRIMARY KEY, -- id_lokalId (uuid)
    navngivenvej      TEXT,             -- navngivenVej (uuid) -> dar_navngivenvej.id
    postnummer_id     TEXT,             -- postnummer (uuid) -> dar_postnummer.id
    kommune           TEXT,             -- kommuneinddeling = DAGI kommune id_lokalId (e.g. "389183") -> dagi_kommuner.dagi_id; NOT a kommunekode
    status            TEXT NOT NULL,    -- raw DAR status code (always '3' after filter)
    generation_number INT  NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The road postnumre[] join is DISTINCT(navngivenvej, postnummer_id); index both.
CREATE INDEX IF NOT EXISTS dar_husnummer_nv_idx   ON dar_husnummer (navngivenvej);
CREATE INDEX IF NOT EXISTS dar_husnummer_pn_idx   ON dar_husnummer (postnummer_id);
CREATE INDEX IF NOT EXISTS dar_husnummer_kom_idx  ON dar_husnummer (kommune);
