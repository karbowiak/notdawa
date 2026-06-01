-- DAGI Afstemningsområde (polling districts). Its own /afstemningsomraader
-- endpoint (composite key kommune/nummer) AND the source of opstillingskredse'
-- kommuner[]. afstemningssted.adgangsadresse depends on DAR adgangsadresser
-- (built LAST); store the raw adresse lokalId now and resolve later.
CREATE TABLE IF NOT EXISTS dagi_afstemningsomraader (
    dagi_id                TEXT PRIMARY KEY,                       -- id_lokalId
    nummer                 TEXT NOT NULL,                          -- afstemningsomraadenummer (e.g. "1")
    navn                   TEXT,
    kommune_lokalid        TEXT,                                   -- kommuneLokalId -> dagi_kommuner.dagi_id
    opstillingskreds_lokalid TEXT,                                 -- opstillingskredsLokalId -> dagi_opstillingskredse.dagi_id
    afstemningssted_navn   TEXT,
    afstemningssted_adresse_lokalid TEXT,                          -- adgangsadresse id (resolve when adresser built)
    aendret                TIMESTAMPTZ,
    geom                   geometry(MultiPolygon, 25832) NOT NULL,
    visueltcenter          geometry(Point, 25832),
    generation_number      INT NOT NULL,
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS dagi_afstem_geom_gix    ON dagi_afstemningsomraader USING GIST (geom);
CREATE INDEX IF NOT EXISTS dagi_afstem_opstil_idx  ON dagi_afstemningsomraader (opstillingskreds_lokalid);
CREATE INDEX IF NOT EXISTS dagi_afstem_kommune_idx ON dagi_afstemningsomraader (kommune_lokalid);

-- Static kredskommune designation per opstillingskreds (NOT in any DAGI extract;
-- an electoral-law assignment). Captured live from DAWA 2026-05-29 (all 92).
CREATE TABLE IF NOT EXISTS opstillingskreds_kredskommune (
    nummer      TEXT PRIMARY KEY,
    kommunekode TEXT NOT NULL
);

INSERT INTO opstillingskreds_kredskommune (nummer, kommunekode) VALUES
 ('1','0101'),('2','0101'),('3','0101'),('4','0101'),('5','0101'),('6','0101'),
 ('7','0101'),('8','0101'),('9','0101'),('10','0147'),('11','0147'),('12','0185'),
 ('13','0157'),('14','0173'),('15','0159'),('16','0175'),('17','0167'),('18','0153'),
 ('19','0169'),('20','0151'),('21','0217'),('22','0210'),('23','0219'),('24','0250'),
 ('25','0240'),('26','0230'),('27','0400'),('28','0400'),('29','0360'),('30','0376'),
 ('31','0390'),('32','0370'),('33','0320'),('34','0259'),('35','0253'),('36','0265'),
 ('37','0316'),('38','0326'),('39','0329'),('40','0330'),('41','0461'),('42','0461'),
 ('43','0461'),('44','0420'),('45','0410'),('46','0450'),('47','0479'),('48','0430'),
 ('49','0540'),('50','0580'),('51','0550'),('52','0561'),('53','0561'),('54','0573'),
 ('55','0575'),('56','0630'),('57','0630'),('58','0607'),('59','0621'),('60','0621'),
 ('61','0510'),('62','0751'),('63','0751'),('64','0751'),('65','0751'),('66','0706'),
 ('67','0730'),('68','0730'),('69','0710'),('70','0746'),('71','0615'),('72','0766'),
 ('73','0671'),('74','0779'),('75','0791'),('76','0791'),('77','0740'),('78','0740'),
 ('79','0756'),('80','0657'),('81','0657'),('82','0661'),('83','0760'),('84','0813'),
 ('85','0860'),('86','0810'),('87','0787'),('88','0820'),('89','0846'),('90','0851'),
 ('91','0851'),('92','0851')
ON CONFLICT (nummer) DO UPDATE SET kommunekode = EXCLUDED.kommunekode;
