package dawa

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Adresse is the DAWA /adresser/{id} response (the enhedsadresse). Field order
// matches the golden exactly: id, kvhx, status, darstatus, href, historik,
// etage, dør, adressebetegnelse, adgangsadresse (the FULL adgangsadresse object
// embedded verbatim).
type Adresse struct {
	ID                string          `json:"id"`
	Kvhx              string          `json:"kvhx"`
	Status            int             `json:"status"`
	Darstatus         int             `json:"darstatus"`
	Href              string          `json:"href"`
	Historik          AdgHistorik     `json:"historik"`
	Etage             *string         `json:"etage"`
	Doer              *string         `json:"dør"`
	Adressebetegnelse string          `json:"adressebetegnelse"`
	Adgangsadresse    *Adgangsadresse `json:"adgangsadresse"`
}

// adresseExtraCols are the adresse-specific scanned columns, selected BEFORE the
// embedded adgangsadresse columns. kvhx is derived in Go from the embedded
// adgangsadresse's kvh + etage + dør.
const adresseExtraCols = `
	a.id_lokalid AS a_id,
	a.dar_status AS a_dar_status,
	a.etagebetegnelse AS a_etage,
	a.doerbetegnelse AS a_doer,
	to_char(a.oprettet AT TIME ZONE 'Europe/Copenhagen', 'YYYY-MM-DD"T"HH24:MI:SS.MS') AS a_oprettet,
	to_char(a.aendret AT TIME ZONE 'Europe/Copenhagen', 'YYYY-MM-DD"T"HH24:MI:SS.MS') AS a_aendret,
	to_char(a.ikrafttraedelse AT TIME ZONE 'Europe/Copenhagen', 'YYYY-MM-DD"T"HH24:MI:SS.MS') AS a_ikraft, `

// adresseFromPrefix joins dar_adresse to its dar_husnummer (the embedded
// adgangsadresse) before the shared adgangsadresseFrom join graph (which is
// anchored on alias h = dar_husnummer).
const adresseFromPrefix = `
	FROM dar_adresse a
	JOIN dar_husnummer h ON h.id = a.husnummer_id AND h.status = '3'`

// scanAdresse reads the adresse-specific columns followed by the embedded
// adgangsadresse columns, then assembles both.
func scanAdresse(row pgx.Row, baseURL string) (*Adresse, error) {
	var aID string
	var aDarStatus *int
	var aEtage, aDoer, aOprettet, aAendret, aIkraft *string
	var s adgScan
	if err := row.Scan(
		&aID, &aDarStatus, &aEtage, &aDoer, &aOprettet, &aAendret, &aIkraft,
		// embedded adgangsadresse columns (same order as scanAdgangsadresse)
		&s.id, &s.darStatus, &s.kommunekode, &s.vejkode, &s.husnrtekst,
		&s.navngivenv, &s.vejnavn, &s.adrnavn, &s.postnr, &s.postnrnavn,
		&s.supplBynavn, &s.supplB2dagi, &s.kommuneNavn,
		&s.hOprettet, &s.hAendret, &s.hIkraft,
		&s.apID, &s.apLon, &s.apLat, &s.apHoejde, &s.apNoej, &s.apKilde, &s.apTeknisk, &s.apTekstret, &s.apAendret,
		&s.vpID, &s.vpKilde, &s.vpNoej, &s.vpTeknisk, &s.vpLon, &s.vpLat, &s.vpAendret,
		&s.ddknM100, &s.ddknKm1, &s.ddknKm10,
		&s.sognKode, &s.sognNavn, &s.regionKode, &s.regionNavn,
		&s.ldNuts3, &s.ldNavn, &s.rkKode, &s.rkNavn, &s.pkKode, &s.pkNavn,
		&s.afNummer, &s.afNavn, &s.afKomm, &s.opNummer, &s.opNavn, &s.skNummer, &s.skNavn, &s.vlBogstav, &s.vlNavn,
		&s.jEjerlavK, &s.jEjerlavN, &s.jMatrikel,
		&s.bebyggelserJSON, &s.brofast,
	); err != nil {
		return nil, err
	}

	adg := buildAdgangsadresse(&s, baseURL)

	a := &Adresse{
		ID:             aID,
		Href:           fmt.Sprintf("%s/adresser/%s", baseURL, aID),
		Etage:          aEtage,
		Doer:           aDoer,
		Adgangsadresse: adg,
		Darstatus:      derefInt(aDarStatus),
	}
	a.Status = darStatusToDawa(a.Darstatus)

	// kvhx = kvh + padUnderscore(etage, 3) + padUnderscore(dør, 4).
	a.Kvhx = adg.Kvh + padUnderscore(deref(aEtage), 3) + padUnderscore(deref(aDoer), 4)

	// adressebetegnelse (full: includes ", {etage}. {dør}")
	a.Adressebetegnelse = adressebetegnelse(
		deref(s.vejnavn), adg.Husnr, deref(aEtage), deref(aDoer),
		deref(s.supplBynavn), deref(s.postnr), deref(s.postnrnavn), false)

	a.Historik = AdgHistorik{
		Oprettet:        aOprettet,
		Aendret:         aAendret,
		Ikrafttraedelse: aIkraft,
		Nedlagt:         nil,
	}
	return a, nil
}

// GetAdresse returns the adresse with the given id (status-3 only), or
// pgx.ErrNoRows.
func GetAdresse(ctx context.Context, pool *pgxpool.Pool, id, baseURL string) (*Adresse, error) {
	sql := "SELECT " + adresseExtraCols + adgangsadresseCols + adresseFromPrefix + adgangsadresseJoins +
		" WHERE a.id_lokalid = $1 AND a.status = '3'"
	return scanAdresse(pool.QueryRow(ctx, sql, id), baseURL)
}

// ListAdresser returns status-3 adresser ordered by id (DAWA's default order).
// limit <= 0 returns all.
func ListAdresser(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit, offset int) ([]*Adresse, error) {
	return ListAdresserFiltered(ctx, pool, baseURL, limit, offset, ListFilter{})
}

// ListAdresserFiltered is ListAdresser with SQL-side per-field filters (postnr,
// vejkode, kommunekode via the shared adgangsadresse clauses, plus etage and dør
// on the enhedsadresse), q= over the address name fields, srid output
// reprojection and an optional spatial filter (against the adgangspunkt), plus
// offset paging. A zero filter reproduces ListAdresser byte-for-byte.
func ListAdresserFiltered(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit, offset int, f ListFilter) ([]*Adresse, error) {
	var wb whereBuilder
	adgFilterClauses(&wb, f)
	if v, ok := f.Filters["etage"]; ok {
		wb.addEq("a.etagebetegnelse", v)
	}
	if v, ok := f.Filters["dør"]; ok {
		wb.addEq("a.doerbetegnelse", v)
	}

	sql := "SELECT " + f.applySRID(adresseExtraCols+adgangsadresseCols+adresseFromPrefix+adgangsadresseJoins) +
		" WHERE a.status = '3'"
	if w := wb.sql(); w != "" {
		sql += " AND " + w
	}
	sql += " ORDER BY a.id_lokalid"
	sql = appendLimitOffset(sql, limit, offset)

	rows, err := pool.Query(ctx, sql, wb.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Adresse
	for rows.Next() {
		a, err := scanAdresse(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
