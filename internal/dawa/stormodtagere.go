package dawa

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// stormodtagere.go wires the DAWA "stormodtager" (large-volume postal recipient,
// a.k.a. firmapostnummer) dimension into the /postnumre resource.
//
// RESOURCE-vs-FIELD decision: /stormodtagere is NOT a standalone DAWA HTTP
// resource. The DAWA source tree (raw.githubusercontent.com/DanmarksAdresser/
// Dawa/master/packages/server/apiSpecification) has NO stormodtagere/ directory —
// only an importer (components/importers/stormodtagere.js), a loader
// (psql/loadStormodtagere*.js), the seed CSV (data/stormodtagere.csv) and the
// table DDL (psql/schema/tables/stormodtagere.sql). The stormodtagere TABLE is
// surfaced exclusively through the POSTNUMMER representation
// (apiSpecification/postnummer/representations.js):
//
//   - json (full):  "stormodtageradresser" — null, or an array of
//                   AdgangsadresseRef {href,id} for the postnummer's
//                   stormodtager addresses. Positioned after "navn".
//   - mini / autocomplete: "stormodtager" — a boolean ("is this postnummer a
//                   stormodtagerpostnummer"). Positioned last.
//
// and (separately) as the "stormodtagerpostnummer" ref on adgangsadresser
// (already modelled in adgangsadresser.go). So this file does NOT invent a
// /stormodtagere endpoint; it provides the data-driven helpers the postnummer
// representation uses.
//
// NOTE on the live data: every row in our stormodtagere table is a FIRMA
// postnummer (nr in the 0800–1999 range, e.g. 1599 Københavns Rådhus) whose nr
// is NOT one of the geographic /postnumre. So for every geographic postnummer
// served by /postnumre, stormodtager is false and stormodtageradresser is null —
// byte-identical to live DAWA (verified: /postnumre/1550 returns
// stormodtageradresser:null, and the postnumre mini/autocomplete stormodtager
// boolean is false). The helpers below are nonetheless data-driven (keyed on the
// stormodtagere table), so they stay correct if a geographic postnr ever becomes
// a stormodtagerpostnummer.

// StormodtagerAdresseRef is the DAWA AdgangsadresseRef {href,id} used as an
// element of a postnummer's stormodtageradresser[] (per the DAWA common schema
// definition AdgangsadresseRef: docOrder [href, id]).
type StormodtagerAdresseRef struct {
	Href string `json:"href"`
	ID   string `json:"id"`
}

// StormodtagerAdresserForPostnr returns the stormodtageradresser[] for a single
// geographic postnummer nr (matched against the stormodtagere firmapostnr), or
// nil when the postnummer is not a stormodtagerpostnummer (the common case). The
// rows are ordered by adgangsadresseid for a stable, reproducible array.
func StormodtagerAdresserForPostnr(ctx context.Context, pool *pgxpool.Pool, nr, baseURL string) ([]StormodtagerAdresseRef, error) {
	const sql = `SELECT adgangsadresseid FROM stormodtagere WHERE nr = $1 ORDER BY adgangsadresseid`
	rows, err := pool.Query(ctx, sql, nr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StormodtagerAdresseRef
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, StormodtagerAdresseRef{
			Href: fmt.Sprintf("%s/adgangsadresser/%s", baseURL, id),
			ID:   id,
		})
	}
	return out, rows.Err()
}

// IsStormodtagerPostnummer reports whether a postnummer nr is a
// stormodtagerpostnummer (the postnumre mini/autocomplete "stormodtager"
// boolean). It is the existence form of StormodtagerAdresserForPostnr.
func IsStormodtagerPostnummer(ctx context.Context, pool *pgxpool.Pool, nr string) (bool, error) {
	const sql = `SELECT EXISTS (SELECT 1 FROM stormodtagere WHERE nr = $1)`
	var exists bool
	if err := pool.QueryRow(ctx, sql, nr).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}
