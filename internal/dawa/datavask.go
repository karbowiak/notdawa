package dawa

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Datavask (address washing) for /datavask/adgangsadresser and
// /datavask/adresser.
//
// Only the kategori-A (exact-match) case is reproduced byte-for-byte against
// DAWA, which is what the goldens capture. The betegnelse for the A case is a
// clean, fully-qualified address, e.g.
//
//	"Rådhuspladsen 1 1550 København V"
//
// which parses into vejnavn + husnr + postnr + postnrnavn. A unique exact match
// yields kategori "A" with the matched address in the reduced "mini"
// representation, a copy of it as "aktueladresse", and a vaskeresultat with
// afstand 0 / all forskelle 0 (a perfect, no-correction wash).
//
// For non-exact / ambiguous / corrected inputs DAWA runs a proprietary fuzzy
// scorer (kategori B/C) whose afstand/forskelle scores cannot be reproduced
// byte-exactly; those cases are handled best-effort (parse + nearest exact-ish
// lookup) and are explicitly NOT claimed to match DAWA byte-for-byte. See
// DatavaskAdgangsadresser / DatavaskAdresser.

// ---- response shapes (struct field order IS the JSON key order) ----

// DatavaskAdgangsadresseResult is the /datavask/adgangsadresser envelope.
type DatavaskAdgangsadresseResult struct {
	Kategori   string                          `json:"kategori"`
	Resultater []DatavaskAdgangsadresseElement `json:"resultater"`
}

// DatavaskAdgangsadresseElement is one resultater[] entry. The embedded mini
// object is keyed "adresse" (not "adgangsadresse") on this endpoint too — DAWA
// reuses the same element shape for both datavask routes.
type DatavaskAdgangsadresseElement struct {
	Adresse       MiniAdgangsadresse      `json:"adresse"`
	Aktueladresse MiniAdgangsadresse      `json:"aktueladresse"`
	Vaskeresultat VaskeresultatAdgangsadr `json:"vaskeresultat"`
}

// DatavaskAdresseResult is the /datavask/adresser envelope.
type DatavaskAdresseResult struct {
	Kategori   string                   `json:"kategori"`
	Resultater []DatavaskAdresseElement `json:"resultater"`
}

// DatavaskAdresseElement is one resultater[] entry.
type DatavaskAdresseElement struct {
	Adresse       MiniAdresse      `json:"adresse"`
	Aktueladresse MiniAdresse      `json:"aktueladresse"`
	Vaskeresultat VaskeresultatAdr `json:"vaskeresultat"`
}

// MiniAdgangsadresse is DAWA's reduced datavask adgangsadresse representation.
// Key order matches the golden exactly:
//
//	id, vejnavn, adresseringsvejnavn, husnr, supplerendebynavn, postnr,
//	postnrnavn, status, virkningstart, virkningslut, href
type MiniAdgangsadresse struct {
	ID                  string  `json:"id"`
	Vejnavn             *string `json:"vejnavn"`
	Adresseringsvejnavn *string `json:"adresseringsvejnavn"`
	Husnr               string  `json:"husnr"`
	Supplerendebynavn   *string `json:"supplerendebynavn"`
	Postnr              *string `json:"postnr"`
	Postnrnavn          *string `json:"postnrnavn"`
	Status              int     `json:"status"`
	Virkningstart       *string `json:"virkningstart"`
	Virkningslut        *string `json:"virkningslut"`
	Href                string  `json:"href"`
}

// MiniAdresse is DAWA's reduced datavask adresse representation. Key order:
//
//	id, vejnavn, adresseringsvejnavn, husnr, supplerendebynavn, postnr,
//	postnrnavn, status, virkningstart, virkningslut, adgangsadresseid, etage,
//	dør, href
type MiniAdresse struct {
	ID                  string  `json:"id"`
	Vejnavn             *string `json:"vejnavn"`
	Adresseringsvejnavn *string `json:"adresseringsvejnavn"`
	Husnr               string  `json:"husnr"`
	Supplerendebynavn   *string `json:"supplerendebynavn"`
	Postnr              *string `json:"postnr"`
	Postnrnavn          *string `json:"postnrnavn"`
	Status              int     `json:"status"`
	Virkningstart       *string `json:"virkningstart"`
	Virkningslut        *string `json:"virkningslut"`
	Adgangsadresseid    string  `json:"adgangsadresseid"`
	Etage               *string `json:"etage"`
	Doer                *string `json:"dør"`
	Href                string  `json:"href"`
}

