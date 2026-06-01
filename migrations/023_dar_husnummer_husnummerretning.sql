-- DAR Husnummer carries husnummerretning — a WKT unit direction vector
-- "POINT (dx dy)" (e.g. "POINT (1 1.2e-16)", "POINT (0.190809 -0.981627)") that
-- gives the orientation of the house-number text. We store the raw dx/dy.
--
-- NOTE on adgangspunkt.tekstretning: DAWA serves an independent stored
-- tekstretning attribute that is NOT byte-derivable from husnummerretning — the
-- compass bearing of this vector matches DAWA's value on only some rows (e.g.
-- vec (1,0) → bearing 0/90/200 candidates, none equal to golden 200), so the
-- serving layer leaves tekstretning NULL and the verifier classifies it as a
-- known-divergence field. The dx/dy columns are still stored as faithful raw
-- register data for any future use.
ALTER TABLE dar_husnummer ADD COLUMN IF NOT EXISTS husnummerretning_dx double precision;
ALTER TABLE dar_husnummer ADD COLUMN IF NOT EXISTS husnummerretning_dy double precision;
