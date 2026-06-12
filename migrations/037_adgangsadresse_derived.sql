-- Precomputed per-address spatial memberships. The adgangsadresse/adresse
-- collection queries previously resolved landsdel/retskreds/politikreds/
-- afstemningsområde by per-row ST_Covers laterals against country-sized
-- multipolygons, plus per-row bebyggelse/brofast point-in-polygon subqueries —
-- measured ~34ms per output row (3.8s per 100-row page). The
-- adgangsadresse-derive import step rebuilds this table set-wise (polygon-outer
-- spatial joins) after the DAR/DAGI/DS/seed loads; serving joins it by PK.
CREATE TABLE IF NOT EXISTS adgangsadresse_derived (
    id               text PRIMARY KEY,  -- = dar_husnummer.id
    afstem_dagi_id   text,              -- dagi_afstemningsomraader.dagi_id covering the adgangspunkt
    landsdel_nuts3   text,
    retskreds_kode   text,
    politikreds_kode text,
    bebyggelser      jsonb,             -- pre-aggregated [{id,kode,type,navn}] (NULL = none)
    brofast          boolean NOT NULL DEFAULT true
);

-- Join-key indexes for the dagi_id-keyed lookups in the address join graph.
-- These joins previously hash/materialize-scanned the DAGI tables per query
-- (e.g. the sogn join discarded ~210k rows per 100 output rows).
CREATE INDEX IF NOT EXISTS dagi_sogne_dagi_id_idx ON dagi_sogne (dagi_id);
CREATE INDEX IF NOT EXISTS dagi_kommuner_dagi_id_idx ON dagi_kommuner (dagi_id);
CREATE INDEX IF NOT EXISTS dagi_opstillingskredse_dagi_id_idx ON dagi_opstillingskredse (dagi_id);
CREATE INDEX IF NOT EXISTS dagi_storkredse_dagi_id_idx ON dagi_storkredse (dagi_id);

-- /historik kommunekode filter: the serve-time filter expression
-- COALESCE(hh.kommunekode, split_part(cur.vejmidte,'-',1)) crosses a join and
-- can never use an index. The historik-segments build (dataVersion 2) resolves
-- it into this column; the old single-column index never matched the filter
-- (pg_stat: 0 scans, 33MB) and is replaced.
ALTER TABLE dar_husnummer_hist_seg ADD COLUMN IF NOT EXISTS kommunekode_resolved text;
DROP INDEX IF EXISTS dar_husnummer_hist_seg_kom_idx;
CREATE INDEX IF NOT EXISTS dar_husnummer_hist_seg_komres_idx
    ON dar_husnummer_hist_seg (kommunekode_resolved, id, virkning_start);

-- Never used by any query (its only consumer aggregates the whole table):
-- 74MB of pure write amplification on a 2.57M-row weekly reload.
DROP INDEX IF EXISTS mat_lodflade_jordstykke_idx;
