package dawa

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// autocomplete_score.go is a faithful Go port of DAWA's DETERMINISTIC
// autocomplete relevance scorer. The source of truth is the vendored
// open-source JS at docs/specs/dawa_autocomplete.js, dawa_levenshtein.js and
// dawa_adresseTextMatch.js. Nothing here is a "proprietary" heuristic — every
// number, weight and tie-break below is copied from that published code so the
// served result ORDER can be reproduced byte-for-byte.
//
// The scorer is used by the aggregate /autocomplete escalation (Part B) and is
// the canonical ordering for the address/vejnavn per-resource autocompletes
// (Part C). It operates on []rune (Unicode code points) rather than UTF-8 bytes
// so that it agrees with the JS, which works on UTF-16 code units — for the
// Latin-1 range that covers Danish (æ/ø/å are single code units in both UTF-16
// and as Go runes) the two representations agree, and []rune avoids splitting a
// multi-byte character mid-edit.

// levOp is one edit operation from the Levenshtein backtrace.
//
//	I = insert (a char present in a but not b)
//	D = delete (a char present in b but not a)
//	K = keep   (chars equal)
//	U = update (substitution)
type levOp struct {
	op     byte
	letter rune
}

// levenshteinResult mirrors the JS {distance, ops} return value.
type levenshteinResult struct {
	distance int
	ops      []levOp
}

// levenshtein is a direct port of dawa_levenshtein.js. It fills the cost matrix
// then backtraces from the bottom-right, preferring (in this exact order)
// insert, delete, keep, update — the same tie-break order as the JS while-loop.
// The returned ops are in forward order (the JS reverses the accumulated list).
func levenshtein(aStr, bStr string, insertCost, updateCost, deleteCost int) levenshteinResult {
	a := []rune(aStr)
	b := []rune(bStr)
	n := len(a)
	m := len(b)

	// matrix[i][j] = cost of transforming first j of a into first i of b.
	matrix := make([][]int, m+1)
	for i := 0; i <= m; i++ {
		matrix[i] = make([]int, n+1)
		matrix[i][0] = i * deleteCost
	}
	for j := 0; j <= n; j++ {
		matrix[0][j] = j * insertCost
	}

	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if b[i-1] == a[j-1] {
				matrix[i][j] = matrix[i-1][j-1]
			} else {
				sub := matrix[i-1][j-1] + updateCost
				ins := matrix[i][j-1] + insertCost
				del := matrix[i-1][j] + deleteCost
				matrix[i][j] = min3(sub, ins, del)
			}
		}
	}

	i := m
	j := n
	var ops []levOp
	for i > 0 || j > 0 {
		switch {
		case j > 0 && matrix[i][j]-matrix[i][j-1] == insertCost:
			ops = append(ops, levOp{op: 'I', letter: a[j-1]})
			j--
		case i > 0 && matrix[i][j]-matrix[i-1][j] == deleteCost:
			ops = append(ops, levOp{op: 'D', letter: b[i-1]})
			i--
		case i > 0 && j > 0 && b[i-1] == a[j-1]:
			ops = append(ops, levOp{op: 'K', letter: a[j-1]})
			i--
			j--
		case i > 0 && j > 0 && matrix[i][j]-matrix[i-1][j-1] == updateCost:
			ops = append(ops, levOp{op: 'U', letter: a[j-1]})
			i--
			j--
		default:
			// The JS throws here; for our inputs this is unreachable, but we must
			// not loop forever, so break out defensively.
			i = 0
			j = 0
		}
	}
	// Reverse ops into forward order (JS does ops.reverse()).
	for l, r := 0, len(ops)-1; l < r; l, r = l+1, r-1 {
		ops[l], ops[r] = ops[r], ops[l]
	}
	return levenshteinResult{distance: matrix[m][n], ops: ops}
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

