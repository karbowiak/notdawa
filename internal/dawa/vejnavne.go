package dawa

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Vejnavn is the DAWA /vejnavne/{navn} response: a distinct vejnavn string with
// its postnumre[] and kommuner[] aggregated+deduped across every status-3
// navngivenvej that bears that name. Field order is significant.
type Vejnavn struct {
	Href      string       `json:"href"`
	Navn      string       `json:"navn"`
	Postnumre []PostnrRef  `json:"postnumre"`
	Kommuner  []KommuneRef `json:"kommuner"`
}

// kommuneRaw is the json_agg intermediate for kommuner[]; href is added in Go.
type kommuneRaw struct {
	Kode string  `json:"kode"`
	Navn *string `json:"navn"`
}

// vejnavnOrder is the ORDER BY expression for the /vejnavne default list order.
// Postgres's default DB collation reproduces DAWA's order for the golden
// fixtures (see verify); kept as one constant so it can be switched in lockstep
// for both the inner LIMIT and the outer sort.
const vejnavnOrder = "navn"

// vejnavnAggSelect computes, per source navn, the deduped postnumre[] and
// kommuner[] across all status-3 navngivneveje with that navn. The caller
// appends the navn source (a single-row literal for Get, a DISTINCT list for
// List) as `src`.
const vejnavnAggSelect = `
	SELECT
		src.navn,
		(SELECT COALESCE(json_agg(json_build_object('nr', t.postnr, 'navn', t.pnavn) ORDER BY t.postnr), '[]')
		 FROM (
			-- branch1 ∪ branch2 (see navngivneveje.go), aggregated+deduped across
			-- every status-3 navngivenvej bearing this navn. NOT the link table.
			SELECT p.postnr, p.navn AS pnavn
			FROM dar_navngivenvej nv
			JOIN dar_husnummer h ON h.navngivenvej = nv.id
			JOIN dar_postnummer p ON p.id = h.postnummer_id
			WHERE nv.status = '3' AND nv.navn = src.navn
			UNION
			SELECT pn.nr AS postnr, pn.navn AS pnavn
			FROM dar_navngivenvej nv
			JOIN dagi_postnumre pn ON nv.geom IS NOT NULL AND ST_Intersects(nv.geom, pn.geom)
			WHERE nv.status = '3' AND nv.navn = src.navn
			  AND ST_Length(ST_Intersection(nv.geom, pn.geom)) > 7
			  AND (pn.nr ~ '^[0-9]+$' AND NOT (pn.nr::int BETWEEN 1000 AND 1999))
		 ) t),
		(SELECT COALESCE(json_agg(json_build_object('kode', t.kommune, 'navn', t.knavn) ORDER BY t.kommune), '[]')
		 FROM (
			SELECT DISTINCT kd.kommune, k.navn AS knavn
			FROM dar_navngivenvej nv
			JOIN dar_navngivenvej_kommunedel kd ON kd.navngivenvej = nv.id AND kd.status = '3'
			LEFT JOIN dagi_kommuner k ON k.kode = kd.kommune
			WHERE nv.status = '3' AND nv.navn = src.navn
		 ) t)
	FROM `

func scanVejnavn(row pgx.Row, baseURL string) (*Vejnavn, error) {
	var vn Vejnavn
	var navn string
	var psJSON, kmJSON []byte
	if err := row.Scan(&navn, &psJSON, &kmJSON); err != nil {
		return nil, err
	}
	vn.Navn = navn
	vn.Href = fmt.Sprintf("%s/vejnavne/%s", baseURL, url.PathEscape(navn))
	postnumre, err := buildPostnrRefs(psJSON, baseURL)
	if err != nil {
		return nil, err
	}
	vn.Postnumre = postnumre
	kommuner, err := buildKommuneRefs(kmJSON, baseURL)
	if err != nil {
		return nil, err
	}
	vn.Kommuner = kommuner
	return &vn, nil
}

// buildKommuneRefs turns the json_agg array into kommuner[] refs.
func buildKommuneRefs(j []byte, baseURL string) ([]KommuneRef, error) {
	out := []KommuneRef{}
	if len(j) == 0 {
		return out, nil
	}
	var raw []kommuneRaw
	if err := json.Unmarshal(j, &raw); err != nil {
		return nil, fmt.Errorf("decode kommuner agg: %w", err)
	}
	for _, r := range raw {
		navn := ""
		if r.Navn != nil {
			navn = *r.Navn
		}
		out = append(out, KommuneRef{
			Href: fmt.Sprintf("%s/kommuner/%s", baseURL, r.Kode),
			Kode: r.Kode,
			Navn: navn,
		})
	}
	return out, nil
}

// GetVejnavn returns the vejnavn aggregate for an exact navn, or pgx.ErrNoRows
// when no status-3 navngivenvej bears that name.
func GetVejnavn(ctx context.Context, pool *pgxpool.Pool, navn, baseURL string) (*Vejnavn, error) {
	sql := vejnavnAggSelect + `(
		SELECT $1::text AS navn
		WHERE EXISTS (SELECT 1 FROM dar_navngivenvej WHERE status = '3' AND navn = $1)
	) src`
	return scanVejnavn(pool.QueryRow(ctx, sql, navn), baseURL)
}

// ListVejnavne returns distinct vejnavne ordered by navn (DAWA's default order),
// each with its aggregated postnumre[]/kommuner[]. limit <= 0 returns all.
func ListVejnavne(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit int) ([]*Vejnavn, error) {
	return ListVejnavneFiltered(ctx, pool, baseURL, limit, 0, ListFilter{})
}

// ListVejnavneFiltered is ListVejnavne with SQL-side navn= equality and q= over
// the distinct vejnavn string, plus offset paging. The filter is pushed into the
// DISTINCT-navn source so paging/aggregation see the filtered set. A zero filter
// reproduces ListVejnavne byte-for-byte. (Vejnavne carry no coordinate output, so
// srid/spatial have no effect.)
func ListVejnavneFiltered(ctx context.Context, pool *pgxpool.Pool, baseURL string, limit, offset int, f ListFilter) ([]*Vejnavn, error) {
	var wb whereBuilder
	if v, ok := f.Filters["navn"]; ok {
		wb.addEq("navn", v)
	}
	wb.addQ(f.Q, "navn")

	inner := "SELECT DISTINCT navn FROM dar_navngivenvej WHERE status = '3' AND navn IS NOT NULL"
	if w := wb.sql(); w != "" {
		inner += " AND " + w
	}
	inner += " ORDER BY " + vejnavnOrder
	inner = appendLimitOffset(inner, limit, offset)

	sql := vejnavnAggSelect + "(" + inner + ") src ORDER BY src." + vejnavnOrder
	rows, err := pool.Query(ctx, sql, wb.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Vejnavn
	for rows.Next() {
		vn, err := scanVejnavn(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, vn)
	}
	return out, rows.Err()
}
