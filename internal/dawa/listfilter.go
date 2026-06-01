package dawa

// listfilter.go threads DAWA's collection query parameters that require SQL-side
// evaluation into the production-scale List* queries: per-field equality filters,
// free-text q=, srid= reprojection of the output coordinates, and the spatial
// polygon/cirkel/bbox filters. Small code-list resources keep using the in-memory
// pipeline in the api package; this file is for the big DAR/MAT/DS/DAGI tables
// where filtering must happen before LIMIT/OFFSET.
//
// Byte-exactness: the DEFAULT request (no filters, no q, srid 4326, no spatial
// filter) must produce SQL identical to the original List* query. srid threading
// only rewrites the OUTPUT coordinate transforms (envelope/visueltcenter/
// koordinater/adgangspunkt) — never a join predicate, since the stored geometry
// stays EPSG:25832. When srid is 0 or 4326 the SQL is left untouched.

import (
	"fmt"
	"strconv"
	"strings"
)

// defaultSRID is DAWA's default output CRS (WGS84). When ListFilter.SRID is 0 or
// 4326 the queries emit the existing byte-exact 4326 output.
const defaultSRID = 4326

// storedSRID is the SRID every geometry column is stored in (ETRS89 / UTM 32N).
// Spatial filter geometries are transformed to this before the ST_* predicate.
const storedSRID = 25832

// ListFilter carries the SQL-side collection parameters. The zero value is the
// neutral filter (no WHERE additions, default 4326 output), so passing a zero
// ListFilter reproduces the original List* result byte-for-byte.
type ListFilter struct {
	// Filters are per-field equality filters keyed by the DAWA representation
	// field name (e.g. "postnr", "kommunekode"). Values are compared with leading
	// zeros normalised where the underlying column is a zero-padded code.
	Filters map[string]string
	// Q is the free-text query (q=/fuzzy=); empty means no free-text filter.
	Q string
	// SRID is the requested output CRS for coordinate arrays/bbox. 0 means the
	// DAWA default (4326).
	SRID int
	// Spatial holds an optional polygon/cirkel/bbox spatial filter; nil = none.
	Spatial *SpatialFilter
}

// srid returns the effective output SRID (defaultSRID when unset).
func (f ListFilter) srid() int {
	if f.SRID == 0 {
		return defaultSRID
	}
	return f.SRID
}

// applySRID rewrites the output coordinate transforms in a SELECT/FROM SQL
// fragment from 4326 to the requested SRID. It is a no-op for the default SRID so
// the byte-exact path is untouched. The only ", 4326)" literals in the List* SQL
// are the output ST_Transform(...,4326) calls (envelope/visueltcenter/
// koordinater/adgangspunkt/vejpunkt); join predicates operate on the raw 25832
// geometry and contain no ", 4326)" literal, so a literal replacement is safe.
func (f ListFilter) applySRID(sql string) string {
	target := f.srid()
	if target == defaultSRID {
		return sql
	}
	return strings.ReplaceAll(sql, ", 4326)", ", "+strconv.Itoa(target)+")")
}

// SpatialFilter is one of bbox / cirkel / polygon. Exactly one shape is set. The
// filter geometry is built in the supplied CRS (the DAWA convention: the spatial
// filter coordinates are in the requested srid, default WGS84 lon/lat) and
// transformed to the stored 25832 before the ST_* predicate.
type SpatialFilter struct {
	// Kind is "bbox", "cirkel" or "polygon".
	Kind string
	// SRID is the CRS the filter coordinates are expressed in (default 4326).
	SRID int
	// Bbox is minx,miny,maxx,maxy (Kind == "bbox").
	Bbox [4]float64
	// Cirkel is x,y,radiusMetres (Kind == "cirkel").
	Cirkel [3]float64
	// Polygon is a single ring of [x,y] pairs, first==last (Kind=="polygon").
	Polygon [][2]float64
}

// srid returns the filter's coordinate CRS (4326 default).
func (s *SpatialFilter) srid() int {
	if s.SRID == 0 {
		return defaultSRID
	}
	return s.SRID
}

