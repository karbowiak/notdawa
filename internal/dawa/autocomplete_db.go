package dawa

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// autocomplete_db.go holds the DB-backed pieces of the autocomplete layer that
// cannot be expressed by filtering an existing List* in Go: the address
// resources (which need a SQL prefix filter + adressebetegnelse ordering to keep
// the result set small) and the dynamic-key SimpleAreaAuto marshaller.

// MarshalJSON renders a SimpleAreaAuto as {"tekst": ..., "<key>": <area>} with
// the dynamic wrapper key (e.g. "retskreds"). It uses MarshalDAWA for the area
// so the nested object is byte-identical to its single-GET representation, then
// reuses the same 2-space indentation by re-indenting the assembled object.
func (a SimpleAreaAuto) MarshalJSON() ([]byte, error) {
	areaBytes, err := json.Marshal(a.Area)
	if err != nil {
		return nil, err
	}
	tekstBytes, err := json.Marshal(a.Tekst)
	if err != nil {
		return nil, err
	}
	keyBytes, err := json.Marshal(a.Key)
	if err != nil {
		return nil, err
	}
	out := []byte(`{"tekst":`)
	out = append(out, tekstBytes...)
	out = append(out, ',')
	out = append(out, keyBytes...)
	out = append(out, ':')
	out = append(out, areaBytes...)
	out = append(out, '}')
	return out, nil
}

// autoTsquery turns an autocomplete q into a Postgres tsquery string the way DAWA
// does (toPgSuggestQuery): strip every non-alphanumeric char to a space (so "st."
// and "th." become the tokens "st"/"th", "27" stays), lowercase, AND all tokens
// together, and give the LAST token a ':*' prefix match (the word being typed).
// Example: "Hellevangen 27 st. th." -> "hellevangen & 27 & st & th:*".
// Returns "" when q has no usable tokens (caller then falls back to no filter).
func autoTsquery(q string) string {
	var toks []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			toks = append(toks, string(cur))
			cur = cur[:0]
		}
	}
	for _, r := range q {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			cur = append(cur, r)
		case r >= 'A' && r <= 'Z':
			cur = append(cur, r+('a'-'A'))
		case r == 'æ' || r == 'ø' || r == 'å' || r == 'Æ' || r == 'Ø' || r == 'Å' || r == 'ä' || r == 'ö' || r == 'ü' || r == 'é':
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	if len(toks) == 0 {
		return ""
	}
	var b strings.Builder
	for i, t := range toks {
		if i > 0 {
			b.WriteString(" & ")
		}
		b.WriteString(t)
		if i == len(toks)-1 {
			b.WriteString(":*")
		}
	}
	return b.String()
}

// adgangsadresseSearchText is the per-row search text mirroring DAWA's
// adgangsadresse tsvector content: vejnavn + husnr + supplerendebynavn + postnr +
// postnrnavn. NOTE: it deliberately has NO etage/dør (matching DAWA), so a query
// carrying an etage/dør token (e.g. "st"/"th") matches NO adgangsadresse — which
// is what pushes the aggregate escalation past adgangsadresse to adresse.
const adgangsadresseSearchText = `coalesce(nv.navn,'') || ' ' || coalesce(h.husnummertekst,'') || ' ' || coalesce(sb.navn,'') || ' ' || coalesce(p.postnr,'') || ' ' || coalesce(p.navn,'')`

// adresseSearchText is adgangsadresseSearchText PLUS etage + dør (DAWA's adresse
// tsvector), so a full "vejnavn husnr etage dør" query narrows to the single row.
const adresseSearchText = `coalesce(nv.navn,'') || ' ' || coalesce(h.husnummertekst,'') || ' ' || coalesce(a.etagebetegnelse,'') || ' ' || coalesce(a.doerbetegnelse,'') || ' ' || coalesce(sb.navn,'') || ' ' || coalesce(p.postnr,'') || ' ' || coalesce(p.navn,'')`

// tsMatch builds the WHERE predicate "<search text> matches the tsquery", using the
// 'simple' config (no stemming/stopwords — DAWA's 'adresser' config is copy=simple)
// over unaccented text so æ/ø/å and case fold consistently. paramIdx is the $N for
// the tsquery string produced by autoTsquery.
func tsMatch(searchText string, paramIdx int) string {
	return fmt.Sprintf("to_tsvector('simple', unaccent(%s)) @@ to_tsquery('simple', unaccent($%d))", searchText, paramIdx)
}

