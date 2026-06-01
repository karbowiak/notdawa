# tests/ — the compatibility oracle

This is the **only** test suite in notdawa, and it does exactly one thing:

> For every DAWA API endpoint, fetch the same path from **our** server and from
> the **live** DAWA API (`https://api.dataforsyningen.dk`) and compare the two
> responses key-for-key. Not identical → **FAIL**, printing every differing JSON
> path with `ours` vs `dawa`.

## Why this and nothing else

The previous suite compared our output to **captured golden fixtures** with a
tolerance classifier we controlled. That is self-grading: a frozen API that never
updates scores 100% forever, because it still matches its own fixtures, and the
"known-divergence" buckets let the implementation quietly agree with itself that
everything is fine. The only oracle that can't be gamed is the live upstream.

DAWA shuts down **2026-07-01**. Until then it is the source of truth. After that,
this suite can't run live — by then the divergences should be understood and the
migration done.

## Running it

```sh
DATABASE_URL="postgres://notdawa:notdawa@localhost:5432/notdawa?sslmode=disable" \
  go test ./tests/ -run TestCompat -v -timeout 1200s
```

- Starts our server **in-process** (`api.NewServer(pool, "https://api.dataforsyningen.dk")`)
  — the base-url is the live host so every `href` is byte-comparable.
- Needs the ingested Postgres+PostGIS mirror. **No DB → skips cleanly** (exit 0, no tests).
- Needs network to live DAWA. **DAWA unreachable → skips** the comparisons.
- Run one family: `-run TestCompat/kommuner` (sub-tests: `collection`, `single`,
  `autocomplete`, `reverse`).

## The one tolerance dial

`allowGeoDriftMeters` in `compat_test.go` is the **only** knob. It starts at `0`
(strict, byte-exact). Run strict first to see every divergence. Bump it (e.g.
`50`) **only** to tolerate GEOS reprojection / pole-of-inaccessibility drift on
numeric geo leaves (`visueltcenter`/`bbox`/`koordinater`/`adgangspunkt`/`vejpunkt`).
It never relaxes a missing field, a `null`, or a non-geo value. Changing it is a
deliberate, reviewable edit — not a fixture nobody reads.

Everything else is fixed on our side. The failures the suite prints are the work
queue.

## Coverage

`families` in `compat_test.go` enumerates every documented DAWA resource family
and which sub-endpoints it exposes. `tilknytningPaths` lists the
`…tilknytninger` endpoints separately: the live gateway 404s them
(`TestTilknytningerMatchLiveDAWA`), so that test fails until our server agrees
with the upstream (remove the endpoints, or establish the real DAWA path).

To add coverage, add a row to `families` (or a path to `tilknytningPaths`). No
fixtures to capture, nothing to keep in sync — the upstream is the spec.
