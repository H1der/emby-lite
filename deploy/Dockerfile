# syntax=docker/dockerfile:1
# emby-lite 多阶段构建：Node 构建 Vue 3 前端 → Go 静态二进制（CGO_ENABLED=0）→ Alpine 极简运行层

# ---- Stage 1: Build Vue 3 frontend ----
FROM node:22-alpine AS web-builder
WORKDIR /src/web
ARG NPM_REGISTRY=https://registry.npmmirror.com
RUN npm config set registry "${NPM_REGISTRY}" && npm install -g pnpm
COPY web/package.json web/pnpm-lock.yaml* web/pnpm-workspace.yaml* ./
RUN pnpm config set registry "${NPM_REGISTRY}" && (pnpm install --frozen-lockfile || pnpm install)
COPY web/ ./
RUN pnpm run build

# ---- Stage 2: Build static CGO_ENABLED=0 Go binary ----
FROM golang:1.27-alpine AS go-builder
WORKDIR /src
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web/embed.go ./web/embed.go
COPY --from=web-builder /src/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/emby-lite ./cmd/emby-lite

# ---- Stage 3: Minimal runtime image (no ffmpeg, no libvips, no Node) ----
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata su-exec
WORKDIR /app
COPY --from=go-builder /out/emby-lite /usr/local/bin/emby-lite
COPY deploy/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN sed -i 's/\r$//' /usr/local/bin/docker-entrypoint.sh && chmod +x /usr/local/bin/docker-entrypoint.sh
ENV TZ=Asia/Shanghai \
    PORT=8096 \
    DATA_DIR=/data \
    CACHE_DIR=/cache \
    BACKUP_DIR=/backups \
    DB_TYPE=sqlite \
    PUID=65532 \
    PGID=65532
VOLUME ["/data", "/cache", "/backups", "/media"]
EXPOSE 8096
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -qO- http://127.0.0.1:${PORT:-8096}/health || exit 1
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
