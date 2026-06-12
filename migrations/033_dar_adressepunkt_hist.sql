-- Adressepunkt virkning history + the segmented husnummer-history serving
-- table. live DAWA's /historik versions adgangspunktstatus on the PUNKT's own
-- lifecycle (verified by sweep 2026-06-11: chains flip 3→1→4 as the punkt
-- moves 6→8→9, and show null before the punkt existed), so the husnummer
-- chain alone cannot reproduce the rows: dar_husnummer_hist is intersected
-- with dar_adressepunkt_hist into dar_husnummer_hist_seg by the derived
-- historik-segments import step, and /historik serves from the _seg table.

CREATE TABLE IF NOT EXISTS dar_adressepunkt_hist (
    id                TEXT NOT NULL,        -- Adressepunkt id_lokalId
    status            TEXT,                 -- raw DAR punkt status (6/8/9)
    virkning_start    TIMESTAMPTZ NOT NULL,
    virkning_slut     TIMESTAMPTZ,
    generation_number INT NOT NULL
);

CREATE INDEX IF NOT EXISTS dar_adressepunkt_hist_id_idx ON dar_adressepunkt_hist (id, virkning_start);

CREATE TABLE IF NOT EXISTS dar_husnummer_hist_seg (
    id                  TEXT NOT NULL,      -- Husnummer id_lokalId
    dar_status          INT,
    husnr               TEXT,
    kommunekode         TEXT,
    vejkode             TEXT,
    navngivenvej        TEXT,
    postnummer          TEXT,
    supplerendebynavn   TEXT,
    adgangspunkt_status TEXT,               -- punkt status DURING this segment (null = punkt not yet/no longer known)
    virkning_start      TIMESTAMPTZ NOT NULL,
    virkning_slut       TIMESTAMPTZ,
    generation_number   INT NOT NULL
);

CREATE INDEX IF NOT EXISTS dar_husnummer_hist_seg_id_idx  ON dar_husnummer_hist_seg (id, virkning_start);
CREATE INDEX IF NOT EXISTS dar_husnummer_hist_seg_kom_idx ON dar_husnummer_hist_seg (kommunekode);
CREATE INDEX IF NOT EXISTS dar_husnummer_hist_seg_pn_idx  ON dar_husnummer_hist_seg (postnummer);