// VaskeVariantAdgangsadr is the adgangsadresse vaskeresultat.variant object.
type VaskeVariantAdgangsadr struct {
	Vejnavn           *string `json:"vejnavn"`
	Husnr             string  `json:"husnr"`
	Supplerendebynavn *string `json:"supplerendebynavn"`
	Postnr            *string `json:"postnr"`
	Postnrnavn        *string `json:"postnrnavn"`
}

// VaskeVariantAdr is the adresse vaskeresultat.variant object (adds etage/dør).
type VaskeVariantAdr struct {
	Vejnavn           *string `json:"vejnavn"`
	Husnr             string  `json:"husnr"`
	Etage             *string `json:"etage"`
	Doer              *string `json:"dør"`
	Supplerendebynavn *string `json:"supplerendebynavn"`
	Postnr            *string `json:"postnr"`
	Postnrnavn        *string `json:"postnrnavn"`
}

// VaskeForskelle is the per-field correction-distance object. 0 = exact match.
type VaskeForskelle struct {
	Vejnavn    int `json:"vejnavn"`
	Husnr      int `json:"husnr"`
	Postnr     int `json:"postnr"`
	Postnrnavn int `json:"postnrnavn"`
}

// VaskeParsetadresse is the parsed-input echo {vejnavn,husnr,postnr,postnrnavn}.
type VaskeParsetadresse struct {
	Vejnavn    *string `json:"vejnavn"`
	Husnr      *string `json:"husnr"`
	Postnr     *string `json:"postnr"`
	Postnrnavn *string `json:"postnrnavn"`
}

// VaskeresultatAdgangsadr is the adgangsadresse vaskeresultat. Key order:
//
//	variant, afstand, forskelle, parsetadresse, ukendtetokens,
//	anvendtstormodtagerpostnummer
type VaskeresultatAdgangsadr struct {
	Variant                       VaskeVariantAdgangsadr `json:"variant"`
	Afstand                       int                    `json:"afstand"`
	Forskelle                     VaskeForskelle         `json:"forskelle"`
	Parsetadresse                 VaskeParsetadresse     `json:"parsetadresse"`
	Ukendtetokens                 []string               `json:"ukendtetokens"`
	Anvendtstormodtagerpostnummer *string                `json:"anvendtstormodtagerpostnummer"`
}

// VaskeresultatAdr is the adresse vaskeresultat (variant carries etage/dør).
type VaskeresultatAdr struct {
	Variant                       VaskeVariantAdr    `json:"variant"`
	Afstand                       int                `json:"afstand"`
	Forskelle                     VaskeForskelle     `json:"forskelle"`
	Parsetadresse                 VaskeParsetadresse `json:"parsetadresse"`
	Ukendtetokens                 []string           `json:"ukendtetokens"`
	Anvendtstormodtagerpostnummer *string            `json:"anvendtstormodtagerpostnummer"`
}

// ---- betegnelse parsing ----

// parsedBetegnelse is the result of splitting a datavask betegnelse string into
// its address components.
type parsedBetegnelse struct {
	vejnavn    string
	husnr      string
	etage      string
	doer       string
	postnr     string
	postnrnavn string
}

// parseBetegnelse splits a clean, fully-qualified address betegnelse into its
// components. DAWA's parser is tolerant; for the kategori-A case the input is a
// clean full address of the form:
//
//	"<vejnavn> <husnr>[, <etage>. <dør>] <postnr> <postnrnavn>"
//
// where vejnavn may contain spaces, husnr is the first token (after the first)
// that starts with a digit, postnr is the first 4-digit token after the husnr,
// and postnrnavn is everything after the postnr. Commas are treated as token
// separators.
func parseBetegnelse(betegnelse string) parsedBetegnelse {
	s := strings.TrimSpace(betegnelse)
	s = strings.ReplaceAll(s, ",", " ")
	fields := strings.Fields(s)

	var p parsedBetegnelse
	if len(fields) == 0 {
		return p
	}

	// Locate the postnr: the first 4-digit token at index >= 1.
	postnrIdx := -1
	for i := 1; i < len(fields); i++ {
		if isPostnr(fields[i]) {
			postnrIdx = i
			break
		}
	}

	if postnrIdx == -1 {
		p.vejnavn, p.husnr = splitVejnavnHusnr(fields)
		return p
	}

	p.postnr = fields[postnrIdx]
	if postnrIdx+1 < len(fields) {
		p.postnrnavn = strings.Join(fields[postnrIdx+1:], " ")
	}
	p.vejnavn, p.husnr = splitVejnavnHusnr(fields[:postnrIdx])
	return p
}

