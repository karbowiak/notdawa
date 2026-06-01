-- Autocomplete performance: the address autocomplete narrows candidates by a
-- road-name prefix (unaccent(nv.navn) ILIKE unaccent(q||'%')) before applying the
-- per-row tsvector AND-match. Without an index that prefix is a full seq scan of
-- dar_navngivenvej (~113k rows, ~90 ms) on EVERY autocomplete request. A GIN
-- trigram index on the unaccented name serves the ILIKE directly.
--
-- Stock unaccent() is STABLE, so it cannot appear in an index expression; wrap it
-- in an IMMUTABLE function (safe here: the unaccent dictionary is fixed at deploy
-- time). The query side must call this SAME function for the index to be used.

CREATE OR REPLACE FUNCTION f_unaccent(text)
RETURNS text
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
STRICT
AS $$
  SELECT public.unaccent('public.unaccent'::regdictionary, $1)
$$;

-- Trigram GIN index on the unaccented road name powers prefix/substring ILIKE.
CREATE INDEX IF NOT EXISTS dar_navngivenvej_navn_trgm
  ON dar_navngivenvej USING gin (f_unaccent(navn) gin_trgm_ops);
