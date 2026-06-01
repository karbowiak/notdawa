-- Ledger of ingestion runs, keyed on the Datafordeler generation so we can
-- detect gaps in the weekly delta chain and avoid re-ingesting a generation.
CREATE TABLE IF NOT EXISTS ingest_runs (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    register          TEXT        NOT NULL,
    entity            TEXT        NOT NULL,
    type_of_download  TEXT        NOT NULL,  -- TotalDownload | DeltaDownload
    generation_number INT         NOT NULL,
    file_name         TEXT        NOT NULL,
    md5_hash          TEXT,
    file_size_bytes   BIGINT,
    status            TEXT        NOT NULL DEFAULT 'pending', -- pending|downloaded|loaded|failed
    rows_loaded       BIGINT,
    error             TEXT,
    started_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at       TIMESTAMPTZ,
    UNIQUE (register, entity, type_of_download, generation_number)
);

CREATE INDEX IF NOT EXISTS ingest_runs_register_entity_idx
    ON ingest_runs (register, entity, generation_number DESC);
