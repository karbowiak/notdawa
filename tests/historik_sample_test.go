// historik_sample_test.go — wide sample-verification of /historik/* against
// LIVE DAWA. The two TestDocumentedCoverage probes pin one id per endpoint;
// this test sweeps hundreds, deliberately weighted toward MULTI-VERSION chains
// (where the join semantics — historical attribution, interval ends, status
// mapping — can actually diverge). It exists because live DAWA dies 2026-07-01:
// the join semantics must be validated while the oracle can still answer, so
// run it (HISTORIK_SAMPLE=1) whenever the historik queries change. It is
// env-gated to keep the regular suite's live-request volume down, and it skips
// under the snapshot oracle (random ids are not in the frozen capture).
package tests

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool is the suite's DB pool, assigned by run() in compat_test.go so
// sampling tests can query the mirror directly.
var testPool *pgxpool.Pool

func TestHistorikSample(t *testing.T) {
	if os.Getenv("HISTORIK_SAMPLE") == "" {
		t.Skip("set HISTORIK_SAMPLE=1 to sweep /historik/* against live DAWA (hundreds of live requests)")
	}
	requireDAWA(t)
	if oracle() != oracleLive {
		t.Skip("live oracle required — sampled ids are not in the frozen snapshot")
	}
	if testPool == nil {
		t.Skip("no DB pool")
	}

	for _, c := range []struct {
		name, path, table string
	}{
		{"adgangsadresser", "/historik/adgangsadresser?id=", "dar_husnummer_hist"},
		{"adresser", "/historik/adresser?id=", "dar_adresse_hist"},
	} {
		c := c
		t.Run(c.name, func(t *testing.T) {
			// Deterministic sample: low-uuid neighbourhoods, multi-version
			// chains first (60) then single-version rows (40).
			ids := sampleHistIDs(t, c.table, true, 60)
			ids = append(ids, sampleHistIDs(t, c.table, false, 40)...)
			if len(ids) == 0 {
				t.Skipf("%s is empty — run `notdawa provision` first", c.table)
			}
			var identical, stale int
			for _, id := range ids {
				switch compareHistorikChain(t, c.path+id) {
				case histIdentical:
					identical++
				case histStale:
					stale++
				}
			}
			t.Logf("%s: %d ids — %d byte-identical, %d DAWA-stale-class (logged), %d failed",
				c.name, len(ids), identical, stale, len(ids)-identical-stale)
		})
	}
}

type histVerdict int

const (
	histIdentical histVerdict = iota
	histStale
	histFailed
)

// compareHistorikChain compares one id's chain against live with the
// DAWA-STALE tolerance: live's /historik is DAWA's internal event-sourced
// table, which (proven 2026-06-12, punkt 18702af7…: virkningsaktør
// "Konvertering2017 (korrektion af status)") never re-consumed DAR's
// RETROACTIVE corrections, contains pre-2018 event rows the bulk extracts
// don't carry, and remembers legacy road-name spellings ("Snapindløkken,
// Havek."). We serve the register's CURRENT history — more correct than the
// dying oracle, same policy class as dawaDecayedDiff. Tolerated (logged, never
// failing): version-topology mismatches (.length + interval bounds) and value
// diffs confined to adgangspunktstatus / vejnavn. ANY other field diff on a
// topology-matched chain (status, postnr, husnr, kommunekode, …) FAILS.
func compareHistorikChain(t *testing.T, path string) histVerdict {
	t.Helper()
	oStatus, oBody := fetch(t, ourBase+path)
	dStatus, dBody := fetch(t, liveDAWA+path)
	if oStatus != dStatus {
		t.Errorf("STATUS mismatch %s ours=%d dawa=%d", path, oStatus, dStatus)
		return histFailed
	}
	if string(oBody) == string(dBody) {
		return histIdentical
	}
	ov, oerr := decodeJSON(oBody)
	dv, derr := decodeJSON(dBody)
	if oerr != nil || derr != nil {
		t.Errorf("non-JSON historik body %s (ours=%v dawa=%v)", path, oerr, derr)
		return histFailed
	}
	var diffs []diff
	compareJSON("", ov, dv, &diffs)

	topologyMismatch := false
	var hard []diff
	for _, d := range diffs {
		leaf := d.path
		if i := strings.LastIndex(d.path, "."); i >= 0 {
			leaf = d.path[i+1:]
		}
		switch leaf {
		case "length":
			topologyMismatch = true
		case "virkningstart", "virkningslut", "adgangspunktstatus", "vejnavn":
			// stale-class value diffs
		default:
			hard = append(hard, d)
		}
	}
	// On a topology mismatch the positional walk pairs unrelated versions, so
	// per-field diffs are artifacts — the whole chain is the stale class.
	if topologyMismatch {
		t.Logf("%s: DAWA-stale topology (live has extra/missing pre-2018 or pre-correction versions; %d positional diffs ignored)", path, len(diffs))
		return histStale
	}
	if len(hard) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "%s: %d HARD field diff(s) beyond the documented stale classes", path, len(hard))
		for i, d := range hard {
			if i >= maxDiffsPerEndpoint {
				break
			}
			fmt.Fprintf(&b, "\n  %s: ours=%s dawa=%s", d.path, d.ours, d.dawa)
		}
		t.Error(b.String())
		return histFailed
	}
	t.Logf("%s: DAWA-stale value diffs only (%d, adgangspunktstatus/vejnavn/interval bounds)", path, len(diffs))
	return histStale
}

// sampleHistIDs returns up to n distinct ids from a hist table, either
// multi-version chains (the interesting ones) or single-version rows.
func sampleHistIDs(t *testing.T, table string, multi bool, n int) []string {
	t.Helper()
	op := "="
	if multi {
		op = ">"
	}
	// table is one of two code-controlled constants, never user input.
	rows, err := testPool.Query(context.Background(), fmt.Sprintf(
		`SELECT id FROM %s GROUP BY id HAVING count(*) %s 1 ORDER BY id LIMIT %d`, table, op, n))
	if err != nil {
		t.Fatalf("sample %s: %v", table, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("sample %s: %v", table, err)
		}
		ids = append(ids, id)
	}
	return ids
}
