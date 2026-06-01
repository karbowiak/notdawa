// replikering.go implements the faithfully-derivable subset of DAWA's
// replikering API (https://dawadocs.dataforsyningen.dk/dok/api/replikering) over
// the local PostGIS mirror.
//
// DAWA's replikering API lets a client mirror DAWA's data:
//
//   - GET /replikering/senestesekvensnummer        the current sequence number
//   - GET /replikering/{entitet}/udtraek           a full snapshot of an entity
//   - GET /replikering/{entitet}/haendelser        the incremental change feed
//   - GET /replikering/transaktioner               the transaction log
//
// notdawa mirrors Datafordeler BULK extracts and keeps NO event log: each ingest
// run REPLACES an entity wholesale (dagiLoad in internal/ingest/ingest.go
// TRUNCATEs the table per run). So only the parts that are genuinely derivable
// from the current rows are backed by real data; the change-history parts get the
// documented envelope with truthful (initial-load / empty) content, never
// fabricated updates or deletes. See the "Replikering" section of
// docs/compatibility_report.md.
//
// Record shape: udtraek/haendelser emit each entity's DAWA representation, built
// from the very same dawa.List* projection the regular collection endpoint serves
// (e.g. dawa.ListRegions). There is no separate stored "flad" column in this
// mirror — the DAWA object is derived at request time from the dagi_* tables — so
// reusing the List* projection is the closest faithful representation. udtraek
// streams newline-delimited JSON (DAWA's default transport for udtraek) when
// ?ndjson is given, otherwise a DAWA JSON array; both use the dawa marshaller's
// byte layout.
package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/karbowiak/notdawa/internal/dawa"
)

// contentTypeNDJSON is the media type DAWA serves for an ndjson stream.
const contentTypeNDJSON = "application/x-ndjson"

// replikeringDatamodelJSON is DAWA's static replikering datamodel descriptor,
// captured verbatim from GET /replikering/datamodel?entitet=vejstykke
// (2026-05-31). DAWA returns the SAME full object — every entitet keyed by name
// with its key/attributes — regardless of the ?entitet value, so we serve these
// exact bytes for any entitet. This is reference metadata, identical run-to-run.
//
//go:embed replikering_datamodel.json
var replikeringDatamodelJSON []byte

// replEntity describes one entity served by the replikering API.
//
//   - countTable is the underlying mirror table whose live row count the snapshot
//     must equal (used by the conformance tests).
//   - list returns the entity's full set of DAWA objects in DAWA's stable order,
//     reusing the regular serving-layer projection (so the replikering record is
//     identical to what the collection endpoint emits).
type replEntity struct {
	countTable string
	list       func(ctx context.Context, pool *pgxpool.Pool, baseURL string) ([]any, error)
}

// replEntities is the registry of entities served by the replikering API. New
// entities are added by listing them here with their dawa.List* projection;
// udtraek/haendelser then work for them with no further code.
//
// Only "regioner" is wired today: it is the small entity with a List* function
// whose snapshot is cheap to materialise and assert on. Other entities
// (kommuner, sogne, postnumre, ...) can be added the same way as their List*
// functions are confirmed stable.
var replEntities = map[string]replEntity{
	"regioner": {
		countTable: "dagi_regioner",
		list: func(ctx context.Context, pool *pgxpool.Pool, baseURL string) ([]any, error) {
			rs, err := dawa.ListRegions(ctx, pool, baseURL)
			if err != nil {
				return nil, err
			}
			out := make([]any, len(rs))
			for i, r := range rs {
				out[i] = r
			}
			return out, nil
		},
	},
}

// dawaReplTime formats a timestamp the way DAWA renders replikering timestamps
// (UTC, millisecond precision, trailing Z).
func dawaReplTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// latestSekvensnummer returns notdawa's current replication sequence number and
// its timestamp.
//
// Semantics: there is no event log, but every bulk extract records its
// Datafordeler generation_number in ingest_runs. We define
//
//	sekvensnummer = MAX(generation_number) over all ingest_runs
//	tidspunkt     = MAX(finished_at)       over all ingest_runs
//
// This is a real, deterministic number that advances monotonically as newer bulk
// extracts are ingested. When the ingest_runs table is absent or empty (e.g. a
// freshly seeded mirror) it falls back to the deterministic baseline (1, now), so
// the endpoint always returns the documented shape.
func latestSekvensnummer(ctx context.Context, pool *pgxpool.Pool) (int64, time.Time) {
	var seq *int64
	var ts *time.Time
	// The to_regclass guard returns no row when ingest_runs does not exist,
	// avoiding a query error on a freshly-seeded mirror; max(...) then handles the
	// empty-table case by yielding NULL.
	err := pool.QueryRow(ctx, `
		SELECT (SELECT max(generation_number) FROM ingest_runs),
		       (SELECT max(finished_at)       FROM ingest_runs)
		WHERE to_regclass('public.ingest_runs') IS NOT NULL`).Scan(&seq, &ts)
	if err != nil || seq == nil {
		return 1, time.Now().UTC()
	}
	t := time.Now().UTC()
	if ts != nil {
		t = ts.UTC()
	}
	return *seq, t
}

