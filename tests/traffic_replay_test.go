// traffic_replay_test.go — replay REAL production traffic against live DAWA.
//
// The server aggregates every GET it serves into the access_paths table
// (internal/api/traffic.go). This test reads those paths (hits-descending)
// and compares ours vs live DAWA for each, using the suite's full tolerance
// machinery. Two jobs, both time-critical before DAWA dies 2026-07-01:
//
//  1. find compatibility bugs in the request shapes REAL clients use — the
//     synthetic families and captured samples can't enumerate what actual
//     traffic does;
//  2. run with DAWA_ORACLE=capture and DAWA's answers to the production
//     traffic freeze into tests/golden/ alongside the suite snapshot —
//     the last, traffic-shaped golden sample.
//
// Run against the PRODUCTION access log (the table lives in the prod DB):
//
//	DATABASE_URL="postgres://…tailscale-or-dump…" TRAFFIC_REPLAY=1 \
//	  DAWA_ORACLE=capture go test ./tests/ -run TestTrafficReplay -v -timeout 3600s
//
// TRAFFIC_REPLAY_LIMIT caps the replayed path count (default 1000, by hits).
package tests

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestTrafficReplay(t *testing.T) {
	if os.Getenv("TRAFFIC_REPLAY") == "" {
		t.Skip("set TRAFFIC_REPLAY=1 to replay the access_paths log against live DAWA (see file header)")
	}
	requireDAWA(t)
	if oracle() == oracleSnapshot {
		t.Skip("live oracle required — replay paths are not in the frozen suite snapshot until a capture run includes them")
	}
	if testPool == nil {
		t.Skip("no DB pool")
	}

	limit := 1000
	if v, err := strconv.Atoi(os.Getenv("TRAFFIC_REPLAY_LIMIT")); err == nil && v > 0 {
		limit = v
	}

	rows, err := testPool.Query(context.Background(),
		`SELECT path, hits FROM access_paths ORDER BY hits DESC, path LIMIT $1`, limit)
	if err != nil {
		t.Fatalf("read access_paths: %v (run against the DB that holds the production access log)", err)
	}
	defer rows.Close()
	type pathRow struct {
		path string
		hits int64
	}
	var paths []pathRow
	for rows.Next() {
		var p pathRow
		if err := rows.Scan(&p.path, &p.hits); err != nil {
			t.Fatalf("scan access_paths: %v", err)
		}
		paths = append(paths, p)
	}
	if len(paths) == 0 {
		t.Skip("access_paths is empty — no traffic recorded yet")
	}

	skipped := 0
	for _, p := range paths {
		if !replayable(p.path) {
			skipped++
			continue
		}
		p := p
		t.Run(label(p.path), func(t *testing.T) {
			// /historik chains get the documented DAWA-stale tolerance; every
			// other path goes through the strict suite comparison.
			if strings.HasPrefix(p.path, "/historik/") {
				compareHistorikChain(t, p.path)
				return
			}
			compareEndpoint(t, p.path)
		})
	}
	t.Logf("replayed %d path(s) (skipped %d non-replayable) — top hits first", len(paths)-skipped, skipped)
}

// replayable filters paths that cannot be meaningfully compared: anything
// token-bearing (never sent upstream or frozen) and obvious scanner noise
// that slipped past the recorder's junk filter.
func replayable(path string) bool {
	if !strings.HasPrefix(path, "/") {
		return false
	}
	low := strings.ToLower(path)
	for _, bad := range []string{"token=", ".php", ".env", "wp-", "admin", "cgi-bin", "%00"} {
		if strings.Contains(low, bad) {
			return false
		}
	}
	return true
}
