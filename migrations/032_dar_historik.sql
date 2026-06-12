-- DAR virkning history for /historik/adgangsadresser and /historik/adresser.
-- Sourced from the DAR *Bitemporal* TotalDownloads (Husnummer 3.6 GB, Adresse
-- 0.8 GB zipped), filtered to the current registrering (registreringTil IS
-- NULL): that filtering yields exactly the virkning chain DAWA's historik
-- endpoints emit, one row per version (verified against live 2026-06-11; live
-- DAWA passes versions through 1:1 even when the projected fields are equal).
--
-- Lean projection only: the endpoints' row shape is small (status, road/postal
-- attribution, husnr, the virkning interval) and names resolve at serve time
-- via the existing current-mirror lookups (dar_navngivenvej, dar_postnummer,
-- dar_supplerendebynavn, dar_adressepunkt).

CREATE TABLE IF NOT EXISTS dar_husnummer_hist (
    id                TEXT NOT NULL,        -- Husnummer id_lokalId (uuid)
    dar_status        INT,                  -- raw DAR status (gældende=3)
    husnr             TEXT,                 -- husnummertekst
    kommunekode       TEXT,                 -- from vejmidte "KKKK-VVVV"
    vejkode           TEXT,                 -- from vejmidte "KKKK-VVVV"
    navngivenvej      TEXT,                 -- -> dar_navngivenvej.id
    postnummer        TEXT,                 -- -> dar_postnummer.id
    supplerendebynavn TEXT,                 -- -> dar_supplerendebynavn.dar_uuid
    adgangspunkt      TEXT,                 -- -> dar_adressepunkt.id_lokalid
    virkning_start    TIMESTAMPTZ NOT NULL,
    virkning_slut     TIMESTAMPTZ,          -- null = current version
    generation_number INT NOT NULL
);

CREATE INDEX IF NOT EXISTS dar_husnummer_hist_id_idx  ON dar_husnummer_hist (id, virkning_start);
CREATE INDEX IF NOT EXISTS dar_husnummer_hist_kom_idx ON dar_husnummer_hist (kommunekode);
CREATE INDEX IF NOT EXISTS dar_husnummer_hist_pn_idx  ON dar_husnummer_hist (postnummer);

CREATE TABLE IF NOT EXISTS dar_adresse_hist (
    id                TEXT NOT NULL,        -- Adresse id_lokalId (uuid)
    dar_status        INT,
    husnummer         TEXT,                 -- -> dar_husnummer_hist.id (adgangsadresseid)
    etage             TEXT,                 -- etagebetegnelse
    doer              TEXT,                 -- dørbetegnelse
    virkning_start    TIMESTAMPTZ NOT NULL,
    virkning_slut     TIMESTAMPTZ,
    generation_number INT NOT NULL
);

CREATE INDEX IF NOT EXISTS dar_adresse_hist_id_idx ON dar_adresse_hist (id, virkning_start);
CREATE INDEX IF NOT EXISTS dar_adresse_hist_hn_idx ON dar_adresse_hist (husnummer);
