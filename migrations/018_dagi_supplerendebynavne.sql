-- DAGI SupplerendeBynavn → /supplerendebynavne2. A hybrid entity: geometry +
-- identity from DAGI; darstatus + the DAR join UUID from DAR SupplerendeBynavn;
-- postnumre[] from the DAR address graph (husnummer.supplerende_bynavn UUID).
--
-- The postnumre[] join needs husnummer.supplerende_bynavn, which migration 010
-- did not store — add it here and re-ingest husnummer.
ALTER TABLE dar_husnummer ADD COLUMN IF NOT EXISTS supplerende_bynavn TEXT;
CREATE INDEX IF NOT EXISTS dar_husnummer_sb_idx ON dar_husnummer (supplerende_bynavn);

CREATE TABLE IF NOT EXISTS dagi_supplerendebynavne (
    dagi_id           TEXT PRIMARY KEY,                       -- DAGI id_lokalId (e.g. "654870")
    navn              TEXT NOT NULL,
    kommune_lokalid   TEXT,                                   -- DAGI kommuneLokalId -> dagi_kommuner.dagi_id
    darstatus         INT,                                    -- from DAR SupplerendeBynavn.status
    dar_uuid          TEXT,                                   -- DAR SupplerendeBynavn.id_lokalId (husnummer join key)
    aendret           TIMESTAMPTZ,
    geom              geometry(MultiPolygon, 25832) NOT NULL,
    visueltcenter     geometry(Point, 25832),
    generation_number INT NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS dagi_supplerendebynavne_geom_gix ON dagi_supplerendebynavne USING GIST (geom);
CREATE INDEX IF NOT EXISTS dagi_supplerendebynavne_dar_idx  ON dagi_supplerendebynavne (dar_uuid);
