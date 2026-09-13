# syntax=docker/dockerfile:1.7
FROM node@sha256:9e70124bd00f47dd023e349cd587132ae61892acc0e47ed641416c3e18f401c3 AS build

ARG CANVAS_VERSION=dev
ARG CANVAS_BUILD_ID=dev
ARG CANVAS_SOURCE_URL
WORKDIR /src
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY frontend/package.json frontend/package.json
RUN --mount=type=cache,target=/root/.local/share/pnpm/store pnpm install --frozen-lockfile
COPY frontend/ frontend/
RUN VITE_CANVAS_SOURCE_URL="${CANVAS_SOURCE_URL}" VITE_CANVAS_BUILD_ID="${CANVAS_BUILD_ID}" pnpm --dir frontend run build

FROM nginx@sha256:42a516af16b852e33b7682d5ef8acbd5d13fe08fecadc7ed98605ba5e3b26ab8
ARG CANVAS_VERSION=dev
ARG CANVAS_BUILD_ID=dev
ARG CANVAS_REVISION=unknown
ARG CANVAS_SOURCE_URL
LABEL org.opencontainers.image.title="Canvas Standalone Web" \
      org.opencontainers.image.version="${CANVAS_VERSION}" \
      org.opencontainers.image.revision="${CANVAS_REVISION}" \
      org.opencontainers.image.source="${CANVAS_SOURCE_URL}" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      io.canvas.build-id="${CANVAS_BUILD_ID}"
RUN rm -rf /usr/share/nginx/html/* && \
    mkdir -p "/usr/share/nginx/html/canvas-static/${CANVAS_BUILD_ID}" /usr/share/nginx/html/studio && \
    chown -R nginx:nginx /usr/share/nginx/html
COPY --from=build --chown=nginx:nginx /src/dist/ /usr/share/nginx/html/canvas-static/${CANVAS_BUILD_ID}/
COPY --from=build --chown=nginx:nginx /src/dist/index.html /usr/share/nginx/html/studio/index.html
COPY --chown=root:root deploy/nginx/canvas.conf /etc/nginx/conf.d/default.conf
EXPOSE 8080
