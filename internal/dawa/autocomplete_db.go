package dawa

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
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
// over dawa_fold'd text so case, æ/ø/å AND the aa↔å digraph fold consistently on
// both sides (see dawa_fold / migration 030). paramIdx is the $N for the tsquery
// string produced by autoTsquery.
func tsMatch(searchText string, paramIdx int) string {
	return fmt.Sprintf("to_tsvector('simple', dawa_fold(%s)) @@ to_tsquery('simple', dawa_fold($%d))", searchText, paramIdx)
}

// fuzzyRoadThreshold is the minimum pg_trgm similarity for the typo-tolerant
// road-name fallback to accept a correction. 0.4 comfortably accepts a single
// missing/extra/wrong letter ("hellevangn" → "Hellevangen", sim ≈ 0.7) while
// rejecting unrelated names.
const fuzzyRoadThreshold = 0.4

// fuzzyRoadName returns the trigram-closest status-3 road name to a (possibly
// misspelled) road part, plus its similarity. It is served by the dawa_fold(navn)
// GIN trigram index on dar_navngivenvej (the same index that backs the exact
// prefix filter — gin_trgm_ops answers the `%` similarity operator too), so it
// stays cheap. Returns "" when nothing is similar enough.
func fuzzyRoadName(ctx context.Context, pool *pgxpool.Pool, road string) (string, float64, error) {
	const sql = `SELECT navn, similarity(dawa_fold(navn), dawa_fold($1)) AS sim
		FROM dar_navngivenvej
		WHERE status = '3' AND dawa_fold(navn) % dawa_fold($1)
		ORDER BY sim DESC, navn
		LIMIT 1`
	rows, err := pool.Query(ctx, sql, road)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()
	if rows.Next() {
		var navn string
		var sim float64
		if err := rows.Scan(&navn, &sim); err != nil {
			return "", 0, err
		}
		return navn, sim, rows.Err()
	}
	return "", 0, rows.Err()
}

// fuzzyCorrectRoad rewrites q with its road-name part replaced by the trigram-
// closest real road name, for the typo-tolerant autocomplete fallback. The
// house-number/etage/dør remainder is preserved verbatim, e.g.
// "hellevangn 27 st. t" → "Hellevangen 27 st. t". Returns "" when there is no
// road part, no sufficiently-similar road, or the road is already spelled
// correctly (retrying the exact query would then be pointless).
func fuzzyCorrectRoad(ctx context.Context, pool *pgxpool.Pool, q string) string {
	road := roadNamePart(q)
	if road == "" {
		return ""
	}
	corrected, sim, err := fuzzyRoadName(ctx, pool, road)
	if err != nil || corrected == "" || sim < fuzzyRoadThreshold || strings.EqualFold(corrected, road) {
		return ""
	}
	qn := strings.TrimSpace(splitTrailingHusnr(q))
	remainder := strings.TrimSpace(strings.TrimPrefix(qn, road))
	if remainder == "" {
		return corrected
	}
	return corrected + " " + remainder
}

