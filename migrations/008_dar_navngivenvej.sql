-- DAR NavngivenVej (base table for /navngivneveje, /vejstykker, /vejnavne).
-- Loaded via the STREAMING loader (394 MB JSON / 112883 rows). Geometry is a
-- line (vejnavnelinje) in EPSG:25832 — bbox/visueltcenter are line-derived, so
-- this uses geometry(Geometry) not MultiPolygon. The serving layer + link
-- tables (vejstykker[]/postnumre[]) are layered on top in a follow-on.
CREATE TABLE IF NOT EXISTS dar_navngivenvej (
    id                             TEXT PRIMARY KEY,          -- id_lokalId (uuid)
    navn                           TEXT,                      -- vejnavn
    adresseringsnavn               TEXT,                      -- vejadresseringsnavn
    udtaltvejnavn                  TEXT,                      -- udtaltVejnavn
    administrerende_kommune        TEXT,                      -- administreresAfKommune (kommunekode)
    status                         TEXT,                      -- raw DAR status code (e.g. "3")
    oprindelse_kilde               TEXT,
    oprindelse_noejagtighedsklasse TEXT,
    oprindelse_registrering        TEXT,
    oprindelse_teknisk_standard    TEXT,
    registrering_fra               TIMESTAMPTZ,
    virkning_fra                   TIMESTAMPTZ,
    geom                           geometry(Geometry, 25832), -- vejnavnelinje, nullable
    generation_number              INT NOT NULL,
    updated_at                     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS dar_navngivenvej_geom_gix    ON dar_navngivenvej USING GIST (geom);
CREATE INDEX IF NOT EXISTS dar_navngivenvej_kommune_idx ON dar_navngivenvej (administrerende_kommune);