// sekvensnummerEnvelope is the senestesekvensnummer response shape. Field order
// is significant (MarshalDAWA preserves struct order).
type sekvensnummerEnvelope struct {
	Sekvensnummer int64  `json:"sekvensnummer"`
	Tidspunkt     string `json:"tidspunkt"`
}

// haendelse is one replikering change event. Field order matches DAWA's event
// representation (operation, tidspunkt, sekvensnummer, data).
type haendelse struct {
	Operation     string `json:"operation"`
	Tidspunkt     string `json:"tidspunkt"`
	Sekvensnummer int64  `json:"sekvensnummer"`
	Data          any    `json:"data"`
}

// transaktion is one replikering transaction descriptor. Field order and key set
// match DAWA's transaction representation exactly:
//
//	{"txid": <int>, "tidspunkt": "<ISO8601>", "operationer": []}
//
// DAWA's senestetransaktion + transaktioner share this shape (verified
// 2026-05-31). operationer is the per-transaction operation summary; the public
// (token-free) endpoint always serves it empty, so we render [] (never null).
type transaktion struct {
	Txid        int64  `json:"txid"`
	Tidspunkt   string `json:"tidspunkt"`
	Operationer []any  `json:"operationer"`
}

// wantReplNDJSON reports whether the client asked for newline-delimited JSON,
// either via the ?ndjson flag or an Accept: application/x-ndjson header (matching
// DAWA's udtraek transport selection).
func wantReplNDJSON(r *http.Request) bool {
	if r.URL.Query().Has("ndjson") {
		return true
	}
	return r.Header.Get("Accept") == contentTypeNDJSON
}

