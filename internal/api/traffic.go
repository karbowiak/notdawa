package api

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// trafficRecorder aggregates every served GET into per-path hit counters and
// flushes them to the access_paths table in batched upserts. The point: before
// DAWA shuts down (2026-07-01) we replay every REAL request shape this server
// has seen against both servers (tests/traffic_replay_test.go) — bugs surface
// from actual traffic, and DAWA's answers to it get frozen while it still
// exists. Recording is deliberately cheap (mutex + map bump on the request
// path; I/O only in the background flusher) and can never break serving: if
// the table is missing or the DB write fails, entries are dropped with one
// stderr note. Paths only — no IPs, no user agents.
type trafficRecorder struct {
	pool *pgxpool.Pool

	mu      sync.Mutex
	counts  map[string]*pathHit
	dropped bool // logged-once marker for flush failures
}

type pathHit struct {
	hits       int64
	lastStatus int
}

// maxPendingPaths caps the in-memory map between flushes so a URL-flooding
// scanner cannot balloon memory; overflow entries are dropped (the flusher
// resets the map every flushInterval anyway).
const maxPendingPaths = 50_000

const flushInterval = 30 * time.Second

// trafficJunk are path prefixes that are never worth replaying: crawler
// boilerplate and the API's own meta endpoints. Health probes never reach the
// recorder (withHealth intercepts them outside accessLog).
var trafficJunk = []string{
	"/robots.txt", "/favicon", "/sitemap", "/.well-known/",
	"/docs", "/openapi", "/apple-touch-icon",
}

func newTrafficRecorder(pool *pgxpool.Pool) *trafficRecorder {
	t := &trafficRecorder{pool: pool, counts: map[string]*pathHit{}}
	go t.loop()
	return t
}

// record notes one served request. Only GETs of non-junk paths count.
func (t *trafficRecorder) record(method, uri string, status int) {
	if method != "GET" {
		return
	}
	for _, j := range trafficJunk {
		if strings.HasPrefix(uri, j) {
			return
		}
	}
	t.mu.Lock()
	if h, ok := t.counts[uri]; ok {
		h.hits++
		h.lastStatus = status
	} else if len(t.counts) < maxPendingPaths {
		t.counts[uri] = &pathHit{hits: 1, lastStatus: status}
	}
	t.mu.Unlock()
}

func (t *trafficRecorder) loop() {
	tick := time.NewTicker(flushInterval)
	defer tick.Stop()
	for range tick.C {
		t.flush()
	}
}

// flush upserts and resets the pending counters. Failures drop the batch —
// the access log is best-effort bookkeeping, never worth failing requests or
// retry-buffering unboundedly.
func (t *trafficRecorder) flush() {
	t.mu.Lock()
	pending := t.counts
	t.counts = map[string]*pathHit{}
	t.mu.Unlock()
	if len(pending) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	batch := &pgx.Batch{}
	for uri, h := range pending {
		batch.Queue(`
			INSERT INTO access_paths (path, hits, last_status)
			VALUES ($1, $2, $3)
			ON CONFLICT (path) DO UPDATE
				SET hits = access_paths.hits + EXCLUDED.hits,
				    last_status = EXCLUDED.last_status,
				    last_seen = now()`, uri, h.hits, h.lastStatus)
	}
	br := t.pool.SendBatch(ctx, batch)
	var err error
	for range pending {
		if _, e := br.Exec(); e != nil && err == nil {
			err = e
		}
	}
	if e := br.Close(); e != nil && err == nil {
		err = e
	}
	if err != nil && !t.dropped {
		t.dropped = true // log once, not per flush — e.g. migrations not applied yet
		fmt.Fprintf(os.Stderr, "notdawa: access_paths flush failed (will keep trying silently): %v\n", err)
	}
}
