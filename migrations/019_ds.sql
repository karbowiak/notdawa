-- DS (Danske Stednavne) → /steder, /stednavne2, /bebyggelser. A union of ~27
-- place-type extracts (one ds_steder row each) plus a separate names table
-- (ds_stednavne) joined on the place's objectid (NOT id_lokalId). Geometry is
-- MIXED (polygon/line/point) across place types, so geometry(Geometry,25832),
-- NOT MultiPolygon — the DAGI geomExpr would drop lines/points.
CREATE TABLE IF NOT EXISTS ds_steder (
    id_lokalId        TEXT PRIMARY KEY,                  -- the public id/href key (UUID)
    objectid          TEXT NOT NULL,                     -- internal key Stednavn joins on
    hovedtype         TEXT NOT NULL,                     -- display type, e.g. "Vej", "Bebyggelse", "Sø"
    undertype         TEXT,                              -- per-type subtype value (vejtype, bebyggelsestype, ...)
    bebyggelseskode   INT,                               -- Bebyggelse only (→ /bebyggelser.kode + egenskaber)
    indbyggertal      INT,                               -- Bebyggelse only (→ egenskaber.indbyggerantal)
    aendret           TIMESTAMPTZ,                       -- best-effort (DAWA uses a uniform import ts; metadata)
    geom              geometry(Geometry, 25832) NOT NULL,
    visueltcenter     geometry(Point, 25832),
    generation_number INT NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ds_steder_geom_gix    ON ds_steder USING GIST (geom);
CREATE INDEX IF NOT EXISTS ds_steder_objectid_idx ON ds_steder (objectid);
CREATE INDEX IF NOT EXISTS ds_steder_hovedtype_idx ON ds_steder (hovedtype);

CREATE TABLE IF NOT EXISTS ds_stednavne (
    id_lokalId        TEXT PRIMARY KEY,                  -- Stednavn id_lokalId
    place_objectid    TEXT NOT NULL,                     -- navngivetObjekt_objectid -> ds_steder.objectid
    skrivemaade       TEXT NOT NULL,                     -- the name
    navnestatus       TEXT,                              -- navnestatus_status
    brugsprioritet    TEXT,                              -- primær | sekundær
    navnefoelgenummer INT,                               -- ordering tie-break
    generation_number INT NOT NULL
);
CREATE INDEX IF NOT EXISTS ds_stednavne_place_idx ON ds_stednavne (place_objectid);