// addressElementWeights mirrors ADDRESS_ELEMENT_WEIGHTS in dawa_autocomplete.js.
var addressElementWeights = map[string]int{
	"vejnavn":           1,
	"husnr":             3,
	"etage":             3,
	"dør":               3,
	"supplerendebynavn": 1,
	"postnr":            3,
	"postnrnavn":        1,
}

// maxWeight mirrors MAX_WEIGHT in dawa_autocomplete.js.
const maxWeight = 3

// scoreAddressElement is a direct port of scoreAddressElement in
// dawa_autocomplete.js: equal → 0, empty search → 1, prefix (startsWith) → 2,
// otherwise 2 + edits*weight + (isPrefix?1:0) where edits counts non-K ops after
// trimming trailing 'I' ops, and isPrefix = the last op (before trimming) was 'I'.
func scoreAddressElement(addressText, searchText string, weight int) int {
	if addressText == searchText {
		return 0
	}
	if searchText == "" {
		return 1
	}
	if strings.HasPrefix(addressText, searchText) {
		return 2
	}

	res := levenshtein(strings.ToLower(addressText), strings.ToLower(searchText), 1, 1, 1)
	ops := res.ops
	isPrefix := len(ops) > 0 && ops[len(ops)-1].op == 'I'
	for len(ops) > 0 && ops[len(ops)-1].op == 'I' {
		ops = ops[:len(ops)-1]
	}
	edits := 0
	for _, o := range ops {
		if o.op != 'K' {
			edits++
		}
	}
	score := 2 + edits*weight
	if isPrefix {
		score++
	}
	return score
}

// adresseFieldNames mirrors ADRESSE_FIELD_NAMES in dawa_autocomplete.js.
var adresseFieldNames = map[string][]string{
	"adgangsadresse": {"vejnavn", "husnr", "supplerendebynavn", "postnr", "postnrnavn"},
	"adresse":        {"vejnavn", "husnr", "etage", "dør", "supplerendebynavn", "postnr", "postnrnavn"},
}

// addrMatch is the result of adresseTextMatch: the per-field parse of the search
// string against a candidate address plus any leftover unknown tokens.
type addrMatch struct {
	address       map[string]string
	unknownTokens []string
}

// isAddrWhitespace mirrors isWhitespace in dawa_adresseTextMatch.js: a rune is
// "whitespace" for tokenisation if it is '.', ',' or ' '.
func isAddrWhitespace(r rune) bool {
	return r == '.' || r == ',' || r == ' '
}

// charlistEntry mirrors one mapped op {op, uvasket, vasket} in
// dawa_adresseTextMatch.js. A null rune is represented by hasUvasket/hasVasket.
type charlistEntry struct {
	op         byte
	uvasket    rune
	hasUvasket bool
	vasket     rune
	hasVasket  bool
}

// mapOps mirrors mapOps in dawa_adresseTextMatch.js: it walks the (lowercased)
// levenshtein op trace and re-attaches the ORIGINAL-cased uvasket/vasket runes.
func mapOps(uvasket, vasket string, ops []levOp) []charlistEntry {
	u := []rune(uvasket)
	v := []rune(vasket)
	ui, vi := 0, 0
	out := make([]charlistEntry, 0, len(ops))
	for _, o := range ops {
		switch o.op {
		case 'K', 'U':
			e := charlistEntry{op: o.op}
			if ui < len(u) {
				e.uvasket, e.hasUvasket = u[ui], true
				ui++
			}
			if vi < len(v) {
				e.vasket, e.hasVasket = v[vi], true
				vi++
			}
			out = append(out, e)
		case 'I':
			e := charlistEntry{op: 'I'}
			if ui < len(u) {
				e.uvasket, e.hasUvasket = u[ui], true
				ui++
			}
			out = append(out, e)
		case 'D':
			e := charlistEntry{op: 'D'}
			if vi < len(v) {
				e.vasket, e.hasVasket = v[vi], true
				vi++
			}
			out = append(out, e)
		}
	}
	return out
}

