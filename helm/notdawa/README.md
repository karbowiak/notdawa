# notdawa Helm chart

Deploys [notdawa](https://github.com/karbowiak/notdawa) — a self-hosted,
DAWA-compatible Danish address API — together with everything it needs:

| Component | Kind | Purpose |
|-----------|------|---------|
| `postgres` | Deployment + PVC + Service | Bundled PostgreSQL 17 + PostGIS 3.5 (disable to use your own) |
| `server` | Deployment + Service | The `notdawa serve` HTTP API |
| `migrate` | Job (Helm hook) | Runs `notdawa migrate` on install/upgrade |
| `provision` | Job (per upgrade) | Runs `notdawa provision` — imports whatever data is missing (full load on a fresh install, no-op when current) |
| `import` | CronJob | Weekly refresh |
| `ingress` | Ingress | Public exposure via ingress-nginx + cert-manager |

The chart ships **no secrets and no environment-specific hostnames** — supply
those through your own values file or a Flux `HelmRelease`.

## Quick start

```sh
helm install notdawa ./helm/notdawa \
  --namespace notdawa --create-namespace \
  --set postgres.auth.password=$(openssl rand -base64 24) \
  --set secret.datafordelerApiKey=YOUR_KEY \
  --set ingress.hosts[0]=notdawa.example.com
```

The install spawns a provision Job that performs the initial full import
(~8 GB download, a few hours). Every later upgrade spawns one too, but it only
imports what a release newly needs — usually nothing, exiting in seconds.

## Key values

| Value | Default | Notes |
|-------|---------|-------|
| `image.repository` / `image.tag` | `ghcr.io/karbowiak/notdawa` / appVersion | App image |
| `secret.existingSecret` | `""` | Use a pre-created Secret (keys: `POSTGRES_PASSWORD`, `DATAFORDELER_API_KEY`, `GSEARCH_TOKEN`) instead of rendering one |
| `postgres.enabled` | `true` | Set `false` + `database.url=...` to use an external Postgres+PostGIS |
| `postgres.auth.password` | `""` | **Required** with the bundled DB unless `secret.existingSecret` is set |
| `postgres.persistence.size` / `storageClass` | `50Gi` / `longhorn` | Data volume (kept on uninstall) |
| `server.replicas` | `2` | Stateless API replicas |
| `server.baseURL` | `https://api.dataforsyningen.dk` | Host used in generated hrefs |
| `provision.enabled` | `true` | Per-upgrade Job importing missing data (the initial load on a fresh install) |
| `import.cron.schedule` | `0 4 * * 2` | Weekly refresh (Tue 04:00, `import.cron.timeZone`) |
| `ingress.hosts` | `[notdawa.com]` | Public host(s) |
| `ingress.tls.clusterIssuer` | `letsencrypt` | cert-manager issuer for the TLS cert |
| `ingress.cacheControl` | `public, max-age=300` | `Cache-Control` added for CDN edge caching |

## Notes

- **Migrations** are baked into the image and applied by a post-install/
  post-upgrade hook, so the schema is always current before the new server
  replicas take traffic (readiness is gated on `/readyz`).
- **Data follows schema**: the provision Job tracks what has been loaded in a
  `data_provisions` ledger, so a release that adds a table (or bumps a step's
  `dataVersion` to force a re-import) populates it on deploy — asynchronously,
  without blocking the rollout.
- A full import streams ~8 GB and runs for a few hours; the import pods get a
  `tmpSize` emptyDir for scratch and generous memory.
- The Postgres PVC is annotated `helm.sh/resource-policy: keep` — `helm
  uninstall` will **not** delete your imported data.
