package ingest

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/datafordeler"
)

// navngivenvejFeature is the subset of raw DAR NavngivenVej grunddata we use.
// The extract is 394 MB JSON / 112883 rows, so it loads via streamLoad.
type navngivenvejFeature struct {
	IDLokalId              string `json:"id_lokalId"`
	Vejnavn                string `json:"vejnavn"`
	Vejadresseringsnavn    string `json:"vejadresseringsnavn"`
	UdtaltVejnavn          string `json:"udtaltVejnavn"`
	AdministreresAfKommune string `json:"administreresAfKommune"` // already a kommunekode, e.g. "0217"
	Status                 string `json:"status"`
	OprKilde               string `json:"vejnavnebeliggenhed_oprindelse_kilde"`
	OprNoejagtighed        string `json:"vejnavnebeliggenhed_oprindelse_nøjagtighedsklasse"`
	OprRegistrering        string `json:"vejnavnebeliggenhed_oprindelse_registrering"`
	OprTekniskStandard     string `json:"vejnavnebeliggenhed_oprindelse_tekniskStandard"`
	RegistreringFra        string `json:"registreringFra"`
	VirkningFra            string `json:"virkningFra"`
	Vejnavnelinje          string `json:"vejnavnebeliggenhed_vejnavnelinje"`  // WKT line, EPSG:25832 (may be empty)
	Vejnavneområde         string `json:"vejnavnebeliggenhed_vejnavneområde"` // WKT polygon/område, EPSG:25832 (may be empty)
}

// NavngivenVej streams DAR NavngivenVej into dar_navngivenvej (the base for the
// /navngivneveje, /vejstykker and /vejnavne endpoints).
func NavngivenVej(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client) (Result, error) {
	return streamLoad(ctx, pool, client, "DAR", "NavngivenVej", "dar_navngivenvej",
		`INSERT INTO dar_navngivenvej
			(id, navn, adresseringsnavn, udtaltvejnavn, administrerende_kommune, status,
			 oprindelse_kilde, oprindelse_noejagtighedsklasse, oprindelse_registrering,
			 oprindelse_teknisk_standard, registrering_fra, virkning_fra, geom, generation_number)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::timestamptz, $12::timestamptz,
			 COALESCE(ST_Force2D(ST_GeomFromText($13, 25832)), ST_Force2D(ST_GeomFromText($15, 25832))), $14)`,
		nil,
		func(f *navngivenvejFeature, gen int) []any {
			return []any{
				f.IDLokalId, nullIfEmpty(f.Vejnavn), nullIfEmpty(f.Vejadresseringsnavn),
				nullIfEmpty(f.UdtaltVejnavn), nullIfEmpty(f.AdministreresAfKommune), nullIfEmpty(f.Status),
				nullIfEmpty(f.OprKilde), nullIfEmpty(f.OprNoejagtighed), nullIfEmpty(f.OprRegistrering),
				nullIfEmpty(f.OprTekniskStandard), nullIfEmpty(f.RegistreringFra), nullIfEmpty(f.VirkningFra),
				nullIfEmpty(f.Vejnavnelinje), gen, nullIfEmpty(f.Vejnavneområde),
			}
		})
}