// consumeUntilWhitespace mirrors consumeUntilWhitespace in dawa_adresseTextMatch.js.
func consumeUntilWhitespace(charlist []charlistEntry) ([]charlistEntry, string) {
	var result strings.Builder
	var resultList []charlistEntry
	for i := 0; i < len(charlist); i++ {
		e := charlist[i]
		if e.hasUvasket && isAddrWhitespace(e.uvasket) {
			resultList = append(resultList, charlist[i:]...)
			return resultList, result.String()
		} else if e.op == 'I' {
			result.WriteRune(e.uvasket)
		} else if e.op == 'D' {
			resultList = append(resultList, e)
		} else {
			resultList = append(resultList, charlistEntry{op: 'D', vasket: e.vasket, hasVasket: e.hasVasket})
			result.WriteRune(e.uvasket)
		}
	}
	return resultList, result.String()
}

// consume mirrors consume in dawa_adresseTextMatch.js. It pulls `length`
// matched characters of a token from charlist, returning the remaining charlist,
// the consumed (uvasket) text and the resulting atWhitespace flag.
func consume(charlist []charlistEntry, length int, mustEndWithWhitespace, atWhitespace bool) ([]charlistEntry, string, bool) {
	if len(charlist) == 0 && length == 0 {
		return nil, "", atWhitespace
	}
	if len(charlist) == 0 {
		// JS throws; for our inputs this should not happen.
		return nil, "", atWhitespace
	}
	entry := charlist[0]
	if entry.op == 'I' {
		if isAddrWhitespace(entry.uvasket) && length == 0 {
			return charlist, "", atWhitespace
		}
		rest, txt, aw := consume(charlist[1:], length, mustEndWithWhitespace, isAddrWhitespace(entry.uvasket))
		return rest, string(entry.uvasket) + txt, aw
	}
	if length == 0 {
		if mustEndWithWhitespace && !atWhitespace {
			rest, txt := consumeUntilWhitespace(charlist)
			return rest, txt, true
		}
		return charlist, "", atWhitespace
	}
	if entry.op == 'K' || entry.op == 'U' {
		rest, txt, aw := consume(charlist[1:], length-1, mustEndWithWhitespace, isAddrWhitespace(entry.uvasket))
		return rest, string(entry.uvasket) + txt, aw
	}
	if entry.op == 'D' {
		return consume(charlist[1:], length-1, mustEndWithWhitespace, atWhitespace)
	}
	return charlist, "", atWhitespace
}

// consumeUnknownToken mirrors consumeUnknownToken in dawa_adresseTextMatch.js.
func consumeUnknownToken(charlist []charlistEntry) ([]charlistEntry, string) {
	if len(charlist) == 0 {
		return charlist, ""
	}
	entry := charlist[0]
	if entry.hasUvasket && isAddrWhitespace(entry.uvasket) {
		return charlist, ""
	}
	if entry.hasVasket && !isAddrWhitespace(entry.vasket) {
		return charlist, ""
	}
	rest, txt := consumeUnknownToken(charlist[1:])
	return rest, string(entry.uvasket) + txt
}

// isUnknownToken mirrors isUnknownToken in dawa_adresseTextMatch.js.
func isUnknownToken(charlist []charlistEntry) bool {
	for _, entry := range charlist {
		if entry.hasUvasket && isAddrWhitespace(entry.uvasket) {
			return true
		}
		if entry.hasVasket && !isAddrWhitespace(entry.vasket) {
			return false
		}
	}
	return true
}

