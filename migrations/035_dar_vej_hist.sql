-- Road virkning history: backs navngivneveje/vejstykker historik (the last
-- blanket-tolerated historik in the suite). Raw reg-current versions, NOT
-- consolidated — historik.ændret derives from MAX(virkning_start), which a
-- projection-merge would destroy. Small data (NavngivenVej bitemporal 0.85 GB,
-- NavngivenVejKommunedel 42 MB).
CREATE TABLE IF NOT EXISTS dar_navngivenvej_hist (
    id                TEXT NOT NULL,        -- NavngivenVej id_lokalId
    dar_status        INT,
    virkning_start    TIMESTAMPTZ NOT NULL,
    virkning_slut     TIMESTAMPTZ,
    generation_number INT NOT NULL
);
CREATE INDEX IF NOT EXISTS dar_navngivenvej_hist_id_idx ON dar_navngivenvej_hist (id, virkning_start);

CREATE TABLE IF NOT EXISTS dar_nvkommunedel_hist (
    id                TEXT NOT NULL,        -- NavngivenVejKommunedel id_lokalId (the vejstykke id)
    dar_status        INT,
    virkning_start    TIMESTAMPTZ NOT NULL,
    virkning_slut     TIMESTAMPTZ,
    generation_number INT NOT NULL
);
CREATE INDEX IF NOT EXISTS dar_nvkommunedel_hist_id_idx ON dar_nvkommunedel_hist (id, virkning_start);