// requestedSnapshotSeq extracts a requested snapshot sequence number from the
// DAWA-compatible sekvensnummer / sekvensnummertil query parameters.
func requestedSnapshotSeq(r *http.Request) (int64, bool) {
	for _, key := range []string{"sekvensnummer", "sekvensnummertil"} {
		if v := r.URL.Query().Get(key); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

// parseReplSeq parses an int64 sequence-number parameter, returning def when the
// value is empty or unparseable.
func parseReplSeq(v string, def int64) int64 {
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

// ---- HTTP handlers (wired in server.routes) ----

// senesteSekvensnummer serves GET /replikering/senestesekvensnummer.
// Shape: {"sekvensnummer": <int>, "tidspunkt": "<ISO8601>"}.
func (s *server) senesteSekvensnummer(w http.ResponseWriter, r *http.Request) {
	seq, ts := latestSekvensnummer(s.ctx(r), s.pool)
	writeMarshalled(w, sekvensnummerEnvelope{Sekvensnummer: seq, Tidspunkt: dawaReplTime(ts)})
}

// udtraek serves GET /replikering/{entitet}/udtraek, a full snapshot of the
// entity in its DAWA representation. Streams ndjson when requested (DAWA's
// default transport for udtraek), otherwise a JSON array.
//
// Honoured params: ndjson (flag). DAWA's sekvensnummer/sekvensnummertil select a
// historical snapshot; we hold only the current bulk snapshot, so a value at or
// below the current sequence returns the current snapshot while a value above
// current yields an empty snapshot (that state does not exist yet), truthful and
// never fabricated.
func (s *server) udtraek(w http.ResponseWriter, r *http.Request) {
	ent, ok := replEntities[r.PathValue("entitet")]
	if !ok {
		writeNotFound(w, map[string]any{"entitet": r.PathValue("entitet")})
		return
	}
	if reqSeq, has := requestedSnapshotSeq(r); has {
		cur, _ := latestSekvensnummer(s.ctx(r), s.pool)
		if reqSeq > cur {
			writeReplList(w, r, []any{})
			return
		}
	}
	records, err := ent.list(s.ctx(r), s.pool, s.baseURL)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeReplList(w, r, records)
}

// haendelser serves GET /replikering/{entitet}/haendelser with the documented
// DAWA event envelope, but with TRUTHFUL content only.
//
// notdawa ingests full bulk extracts and has no per-row change log, so genuine
// insert/update/delete history cannot be reconstructed. The only non-fabricated
// statement we can make is that, at the initial bulk load, every current row was
// CREATED. We therefore emit exactly one "insert" event per current row, stamped
// at the load sekvensnummer, and ONLY when the requested
// [sekvensnummerfra, sekvensnummertil] window includes that load sekvensnummer.
// Any other window returns an empty list; we never invent updates or deletes that
// did not happen. See docs/compatibility_report.md.
func (s *server) haendelser(w http.ResponseWriter, r *http.Request) {
	ent, ok := replEntities[r.PathValue("entitet")]
	if !ok {
		writeNotFound(w, map[string]any{"entitet": r.PathValue("entitet")})
		return
	}
	loadSeq, ts := latestSekvensnummer(s.ctx(r), s.pool)
	q := r.URL.Query()
	fra := parseReplSeq(q.Get("sekvensnummerfra"), 0)
	til := parseReplSeq(q.Get("sekvensnummertil"), loadSeq)

	events := []haendelse{}
	if loadSeq >= fra && loadSeq <= til {
		records, err := ent.list(s.ctx(r), s.pool, s.baseURL)
		if err != nil {
			writeServerError(w, err)
			return
		}
		stamp := dawaReplTime(ts)
		events = make([]haendelse, 0, len(records))
		for _, rec := range records {
			events = append(events, haendelse{
				Operation:     "insert",
				Tidspunkt:     stamp,
				Sekvensnummer: loadSeq,
				Data:          rec,
			})
		}
	}
	writeReplList(w, r, events)
}

// transaktioner serves GET /replikering/transaktioner as a DAWA-shaped JSON
// array of {txid, tidspunkt, operationer} objects.
//
// A real transaction log is an artifact of DAWA's incremental ingest pipeline
// and does not exist for bulk extracts. The only truthful transaction we can
// state is the single bulk-load transaction; we never fabricate additional rows
// to pad the page to per_side. So the SHAPE/keys/types match DAWA exactly, while
// the array MEMBERSHIP and the txid/tidspunkt VALUES differ — those come from
// DAWA's global event log and are not reproducible from our bulk snapshot.
//
// side/per_side are honoured against that truthful single-transaction log: page
// 1 yields the bulk-load transaction; later pages yield an empty array.
func (s *server) transaktioner(w http.ResponseWriter, r *http.Request) {
	loadSeq, ts := latestSekvensnummer(s.ctx(r), s.pool)
	all := []transaktion{{
		Txid:        loadSeq,
		Tidspunkt:   dawaReplTime(ts),
		Operationer: []any{},
	}}
	writeReplList(w, r, paginateTransaktioner(r, all))
}

// senesteTransaktion serves GET /replikering/senestetransaktion.
// Shape: {"txid": <int>, "tidspunkt": "<ISO8601>", "operationer": []}.
//
// Like senestesekvensnummer, txid and tidspunkt come from DAWA's global event
// log; we surface our bulk-load generation as the txid so the keys/types/shape
// match DAWA exactly. The residual txid/tidspunkt value diff is an accepted,
// unreproducible source-data limitation.
func (s *server) senesteTransaktion(w http.ResponseWriter, r *http.Request) {
	loadSeq, ts := latestSekvensnummer(s.ctx(r), s.pool)
	writeMarshalled(w, transaktion{
		Txid:        loadSeq,
		Tidspunkt:   dawaReplTime(ts),
		Operationer: []any{},
	})
}

// datamodel serves GET /replikering/datamodel. DAWA returns a STATIC reference
// descriptor — every replikering entitet keyed by name with its key + attribute
// metadata — and serves the SAME full object for any ?entitet value. We serve
// the captured bytes verbatim (see replikeringDatamodelJSON). Status 200.
func (s *server) datamodel(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, replikeringDatamodelJSON)
}

// paginateTransaktioner applies DAWA's side/per_side paging to the transaction
// slice. Absent or unparseable paging params return the whole slice (DAWA's
// default is the full, unpaged log). side is 1-based.
func paginateTransaktioner(r *http.Request, all []transaktion) []transaktion {
	q := r.URL.Query()
	perSide := parseReplSeq(q.Get("per_side"), 0)
	if perSide <= 0 {
		return all
	}
	side := parseReplSeq(q.Get("side"), 1)
	if side < 1 {
		side = 1
	}
	start := (side - 1) * perSide
	if start >= int64(len(all)) {
		return []transaktion{}
	}
	end := start + perSide
	if end > int64(len(all)) {
		end = int64(len(all))
	}
	return all[start:end]
}

// writeReplList renders a typed slice as an ndjson stream (one COMPACT object per
// line, when requested) or a DAWA JSON array (indented, matching the regular API
// byte layout). DAWA's ndjson transport puts one record per physical line, so the
// ndjson path emits each object compactly (single line) — unlike the array path,
// which uses the dawa marshaller's multi-line layout.
func writeReplList[T any](w http.ResponseWriter, r *http.Request, items []T) {
	if wantReplNDJSON(r) {
		w.Header().Set("Content-Type", contentTypeNDJSON+"; charset=UTF-8")
		w.WriteHeader(http.StatusOK)
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false) // keep æ/ø/& literal, like the dawa marshaller
		for _, it := range items {
			// json.Encoder writes one compact object terminated by '\n' — exactly
			// one ndjson record per line.
			if err := enc.Encode(it); err != nil {
				return
			}
		}
		return
	}
	body, err := dawa.MarshalDAWAList(items)
	if err != nil {
		writeServerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}