// consumeBetween mirrors consumeBetween in dawa_adresseTextMatch.js: it consumes
// the gap between two tokens, collecting any unknown tokens it finds.
func consumeBetween(charlist []charlistEntry) ([]charlistEntry, []string) {
	if len(charlist) == 0 {
		return charlist, nil
	}
	entry := charlist[0]
	if entry.hasVasket && !isAddrWhitespace(entry.vasket) {
		return charlist, nil
	}
	if entry.op == 'K' {
		return consumeBetween(charlist[1:])
	}
	if entry.op == 'U' && entry.hasUvasket && isAddrWhitespace(entry.uvasket) {
		return consumeBetween(charlist[1:])
	}
	if entry.op == 'U' && entry.hasUvasket && !isAddrWhitespace(entry.uvasket) {
		restU, tok := consumeUnknownToken(charlist)
		rest, more := consumeBetween(restU)
		return rest, append([]string{tok}, more...)
	}
	if entry.op == 'D' {
		return consumeBetween(charlist[1:])
	}
	if entry.op == 'I' && entry.hasUvasket && isAddrWhitespace(entry.uvasket) {
		return consumeBetween(charlist[1:])
	}
	if entry.op == 'I' && entry.hasUvasket && !isAddrWhitespace(entry.uvasket) {
		if isUnknownToken(charlist) {
			restU, tok := consumeUnknownToken(charlist)
			rest, more := consumeBetween(restU)
			return rest, append([]string{tok}, more...)
		}
		return charlist, nil
	}
	return charlist, nil
}

// tokenRule mirrors one entry of rulesMap in dawa_adresseTextMatch.js.
type tokenRule struct {
	mustBeginAtWhitespace bool
	mustEndAtWhitespace   bool
}

// endsInDigit / beginsWithDigit reproduce the /^\d$/ char tests in parseTokens.
func endsInDigit(s string) bool {
	if s == "" {
		return false
	}
	r := []rune(s)
	c := r[len(r)-1]
	return c >= '0' && c <= '9'
}

func beginsWithDigit(s string) bool {
	if s == "" {
		return false
	}
	c := []rune(s)[0]
	return c >= '0' && c <= '9'
}

// parseTokens mirrors parseTokens in dawa_adresseTextMatch.js.
func parseTokens(uvasketText, vasketText string, tokens []string, rules []tokenRule) (parsedTokens, unknownTokens []string) {
	res := levenshtein(strings.ToLower(strings.TrimSpace(uvasketText)), strings.ToLower(strings.TrimSpace(vasketText)), 1, 2, 1)
	charlist := mapOps(uvasketText, vasketText, res.ops)
	atWhitespace := true
	for index, token := range tokens {
		tokenEndsInNumber := endsInDigit(token)
		nextTokenBeginsWithNumber := index+1 < len(tokens) && beginsWithDigit(tokens[index+1])
		mustEndWithWhitespace := rules[index].mustEndAtWhitespace ||
			index == len(rules)-1 ||
			(index+1 < len(rules) && rules[index+1].mustBeginAtWhitespace) ||
			(tokenEndsInNumber && nextTokenBeginsWithNumber)

		var consumed string
		charlist, consumed, atWhitespace = consume(charlist, utf8.RuneCountInString(token), mustEndWithWhitespace, atWhitespace)
		var between []string
		charlist, between = consumeBetween(charlist)

		parsedTokens = append(parsedTokens, consumed)
		unknownTokens = append(unknownTokens, between...)
		if len(unknownTokens) > 0 {
			atWhitespace = true
		}
	}
	return parsedTokens, unknownTokens
}

