-- DAWA treats the Danish "aa" digraph and "å" as the SAME letter in address
-- search, so a query for "Kongsgårdsvej" also matches roads spelled
-- "Kongsgaardsvej" (and vice-versa). Plain unaccent() maps å→a but leaves aa→aa,
-- so the two spellings never meet. dawa_fold() lowercases, unaccents (å→a, ø→o, …)
-- AND collapses aa→a, giving both spellings one canonical form ("kongsgardsvej").
-- The autocomplete road/address matchers call it on BOTH the query and the column
-- so membership (and the vejnavn→adgangsadresse→adresse escalation that hinges on
-- match counts) reproduces DAWA's folding.
--
-- IMMUTABLE (the unaccent dictionary is fixed at deploy time) so it can index.
CREATE OR REPLACE FUNCTION dawa_fold(text)
RETURNS text
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
STRICT
AS $$
  SELECT replace(lower(public.unaccent('public.unaccent'::regdictionary, $1)), 'aa', 'a')
$$;

-- Trigram GIN index on the FOLDED road name so the autocomplete road-name prefix
-- filter (dawa_fold(nv.navn) ILIKE dawa_fold(q||'%')) and the fuzzy similarity (%)
-- stay index-served, exactly as 029 did for f_unaccent.
CREATE INDEX IF NOT EXISTS dar_navngivenvej_navn_fold_trgm
  ON dar_navngivenvej USING gin (dawa_fold(navn) gin_trgm_ops);
