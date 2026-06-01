-- DAGI Postnummerinddeling (postal-code areas) → /postnumre. Polygon geometry
-- like the other DAGI area entities, but keyed by `nr` (not kode) and with an
-- address-based kommuner[] relation. The polygons ALSO supply the road entities'
-- spatial postnumre branch (a postnummer polygon intersecting a road line by
-- >7 m, excluding gade-postnumre — see internal/dawa road queries).
CREATE TABLE IF NOT EXISTS dagi_postnumre (
    nr                TEXT PRIMARY KEY,                       -- DAGI postnummer (e.g. "2100")
    navn              TEXT NOT NULL,                          -- e.g. "København Ø"
    dagi_id           TEXT,                                   -- id_lokalId (e.g. "192100")
    aendret           TIMESTAMPTZ,                            -- best-effort (datafordelerOpdateringstid)
    geom              geometry(MultiPolygon, 25832) NOT NULL,
    generation_number INT NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS dagi_postnumre_geom_gix ON dagi_postnumre USING GIST (geom);

-- postnumre_kommuner is the ADDRESS-BASED postnr↔kommune relation that backs
-- /postnumre kommuner[] (and the inverse /postnumre?kommunekode= filter). It is
-- NOT spatial (proven: polygon intersection over-includes via slivers, 299/1089
-- wrong). DAWA derives it as DISTINCT(postnr, kommunekode) over the addresses
-- (Husnummer). We populate it from dar_husnummer + dar_postnummer + dagi_kommuner
-- (a pure in-DB SQL step; see ingest.PostnumreKommuner).
CREATE TABLE IF NOT EXISTS postnumre_kommuner (
    nr          TEXT NOT NULL, -- dar_postnummer.postnr
    kommunekode TEXT NOT NULL, -- dagi_kommuner.kode
    PRIMARY KEY (nr, kommunekode)
);

CREATE INDEX IF NOT EXISTS postnumre_kommuner_kom_idx ON postnumre_kommuner (kommunekode);