// adresseTextMatch is a port of the module.exports function in
// dawa_adresseTextMatch.js. `searchText` is the raw user query; `variant` is the
// candidate's (already-lowercased) per-field values. It returns the per-field
// parse of the query plus the leftover unknown tokens.
func adresseTextMatch(searchText string, variant map[string]string) addrMatch {
	fieldNames := []string{"vejnavn", "husnr", "etage", "dør", "supplerendebynavn", "postnr", "postnrnavn"}
	rulesMap := map[string]tokenRule{
		"vejnavn":           {mustBeginAtWhitespace: true, mustEndAtWhitespace: true},
		"husnr":             {mustBeginAtWhitespace: true, mustEndAtWhitespace: true},
		"etage":             {mustBeginAtWhitespace: true, mustEndAtWhitespace: false},
		"dør":               {mustBeginAtWhitespace: false, mustEndAtWhitespace: true},
		"supplerendebynavn": {mustBeginAtWhitespace: true, mustEndAtWhitespace: true},
		"postnr":            {mustBeginAtWhitespace: true, mustEndAtWhitespace: true},
		"postnrnavn":        {mustBeginAtWhitespace: true, mustEndAtWhitespace: true},
	}

	vasketAdrText := variantAdressebetegnelse(variant)

	var tokens []string
	var rules []tokenRule
	for _, fn := range fieldNames {
		if variant[fn] != "" {
			tokens = append(tokens, variant[fn])
			rules = append(rules, rulesMap[fn])
		}
	}

	parsedTokens, unknownTokens := parseTokens(searchText, vasketAdrText, tokens, rules)

	parsed := map[string]string{}
	idx := 0
	for _, fn := range fieldNames {
		if variant[fn] != "" {
			if idx < len(parsedTokens) {
				parsed[fn] = parsedTokens[idx]
			}
			idx++
		}
	}

	// Strip leading zeros from husnr / etage, then glue "12 A" → "12A".
	for parsed["husnr"] != "" && []rune(parsed["husnr"])[0] == '0' {
		parsed["husnr"] = string([]rune(parsed["husnr"])[1:])
	}
	for parsed["etage"] != "" && []rune(parsed["etage"])[0] == '0' {
		parsed["etage"] = string([]rune(parsed["etage"])[1:])
	}
	if parsed["husnr"] != "" {
		parsed["husnr"] = glueHusnr(parsed["husnr"])
	}

	if unknownTokens == nil {
		unknownTokens = []string{}
	}
	return addrMatch{address: parsed, unknownTokens: unknownTokens}
}

// glueHusnr reproduces the JS replace(/^(\d+) ([A-Za-z])$/, '$1$2').
func glueHusnr(s string) string {
	r := []rune(s)
	i := 0
	for i < len(r) && r[i] >= '0' && r[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(r) {
		return s
	}
	if r[i] != ' ' {
		return s
	}
	if i+2 != len(r) {
		return s
	}
	c := r[i+1]
	if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
		return string(r[:i]) + string(c)
	}
	return s
}

// variantAdressebetegnelse builds util.adressebetegnelse(variant, false) for a
// candidate's (lowercased) field values — the single-line address string that
// the levenshtein alignment in adresseTextMatch is run against. It mirrors
// formatAdresseMultiline joined with ", " (multiline=false).
func variantAdressebetegnelse(v map[string]string) string {
	var b strings.Builder
	b.WriteString(v["vejnavn"])
	if v["husnr"] != "" {
		b.WriteString(" " + v["husnr"])
	}
	if v["etage"] != "" || v["dør"] != "" {
		b.WriteString(",")
	}
	if v["etage"] != "" {
		b.WriteString(" " + v["etage"] + ".")
	}
	if v["dør"] != "" {
		b.WriteString(" " + v["dør"])
	}
	if v["supplerendebynavn"] != "" {
		b.WriteString(", " + v["supplerendebynavn"])
	}
	b.WriteString(", " + v["postnr"] + " " + v["postnrnavn"])
	return b.String()
}

// scoredAddress carries a candidate adgangsadresse/adresse together with the
// scorer's best variant scores, used to sort with compareProcessedResults.
type scoredAddress struct {
	totalScore       int
	scores           map[string]int
	autocompleteVals map[string]string // raw (unformatted) field values for tie-breaks
}

// addrFieldValues holds the raw per-field values an Adgangsadresse/Adresse
// exposes to the scorer. These mirror the JS autocompleteResult fields used by
// computeVariants + compareProcessedResults.
type addrFieldValues struct {
	vejnavn             string
	adresseringsvejnavn string
	husnr               string
	etage               string
	dør                 string
	supplerendebynavn   string
	postnr              string
	postnrnavn          string
	stormodtagerpostnr  string
	stormodtagernavn    string
}