// geomSQL returns a SQL expression (in the stored 25832 CRS) for the filter shape
// using the next available $N placeholders, appending the bound args to args and
// returning the updated args slice. nextArg is the 1-based index of the first
// placeholder to use; the returned int is the next free index.
func (s *SpatialFilter) geomSQL(nextArg int, args []any) (string, int, []any) {
	srid := s.srid()
	switch s.Kind {
	case "bbox":
		// ST_MakeEnvelope(minx,miny,maxx,maxy,srid) transformed to 25832.
		expr := fmt.Sprintf(
			"ST_Transform(ST_MakeEnvelope($%d,$%d,$%d,$%d,%d), %d)",
			nextArg, nextArg+1, nextArg+2, nextArg+3, srid, storedSRID)
		args = append(args, s.Bbox[0], s.Bbox[1], s.Bbox[2], s.Bbox[3])
		return expr, nextArg + 4, args
	case "cirkel":
		// Buffer a point by the radius in metres. The point is transformed to 25832
		// (a metric CRS) first so the buffer radius is in metres.
		expr := fmt.Sprintf(
			"ST_Buffer(ST_Transform(ST_SetSRID(ST_Point($%d,$%d),%d), %d), $%d)",
			nextArg, nextArg+1, srid, storedSRID, nextArg+2)
		args = append(args, s.Cirkel[0], s.Cirkel[1], s.Cirkel[2])
		return expr, nextArg + 3, args
	default: // polygon
		// Build a WKT ring and parse with ST_GeomFromText, then transform to 25832.
		var b strings.Builder
		b.WriteString("POLYGON((")
		for i, p := range s.Polygon {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(strconv.FormatFloat(p[0], 'f', -1, 64))
			b.WriteString(" ")
			b.WriteString(strconv.FormatFloat(p[1], 'f', -1, 64))
		}
		b.WriteString("))")
		expr := fmt.Sprintf("ST_Transform(ST_GeomFromText($%d,%d), %d)", nextArg, srid, storedSRID)
		args = append(args, b.String())
		return expr, nextArg + 1, args
	}
}

// predicate returns a full spatial WHERE predicate ST_Intersects(geomCol,
// filterGeom) for the stored geometry column geomCol, plus the updated arg index/
// list. bbox/polygon/cirkel all use ST_Intersects (a feature whose geometry
// intersects the filter area is included — DAWA's documented behaviour; for
// cirkel ST_Intersects against the buffered circle is equivalent to
// ST_DWithin(geom, point, radius)).
func (s *SpatialFilter) predicate(geomCol string, nextArg int, args []any) (string, int, []any) {
	expr, next, args := s.geomSQL(nextArg, args)
	return fmt.Sprintf("ST_Intersects(%s, %s)", geomCol, expr), next, args
}

// ---- diacritic folding mirror of FoldString (autocomplete.go) for SQL ----

// sqlFoldFrom/sqlFoldTo are the translate() argument pair mirroring the dawa
// package's FoldString (foldRunes) in SQL: each accented Latin letter maps to its
// base letter. The pair MUST match foldRunes EXACTLY so SQL-side column folding
// and Go-side value folding agree — in particular å/ø/æ are deliberately NOT
// folded (foldRunes keeps them). The pair is built at init from foldPairs so the
// two translate() strings are always the same length (translate would otherwise
// DELETE unmatched from-chars). The unaccent extension is not installed, so this
// translate() is the portable column-side equivalent; the value side is folded in
// Go via FoldString.
var sqlFoldFrom, sqlFoldTo string

// foldPairs maps each diacritic Latin letter to its base letter, mirroring
// foldRunes (lowercase only — the SQL side lower()s before translate()).
var foldPairs = map[rune]rune{
	'à': 'a', 'á': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a',
	'è': 'e', 'é': 'e', 'ê': 'e', 'ë': 'e',
	'ì': 'i', 'í': 'i', 'î': 'i', 'ï': 'i',
	'ò': 'o', 'ó': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o',
	'ù': 'u', 'ú': 'u', 'û': 'u', 'ü': 'u',
	'ñ': 'n', 'ç': 'c', 'ý': 'y', 'ÿ': 'y',
}

func init() {
	var from, to strings.Builder
	for f, t := range foldPairs {
		from.WriteRune(f)
		to.WriteRune(t)
	}
	sqlFoldFrom = from.String()
	sqlFoldTo = to.String()
}

