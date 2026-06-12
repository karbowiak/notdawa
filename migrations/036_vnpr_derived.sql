-- Precomputed /vejnavnpostnummerrelationer derivation. DAWA itself served this
-- resource from import-time materializations (navngivenvejpostnummerrelation /
-- vejnavnpostnummerrelation); computing the 6-CTE spatial derivation per
-- request measured ~60s per collection query. Rebuilt by the vnpr-derive
-- import step (one transaction, TRUNCATE + INSERT from the same CTE bodies the
-- serving code used to inline — values byte-identical, computed weekly).

-- vnpr_perrel: the (navngivenvej, postnr) relation membership. Build-internal:
-- the vnpr_agg kommuner[] aggregation probes it by (postnr); serving never
-- touches it.
CREATE TABLE IF NOT EXISTS vnpr_perrel (
    nvid   text NOT NULL,
    postnr int  NOT NULL,
    PRIMARY KEY (postnr, nvid)
);

-- vnpr_agg: the served relation rows aggregated by vejnavn text. The PK matches
-- the resource's fixed ORDER BY (postnr, vejnavn) and the single-GET key.
-- kommuner is the fully-aggregated [{kode,navn}] JSON (the correlated subquery
-- it replaces depended only on (postnr, vejnavn) — measured ~1ms/row, which is
-- 2min for the full dump if evaluated at serve time).
CREATE TABLE IF NOT EXISTS vnpr_agg (
    vejnavn  text  NOT NULL,
    postnr   int   NOT NULL,
    geom     geometry(Geometry, 25832),
    kommuner jsonb NOT NULL DEFAULT '[]'::jsonb,
    PRIMARY KEY (postnr, vejnavn)
);