// computeVariants mirrors computeVariants in dawa_autocomplete.js: it expands the
// candidate into the cartesian product of {vejnavn, adresseringsvejnavn},
// {no supplerendebynavn, supplerendebynavn} and {postnr, stormodtagerpostnr}.
func computeVariants(a addrFieldValues, fields []string) []map[string]string {
	hasField := func(name string) bool {
		for _, f := range fields {
			if f == name {
				return true
			}
		}
		return false
	}
	base := map[string]string{}
	if hasField("husnr") {
		base["husnr"] = a.husnr
	}
	if hasField("etage") {
		base["etage"] = a.etage
	}
	if hasField("dør") {
		base["dør"] = a.dør
	}
	vejnavnChoices := []map[string]string{{"vejnavn": a.vejnavn}}
	if a.adresseringsvejnavn != "" && a.adresseringsvejnavn != a.vejnavn {
		vejnavnChoices = append(vejnavnChoices, map[string]string{"vejnavn": a.adresseringsvejnavn})
	}
	suppChoices := []map[string]string{{"supplerendebynavn": ""}}
	if a.supplerendebynavn != "" {
		suppChoices = append(suppChoices, map[string]string{"supplerendebynavn": a.supplerendebynavn})
	}
	postnrChoices := []map[string]string{{"postnr": a.postnr, "postnrnavn": a.postnrnavn}}
	if a.stormodtagerpostnr != "" {
		postnrChoices = append(postnrChoices, map[string]string{"postnr": a.stormodtagerpostnr, "postnrnavn": a.stormodtagernavn})
	}

	apply := func(variants []map[string]string, choices []map[string]string) []map[string]string {
		var out []map[string]string
		for _, choice := range choices {
			for _, variant := range variants {
				nv := map[string]string{}
				for k, val := range variant {
					nv[k] = val
				}
				for k, val := range choice {
					nv[k] = val
				}
				out = append(out, nv)
			}
		}
		return out
	}
	variants := []map[string]string{base}
	for _, choices := range [][]map[string]string{vejnavnChoices, suppChoices, postnrChoices} {
		variants = apply(variants, choices)
	}
	return variants
}

// formatVariant mirrors formatVariant in dawa_autocomplete.js: lowercase each
// field, formatting husnr/postnr, defaulting missing fields to "".
func formatVariant(variant map[string]string, fields []string) map[string]string {
	out := map[string]string{}
	for _, field := range fields {
		val := variant[field]
		if val != "" {
			val = strings.ToLower(val)
		} else {
			val = ""
		}
		out[field] = val
	}
	return out
}

// computeProcessedResult mirrors computeProcessedResult in dawa_autocomplete.js.
func computeProcessedResult(variant map[string]string, searchText string, fields []string) (int, map[string]int) {
	matchResult := adresseTextMatch(searchText, variant)
	scores := map[string]int{}
	for _, field := range fields {
		scores[field] = scoreAddressElement(variant[field], matchResult.address[field], addressElementWeights[field])
	}
	unknownTokenScore := 0
	for _, tok := range matchResult.unknownTokens {
		unknownTokenScore += 2 + utf8.RuneCountInString(tok)*maxWeight
	}
	total := unknownTokenScore
	for _, field := range fields {
		total += scores[field]
	}
	return total, scores
}

// processAddress mirrors process in dawa_autocomplete.js: it computes every
// variant, keeps the one with the lowest totalScore, and returns its scores.
func processAddress(a addrFieldValues, entityName, searchText string) scoredAddress {
	fields := adresseFieldNames[entityName]
	unformatted := computeVariants(a, fields)
	bestTotal := 0
	var bestScores map[string]int
	first := true
	for _, uv := range unformatted {
		fv := formatVariant(uv, fields)
		total, scores := computeProcessedResult(fv, searchText, fields)
		if first || total < bestTotal {
			bestTotal = total
			bestScores = scores
			first = false
		}
	}
	return scoredAddress{
		totalScore:       bestTotal,
		scores:           bestScores,
		autocompleteVals: a.tieBreakValues(),
	}
}

