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

DAWA shuts down **2026-07-01**. Until then it is the source of truth. For after,
the upstream's responses are **frozen verbatim** in `golden/` (see "The oracle
after the shutdown" below) — that is not the old self-grading-fixture trap,
because what's frozen is the *upstream's* output captured while it lived, never
our own.

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
fixtures to capture, nothing to keep in sync — the upstream is the spec. (After
adding URLs, re-run a capture so the post-shutdown snapshot stays complete.)

## The oracle after the shutdown (`DAWA_ORACLE`, `golden/`)

Live DAWA is **decaying** on its way out: on 2026-06-11 it started returning
`null` for the address→jordstykke relation it served correctly ten days
earlier. Two mechanisms deal with the death of the oracle (`oracle_test.go`):

- **Decay tolerance** — `dawaDecayedDiff` / `fladDecayed` tolerate (and loudly
  log) fields where *DAWA* has degraded to null while we serve the real value.
  Asymmetric on purpose: our side going null still fails, and
  `TestDecayedFieldsStillServed` pins our values so a both-sides-null
  regression can't hide.
- **Frozen snapshot** — `DAWA_ORACLE=capture go test ./tests/` saves every
  upstream response byte-exact under `golden/` (raw bodies + `manifest.json`).
  `DAWA_ORACLE=snapshot` then serves the DAWA side from disk. With the env
  unset the suite stays live while the upstream answers and **auto-falls back**
  to the snapshot once it stops. Data keeps moving after the freeze (Datafordeler
  lives on; only the DAWA API dies), so post-shutdown value diffs accumulate
  honestly — re-bless by deliberate re-capture review, never by hand-editing.
