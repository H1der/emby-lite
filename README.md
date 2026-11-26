# Emby-Lite

轻量、零外部强制依赖、纯直连 / 302 播放的 Emby API 兼容媒体服务器（`Go + Gin + GORM + SQLite/PostgreSQL + 可选 Redis + Vue 3`）。

## 当前实现状态（M1–M5 全部里程碑已完成）

- **静态单二进制（`CGO_ENABLED=0`）**：内置 Vue 3 + Naive UI + Tailwind CSS 前端（`go:embed`），运行期零 Node / `libvips` / 强制 `ffmpeg` 依赖。
- **双数据库 + 可选 Redis**：支持零依赖 SQLite（WAL + FTS5 全文检索，基于无 CGO `glebarez/sqlite`）与 PostgreSQL（`pg_trgm` 模糊检索），可选 Redis 热缓存（未配置时自动降级为进程内 LRU/TTL 缓存）。
- **增量扫描 v2 与原生探测**：零改名接管 Emby 目录、文件指纹移动识别（移目录保留条目 ID 与观看进度/收藏）；自研纯 Go `nativeprobe` 容器头解析（MP4/MKV/HDR/音轨/字幕 + `.strm` 有界 HTTP Range），检测到 `ffprobe` 时自动兜底。
- **TMDB 刮削与人工干预**：AES-GCM 加密凭据（只写不回显 + 连接测试 + 镜像/代理支持）、`provider_cache` 持久化缓存（重刮零 API 调用）、自动打分匹配 + 低分人工匹配队列（并列对比候选海报/手工搜 TMDB）、元数据手工编辑 + 字段级锁定（扫描与重刮均不覆盖锁定字段）与可选 NFO 回写。
- **多用户权限、虚拟库、拼贴封面与片头跳过**：权限组模板与库级白名单、用户独立权限编辑、历史设备封禁/踢下线与活跃播放会话终止；虚拟库/组合库动态规则聚合（同步呈现在 Emby Views 且严守用户库隔离）；纯 Go 媒体库 16:9 拼贴封面自动生成与自定义上传；片头/片尾跳过标记（Emby 客户端 `Chapters` 原生兼容 + Web 播放器悬浮「跳过片头」按钮）。
- **运维、迁移与文件管理**：受限 Web 文件管理器（`/api/files/*`，防路径越界与符号链接逃逸）、Emby 旧配置整库迁移向导（纯 Go 无 CGO，自动识别 `root/default` 媒体库 `options.xml`/`.mblink`、`library.db` 影视元数据（`ItemPeople2` 全部演职人员、标签、章节、媒体流/外挂字幕、BoxSet 合集，合集库可像媒体库一样浏览、筛选并查看成员）与层级、`users.db` 用户、用户设置与观看进度/收藏，支持原生完整映射 + `library.db`/`users.db` 原始库快照（`snapshot_dir`），兼容 Emby 4.8 `TypedBaseItems` 与 4.9+ `MediaItems`/`ItemLinks2` 两代库结构，支持路径前缀映射、可视化目录选择与预检报告；确认导入后投递到 DB 持久化后台任务队列执行，关闭或刷新页面不会中断，向导与「后台任务」页均可实时查看条目级进度并随时取消；写入按批事务 + 主键 upsert 合并（每批 500 条一个事务，逐条 `Save` 与存在性查询被替换为一次批量装载主键表），条目视频/图片探测在同一目录内复用 5 分钟缓存，实测 5.8 万条目库由约 2 分 30 秒降到约 35 秒）、可移植 ZIP 跨库备份恢复（支持 SQLite ↔ PostgreSQL）与 SQLite 在线快照、API Key 管理（最小权限隔离 `/api/admin/*`）与管理员操作审计日志。
- **图片管线与后台任务**：纯 Go 按需缩放（`maxWidth`/`maxHeight`）+ 本地/远程图回源内容寻址磁盘缓存 + ETag/304 + 一键清理；DB 持久化任务队列（去重/取消/重启恢复）+ SSE 实时进度推送。
- **Emby API 兼容（对齐 Emby 4.10.2.0）**：支持 `/` 与 `/emby` 双前缀及大小写/多斜杠宽容路由、全套 Emby 鉴权头与查询参数、4.10 `HomeSections` / `Sections/:section/Items` / `MediaSegments` / `Search/Hints` / `Filters2` / `Studios` / `/embywebsocket` 保活、`NameLongIdPair` 整数 ID 与严苛 DTO 类型对齐（适配 Yamby / Infuse / VidHub / SenPlayer / Fileball / AfuseKt）、签名游标分页（`Cursor` / `NextCursor` / `X-Next-Cursor`）、Items 浏览/FTS5/pg_trgm/拼音搜索、剧集季/集/NextUp、人物列表/详情/头像、图片与外挂字幕流、PlaybackInfo、本地 HTTP Range(206) 直出、`.strm` 302 重定向、按用户可控下载开关（默认关闭）、播放进度与收藏/已看状态。