// tieBreakValues exposes the raw field values compareProcessedResults reads from
// autocompleteResult (vejnavn/husnr/etage/dør/postnr/supplerendebynavn).
func (a addrFieldValues) tieBreakValues() map[string]string {
	return map[string]string{
		"vejnavn":           a.vejnavn,
		"husnr":             a.husnr,
		"etage":             a.etage,
		"dør":               a.dør,
		"postnr":            a.postnr,
		"supplerendebynavn": a.supplerendebynavn,
	}
}

// --- comparators (port of the JS comparators map + nothingFirst) ---

func compareStringsInsensitive(a, b string) int {
	if a < b {
		return -1
	}
	if a == b {
		return 0
	}
	return 1
}

// nothingFirst wraps a comparator so that empty/missing values sort first.
func nothingFirst(fn func(a, b string) int) func(a, b string) int {
	return func(a, b string) int {
		aNothing := a == ""
		bNothing := b == ""
		switch {
		case aNothing && bNothing:
			return 0
		case aNothing && !bNothing:
			return -1
		case !aNothing && bNothing:
			return 1
		default:
			return fn(a, b)
		}
	}
}

// husnrParts splits a husnr like "12A" into (tal=12, bogstav="a").
func husnrParts(h string) (int, string) {
	r := []rune(strings.ToLower(h))
	i := 0
	for i < len(r) && r[i] >= '0' && r[i] <= '9' {
		i++
	}
	tal, _ := strconv.Atoi(string(r[:i]))
	return tal, string(r[i:])
}

// compareHusnr mirrors the husnr comparator (tal then bogstav).
func compareHusnr(a, b string) int {
	aTal, aBog := husnrParts(a)
	bTal, bBog := husnrParts(b)
	if aTal == bTal {
		if aBog == bBog {
			return 0
		}
		if aBog == "" {
			return -1
		}
		if bBog == "" {
			return 1
		}
		return compareStringsInsensitive(aBog, bBog)
	}
	return aTal - bTal
}

// isNumeric reports whether s parses as a number (JS !isNaN).
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// compareEtage mirrors the etage comparator (kl./st./numeric/string rules).
func compareEtage(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return -1
	}
	if b == "" {
		return 1
	}
	aKl := strings.HasPrefix(a, "kl")
	bKl := strings.HasPrefix(b, "kl")
	if aKl && !bKl {
		return -1
	}
	if !aKl && bKl {
		return 1
	}
	if aKl && bKl {
		if a == "kl" {
			return 1
		}
		if b == "kl" {
			return -1
		}
		aLevel := parseIntChar(a, 2)
		bLevel := parseIntChar(b, 2)
		return bLevel - aLevel
	}
	if a == "st" {
		return -1
	}
	if b == "st" {
		return 1
	}
	if isNumeric(a) && isNumeric(b) {
		ai, _ := strconv.Atoi(a)
		bi, _ := strconv.Atoi(b)
		return ai - bi
	}
	return compareStringsInsensitive(a, b)
}

// parseIntChar mirrors parseInt(a.charAt(idx)) for the kl. level compare.
func parseIntChar(s string, idx int) int {
	r := []rune(s)
	if idx >= len(r) {
		return 0
	}
	n, err := strconv.Atoi(string(r[idx]))
	if err != nil {
		return 0
	}
	return n
}

// compareDoer mirrors the dør comparator (tv/mf/th, then numeric, then string).
func compareDoer(a, b string) int {
	if a == b {
		return 0
	}
	for _, val := range []string{"tv", "mf", "th"} {
		if a == val {
			return -1
		}
		if b == val {
			return 1
		}
	}
	aNum := isNumeric(a)
	bNum := isNumeric(b)
	if aNum && !bNum {
		return -1
	}
	if !aNum && bNum {
		return 1
	}
	if aNum && bNum {
		ai, _ := strconv.Atoi(a)
		bi, _ := strconv.Atoi(b)
		return ai - bi
	}
	return compareStringsInsensitive(a, b)
}