// listAdgangsadresserMatching returns status-3 adgangsadresser matching q as an
// AND of all its tokens (DAWA tsquery semantics) over the adgangsadresse search
// text (vejnavn+husnr+suppl+postnr — NO etage/dør), ordered by adressebetegnelse
// (vejnavn, then husnr numerically) — DAWA's autocomplete order. perSide<=0
// returns all matches; offset skips the first results.
func listAdgangsadresserMatching(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*Adgangsadresse, error) {
	tsq := autoTsquery(q)
	where := "h.status = '3'"
	args := []any{}
	if tsq != "" {
		// Road-name prefix pre-filter FIRST: it cuts dar_navngivenvej (small) to the
		// matching road(s) via the f_unaccent trigram index, so the expensive per-row
		// tsvector AND-match only runs on those rows' husnumre — not the whole 2.6M
		// table. The road name is always the leading token of q, so this never drops
		// a real match. The tsvector AND then narrows by husnr/postnr (DAWA semantics).
		args = append(args, tsq)
		where += " AND " + tsMatch(adgangsadresseSearchText, 1)
		if road := roadNamePart(q); road != "" {
			args = append(args, road+"%")
			where += fmt.Sprintf(" AND f_unaccent(nv.navn) ILIKE f_unaccent($%d)", len(args))
		}
	}
	sql := "SELECT " + adgangsadresseCols + adgangsadresseFrom +
		" WHERE " + where +
		" ORDER BY nv.navn, NULLIF(regexp_replace(h.husnummertekst, '\\D', '', 'g'), '')::int NULLS LAST, h.husnummertekst, h.id"
	sql += pageClause(perSide, offset)
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Adgangsadresse
	for rows.Next() {
		a, err := scanAdgangsadresse(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// listAdresserMatching mirrors listAdgangsadresserMatching for enhedsadresser, but
// its search text INCLUDES etage + dør (DAWA's adresse tsvector), so a query like
// "Hellevangen 27 st. th." narrows to the single matching enhedsadresse. Ordered
// by adressebetegnelse then etage/dør.
func listAdresserMatching(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*Adresse, error) {
	tsq := autoTsquery(q)
	where := "a.status = '3'"
	args := []any{}
	if tsq != "" {
		// Road-name prefix pre-filter first (f_unaccent trigram index) to bound the
		// tsvector AND-match to the matching road's addresses — see the adgangs-
		// adresse version above. Then the tsvector narrows by husnr/etage/dør/postnr.
		args = append(args, tsq)
		where += " AND " + tsMatch(adresseSearchText, 1)
		if road := roadNamePart(q); road != "" {
			args = append(args, road+"%")
			where += fmt.Sprintf(" AND f_unaccent(nv.navn) ILIKE f_unaccent($%d)", len(args))
		}
	}
	sql := "SELECT " + adresseExtraCols + adgangsadresseCols + adresseFromPrefix + adgangsadresseJoins +
		" WHERE " + where +
		" ORDER BY nv.navn, NULLIF(regexp_replace(h.husnummertekst, '\\D', '', 'g'), '')::int NULLS LAST, h.husnummertekst, a.etagebetegnelse NULLS FIRST, a.doerbetegnelse NULLS FIRST, a.id_lokalid"
	sql += pageClause(perSide, offset)
	rows, err := pool.Query(ctx, sql, args...)
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

// The remaining helpers SQL-filter the LARGE resources (millions of rows) so the
// whole table is never materialised in Go for an autocomplete query. The WHERE
// clause matches the resource's name column with unaccent+ILIKE (case- and
// diacritic-insensitive, DAWA-style); the per-resource ORDER BY mirrors that
// resource's default List order so the served order matches the goldens. Each
// returns the same *T as the resource's List*, so the wrapper assembly upstream
// is unchanged and the nested object stays byte-identical to its single-GET form.

// listVejstykkerMatching returns status-3 vejstykker whose navn matches q.
func listVejstykkerMatching(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*Vejstykke, error) {
	sql := "SELECT " + vejstykkeSelect + vejstykkeFrom +
		" WHERE kd.status = '3' AND unaccent(nv.navn) ILIKE unaccent($1)" +
		" ORDER BY kd.kommune, kd.vejkode" + pageClause(perSide, offset)
	rows, err := pool.Query(ctx, sql, "%"+q+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Vejstykke
	for rows.Next() {
		v, err := scanVejstykke(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// listVejnavneMatching returns DISTINCT status-3 vejnavne whose navn matches q.
func listVejnavneMatching(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*Vejnavn, error) {
	sql := vejnavnAggSelect + `(
		SELECT DISTINCT nv.navn
		FROM dar_navngivenvej nv
		WHERE nv.status = '3' AND unaccent(nv.navn) ILIKE unaccent($1)
	) src ORDER BY src.` + vejnavnOrder + pageClause(perSide, offset)
	rows, err := pool.Query(ctx, sql, "%"+q+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Vejnavn
	for rows.Next() {
		v, err := scanVejnavn(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// navngivenvejAuto carries a NavngivenVej plus its visueltcenter point
// coordinates (reprojected to WGS84), which the /navngivneveje/autocomplete
// reduced element emits as visueltcenter_x / visueltcenter_y but the regular
// NavngivenVej representation does not carry.
type navngivenvejAuto struct {
	nv *NavngivenVej
	vx *float64
	vy *float64
}

// listNavngivnevejeMatchingAuto returns status-3 navngivneveje whose navn
// token-prefix-matches q, together with each road's visueltcenter point coords
// (vejnavnebeliggenhed_vejnavnepunkt reprojected to 4326 — verified byte-identical
// to DAWA's visueltcenter_x/_y). The filter is a word-boundary prefix (DAWA's
// token-prefix autocomplete selection: q matches at the start of any word in the
// name, e.g. q=Vester matches "Vester Alle").
func listNavngivnevejeMatchingAuto(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*navngivenvejAuto, error) {
	// The two trailing columns are the road's visueltcenter point (g.vc, the point
	// on the line nearest its centroid, reprojected to 4326) — byte-identical to
	// the navngivenvej's own visueltcenter and to DAWA's visueltcenter_x/_y. The
	// WHERE is a word-boundary prefix (\m + q): DAWA's token-prefix autocomplete
	// selection (q matches at the start of any word in the road name).
	sql := "SELECT " + navngivnevejSelect + `,
		CASE WHEN nv.geom IS NULL THEN NULL ELSE round(ST_X(g.vc)::numeric, 8)::float8 END AS vc_x,
		CASE WHEN nv.geom IS NULL THEN NULL ELSE round(ST_Y(g.vc)::numeric, 8)::float8 END AS vc_y` +
		navngivnevejFrom +
		" WHERE nv.status = '3' AND unaccent(nv.navn) ~* ('\\m' || unaccent($1))" +
		" ORDER BY nv.id" + pageClause(perSide, offset)
	rows, err := pool.Query(ctx, sql, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*navngivenvejAuto
	for rows.Next() {
		nv, vx, vy, err := scanNavngivenVejAuto(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, &navngivenvejAuto{nv: nv, vx: vx, vy: vy})
	}
	return out, rows.Err()
}

// listNavngivnevejeMatching returns status-3 navngivneveje whose navn matches q.
func listNavngivnevejeMatching(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*NavngivenVej, error) {
	sql := "SELECT " + navngivnevejSelect + navngivnevejFrom +
		" WHERE nv.status = '3' AND unaccent(nv.navn) ILIKE unaccent($1)" +
		" ORDER BY nv.id" + pageClause(perSide, offset)
	rows, err := pool.Query(ctx, sql, "%"+q+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*NavngivenVej
	for rows.Next() {
		nv, err := scanNavngivenVej(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, nv)
	}
	return out, rows.Err()
}

// listSupplerendebynavneMatching returns bynavne whose navn matches q.
func listSupplerendebynavneMatching(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*SupplerendeBynavn, error) {
	sql := "SELECT " + supplerendeBynavnSelect + " FROM (" + supplerendeBynavnGeom +
		" WHERE unaccent(sb.navn) ILIKE unaccent($1)) q ORDER BY dagi_id" + pageClause(perSide, offset)
	rows, err := pool.Query(ctx, sql, "%"+q+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SupplerendeBynavn
	for rows.Next() {
		s, err := scanSupplerendeBynavn(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// listJordstykkerMatching returns jordstykker whose tekst ("{matrikelnr}
// {ejerlav.navn}") matches q. The match is on the composed text, so it is
// applied as a HAVING-style predicate over the same FROM the entity uses.
func listJordstykkerMatching(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*Jordstykke, error) {
	sql := "SELECT " + jordstykkeCols + " FROM (" + jordstykkeFrom + ") q" +
		" WHERE unaccent(matrikelnr || ' ' || COALESCE(ejerlav_navn, '')) ILIKE unaccent($1)" +
		" ORDER BY ejerlav_kode, matrikelnr" + pageClause(perSide, offset)
	rows, err := pool.Query(ctx, sql, "%"+q+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Jordstykke
	for rows.Next() {
		j, err := scanJordstykke(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// listStednavne2Matching returns stednavne2 rows whose name matches q. The whole
// ds_stednavne table is large, so the q filter + LIMIT are applied in SQL (an
// unbounded List + Go filter times out).
func listStednavne2Matching(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*Stednavn2, error) {
	sql := "SELECT " + stednavne2Select + stednavne2From +
		" WHERE unaccent(sn.skrivemaade) ILIKE unaccent($1)" +
		" ORDER BY s.id_lokalId, sn.navnefoelgenummer NULLS LAST, sn.skrivemaade" +
		pageClause(perSide, offset)
	rows, err := pool.Query(ctx, sql, "%"+q+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Stednavn2
	for rows.Next() {
		sn, err := scanStednavn2(rows, baseURL)
		if err != nil {
			return nil, err
		}
		out = append(out, sn)
	}
	return out, rows.Err()
}

// pageClause builds a LIMIT/OFFSET suffix. perSide<=0 means no LIMIT.
func pageClause(perSide, offset int) string {
	clause := ""
	if perSide > 0 {
		clause += fmt.Sprintf(" LIMIT %d", perSide)
	}
	if offset > 0 {
		clause += fmt.Sprintf(" OFFSET %d", offset)
	}
	return clause
}
