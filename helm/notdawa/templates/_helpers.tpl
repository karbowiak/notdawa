{{/* Chart/app name. */}}
{{- define "notdawa.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fully qualified app name. */}}
{{- define "notdawa.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Common labels. */}}
{{- define "notdawa.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "notdawa.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/* Selector labels. */}}
{{- define "notdawa.selectorLabels" -}}
app.kubernetes.io/name: {{ include "notdawa.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/* Resolved image reference. */}}
{{- define "notdawa.image" -}}
{{- printf "%s:%s" .Values.image.repository (.Values.image.tag | default .Chart.AppVersion) -}}
{{- end -}}

{{/* Name of the Secret carrying app credentials. */}}
{{- define "notdawa.secretName" -}}
{{- if .Values.secret.existingSecret -}}
{{- .Values.secret.existingSecret -}}
{{- else -}}
{{- printf "%s-secrets" (include "notdawa.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/* Postgres service hostname. */}}
{{- define "notdawa.postgresHost" -}}
{{- printf "%s-postgres" (include "notdawa.fullname" .) -}}
{{- end -}}

{{/*
Database environment shared by every component. When the bundled postgres is
used, the password is injected from the Secret and referenced via
$(POSTGRES_PASSWORD) so it never appears in the rendered manifest. The
assembled DSN carries pool_max_conns (pgx pool sizing) so replicas and jobs
stay within postgres max_connections.
*/}}
{{- define "notdawa.dbEnv" -}}
{{- if .Values.database.url }}
- name: DATABASE_URL
  value: {{ .Values.database.url | quote }}
{{- else }}
- name: POSTGRES_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "notdawa.secretName" . }}
      key: POSTGRES_PASSWORD
- name: DATABASE_URL
  value: "postgres://{{ .Values.postgres.auth.username }}:$(POSTGRES_PASSWORD)@{{ include "notdawa.postgresHost" . }}:5432/{{ .Values.postgres.auth.database }}?sslmode={{ .Values.database.sslmode }}&pool_max_conns={{ .Values.postgres.poolMaxConns }}"
{{- end }}
{{- end -}}

{{/*
Environment for the import/migrate/provision jobs: the database plus the
Datafordeler key and optional GSSearch token. The serve Deployment deliberately
uses notdawa.dbEnv only — `serve` never reads the import credentials, and
keeping them out of the internet-facing pods shrinks the blast radius of any
server-pod compromise.
*/}}
{{- define "notdawa.appEnv" -}}
- name: DATAFORDELER_API_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "notdawa.secretName" . }}
      key: DATAFORDELER_API_KEY
      optional: true
- name: GSEARCH_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ include "notdawa.secretName" . }}
      key: GSEARCH_TOKEN
      optional: true
{{ include "notdawa.dbEnv" . }}
{{- end -}}

{{/*
Hardened container securityContext for the app (Go binary) containers. The
image runs as uid 10001 and writes only TMPDIR, so the root filesystem can be
read-only and every capability dropped.
*/}}
{{- define "notdawa.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: ["ALL"]
{{- end -}}

{{/* wait-for-db init container (only meaningful when the bundled DB is used). */}}
{{- define "notdawa.waitForDb" -}}
{{- if and .Values.postgres.enabled (not .Values.database.url) }}
- name: wait-for-db
  image: {{ include "notdawa.image" . }}
  imagePullPolicy: {{ .Values.image.pullPolicy }}
  securityContext:
    {{- include "notdawa.containerSecurityContext" . | nindent 4 }}
  command:
    - sh
    - -c
    - 'until pg_isready -h {{ include "notdawa.postgresHost" . }} -p 5432 -U {{ .Values.postgres.auth.username }}; do echo "waiting for database..."; sleep 2; done'
{{- end }}
{{- end -}}