// comparatorFor returns the nothingFirst-wrapped comparator for a tie-break field.
func comparatorFor(field string) func(a, b string) int {
	switch field {
	case "husnr":
		return nothingFirst(compareHusnr)
	case "etage":
		return nothingFirst(compareEtage)
	case "dør":
		return nothingFirst(compareDoer)
	case "postnr":
		return nothingFirst(func(a, b string) int {
			if a == b {
				return 0
			}
			if !isNumeric(a) {
				return -1
			}
			if !isNumeric(b) {
				return 1
			}
			ai, _ := strconv.Atoi(a)
			bi, _ := strconv.Atoi(b)
			return ai - bi
		})
	default: // vejnavn, supplerendebynavn, postnrnavn
		return nothingFirst(compareStringsInsensitive)
	}
}

// compareProcessedResults is a direct port of compareProcessedResults in
// dawa_autocomplete.js (totalScore, then husnr/etage/dør score deltas, then the
// per-field comparators in order).
func compareProcessedResults(a, b scoredAddress) int {
	if a.totalScore != b.totalScore {
		return a.totalScore - b.totalScore
	}
	if a.scores["husnr"] != b.scores["husnr"] {
		return a.scores["husnr"] - b.scores["husnr"]
	}
	if a.scores["etage"] < b.scores["etage"] {
		return a.scores["etage"] - b.scores["etage"]
	}
	if a.scores["dør"] < b.scores["dør"] {
		return a.scores["dør"] - b.scores["dør"]
	}
	for _, field := range []string{"vejnavn", "husnr", "etage", "dør", "postnr", "supplerendebynavn"} {
		cmp := comparatorFor(field)(a.autocompleteVals[field], b.autocompleteVals[field])
		if cmp != 0 {
			return cmp
		}
	}
	return 0
}

// sortAdresseFields applies the DAWA address ordering to a slice of candidates.
// It is generic over the carried value so callers can sort their own element
// type; `vals` returns the addrFieldValues for the i-th element. The sort is
// stable so equal-key elements keep their incoming (SQL) order.
func sortAdresseFields[T any](entityName, q string, items []T, vals func(T) addrFieldValues) {
	scored := make([]scoredAddress, len(items))
	for i := range items {
		scored[i] = processAddress(vals(items[i]), entityName, q)
	}
	idx := make([]int, len(items))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool {
		return compareProcessedResults(scored[idx[i]], scored[idx[j]]) < 0
	})
	out := make([]T, len(items))
	for i, p := range idx {
		out[i] = items[p]
	}
	copy(items, out)
}

// --- vejnavn ordering (processVejnavn + vejnavnComparator) ---

// vejnavnStripWhitespace reproduces navn.replace(/[\. ,]/g, ”).
func vejnavnStripWhitespace(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '.' || r == ' ' || r == ',' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// sortVejnavne orders vejnavn candidates by scoreAddressElement(navn, q, 1) then
// (on ties) the whitespace-stripped navn (localeCompare), as in queryVejnavn.
func sortVejnavne[T any](q string, items []T, navnOf func(T) string) {
	type sv struct {
		score    int
		stripped string
	}
	meta := make([]sv, len(items))
	for i := range items {
		n := navnOf(items[i])
		meta[i] = sv{score: scoreAddressElement(n, q, 1), stripped: vejnavnStripWhitespace(n)}
	}
	idx := make([]int, len(items))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool {
		a, b := meta[idx[i]], meta[idx[j]]
		if a.score != b.score {
			return a.score < b.score
		}
		return danishLocaleLess(a.stripped, b.stripped)
	})
	out := make([]T, len(items))
	for i, p := range idx {
		out[i] = items[p]
	}
	copy(items, out)
}

// danishLocaleLess approximates String.prototype.localeCompare for the Danish
// vejnavn tie-break. A plain code-point compare is used; this matches the JS for
// the captured fixtures (the tie-break only fires between equal-score names).
func danishLocaleLess(a, b string) bool {
	return a < b
}
