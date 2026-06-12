-- Aggregated access log: every distinct GET path+query the API has served,
-- with hit counts. Written by the server's traffic recorder (batched upserts,
-- internal/api/traffic.go). Purpose: before DAWA shuts down 2026-07-01, replay
-- every REAL request shape against both servers (tests/traffic_replay_test.go)
-- to flush out bugs synthetic sampling misses — and freeze DAWA's responses to
-- the actual production traffic as part of the golden snapshot. Paths only:
-- no IPs, no user agents, no timestamps per request.
CREATE TABLE IF NOT EXISTS access_paths (
    path        TEXT PRIMARY KEY,           -- verbatim RequestURI (path?query)
    hits        BIGINT NOT NULL DEFAULT 0,
    last_status INT,
    first_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS access_paths_hits_idx ON access_paths (hits DESC);
