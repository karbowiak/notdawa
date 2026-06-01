-- DAGI MRafstemningsområde (menighedsråd / parish-council voting districts).
-- Mirrors dagi_afstemningsomraader (regular polling districts) in shape: same
-- geometry + identity columns, its own /menighedsraadsafstemningsomraader
-- endpoint, and the future source of /menighedsraadsafstemningsomraadetilknytninger.
-- Each row is a full-res (1:10.000) feature keyed by its DAGI id_lokalId.
--
-- KEY CHOICE: unlike Afstemningsområde (composite kommunekode/nummer), the live
-- MRafstemningsomraade extract carries a NULL MRafstemningsomraadenummer on every
-- feature, so (kommune, nummer) is not a usable key. id_lokalId is the only stable
-- unique identity (2128 distinct ids == 2128 full-res rows in gen 645) — so the PK
-- is dagi_id. nummer is kept (nullable) to mirror the afstemningsomraader column.
CREATE TABLE IF NOT EXISTS dagi_mrafstemningsomraader (
    dagi_id                TEXT PRIMARY KEY,                       -- id_lokalId
    nummer                 TEXT,                                   -- MRafstemningsomraadenummer (null in current extract)
    navn                   TEXT,
    kommune_lokalid        TEXT,                                   -- kommuneLokalId -> dagi_kommuner.dagi_id
    sogn_lokalid           TEXT,                                   -- sognLokalId -> dagi_sogne.dagi_id
    aendret                TIMESTAMPTZ,
    geom                   geometry(MultiPolygon, 25832) NOT NULL,
    visueltcenter          geometry(Point, 25832),
    generation_number      INT NOT NULL,
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS dagi_mrafstem_geom_gix    ON dagi_mrafstemningsomraader USING GIST (geom);
CREATE INDEX IF NOT EXISTS dagi_mrafstem_kommune_idx ON dagi_mrafstemningsomraader (kommune_lokalid);
CREATE INDEX IF NOT EXISTS dagi_mrafstem_sogn_idx    ON dagi_mrafstemningsomraader (sogn_lokalid);
