-- Precomputed vejstykke→postnumre[] (DAWA had vejstykkerpostnumremat). The
-- serving queries previously evaluated a per-row LATERAL unioning address
-- postnumre with a road∩kommune∩postnr polygon clip (~9.5ms/row; 1-2s cold
-- autocomplete). Rebuilt by the vejstykke-postnumre import step; serving joins
-- by (navngivenvej, kommune). Pairs with no postnumre have no row (serve-time
-- COALESCE supplies the empty []).
CREATE TABLE IF NOT EXISTS vejstykke_postnumre (
    navngivenvej text  NOT NULL,
    kommune      text  NOT NULL,
    postnumre    jsonb NOT NULL,
    PRIMARY KEY (navngivenvej, kommune)
);