// splitVejnavnHusnr splits the leading tokens into a vejnavn (which may contain
// spaces) and a husnr. The husnr is the first token after the first whose first
// rune is a digit. Tokens after the husnr (etage/dør on the best-effort path)
// are ignored for the exact-match lookup.
func splitVejnavnHusnr(tokens []string) (vejnavn, husnr string) {
	if len(tokens) == 0 {
		return "", ""
	}
	husnrIdx := -1
	for i := 1; i < len(tokens); i++ {
		t := tokens[i]
		if t != "" && t[0] >= '0' && t[0] <= '9' {
			husnrIdx = i
			break
		}
	}
	if husnrIdx == -1 {
		return strings.Join(tokens, " "), ""
	}
	return strings.Join(tokens[:husnrIdx], " "), tokens[husnrIdx]
}

func isPostnr(s string) bool {
	if len(s) != 4 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ---- SQL projections (the datavask mini shape) ----
//
// The DAR extract has no "virkningstart"/"virkningslut" columns; DAWA's
// virkningstart for these rows equals the row's oprettet timestamp (verified:
// oprettet == ikrafttraedelse == aendret == golden virkningstart for the
// Rådhuspladsen 1 husnummer), and virkningslut is null. husnr is post-processed
// with formatHusnr in Go.

const datavaskAdgangsadresseCols = `
	h.id,
	nv.navn AS vejnavn,
	nv.adresseringsnavn,
	h.husnummertekst,
	sb.navn AS supplerendebynavn,
	p.postnr,
	p.navn AS postnrnavn,
	h.dar_status,
	to_char(h.oprettet AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS virkningstart`

const datavaskAdgangsadresseFrom = `
	FROM dar_husnummer h
	LEFT JOIN dar_navngivenvej nv ON nv.id = h.navngivenvej
	LEFT JOIN dar_postnummer p ON p.id = h.postnummer_id
	LEFT JOIN dagi_supplerendebynavne sb ON sb.dar_uuid = h.supplerende_bynavn`

const datavaskAdresseCols = `
	a.id_lokalid AS id,
	nv.navn AS vejnavn,
	nv.adresseringsnavn,
	h.husnummertekst,
	sb.navn AS supplerendebynavn,
	p.postnr,
	p.navn AS postnrnavn,
	a.dar_status,
	to_char(a.oprettet AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') AS virkningstart,
	h.id AS adgangsadresseid,
	a.etagebetegnelse AS etage,
	a.doerbetegnelse AS doer`

const datavaskAdresseFrom = `
	FROM dar_adresse a
	JOIN dar_husnummer h ON h.id = a.husnummer_id
	LEFT JOIN dar_navngivenvej nv ON nv.id = h.navngivenvej
	LEFT JOIN dar_postnummer p ON p.id = h.postnummer_id
	LEFT JOIN dagi_supplerendebynavne sb ON sb.dar_uuid = h.supplerende_bynavn`

// ---- scanned rows ----

type miniAdgScan struct {
	id            string
	vejnavn       *string
	adrnavn       *string
	husnrtekst    *string
	supplBynavn   *string
	postnr        *string
	postnrnavn    *string
	darStatus     *int
	virkningstart *string
}

type miniAdrScan struct {
	id               string
	vejnavn          *string
	adrnavn          *string
	husnrtekst       *string
	supplBynavn      *string
	postnr           *string
	postnrnavn       *string
	darStatus        *int
	virkningstart    *string
	adgangsadresseid string
	etage            *string
	doer             *string
}

// ---- builders ----

func (s *miniAdgScan) toMini(baseURL string) MiniAdgangsadresse {
	return MiniAdgangsadresse{
		ID:                  s.id,
		Vejnavn:             s.vejnavn,
		Adresseringsvejnavn: s.adrnavn,
		Husnr:               formatHusnr(deref(s.husnrtekst)),
		Supplerendebynavn:   s.supplBynavn,
		Postnr:              s.postnr,
		Postnrnavn:          s.postnrnavn,
		Status:              darStatusToDawa(derefInt(s.darStatus)),
		Virkningstart:       s.virkningstart,
		Virkningslut:        nil,
		Href:                fmt.Sprintf("%s/adgangsadresser/%s", baseURL, s.id),
	}
}

func (s *miniAdrScan) toMini(baseURL string) MiniAdresse {
	return MiniAdresse{
		ID:                  s.id,
		Vejnavn:             s.vejnavn,
		Adresseringsvejnavn: s.adrnavn,
		Husnr:               formatHusnr(deref(s.husnrtekst)),
		Supplerendebynavn:   s.supplBynavn,
		Postnr:              s.postnr,
		Postnrnavn:          s.postnrnavn,
		Status:              darStatusToDawa(derefInt(s.darStatus)),
		Virkningstart:       s.virkningstart,
		Virkningslut:        nil,
		Adgangsadresseid:    s.adgangsadresseid,
		Etage:               s.etage,
		Doer:                s.doer,
		Href:                fmt.Sprintf("%s/adresser/%s", baseURL, s.id),
	}
}

// kategoriAVaskeresultatAdg builds the perfect-match (afstand 0, all forskelle
// 0) adgangsadresse vaskeresultat from the matched row + parsed input.
func kategoriAVaskeresultatAdg(m MiniAdgangsadresse, p parsedBetegnelse) VaskeresultatAdgangsadr {
	return VaskeresultatAdgangsadr{
		Variant: VaskeVariantAdgangsadr{
			Vejnavn:           m.Vejnavn,
			Husnr:             m.Husnr,
			Supplerendebynavn: m.Supplerendebynavn,
			Postnr:            m.Postnr,
			Postnrnavn:        m.Postnrnavn,
		},
		Afstand:                       0,
		Forskelle:                     VaskeForskelle{},
		Parsetadresse:                 parsetFrom(p),
		Ukendtetokens:                 []string{},
		Anvendtstormodtagerpostnummer: nil,
	}
}

// kategoriAVaskeresultatAdr builds the perfect-match adresse vaskeresultat.
func kategoriAVaskeresultatAdr(m MiniAdresse, p parsedBetegnelse) VaskeresultatAdr {
	return VaskeresultatAdr{
		Variant: VaskeVariantAdr{
			Vejnavn:           m.Vejnavn,
			Husnr:             m.Husnr,
			Etage:             m.Etage,
			Doer:              m.Doer,
			Supplerendebynavn: m.Supplerendebynavn,
			Postnr:            m.Postnr,
			Postnrnavn:        m.Postnrnavn,
		},
		Afstand:                       0,
		Forskelle:                     VaskeForskelle{},
		Parsetadresse:                 parsetFrom(p),
		Ukendtetokens:                 []string{},
		Anvendtstormodtagerpostnummer: nil,
	}
}

// parsetFrom echoes the parsed input as the vaskeresultat.parsetadresse object.
func parsetFrom(p parsedBetegnelse) VaskeParsetadresse {
	return VaskeParsetadresse{
		Vejnavn:    strOrNil(p.vejnavn),
		Husnr:      strOrNil(p.husnr),
		Postnr:     strOrNil(p.postnr),
		Postnrnavn: strOrNil(p.postnrnavn),
	}
}

func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ---- public entrypoints ----

// DatavaskAdgangsadresser washes a single adgangsadresse betegnelse. A unique
// exact match (vejnavn + husnr + postnr) yields kategori "A" with the matched
// address in the mini representation, a copy as aktueladresse, and a perfect
// (afstand 0) vaskeresultat. Zero / multiple matches fall to the best-effort
// kategori "C" branch (NOT byte-exact vs DAWA).
func DatavaskAdgangsadresser(ctx context.Context, pool *pgxpool.Pool, betegnelse, baseURL string) (*DatavaskAdgangsadresseResult, error) {
	p := parseBetegnelse(betegnelse)
	rows, err := lookupMiniAdgangsadresser(ctx, pool, p)
	if err != nil {
		return nil, err
	}

	kategori := "A"
	if len(rows) != 1 {
		kategori = "C"
	}

	elems := make([]DatavaskAdgangsadresseElement, 0, len(rows))
	for _, r := range rows {
		m := r.toMini(baseURL)
		elems = append(elems, DatavaskAdgangsadresseElement{
			Adresse:       m,
			Aktueladresse: m,
			Vaskeresultat: kategoriAVaskeresultatAdg(m, p),
		})
	}
	return &DatavaskAdgangsadresseResult{Kategori: kategori, Resultater: elems}, nil
}

// DatavaskAdresser washes a single adresse betegnelse, analogous to
// DatavaskAdgangsadresser. The exact case constrains etage/dør to the parsed
// values (NULL when absent) so a unique ground-floor address is returned.
func DatavaskAdresser(ctx context.Context, pool *pgxpool.Pool, betegnelse, baseURL string) (*DatavaskAdresseResult, error) {
	p := parseBetegnelse(betegnelse)
	rows, err := lookupMiniAdresser(ctx, pool, p)
	if err != nil {
		return nil, err
	}

	kategori := "A"
	if len(rows) != 1 {
		kategori = "C"
	}

	elems := make([]DatavaskAdresseElement, 0, len(rows))
	for _, r := range rows {
		m := r.toMini(baseURL)
		elems = append(elems, DatavaskAdresseElement{
			Adresse:       m,
			Aktueladresse: m,
			Vaskeresultat: kategoriAVaskeresultatAdr(m, p),
		})
	}
	return &DatavaskAdresseResult{Kategori: kategori, Resultater: elems}, nil
}

// ---- DB lookups ----

func lookupMiniAdgangsadresser(ctx context.Context, pool *pgxpool.Pool, p parsedBetegnelse) ([]miniAdgScan, error) {
	conds := []string{"h.status = '3'"}
	args := []any{}
	i := 1
	if p.vejnavn != "" {
		conds = append(conds, fmt.Sprintf("lower(nv.navn) = lower($%d)", i))
		args = append(args, p.vejnavn)
		i++
	}
	if p.husnr != "" {
		conds = append(conds, fmt.Sprintf("h.husnummertekst = $%d", i))
		args = append(args, p.husnr)
		i++
	}
	if p.postnr != "" {
		conds = append(conds, fmt.Sprintf("p.postnr = $%d", i))
		args = append(args, p.postnr)
		i++
	}
	sql := "SELECT" + datavaskAdgangsadresseCols + datavaskAdgangsadresseFrom + " WHERE " + strings.Join(conds, " AND ")
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []miniAdgScan
	for rows.Next() {
		var s miniAdgScan
		if err := rows.Scan(
			&s.id, &s.vejnavn, &s.adrnavn, &s.husnrtekst, &s.supplBynavn,
			&s.postnr, &s.postnrnavn, &s.darStatus, &s.virkningstart,
		); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func lookupMiniAdresser(ctx context.Context, pool *pgxpool.Pool, p parsedBetegnelse) ([]miniAdrScan, error) {
	conds := []string{"a.status = '3'"}
	args := []any{}
	i := 1
	if p.vejnavn != "" {
		conds = append(conds, fmt.Sprintf("lower(nv.navn) = lower($%d)", i))
		args = append(args, p.vejnavn)
		i++
	}
	if p.husnr != "" {
		conds = append(conds, fmt.Sprintf("h.husnummertekst = $%d", i))
		args = append(args, p.husnr)
		i++
	}
	if p.postnr != "" {
		conds = append(conds, fmt.Sprintf("p.postnr = $%d", i))
		args = append(args, p.postnr)
		i++
	}
	// For the kategori-A exact case the input has no etage/dør → the
	// ground-floor / no-door unit. Constrain to the parsed etage/dør (NULL when
	// absent) so we land on the unique address rather than every unit.
	if p.etage == "" {
		conds = append(conds, "a.etagebetegnelse IS NULL")
	} else {
		conds = append(conds, fmt.Sprintf("a.etagebetegnelse = $%d", i))
		args = append(args, p.etage)
		i++
	}
	if p.doer == "" {
		conds = append(conds, "a.doerbetegnelse IS NULL")
	} else {
		conds = append(conds, fmt.Sprintf("a.doerbetegnelse = $%d", i))
		args = append(args, p.doer)
		i++
	}
	sql := "SELECT" + datavaskAdresseCols + datavaskAdresseFrom + " WHERE " + strings.Join(conds, " AND ")
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []miniAdrScan
	for rows.Next() {
		var s miniAdrScan
		if err := rows.Scan(
			&s.id, &s.vejnavn, &s.adrnavn, &s.husnrtekst, &s.supplBynavn,
			&s.postnr, &s.postnrnavn, &s.darStatus, &s.virkningstart,
			&s.adgangsadresseid, &s.etage, &s.doer,
		); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
