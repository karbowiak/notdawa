-- /opstillingskredse serving extras. bbox/visueltcenter come from the
-- opstillingskreds's OWN polygon (verified: opk 11's own bbox == DAWA; the
-- København opstillingskredse share their kredskommune polygon in the raw DAGI
-- data, so their bbox == kommune 0101's). visueltcenter is the Mapbox polylabel,
-- precomputed at ingest like storkredse/valglandsdele.
ALTER TABLE dagi_opstillingskredse
    ADD COLUMN IF NOT EXISTS visueltcenter geometry(Point, 25832);

-- The kredskommune-first kommuner[] relation: DAWA lists the kredskommune first,
-- then the OTHER kommuner the opstillingskreds geographically spans. Stored as a
-- derived relation with an explicit ordinal so serving preserves DAWA's order.
CREATE TABLE IF NOT EXISTS opstillingskreds_kommuner (
    opstillingskredsnummer TEXT NOT NULL,
    kommunekode            TEXT NOT NULL,
    ord                    INT  NOT NULL, -- 0 = kredskommune, then ascending
    PRIMARY KEY (opstillingskredsnummer, kommunekode)
);
