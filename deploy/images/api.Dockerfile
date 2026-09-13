# syntax=docker/dockerfile:1.7
FROM golang@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36 AS build

ARG CANVAS_VERSION=dev
ARG CANVAS_REVISION=unknown
ARG CANVAS_BUILD_TIME=unknown
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY backend/ ./
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X github.com/dolorous01/canvas-standalone/backend/internal/buildinfo.Version=${CANVAS_VERSION} -X github.com/dolorous01/canvas-standalone/backend/internal/buildinfo.Revision=${CANVAS_REVISION} -X github.com/dolorous01/canvas-standalone/backend/internal/buildinfo.BuildTime=${CANVAS_BUILD_TIME}" \
      -o /out/canvas-api ./cmd/canvas-api && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X github.com/dolorous01/canvas-standalone/backend/internal/buildinfo.Version=${CANVAS_VERSION} -X github.com/dolorous01/canvas-standalone/backend/internal/buildinfo.Revision=${CANVAS_REVISION} -X github.com/dolorous01/canvas-standalone/backend/internal/buildinfo.BuildTime=${CANVAS_BUILD_TIME}" \
      -o /out/canvas-worker ./cmd/canvas-worker && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X github.com/dolorous01/canvas-standalone/backend/internal/buildinfo.Version=${CANVAS_VERSION} -X github.com/dolorous01/canvas-standalone/backend/internal/buildinfo.Revision=${CANVAS_REVISION} -X github.com/dolorous01/canvas-standalone/backend/internal/buildinfo.BuildTime=${CANVAS_BUILD_TIME}" \
      -o /out/canvas-migrate ./cmd/canvas-migrate && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/canvas-contract ./cmd/canvas-contract && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/canvas-healthcheck ./cmd/canvas-healthcheck

FROM scratch
ARG CANVAS_VERSION=dev
ARG CANVAS_REVISION=unknown
ARG CANVAS_SOURCE_URL
LABEL org.opencontainers.image.title="Canvas Standalone API" \
      org.opencontainers.image.version="${CANVAS_VERSION}" \
      org.opencontainers.image.revision="${CANVAS_REVISION}" \
      org.opencontainers.image.source="${CANVAS_SOURCE_URL}" \
      org.opencontainers.image.licenses="AGPL-3.0-only"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/ /usr/local/bin/
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/canvas-api"]
