-- 021_mat_jordstykke_dedup.sql — relax the (ejerlav_kode, matrikelnr) uniqueness
-- from a constraint enforced mid-INSERT to a plain lookup index. The raw MAT
-- Jordstykke extract contains multiple records (distinct id_lokalId) for the same
-- (ejerlav, matrikelnr) — temporal/registration variants — so a UNIQUE index
-- aborts the streaming batch insert. We instead load everything keyed by
-- id_lokalid (PK) and dedup to the current parcel (latest registreringFra) per
-- key in a post-load pass (see ingest.Jordstykke). DAWA's contract is one current
-- parcel per (ejerlavkode, matrikelnr); the dedup pass restores that invariant.
DROP INDEX IF EXISTS mat_jordstykke_ejerlav_matr_uq;
CREATE INDEX IF NOT EXISTS mat_jordstykke_ejerlav_matr_idx
    ON mat_jordstykke (ejerlav_kode, matrikelnr);