// listAdgangsadresserMatching returns status-3 adgangsadresser matching q as an
// AND of all its tokens (DAWA tsquery semantics) over the adgangsadresse search
// text (vejnavn+husnr+suppl+postnr — NO etage/dør), ordered by adressebetegnelse
// (vejnavn, then husnr numerically) — DAWA's autocomplete order. perSide<=0
// returns all matches; offset skips the first results. When the exact query finds
// nothing (so DAWA would return [] too), it retries once with the road name
// trigram-corrected — typo tolerance that never changes a query DAWA can answer.
func listAdgangsadresserMatching(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*Adgangsadresse, error) {
	run := func(qq string) ([]*Adgangsadresse, error) {
		tsq := autoTsquery(qq)
		where := "h.status = '3'"
		args := []any{}
		if tsq != "" {
			// Road-name prefix pre-filter FIRST: it cuts dar_navngivenvej (small) to the
			// matching road(s) via the dawa_fold trigram index, so the expensive per-row
			// tsvector AND-match only runs on those rows' husnumre — not the whole 2.6M
			// table. The road name is always the leading token of q, so this never drops
			// a real match. The tsvector AND then narrows by husnr/postnr (DAWA semantics).
			args = append(args, tsq)
			where += " AND " + tsMatch(adgangsadresseSearchText, 1)
			if road := roadNamePart(qq); road != "" {
				args = append(args, road+"%")
				where += fmt.Sprintf(" AND dawa_fold(nv.navn) ILIKE dawa_fold($%d)", len(args))
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
	out, err := run(q)
	if err != nil || len(out) > 0 || q == "" {
		return out, err
	}
	if q2 := fuzzyCorrectRoad(ctx, pool, q); q2 != "" {
		return run(q2)
	}
	return out, nil
}

// listAdresserMatching mirrors listAdgangsadresserMatching for enhedsadresser, but
// its search text INCLUDES etage + dør (DAWA's adresse tsvector), so a query like
// "Hellevangen 27 st. th." narrows to the single matching enhedsadresse. Ordered
// by adressebetegnelse then etage/dør. Same zero-result road-name fuzzy fallback.
func listAdresserMatching(ctx context.Context, pool *pgxpool.Pool, q, baseURL string, perSide, offset int) ([]*Adresse, error) {
	run := func(qq string) ([]*Adresse, error) {
		tsq := autoTsquery(qq)
		where := "a.status = '3'"
		args := []any{}
		if tsq != "" {
			// Road-name prefix pre-filter first (dawa_fold trigram index) to bound the
			// tsvector AND-match to the matching road's addresses — see the adgangs-
			// adresse version above. Then the tsvector narrows by husnr/etage/dør/postnr.
			args = append(args, tsq)
			where += " AND " + tsMatch(adresseSearchText, 1)
			if road := roadNamePart(qq); road != "" {
				args = append(args, road+"%")
				where += fmt.Sprintf(" AND dawa_fold(nv.navn) ILIKE dawa_fold($%d)", len(args))
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
	out, err := run(q)
	if err != nil || len(out) > 0 || q == "" {
		return out, err
	}
	if q2 := fuzzyCorrectRoad(ctx, pool, q); q2 != "" {
		return run(q2)
	}
	return out, nil
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
		" WHERE kd.status = '3' AND dawa_fold(nv.navn) ILIKE dawa_fold($1)" +
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

// listVejnavnNamesMatching returns DISTINCT status-3 vejnavne as navn+href ONLY,
// for the autocomplete callers. It deliberately SKIPS vejnavnAggSelect's per-name
// postnumre[]/kommuner[] aggregates: both vejnavn autocomplete consumers (the
// /vejnavne/autocomplete {navn,href} element and the aggregate /autocomplete
// vejnavn escalation step) use only navn, yet they fetch the full match set with no
// LIMIT (the scorer needs every match before truncating). Computing the PostGIS-
// backed postnumre/kommuner subqueries for every matching road then made a 2-char
// query like "ko" (≈3000 roads) run for minutes; navn alone is an ~90ms seq scan.
func listVejnavnNamesMatching(ctx context.Context, pool *pgxpool.Pool, q, baseURL string) ([]*Vejnavn, error) {
	sql := `SELECT DISTINCT nv.navn
		FROM dar_navngivenvej nv
		WHERE nv.status = '3' AND dawa_fold(nv.navn) ILIKE dawa_fold($1)
		ORDER BY nv.navn`
	rows, err := pool.Query(ctx, sql, "%"+q+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Vejnavn
	for rows.Next() {
		var navn string
		if err := rows.Scan(&navn); err != nil {
			return nil, err
		}
		out = append(out, &Vejnavn{
			Navn: navn,
			Href: fmt.Sprintf("%s/vejnavne/%s", baseURL, url.PathEscape(navn)),
		})
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
		" WHERE nv.status = '3' AND dawa_fold(nv.navn) ~* ('\\m' || dawa_fold($1))" +
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
		" WHERE nv.status = '3' AND dawa_fold(nv.navn) ILIKE dawa_fold($1)" +
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
