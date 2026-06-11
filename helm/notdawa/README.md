# notdawa Helm chart

Deploys [notdawa](https://github.com/karbowiak/notdawa) — a self-hosted,
DAWA-compatible Danish address API — together with everything it needs:

| Component | Kind | Purpose |
|-----------|------|---------|
| `postgres` | Deployment + PVC + Service | Bundled PostgreSQL 17 + PostGIS 3.5 (disable to use your own) |
| `server` | Deployment + Service | The `notdawa serve` HTTP API |
| `migrate` | Job (Helm hook) | Runs `notdawa migrate` on install/upgrade |
| `bootstrap` | Job (opt-in) | One-time full `notdawa import` of every register |
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

Then trigger the one-time import:

```sh
helm upgrade notdawa ./helm/notdawa --reuse-values --set import.bootstrap.enabled=true
```

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
| `import.bootstrap.enabled` | `false` | One-time full import Job |
| `import.cron.schedule` | `0 4 * * 2` | Weekly refresh (Tue 04:00, `import.cron.timeZone`) |
| `ingress.hosts` | `[notdawa.com]` | Public host(s) |
| `ingress.tls.clusterIssuer` | `letsencrypt` | cert-manager issuer for the TLS cert |
| `ingress.cacheControl` | `public, max-age=300` | `Cache-Control` added for CDN edge caching |

## Notes

- **Migrations** are baked into the image and applied by a post-install/
  post-upgrade hook, so the schema is always current before the new server
  replicas take traffic (readiness is gated on `/regioner`).
- A full import streams ~8 GB and runs for a few hours; the import pods get a
  `tmpSize` emptyDir for scratch and generous memory.
- The Postgres PVC is annotated `helm.sh/resource-policy: keep` — `helm
  uninstall` will **not** delete your imported data.
