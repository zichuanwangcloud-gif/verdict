# syntax=docker/dockerfile:1

# ---- 前端 ----
FROM node:22-alpine AS web
WORKDIR /src/web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# ---- 后端 + 各平台 agent ----
FROM golang:1.25-alpine AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
ARG VERSION=0.1.0
ENV CGO_ENABLED=0
RUN set -e; mkdir -p /out/dist; \
    LDFLAGS="-s -w -X verdict/internal/protocol.Version=${VERSION}"; \
    go build -ldflags "$LDFLAGS" -o /out/verdict ./cmd/verdict; \
    for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do \
      GOOS=${target%/*} GOARCH=${target#*/} go build -ldflags "$LDFLAGS" -o /out/dist/verdict-${target%/*}-${target#*/} ./cmd/verdict; \
    done

# ---- 运行镜像 ----
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 verdict && mkdir -p /data && chown verdict /data
COPY --from=go /out/verdict /app/verdict
COPY --from=go /out/dist /app/dist
COPY --from=web /src/web/dist /app/web
USER verdict
ENV VERDICT_ADDR=:8791 \
    VERDICT_DB=/data/verdict.db \
    VERDICT_WEB_DIR=/app/web \
    VERDICT_BIN_DIR=/app/dist
VOLUME /data
EXPOSE 8791
ENTRYPOINT ["/app/verdict", "server"]
