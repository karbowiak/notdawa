# notdawa

A self-hosted, [DAWA](https://dawadocs.dataforsyningen.dk/)-compatible address API for
Denmark, backed by a **local mirror** of the official Datafordeler registers
(DAR, DAGI, MAT, DS). No runtime dependency on DAWA — which shuts down
**2026-07-01** — and no dependency on Datafordeler's GraphQL.

Data is pulled from Datafordeler's **Fildownload** service, transformed into DAWA's
exact JSON shapes, and served from **PostgreSQL + PostGIS** behind a **Huma v2**
HTTP front, with OpenAPI + Scalar docs at `/docs`.

## Requirements

- Go 1.25+
- Docker (for the bundled PostgreSQL 17 + PostGIS 3.5), or your own Postgres + PostGIS
- A Datafordeler **apiKey** — free from <https://selfservice.datafordeler.dk>

## Run

```bash
cp .env.example .env     # add your DATAFORDELER_API_KEY
make db-up               # start Postgres + PostGIS (docker compose)
make migrate             # extensions, ingest ledger and tables
make serve               # serve the API on :8080  (docs at /docs)
```

Config is read from `.env` (or the environment): `DATAFORDELER_API_KEY` and
`DATABASE_URL` (defaults to the docker-compose database). The CLI has three
commands: `migrate`, `import`, `serve`.

## Import the data

`notdawa import` downloads every register from Fildownload and loads it into
Postgres in dependency order, printing the plan first and a live progress bar per
entity.

```bash
make import                          # everything (~8 GB download, a few hours)

./bin/notdawa import --dry-run       # preview the plan, download nothing
./bin/notdawa import adresser-core   # a group: adressepunkt + husnummer + adresse
./bin/notdawa import regioner        # a single entity
```

Groups: `all` (default), `election`, `vejlinks`, `adresser-core`, `mat`.

Each loader truncates and reloads its table inside one transaction, so a re-import
is an atomic snapshot swap — the API keeps serving the old data until the new data
commits, and the mirror can't drift.

## Keep it up to date

Datafordeler publishes a fresh full extract per register once a week (DAR Saturday,
DAGI + MAT Sunday, DS Monday — each in a 03:00–06:00 window). Re-running
`notdawa import` weekly is all you need; run it Tuesday morning, after the whole
week's extracts have published:

```cron
# crontab — Tue 04:00
0 4 * * 2  cd /opt/notdawa && ./bin/notdawa import >> /var/log/notdawa-import.log 2>&1
```

Daily deltas exist too, but applying them needs change-set logic the
truncate-and-reload importer doesn't have — and only DAR (addresses) changes daily
in a way users notice. A weekly full re-import keeps the mirror correct and
drift-free with no extra moving parts.