完整架构设计与细节见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)。

## 快速开始

### 本地开发

```bash
# 1. 构建前端（产物自动嵌入 Go 二进制）
cd web
pnpm install
pnpm run build

# 2. 编译静态单二进制
cd ..
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/emby-lite ./cmd/emby-lite

# 3. 启动服务（默认 SQLite + 内存缓存，监听 :8096）
./bin/emby-lite
```

### Docker 部署（镜像 `hider188/emby-lite:latest`）

- 单容器 SQLite：`docker compose -f deploy/docker-compose.sqlite.yml up -d`
- PostgreSQL 档：`docker compose -f deploy/docker-compose.pg.yml up -d`
- PostgreSQL + Redis 档：`docker compose -f deploy/docker-compose.redis.yml up -d`
  - Redis 默认启用 `requirepass` 认证，启动前先 `export REDIS_PASSWORD=你的密码`（未设置时回退到 `embylite_change_me`，仅供本地试用，务必替换）

## 常用环境变量

> 所有核心变量均使用简洁无前缀命名（兼容旧版 `EMBY_LITE_*` 回退）。

| 变量 | 说明 | 默认值 |
| --- | --- | --- |
| `PORT` / `LISTEN` | 监听端口或地址（如 `8096` 或 `:8096`） | `8096` |
| `SERVER_NAME` | 服务器显示名称 | `Emby-Lite` |
| `DB_TYPE` | 数据库类型（`sqlite` / `postgres`） | `sqlite` |
| `DSN` | PostgreSQL DSN，或 SQLite 文件路径（相对文件名自动落于 `DATA_DIR` 下） | 空（`/data/emby-lite.db`） |
| `SQLITE_PATH` | 显式指定 SQLite 数据库文件路径 | `./data/emby-lite.db` |
| `DATA_DIR` / `CACHE_DIR` / `BACKUP_DIR` | 数据、图片缓存、备份归档目录 | `./data` / `./cache` / `./backups`（容器内 `/data` 等） |
| `REDIS_URL` | 可选 Redis 地址（如 `redis://redis:6379/0`） | 空（内存缓存） |
| `REDIS_PASSWORD` | 可选 Redis 密码（`REDIS_URL` 中已含密码时以 URL 为准） | 空 |
| `REDIS_DB` / `REDIS_PREFIX` | 可选 Redis 库号与 key 前缀（多实例共用同一 Redis 时靠前缀隔离） | `0` / `embylite:` |
| `EMBY_METADATA_DIR` | 可选：挂载原 Emby 的 `metadata` 或 `metadata/people` 目录以零拷贝共享演员头像 | 空 |
| `EMBY_METADATA_WRITEBACK` | 目录可写时是否将 TMDB 新获取的演员头像按 Emby 规范补写落盘（只补缺、不覆盖） | `true` |
| `PUID` / `PGID` | Docker 容器运行用户 UID/GID（设 `0` 则以 root 运行） | `65532` / `65532` |
| `ADMIN_USER` / `ADMIN_PASSWORD` | 空库首启自动创建管理员（可选，否则走 Web 向导） | `admin` / 空 |