// sqlFold wraps an expr so it matches the Go-side FoldString output EXACTLY:
// lower(), translate the single-char accented Latin letters to their base, then
// fold the Danish letters å→aa, ø→o, æ→ae the same way foldString does. The
// Danish folds are 1→{1,2} chars so they cannot go through translate() (which is
// strictly 1→1 and would otherwise DELETE the unmatched from-char); they are
// applied with replace() AFTER lower() so the literal lowercase forms match. A
// NULL column yields NULL (so the LIKE is NULL/false, never a match), which is the
// desired behaviour.
//
// Without the Danish folds the column side kept å/ø/æ while the Go value side
// folded them (e.g. FoldString("Rådhuspladsen")="raadhuspladsen"), so a q with
// å/ø/æ matched nothing — that was the /vejnavnpostnummerrelationer/autocomplete
// (and any addQ filter) bug where q=Rådhuspladsen returned 0 rows.
func sqlFold(expr string) string {
	// replace(replace(replace(lower(expr),'å','aa'),'ø','o'),'æ','ae')
	lowered := fmt.Sprintf(
		"replace(replace(replace(lower(%s), 'å', 'aa'), 'ø', 'o'), 'æ', 'ae')",
		expr,
	)
	return fmt.Sprintf("translate(%s, '%s', '%s')", lowered, sqlFoldFrom, sqlFoldTo)
}

// ---- where builder ----

// whereBuilder accumulates parameterized predicates and their bound args. Each
// add* call binds one or more $N placeholders, where N is derived from the
// current arg count, so the resulting SQL is always self-consistent.
type whereBuilder struct {
	clauses []string
	args    []any
}

// addEq appends "col = $N" binding value verbatim (exact string comparison).
func (b *whereBuilder) addEq(col, value string) {
	n := len(b.args) + 1
	b.clauses = append(b.clauses, fmt.Sprintf("%s = $%d", col, n))
	b.args = append(b.args, value)
}

// addEqInt appends "col = $N" binding value as an integer (for integer columns
// like ejerlav_kode). A non-numeric value yields an impossible "FALSE" clause so
// the result is empty rather than a type error.
func (b *whereBuilder) addEqInt(col, value string) {
	n, err := strconv.Atoi(value)
	if err != nil {
		b.clauses = append(b.clauses, "FALSE")
		return
	}
	idx := len(b.args) + 1
	b.clauses = append(b.clauses, fmt.Sprintf("%s = $%d", col, idx))
	b.args = append(b.args, n)
}

// addCode appends a zero-normalised code comparison: when value is an integer it
// matches either the verbatim string or the numeric value (so postnr=101 matches
// a stored "0101"); otherwise it falls back to exact string equality.
func (b *whereBuilder) addCode(col, value string) {
	if _, err := strconv.Atoi(value); err == nil {
		n := len(b.args) + 1
		// One bound value, referenced by two placeholders of the same index.
		b.clauses = append(b.clauses,
			fmt.Sprintf("(%s = $%d OR (%s ~ '^[0-9]+$' AND %s::int = $%d::int))", col, n, col, col, n))
		b.args = append(b.args, value)
		return
	}
	b.addEq(col, value)
}

// addQ appends a case/diacritic-insensitive contains predicate over the given
// text expressions (DAWA's q=). The bound value is folded+wrapped with % for
// LIKE; the columns are folded with sqlFold so diacritics match. exprs are OR-ed.
func (b *whereBuilder) addQ(value string, exprs ...string) {
	v := strings.TrimSpace(value)
	if v == "" || len(exprs) == 0 {
		return
	}
	n := len(b.args) + 1
	ors := make([]string, len(exprs))
	for i, e := range exprs {
		ors[i] = fmt.Sprintf("%s LIKE $%d", sqlFold(e), n)
	}
	b.clauses = append(b.clauses, "("+strings.Join(ors, " OR ")+")")
	b.args = append(b.args, "%"+FoldString(v)+"%")
}

// addSpatial appends a spatial predicate against geomCol using the filter shape.
func (b *whereBuilder) addSpatial(sf *SpatialFilter, geomCol string) {
	clause, _, args := sf.predicate(geomCol, len(b.args)+1, b.args)
	b.clauses = append(b.clauses, clause)
	b.args = args
}

// sql renders the accumulated clauses joined by AND, or "" when there are none.
func (b *whereBuilder) sql() string {
	if len(b.clauses) == 0 {
		return ""
	}
	return strings.Join(b.clauses, " AND ")
}

// appendLimitOffset appends LIMIT/OFFSET to a query. limit<=0 means no limit (all
// rows); a positive offset is applied only alongside a limit. Values are integers
// so they are safe to inline.
func appendLimitOffset(sql string, limit, offset int) string {
	if limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", limit)
		if offset > 0 {
			sql += fmt.Sprintf(" OFFSET %d", offset)
		}
	}
	return sql
}
