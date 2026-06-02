-- Brofasthed: DAWA's per-sted (place/island) "brofast" flag — whether a place is
-- connected to the bridge-fast ("brofaste") road network. It is NOT a DAR source
-- attribute (the DAR Husnummer object has no brofast field); DAWA computes
-- adgangsadresse.brofast from this curated dataset: an address is NOT brofast iff
-- its adgangspunkt lies in a sted marked brofast=false (the ~377 non-connected
-- islands — Bornholm, Ærø, Fanø, Samsø, Læsø, …). Seeded from DAWA's vendored
-- data/brofasthed.csv (MIT, SDFI/SDFE open data), keyed by ds_steder.id_lokalId.
CREATE TABLE IF NOT EXISTS brofasthed (
    stedid  TEXT PRIMARY KEY,   -- ds_steder.id_lokalId of the place/island
    brofast BOOLEAN             -- true = on the bridge-fast network
);
