#!/bin/sh
# emby-lite 容器入口（参考 drive-tools）：
#   1. 以 root 启动时，按 PUID/PGID 修正 DATA_DIR / CACHE_DIR / BACKUP_DIR 属主（仅顶层属主不一致时）
#   2. 降权到 PUID:PGID 运行应用（当 PUID=0 且 PGID=0 时直接以 root 运行）
set -e

PUID="${PUID:-65532}"
PGID="${PGID:-65532}"
DATA_DIR="${DATA_DIR:-/data}"
CACHE_DIR="${CACHE_DIR:-/cache}"
BACKUP_DIR="${BACKUP_DIR:-/backups}"
APP=/usr/local/bin/emby-lite

if [ "$(id -u)" = "0" ] && [ "$PUID:$PGID" != "0:0" ]; then
  for dir in "$DATA_DIR" "$CACHE_DIR" "$BACKUP_DIR"; do
    [ -n "$dir" ] || continue
    mkdir -p "$dir" 2>/dev/null || true
    if [ -d "$dir" ]; then
      owner="$(stat -c '%u:%g' "$dir" 2>/dev/null || true)"
      if [ "$owner" != "$PUID:$PGID" ]; then
        echo "emby-lite: chown -R $PUID:$PGID $dir"
        chown -R "$PUID:$PGID" "$dir" 2>/dev/null || true
      fi
    fi
  done
  exec su-exec "$PUID:$PGID" "$APP" "$@"
fi

exec "$APP" "$@"
