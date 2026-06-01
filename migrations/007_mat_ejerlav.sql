-- MAT Ejerlav (ejerlav, cadastral districts). DAWA output is minimal: href,
-- kode (INTEGER), navn, bbox, visueltcenter — no DAWA-internal metadata fields.
--   ejerlavskode -> kode (int), ejerlavsnavn -> navn, geometri -> geom.
CREATE TABLE IF NOT EXISTS mat_ejerlav (
    kode              INTEGER PRIMARY KEY,                    -- ejerlavskode, e.g. 10051
    navn              TEXT NOT NULL,
    geom              geometry(MultiPolygon, 25832) NOT NULL,
    generation_number INT  NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS mat_ejerlav_geom_gix ON mat_ejerlav USING GIST (geom);
