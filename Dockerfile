# syntax=docker/dockerfile:1

# ---- build stage ---------------------------------------------------------
# Cross-compile from the build host's native arch (fast, no qemu) to the
# requested target. pgx + huma are pure Go, so CGO can stay off.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
WORKDIR /src

# Cache module downloads on their own layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /out/notdawa ./cmd/notdawa

# ---- runtime stage -------------------------------------------------------
FROM alpine:3
# ca-certificates: HTTPS to Datafordeler Fildownload during `import`.
# tzdata:          Europe/Copenhagen conversions.
# postgresql-client: pg_isready, used by the chart's wait-for-db init containers.
RUN apk add --no-cache ca-certificates tzdata postgresql-client \
    && adduser -D -u 10001 notdawa
WORKDIR /app
# `notdawa migrate` reads SQL from ./migrations relative to the working dir.
COPY --from=build /src/migrations ./migrations
COPY --from=build /out/notdawa /usr/local/bin/notdawa
USER notdawa
EXPOSE 8080
ENTRYPOINT ["notdawa"]
CMD ["serve", "--addr", ":8080"]
