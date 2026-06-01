-- valgkredsnummer is a served opstillingskreds field (a separate, non-monotonic
-- string from nummer, e.g. opk 59 has valgkredsnummer 11). It was missing from
-- migration 012; add it and re-ingest opstillingskredse to populate.
ALTER TABLE dagi_opstillingskredse
    ADD COLUMN IF NOT EXISTS valgkredsnummer TEXT;
