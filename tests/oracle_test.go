// oracle_test.go — the DAWA-side oracle source. Live DAWA shuts down
// 2026-07-01; this file lets the suite keep an oracle after that by freezing
// the upstream's responses VERBATIM while it still exists.
//
// Modes (env DAWA_ORACLE):
//
//	live      — fetch the DAWA side from https://api.dataforsyningen.dk (today's
//	            default behaviour).
//	capture   — live, PLUS every DAWA response is saved byte-exact under
//	            tests/golden/ (body files + manifest.json). Run the full suite
//	            once in this mode to (re)freeze the oracle:
//	              DAWA_ORACLE=capture go test ./tests/ -timeout 1800s
//	snapshot  — the DAWA side is served from tests/golden/ instead of the
//	            network. After the shutdown this is the ONLY oracle. A request
//	            not present in the snapshot skips its test loudly (re-capturing
//	            is impossible once DAWA is gone — keep the suite's URL set in
//	            sync with the snapshot while live capture still works).
//	(unset)   — auto: live when the upstream answers the reachability probe;
//	            otherwise falls back to the snapshot (if one exists) with a
//	            loud stderr note. Zero-config survival once DAWA dies.
//
// Bodies are stored as raw bytes, never re-marshalled: the flad comparator
// checks KEY ORDER on the raw bytes and captured-autocomplete compares
// byte-identity, so any re-encoding would corrupt the oracle. Responses that
// are upstream throttling (429/5xx) and token-bearing URLs are never frozen.
package tests

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	goldenDir      = "golden"
	goldenManifest = "golden/manifest.json"
)

type oracleSource int

const (
	oracleLive oracleSource = iota
	oracleCapture
	oracleSnapshot
)

type manifestEntry struct {
	File   string `json:"file"`
	Status int    `json:"status"`
}

type manifest struct {
	// CapturedAt is when the snapshot was (re)frozen from live DAWA.
	CapturedAt string                   `json:"captured_at"`
	Note       string                   `json:"note"`
	Entries    map[string]manifestEntry `json:"entries"`
}

var (
	oracleOnce sync.Once
	oracleSrc  oracleSource

	manifestMu sync.Mutex
	manifestV  *manifest
)

// oracle resolves the DAWA-side source once per run. An explicit DAWA_ORACLE
// wins; with it unset the suite stays live while the upstream answers and
// falls back to the snapshot when it does not (post-shutdown auto-survival).
func oracle() oracleSource {
	oracleOnce.Do(func() {
		switch os.Getenv("DAWA_ORACLE") {
		case "live":
			oracleSrc = oracleLive
		case "capture":
			oracleSrc = oracleCapture
		case "snapshot":
			oracleSrc = oracleSnapshot
		case "":
			if liveProbeOK() {
				oracleSrc = oracleLive
				return
			}
			if m := loadManifest(); m != nil {
				fmt.Fprintf(os.Stderr, "compat: live DAWA unreachable — using the FROZEN snapshot oracle (captured %s). Diffs are vs DAWA-as-it-was.\n", m.CapturedAt)
				oracleSrc = oracleSnapshot
				return
			}
			oracleSrc = oracleLive // no snapshot either; requireDAWA will skip
		default:
			fmt.Fprintf(os.Stderr, "compat: unknown DAWA_ORACLE=%q (want live|capture|snapshot) — using live\n", os.Getenv("DAWA_ORACLE"))
			oracleSrc = oracleLive
		}
	})
	return oracleSrc
}

// liveProbeOK is a cheap reachability check used only by auto mode.
func liveProbeOK() bool {
	resp, err := client.Get(liveDAWA + "/kommuner?per_side=1")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// loadManifest reads (and caches) the snapshot manifest; nil when absent.
func loadManifest() *manifest {
	manifestMu.Lock()
	defer manifestMu.Unlock()
	if manifestV != nil {
		return manifestV
	}
	b, err := os.ReadFile(goldenManifest)
	if err != nil {
		return nil
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		fmt.Fprintf(os.Stderr, "compat: unreadable %s: %v\n", goldenManifest, err)
		return nil
	}
	if m.Entries == nil {
		m.Entries = map[string]manifestEntry{}
	}
	manifestV = &m
	return manifestV
}

// snapshotFetch serves a DAWA request (path relative to the host) from the
// frozen snapshot. A miss skips the calling test loudly: the suite asked for a
// URL the capture run never saw, which needs a re-capture while DAWA is alive.
func snapshotFetch(t *testing.T, rel string) (int, []byte) {
	t.Helper()
	m := loadManifest()
	if m == nil {
		t.Skipf("snapshot oracle requested but %s does not exist — run DAWA_ORACLE=capture go test ./tests/ while live DAWA is up", goldenManifest)
	}
	e, ok := m.Entries[rel]
	if !ok {
		t.Skipf("snapshot oracle has no entry for %s — the suite's URL set drifted from the capture; re-capture while live DAWA still exists", rel)
	}
	b, err := os.ReadFile(filepath.Join(goldenDir, e.File))
	if err != nil {
		t.Fatalf("snapshot body missing for %s: %v", rel, err)
	}
	return e.Status, b
}

// captureSave freezes one DAWA response. Throttle statuses are transient (the
// next run may capture the real body) and token-bearing URLs must not be
// committed, so neither is saved.
func captureSave(rel string, status int, body []byte) {
	if strings.Contains(rel, "token=") {
		return
	}
	switch status {
	case 429, 500, 502, 503, 504:
		return
	}
	manifestMu.Lock()
	defer manifestMu.Unlock()
	if manifestV == nil {
		manifestV = &manifest{Entries: map[string]manifestEntry{}}
	}
	if manifestV.CapturedAt == "" {
		manifestV.CapturedAt = time.Now().UTC().Format(time.RFC3339)
		manifestV.Note = "Frozen live-DAWA responses (verbatim bytes). The post-shutdown oracle: DAWA dies 2026-07-01. Re-bless deliberately, never edit by hand."
	}
	name := bodyFileName(rel)
	if err := os.MkdirAll(filepath.Join(goldenDir, "r"), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "compat capture: %v\n", err)
		return
	}
	if err := os.WriteFile(filepath.Join(goldenDir, name), body, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "compat capture: %v\n", err)
		return
	}
	manifestV.Entries[rel] = manifestEntry{File: name, Status: status}
	flushManifestLocked()
}

// flushManifestLocked rewrites manifest.json (caller holds manifestMu). Map
// keys marshal sorted, so the file is git-diff stable.
func flushManifestLocked() {
	b, err := json.MarshalIndent(manifestV, "", " ")
	if err == nil {
		err = os.WriteFile(goldenManifest, append(b, '\n'), 0o644)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "compat capture: write manifest: %v\n", err)
	}
}

// bodyFileName derives a readable, collision-safe file name for a request: a
// sanitised slug of the path+query plus a short content-independent hash of
// the exact URL (two URLs can share a slug after sanitisation, never a hash).
func bodyFileName(rel string) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '=', r == '&':
			return r
		default:
			return '_'
		}
	}, strings.TrimPrefix(rel, "/"))
	if len(slug) > 80 {
		slug = slug[:80]
	}
	sum := sha1.Sum([]byte(rel))
	return filepath.Join("r", slug+"-"+hex.EncodeToString(sum[:4])+".resp")
}
