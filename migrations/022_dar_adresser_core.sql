-- DAR address core: the adgangsadresse (Husnummer) full attributes, the
-- adgangspunkt/vejpunkt geometry+origin (Adressepunkt) and the enhedsadresse
-- (Adresse). Together with the existing DAGI/MAT mirrors these back
-- /adgangsadresser and /adresser.
--
-- The existing dar_husnummer (migration 010) only carried the road-endpoint
-- link columns (navngivenvej, postnummer_id, kommwune_kode [sic — the typo is
-- load-bearing: the vej/postnr queries reference it], supplerende_bynavn,
-- status). The full adgangsadresse needs the husnummertekst, the
-- adgangspunkt/vejpunkt UUIDs, the DAGI inddeling UUIDs (sogn, afstemning),
-- the jordstykke UUID, vejmidte (the "KKKK-VVVV" fast path for kommune+vejkode)
-- and the historik timestamps. Added via ALTER ... ADD COLUMN IF NOT EXISTS so
-- the existing columns and their indexes are untouched.

ALTER TABLE dar_husnummer ADD COLUMN IF NOT EXISTS husnummertekst        text;
ALTER TABLE dar_husnummer ADD COLUMN IF NOT EXISTS vejmidte              text;   -- "KKKK-VVVV" kommunekode-vejkode
ALTER TABLE dar_husnummer ADD COLUMN IF NOT EXISTS adgangspunkt_id       text;   -- -> dar_adressepunkt.id_lokalid
ALTER TABLE dar_husnummer ADD COLUMN IF NOT EXISTS vejpunkt_id           text;   -- -> dar_adressepunkt.id_lokalid
ALTER TABLE dar_husnummer ADD COLUMN IF NOT EXISTS sogn_dagi_id          text;   -- DAGI Sogneinddeling id_lokalId
ALTER TABLE dar_husnummer ADD COLUMN IF NOT EXISTS afstemning_dagi_id    text;   -- DAGI Afstemningsomraade id_lokalId
ALTER TABLE dar_husnummer ADD COLUMN IF NOT EXISTS jordstykke_id         text;   -- MAT Jordstykke id_lokalId
ALTER TABLE dar_husnummer ADD COLUMN IF NOT EXISTS dar_status            int;    -- raw DAR status int (gældende=3)
ALTER TABLE dar_husnummer ADD COLUMN IF NOT EXISTS oprettet              timestamptz; -- registreringFra
ALTER TABLE dar_husnummer ADD COLUMN IF NOT EXISTS ikrafttraedelse       timestamptz; -- virkningFra
ALTER TABLE dar_husnummer ADD COLUMN IF NOT EXISTS aendret               timestamptz; -- datafordelerOpdateringstid

CREATE INDEX IF NOT EXISTS dar_husnummer_adgangspunkt_idx ON dar_husnummer (adgangspunkt_id);
CREATE INDEX IF NOT EXISTS dar_husnummer_vejpunkt_idx     ON dar_husnummer (vejpunkt_id);
CREATE INDEX IF NOT EXISTS dar_husnummer_jordstykke_idx   ON dar_husnummer (jordstykke_id);

-- DAR Adressepunkt: adgangspunkt + vejpunkt geometry & origin metadata. The
-- raw 25832 point is stored for DDKN, spatial ST_Covers tilknytninger and
-- reverse KNN; koordinater are derived to WGS84 8-dec at serve time.
CREATE TABLE IF NOT EXISTS dar_adressepunkt (
	id_lokalid         text PRIMARY KEY,
	geom               geometry(Point, 25832),
	hoejde             double precision, -- "højde"
	tekstretning       double precision, -- "tekstretning"
	noejagtighedsklasse text,            -- adgangspunkt/vejpunkt "nøjagtighed" (A/...)
	tekniskstandard    text,             -- "tekniskstandard" (TN/V0/...)
	kilde              text,             -- origin source code/string (kilde)
	aendret            timestamptz,      -- origin "ændret" (registreringFra/oprindelse_registrering)
	status             text,
	generation_number  int
);

CREATE INDEX IF NOT EXISTS dar_adressepunkt_geom_idx ON dar_adressepunkt USING gist (geom);

-- DAR Adresse: the enhedsadresse / unit address. Each row links to a Husnummer
-- (the embedded adgangsadresse) and carries the etage/dør betegnelser.
CREATE TABLE IF NOT EXISTS dar_adresse (
	id_lokalid         text PRIMARY KEY,
	husnummer_id       text,             -- -> dar_husnummer.id
	etagebetegnelse    text,
	doerbetegnelse     text,             -- "dørbetegnelse"
	status             text,
	dar_status         int,              -- raw DAR status int (gældende=3)
	oprettet           timestamptz,      -- registreringFra
	ikrafttraedelse    timestamptz,      -- virkningFra
	aendret            timestamptz,      -- datafordelerOpdateringstid
	generation_number  int
);

CREATE INDEX IF NOT EXISTS dar_adresse_husnummer_idx ON dar_adresse (husnummer_id);
