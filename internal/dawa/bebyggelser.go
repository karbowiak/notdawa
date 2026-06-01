package dawa

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Bebyggelse is the DAWA /bebyggelser/{id} response: a flat projection of the
// ds_steder rows whose hovedtype is "bebyggelse". type is the place's undertype,
// navn its primary name, kode the (nullable) bebyggelseskode. The metadata trio
// (ændret/geo_ændret/geo_version) is DAWA import-batch metadata.
type Bebyggelse struct {
	ID         string  `json:"id"`
	Type       *string `json:"type"`
	Navn       *string `json:"navn"`
	Kode       *int    `json:"kode"`
	Aendret    *string `json:"ændret"`
	GeoAendret *string `json:"geo_ændret"`
	GeoVersion *int    `json:"geo_version"`
	Href       string  `json:"href"`
}

// bebyggelseSelect lists the bebyggelse columns in DAWA field order: the primary
// name is resolved from ds_stednavne (brugsprioritet='primær').
const bebyggelseSelect = `
	s.id_lokalId,
	s.undertype,
	nm.primaer_navn,
	s.bebyggelseskode,
	to_char(s.aendret AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS aendret`

const bebyggelseFrom = `
	FROM ds_steder s
	LEFT JOIN LATERAL (
		SELECT (array_agg(sn.skrivemaade ORDER BY sn.navnefoelgenummer NULLS LAST, sn.skrivemaade)
			FILTER (WHERE sn.brugsprioritet = 'primær'))[1] AS primaer_navn
		FROM ds_stednavne sn
		WHERE sn.place_objectid = s.objectid
	) nm ON true
	WHERE s.hovedtype = 'bebyggelse'`

func scanBebyggelse(row pgx.Row, baseURL string) (*Bebyggelse, error) {
	var b Bebyggelse
	var id string
	if err := row.Scan(&id, &b.Type, &b.Navn, &b.Kode, &b.Aendret); err != nil {
		return nil, err
	}
	b.ID = id
	b.GeoAendret = b.Aendret
	b.Href = fmt.Sprintf("%s/bebyggelser/%s", baseURL, id)
	return &b, nil
}

// GetBebyggelse returns the bebyggelse with the given id, or pgx.ErrNoRows.
func GetBebyggelse(ctx context.Context, pool *pgxpool.Pool, id, baseURL string) (*Bebyggelse, error) {
	sql := "SELECT " + bebyggelseSelect + bebyggelseFrom + " AND s.id_lokalId = $1"
	return scanBebyggelse(pool.QueryRow(ctx, sql, id), baseURL)
}

// ListBebyggelser returns bebyggelser ordered by id_lokalId. limit <= 0 = all.
func ListBebyggelser(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit, offset int) ([]*Bebyggelse, error) {
	return ListBebyggelserFiltered(ctx, pool, baseURL, limit, offset, ListFilter{})
}

// ListBebyggelserFiltered is ListBebyggelser with SQL-side q= over the primary
// name plus offset paging. A zero filter reproduces ListBebyggelser
// byte-for-byte. (Bebyggelser carry no coordinate output, so srid/spatial have no
// effect here.)
func ListBebyggelserFiltered(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit, offset int, f ListFilter) ([]*Bebyggelse, error) {
	var wb whereBuilder
	wb.addQ(f.Q, "nm.primaer_navn")

	sql := "SELECT " + bebyggelseSelect + bebyggelseFrom
	if w := wb.sql(); w != "" {
		sql += " AND " + w
	}
	sql += " ORDER BY s.id_lokalId"
	sql = appendLimitOffset(sql, limit, offset)

	rows, err := pool.Query(ctx, sql, wb.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Bebyggelse
	for rows.Next() {
		b, err := scanBebyggelse(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
