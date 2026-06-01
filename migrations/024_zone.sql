-- Plandata.dk planning zones (byzone / sommerhusområde / landzone) → /zonetilknytninger.
-- Source: Plandata open WFS pdk:theme_pdk_zonekort_v (6126 polygon features, EPSG:25832).
-- Loaded by internal/ingest/zone.go (Zoner). The integer `zone` column matches DAWA's
-- zone codes (1 byzone, 2 sommerhusområde, 3 landzone) so downstream output is byte-compatible.
CREATE TABLE IF NOT EXISTS zone (
    id                BIGSERIAL PRIMARY KEY,
    zone              INT NOT NULL,                          -- 1 byzone, 2 sommerhusområde, 3 landzone (DAWA codes)
    zone_navn         TEXT,                                  -- source label (Byzone / Sommerhusområde / Landzone)
    geom              geometry(MultiPolygon, 25832) NOT NULL,
    generation_number INT
);
CREATE INDEX IF NOT EXISTS zone_geom_gix ON zone USING GIST (geom);
CREATE INDEX IF NOT EXISTS zone_zone_idx ON zone (zone);
