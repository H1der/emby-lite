# emby-lite 架构设计与实施规划

> 状态：**M1–M5 全部里程碑已实现并验证**  
> 日期：2026-11-24  
> 技术栈：Go + Gin + GORM（SQLite / PostgreSQL）+ Redis（可选）+ Vue 3（TypeScript / Vite / Pinia / Tailwind CSS / Naive UI）

---

## 1. 项目定位与参考取舍

`emby-lite` 是一个**轻量、零外部强制依赖、纯直连/302 播放的 Emby API 兼容媒体服务器**。它面向个人 NAS、家庭共享与小规模多用户场景，核心目标是用一个 `CGO_ENABLED=0` 静态单二进制（内嵌 Vue 3 前端），同时提供**高兼容度的 Emby 协议服务**与**现代化 Web 管理/浏览后台**。

### 1.1 核心边界

- **播放定位**：仅做**本地文件 HTTP Range（206）直出**与 **`.strm` / 远程源 302 重定向**，不做服务端视频转码、转封装或 HLS 切片。主打 Infuse、VidHub、SenPlayer、Fileball、Yamby、AfuseKt、Emby 官方客户端等第三方播放器。
- **媒体源边界**：聚焦本地挂载目录与 `.strm` 文件，不做下载器/PT 订阅整理，但预留**直连源加速 / 302 路由扩展点（Fast Path Hook）**。
- **运行时形态**：严格保持 `CGO_ENABLED=0` 静态单二进制；不强依赖 `ffmpeg`、`ffprobe` 或 `libvips`。媒体探测优先走纯 Go 容器头解析，检测到 `ffprobe` 时可按需兜底；图片处理纯 Go 实现。
- **部署弹性**：开箱即用的单容器 SQLite 起步，无缝升级到 PostgreSQL 与可选 Redis 热缓存。

### 1.2 参考项目取舍矩阵

| 参考来源 | 借鉴的核心设计 | 主动舍弃的部分 |
| --- | --- | --- |
| **go-emby（公开仓库 + 镜像分析）** | Emby 双前缀（`/` + `/emby`）与多鉴权头兼容、游标分页与客户端回归细节、自研 `nativeprobe` 探测、增量扫描 v2、权限组与设备播放租约、虚拟库/组合库、拼贴封面、片头片尾标记、Emby SQLite 迁移、受限文件管理 `/api/files`、Fast Path 扩展点 | 商业授权码 `licensesdk`、CGO 动态链接（`libvips` + `mattn/go-sqlite3`）、小雅专属同步、内置自更新器 |
| **LMBY** | 零改名接管 Emby 目录、增量移动识别（文件指纹）、本地 NFO/图片绝对优先、`provider_cache` 持久化缓存、自动打分 + 低分人工匹配队列、**手工编辑字段锁定**、DB 持久化任务队列 + SSE 实时进度、首启初始化向导、审计日志 | 仅限纯 Web 播放、ffmpeg 硬件转码与直播电视模块 |
| **MediaStationGo** | Gin + GORM + `glebarez/sqlite`（无 CGO）+ PostgreSQL 双库适配、可选 Redis 热缓存、三档 Docker Compose（SQLite / PG / PG+Redis）部署体验、双库原生备份与恢复 | qBittorrent 下载/PT 订阅、网盘直挂（115/CD2/OpenList）、OpenSearch 重组件 |

---

## 2. 技术栈与关键依赖

| 层级 | 选型 | 说明 |
| --- | --- | --- |
| 后端语言 & Web | Go 1.24+、`gin-gonic/gin` | 高性能路由、中间件清晰，支持大小写宽容与 `/emby` 双前缀分发 |
| ORM & 数据库 | `gorm.io/gorm`、`glebarez/sqlite`（纯 Go，基于 `modernc.org/sqlite`）、`gorm.io/driver/postgres`（`pgx/v5`） | 全程 `CGO_ENABLED=0`；Emby 旧库迁移也复用纯 Go SQLite 驱动 |
| 缓存层（可选） | `redis/go-redis/v9` + 进程内 LRU/TTL（降级实现） | 统一 `CacheStore` 接口，未配置 Redis 时无缝退化为内存缓存 |
| 媒体探测 | 自研 `internal/nativeprobe`（纯 Go MP4/MKV/AVI/TS + 有界 HTTP Range）+ 可选 `ffprobe` 兜底 | 无外部依赖也可完整识别常见编码、HDR、音轨、内封字幕 |
| 图片流水线 | 标准库 `image` + `golang.org/x/image` + 纯 Go WebP 编解码 + 磁盘内容寻址缓存 | 零 CGO 依赖，支持按需缩放、ETag/304、媒体库拼贴封面 |
| 鉴权与加密 | `golang.org/x/crypto/argon2`（口令哈希）、HMAC-SHA256（图片签名 Tag / 游标签名）、AES-GCM（敏感设置加密存库） | 密钥与 API Token 只写不回显 |
| 配置与日志 | `log/slog`（结构化日志 + 文件滚动）、YAML/TOML 配置 + 环境变量覆盖 | 优先级：内置默认 < 配置文件 < 环境变量 < 管理后台动态设置 |
| 前端 | Vue 3 + TypeScript + Vite + Pinia + Vue Router + Tailwind CSS + Naive UI | 暗色影视风媒体端 + 高效后台管理端；产物通过 `go:embed` 内嵌二进制 |

---

## 3. 目录结构规划

新项目独立存放在工作区子目录 `emby-lite/` 下，现有参考目录保持只读互不干扰：

```text
emby-lite/
├── cmd/
│   └── emby-lite/
│       └── main.go                 # 入口（serve / migrate / backup / restore 子命令）
├── internal/
│   ├── config/                     # 配置加载（默认 / 文件 / 环境变量 / 动态设置）
│   ├── database/                   # GORM 初始化（SQLite/PG）、迁移执行、方言能力检测
│   ├── cache/                      # CacheStore 接口（Redis 实现 + 内存 LRU 降级）
│   ├── model/                      # GORM 实体定义（全部核心表）
│   ├── repository/                 # 数据访问层 + SQLite(FTS5) / PG(pg_trgm) 方言查询
│   ├── auth/                       # 密码哈希、Emby 多形态 Token/APIKey 解析、权限范围、设备租约
│   ├── emby/                       # Emby 协议层（DTO 组装、游标编解码、路由适配器、客户端兼容）
│   ├── playback/                   # PlaybackInfo 协商、本地 Range 直出、STRM 302、Fast Path 接口、下载开关
│   ├── scanner/                    # 增量扫描 v2（指纹、移动识别、命名解析、目录依赖、NFO/外挂字幕发现）
│   ├── nativeprobe/                # 纯 Go 容器头解析（MP4/MKV/HDR/音轨/字幕 + STRM Range）+ ffprobe 兜底
│   ├── scraper/                    # 元数据 Provider 接口、NFO 读写、TMDB 客户端、打分器、人工匹配、字段锁定
│   ├── image/                      # 图片管线（签名校验、按需缩放、磁盘缓存、媒体库拼贴封面）
│   ├── task/                       # DB 持久化任务队列、Worker 池、SSE 事件总线
│   ├── files/                      # 受限 Web 文件管理（路径越界/符号链接防御）
│   ├── migration/                  # Emby 旧 SQLite 库导入（用户/收藏/进度/媒体映射预览与执行）
│   ├── backup/                     # SQLite/PG 原生快照与可移植 JSON/ZIP 备份恢复
│   ├── audit/                      # 管理员操作审计日志
│   └── server/                     # Gin 引擎装配、中间件（CORS/限流/恢复/前缀重写）、路由注册
│       ├── emby_routes.go          # / 与 /emby/* 兼容路由
│       ├── admin_routes.go         # /api/admin/* 管理后台接口
│       ├── web_routes.go           # /api/web/* Web 客户端补充接口（初始化向导、SSE、外部播放器等）
│       └── file_routes.go          # /api/files/* 受限文件管理接口
├── migrations/                     # 版本化 SQL 迁移（common / sqlite / postgres）
├── web/                            # Vue 3 + Vite + TS 前端源码
│   ├── src/
│   │   ├── api/                    # Axios/Fetch 封装（Emby API + Admin API + SSE）
│   │   ├── stores/                 # Pinia stores（auth / library / tasks / theme / player）
│   │   ├── layouts/                # 媒体消费端布局 / 管理后台布局
│   │   ├── views/
│   │   │   ├── media/              # 首页、海报墙、剧集详情、人物、搜索、收藏、Web 直出播放器
│   │   │   └── admin/              # 控制台、库管理、虚拟库与封面、扫描/任务、刮削与人工匹配、
│   │   │                           # 元数据编辑、片头片尾、用户/权限组/设备、文件管理、备份/迁移、设置/日志
│   │   └── components/             # 海报卡、外部播放器调起器、目录选择器、元数据字段锁组件等
│   └── embed.go                    # go:embed dist/*
├── deploy/
│   ├── Dockerfile                  # 多阶段构建（Node 构建前端 → Go 静态编译 → Alpine/Distroless 运行）
│   ├── docker-compose.sqlite.yml   # 单容器 SQLite 档（零外部依赖）
│   ├── docker-compose.pg.yml       # PostgreSQL 档
│   └── docker-compose.redis.yml    # PostgreSQL + Redis 档
├── docs/
│   └── ARCHITECTURE.md             # 本文档
├── go.mod
└── Taskfile.yml / Makefile
```

---

## 4. 核心数据模型与双库（SQLite / PostgreSQL）设计

所有表主键采用稳定字符串 ID（UUIDv7 / 规范化 32 位 hex，兼容 Emby 客户端对 `ItemId`/`UserId` 的格式预期），时间字段统一存储 UTC Unix 毫秒或 RFC3339 时间戳，避免双库时区行为分歧。

### 4.1 核心表分组总览

| 分组 | 核心表 | 关键字段与职责 |
| --- | --- | --- |
| **用户与权限** | `users` | `id`, `username`, `display_name`, `password_hash`(argon2id), `is_admin`, `is_primary_admin`, `permission_group_id`, `max_devices`, `can_download`, `can_view_hidden`, `disabled`, `expires_at` |
| | `permission_groups` | `id`, `name`, `description`, `allow_playback`, `allow_download`, `max_devices`, `max_concurrent_streams`, `can_view_hidden` |
| | `group_libraries` / `user_libraries` | 组级与用户级媒体库访问白名单（用户显式规则优先于权限组） |
| | `tokens` | `id`, `token_hash`, `user_id`, `device_id`, `device_name`, `client`, `expires_at`, `last_used_at` |
| | `api_keys` | `id`, `name`, `key_hash`, `created_by`, `created_at`（仅限工具查询与标准管理兼容，不可调用 `/api/admin/*` 与个人写状态） |
| | `user_devices` | `user_id`, `device_id`, `device_name`, `client`, `ip`, `first_seen_at`, `last_seen_at`, `blocked` |
| | `user_avatars` | `user_id`, `mime`, `data`（BLOB/BYTEA）, `revision` |
| **媒体库与虚拟库** | `libraries` | `id`, `name`, `kind`（movies/tvshows/mixed）, `paths_json`（支持多根路径）, `hidden`, `sort_order`, `scan_schedule`, `cover_revision`, `status`, `last_scanned_at` |
| | `virtual_libraries` | `id`, `name`, `rule_json`（按类型/标签/库组合/最近添加等动态规则）, `sort_order`, `enabled`, `cover_revision` |
| | `library_covers` | `library_id`, `kind`（custom/collage）, `mime`, `data`, `revision`, `source_hash` |
| **媒体条目与源** | `items` | `id`, `library_id`, `parent_id`, `series_id`, `season_id`, `type`（Movie/Series/Season/Episode/Folder）, `name`, `original_name`, `sort_name`, `pinyin_initials`, `overview`, `year`, `premiere_date`, `season_num`, `episode_num`, `community_rating`, `official_rating`, `runtime_ticks`, `provider_ids_json`, `added_at`, `random_key`, `soft_deleted_at` |
| | `media_sources` | `id`, `item_id`, `source_type`（local/strm）, `path`, `strm_url`, `file_size`, `file_mtime`, `fingerprint`（移动识别）, `version_label`（支持同电影多版本如 4K/1080p）, `container`, `video_codec`, `hdr_type`, `probe_json`（音轨/内封字幕/码率）, `probed_at` |
| | `media_subtitles` | `id`, `media_source_id`, `item_id`, `stream_index`, `is_external`, `path`, `language`, `title`, `codec`（srt/ass/ssa/vtt）, `is_default`, `is_forced` |
| **元数据与人工干预** | `item_genres` / `item_tags` | 规范化多对多关联，支撑 Emby `/Genres`、`/Tags`、`/Items/Filters2` 高效筛选 |
| | `people` / `item_people` | 演职员档案（`tmdb_id`, `name`, `thumb_path`）与条目角色关联（Actor/Director/Writer） |
| | `item_metadata_locks` | `item_id`, `locked_fields_json`（字段级锁定，自动刮削/NFO 重扫不覆盖）, `override_json`, `revision` |
| | `scrape_match_queue` | `id`, `item_id`, `status`（pending/resolved/ignored）, `candidates_json`（候选与打分明细）, `best_score`, `updated_at` |
| | `provider_cache` | `provider`（tmdb）, `cache_key`, `payload_json`, `expires_at`（重刮零 API 调用） |
| | `item_images` | `id`, `item_id`, `kind`（Primary/Backdrop/Thumb/Logo/Banner）, `index_num`, `source_type`（local/remote/upload）, `source_ref`, `tag`（内容摘要）, `width`, `height` |
| **播放、进度与跳过** | `user_data` | `user_id`, `item_id`, `played`, `position_ticks`, `play_count`, `is_favorite`, `last_played_at`, `hide_from_resume` |
| | `playback_leases` | `play_session_id`, `user_id`, `device_id`, `item_id`, `media_source_id`, `position_ticks`, `is_paused`, `expires_at`（无 Redis 时落库，有 Redis 时主写 Redis） |
| | `intro_markers` | `id`, `series_id`, `season_num`, `item_id`, `intro_start_ticks`, `intro_end_ticks`, `credits_start_ticks`, `source`（manual/nfo/imported）, `confidence`, `updated_at` |
| **扫描、任务与运维** | `scan_files` | `library_id`, `path`, `dir_path`, `size`, `mtime`, `fingerprint`, `dep_hash`（同目录 NFO/图片/字幕变更感知）, `item_id`, `last_scan_run` |
| | `background_tasks` | `id`, `type`（scan/scrape/probe/cover/backup/migrate）, `status`, `priority`, `payload_json`, `progress`, `total`, `message`, `error`, `created_at`, `started_at`, `finished_at` |
| | `settings` | `key`, `value`, `encrypted`（bool）, `updated_at` |
| | `audit_logs` | `id`, `user_id`, `action`, `target_type`, `target_id`, `detail_json`, `ip`, `created_at` |

### 4.2 SQLite 与 PostgreSQL 方言策略

- **SQLite 模式（单容器起步）**：
  - 连接参数默认启用 `WAL`、`busy_timeout=5000`、`foreign_keys=ON`、`synchronous=NORMAL`；写操作通过轻量读写分离连接配置（单写连接池防 `SQLITE_BUSY`）。
  - 中文与模糊搜索：创建独立虚拟表 `items_fts`（FTS5，结合中文二元组切词 + 拼音首字母列 `pinyin_initials`），配合 `LIKE` 兜底。
- **PostgreSQL 模式（多用户高并发）**：
  - 启动时检测 UTF-8 编码并尝试启用 `pg_trgm` 扩展（若无权限则自动降级为 `ILIKE` + 索引，并记录告警）。
  - 搜索结合 `pg_trgm` 相似度 + `pinyin_initials` + 多字段权重排序。
- **Repository 方言隔离**：
  - 90% 的基础 CRUD 与简单查询复用统一 GORM 代码；
  - Emby `/Items` 复杂筛选 + 签名游标分页 + 全文/中文搜索封装在 `internal/repository/browse_dialect.go`，通过 `Dialect`（`sqlite` / `postgres`）显式分发，确保两种库查询语义一致且走对索引。

---

## 5. 缓存（可选 Redis）与异步任务架构

### 5.1 统一 `CacheStore` 分层

```text
                ┌──────────────────────────────┐
                │      CacheStore Interface    │
                │  Get / Set / Del / DelPrefix │
                │  IncrWithTTL / AcquireLease  │
                └──────────────┬───────────────┘
                               │
              ┌────────────────┴────────────────┐
              ▼                                 ▼
   [未配置 REDIS_URL]                  [已配置 REDIS_URL]
   MemoryCache (分片 LRU + TTL)        RedisCache (go-redis/v9)
   单进程零依赖，自动清理过期键          多用户热缓存 + 精确租约与限流
```

**缓存覆盖场景**：
1. 认证 Token / API Key 校验结果短缓存（权限变更或登出时主动按前缀失效）。
2. Emby 热点只读接口（`/Views`、`/Items/Latest`、`/Items/Counts`、`/Genres`）按 `user_id + library_revision` 缓存，扫库或元数据变更时递增 `library_revision` 自动失效。
3. 登录失败限流计数器（按 IP + 用户名滑动窗口）。
4. 活跃播放设备租约（`playback_leases`）与 STRM Fast Path 解析结果短缓存。

### 5.2 DB 持久化任务队列 + SSE 事件流

- 所有耗时作业（全量/增量扫库、定向刷新、TMDB 批量刮削、媒体探测、封面拼贴、备份/迁移）统一写入 `background_tasks` 表，支持**任务去重、优先级、取消、重启恢复**。
- 进程内多分组 Worker 池按任务类型限制并发（例如扫描串行/按库互斥，TMDB 刮削受速率限制，探测并发可控），不因 Redis 是否开启而改变持久化语义。
- 任务进度与系统日志通过 `/api/admin/tasks/events`（SSE）实时推送到 Vue 3 前端。

---

## 6. Emby API 兼容矩阵与路由边界

### 6.1 路由前缀与鉴权规范

- **双前缀与大小写宽容**：中间件在进入 Gin 路由前将 `/emby/...` 规范化并兼容大小写路径匹配（保留原始参数值如 `ItemId`、`Cursor`、`Tag` 大小写不变）。
- **多形态凭据解析顺序**：`X-Emby-Token` → `X-MediaBrowser-Token` → `X-Emby-Authorization` / `Authorization` 中的 `Token="..."` → `Authorization: Bearer` → `X-Emby-Api-Key` → Query 参数（`api_key` / `X-Emby-Token` / `X-Emby-Authorization`）。
- **权限一致性**：所有详情、图片、字幕、播放、下载入口均强制校验当前用户的库级可见性与权限组；普通用户不能越权指定他人 `UserId`。

### 6.2 Emby 兼容端点规划

| 模块 | 核心路由（均支持 `/` 与 `/emby` 前缀） | 关键语义 |
| --- | --- | --- |
| **系统与认证** | `GET /health`、`GET /System/Ping`、`GET /System/Info/Public`、`GET /System/Info`、`GET /System/Endpoint`、`GET /Users/Public`、`POST /Users/AuthenticateByName`、`GET /Users/Me`、`GET /Users/:userId`、`POST /Sessions/Logout` | 兼容 JSON 与 Form 登录；`/Users/Public` 返回空数组防枚举；返回稳定 `ServerId` 与协议版本 `4.8.x` |
| **库与浏览** | `GET /Users/:userId/Views`、`GET /Library/MediaFolders`、`GET /Library/VirtualFolders[/Query]`、`GET /Library/SelectableMediaFolders`、`GET /Users/:userId/Items/Root`、`GET [/Users/:userId]/Items`、`GET /Items/:itemId/Children`、`GET [/Users/:userId]/Items/:itemId`、`GET [/Users/:userId]/Items/Latest`、`GET [/Users/:userId]/Items/Resume` | 支持 `ParentId`、`Recursive`、`Ids`、`SearchTerm`、`NameStartsWith`、`IncludeItemTypes`、`SortBy/SortOrder`、`Filters`、`Genres/Tags/Years/PersonIds`、`StartIndex/Limit`、签名 `Cursor` 分页与 `Fields` 按需裁剪 |
| **剧集与发现** | `GET /Shows/:seriesId/Seasons`、`GET /Shows/:seriesId/Episodes`、`GET /Shows/NextUp`、`GET /Items/:itemId/Similar`、`GET /Movies/Recommendations`、`GET /Items/Counts`、`GET /Persons[/:id]`、`GET /Genres[/:id]`、`GET /Tags[/:id]`、`GET /Items/Filters[2]`、`GET /DisplayPreferences/:id` | 正确按季/集号排序；单集标题遵循 NFO → TMDB zh-CN → 已有名称优先级 |
| **图片与字幕** | `GET/HEAD /Items/:itemId/Images/:type[/:index]`、`GET/HEAD /Persons/:id/Images/:type`、`GET/HEAD /Genres/:id/Images/:type`、`GET/HEAD /Users/:userId/Images/Primary`、`GET/HEAD /Videos/:itemId/:sourceId/Subtitles/:index/Stream.:ext` | 服务端签名 `tag` 作为受限图片凭据（仍校验当前权限）；支持 `maxWidth`/`maxHeight` 与 `ETag/304`；外挂字幕原样输出 `srt/ass/ssa/vtt` |
| **播放与下载** | `GET/POST /Items/:itemId/PlaybackInfo`、`GET/HEAD /Videos/:itemId/stream[.:ext]`、`GET/HEAD /Videos/:itemId/original[.:ext]`、`GET/HEAD /Items/:itemId/Download` | `SupportsDirectPlay=true`、`SupportsDirectStream=true`、`SupportsTranscoding=false`；本地文件走 `http.ServeContent` Range/206；STRM 走 302（支持 Fast Path Hook）；`/Download` 默认关闭，按用户/权限组 `can_download` 放行；所有转码/HLS 路径返回 400 |
| **进度与状态** | `POST /Sessions/Playing`、`POST /Sessions/Playing/Progress`、`POST /Sessions/Playing/Stopped`、`POST /Sessions/Capabilities[/Full]`、`GET/POST /Users/:userId/Items/:itemId/UserData`、`POST/DELETE /Users/:userId/PlayedItems/:itemId`、`POST/DELETE /Users/:userId/FavoriteItems/:itemId`、`POST /Users/:userId/Items/:itemId/HideFromResume`、`DELETE /Items[/:itemId]/Resume` | Emby ticks（`1s = 10,000,000 ticks`）；旧会话/延迟停止保护；进度心跳维持设备租约；支持片头片尾标记（通过 `Chapters` / 兼容字段向客户端暴露） |
| **标准管理兼容** | `GET /Users[/Query]`、`POST /Users/New`、`POST /Users/:id/Policy`、`GET/POST /Users/:id/Configuration`、`POST /Users/:id/Password`、`DELETE /Users/:id`、`GET /Sessions`、`GET /ScheduledTasks`、`POST /Library/Refresh`、`POST /Items/:id/Refresh`、`DELETE /Items/:id` | 允许常用第三方工具/客户端调用刷新与会话查询；保护首位主管理员不被误删或降权 |

### 6.3 扩展接口边界

- `/api/web/*`：面向 Vue 3 消费端的轻量增强接口（初始化状态/向导、任务 SSE、片头片尾读取、外部播放器直链生成）。
- `/api/admin/*`：仅限管理员 Session（拒绝普通用户与 API Key），覆盖媒体库、虚拟库、封面、扫描/任务、TMDB 刮削与人工匹配、元数据编辑与字段锁、片头片尾、用户/权限组/设备、Emby 迁移、备份恢复、系统监控与审计日志。
- `/api/files/*`：仅限管理员 Session 的受限文件管理（`GET /api/files`、`rename`、`mkdir`、`move`、`upload`、`download`），严格校验配置白名单根目录与 `filepath.EvalSymlinks` 防穿越。

---

## 7. 关键子系统设计要点

1. **增量扫描 v2（零改名接管 Emby 目录）**：
   - 借鉴 `LMBY` 与 `go-emby` 的 `scan_v2`：按 `(path, size, mtime)` 快速跳过未变更文件；目录内 NFO/海报/字幕变更通过 `dep_hash` 精确触发单目录增量刷新；支持通过文件指纹识别「文件移动/重命名」而不丢失观看进度、收藏和人工锁定字段。
   - 命名解析器兼容常见 Emby/TMM/MoviePilot 目录结构（电影目录、多版本命名、剧集 `Season XX` / `S01E01` / 特别篇 `Season 00` / 合集）。
2. **原生媒体探测（`internal/nativeprobe`）**：
   - 纯 Go 解析 MKV/WebM（EBML）与 MP4/MOV（ISO BMFF）容器头，提取时长、分辨率、视频编码、HDR（HDR10/DV/HLG）、音轨语言/声道、内封字幕流。
   - 对 `.strm` 远程链接采用**有界 HTTP Range 请求**（例如仅读头部 + 尾部小块字节）探测，绝不下载整片，且可通过开关关闭或延迟到首次 `PlaybackInfo` 后台异步探测。
   - 若系统环境检测到可用 `ffprobe` 且设置允许兜底，对原生解析器不支持的冷门容器（如 AVI/MPEG-TS/ISO）自动调用 `ffprobe`。
3. **刮削与人工干预流水线**：
   - 严格优先级：**手工锁定字段 > 本地 NFO / 本地图 > TMDB 刮削缓存 > 文件名解析**。
   - TMDB 原始 JSON 响应持久化写入 `provider_cache`，重刮不重复消耗配额；支持配置自定义 TMDB API Mirror / HTTP 代理。
   - 自动匹配打分器综合标题相似度（繁简折叠 + 中英文别名）、年份、集数/时长；高于阈值自动入库，低于阈值进入 `scrape_match_queue` 供管理员在后台并列对比海报与候选详情一键选定。
4. **直连播放与 Fast Path 扩展点**：
   - `playback.Resolver` 接口允许按规则（如域名匹配、STRM 前缀）挂载自定义 302 解析/缓存加速中间件，同时默认直返 STRM 原始 URL，不绑定任何特定第三方网盘服务。
   - 实际取流前执行三层校验：库级可见性 → 账号/组播放权限 → 同时播放设备租约上限。
5. **媒体库拼贴封面与图片管线**：
   - 纯 Go 按需缩放（Lanczos/CatmullRom）并落盘内容寻址缓存目录（可随时安全清空重建）。
   - 支持为物理库与虚拟库自动选取代表海报合成拼贴封面，或管理员上传自定义封面，带版本 `revision` 避免客户端旧图缓存不刷新。
6. **Emby 旧库迁移与双库备份**：
   - 迁移模块用纯 Go SQLite 驱动只读打开旧 Emby `users.db` / `library.db`（或兼容数据库），提供「预检报告（匹配到的用户/媒体/观看记录数量）→ 确认导入」两步流程，不修改源文件。
   - 同时兼容两代库结构：Emby 4.8 及更早的 `TypedBaseItems`（文本类型名 + `TopParentId`/`SeriesId`/`SeasonId` 列），与 Emby 4.9+ 的 `MediaItems`（数字 `type` 枚举，媒体库是 `type=4` 的 CollectionFolder、目录与分类/工作室全部走 `ItemLinks2`，无父级/剧集列）。后者按 `ItemLinks2` Type 8 还原库路径、Type 2/3 还原分类与工作室，父级链推导剧集/季层级并按最长库路径前缀（缺失路径的季继承上级）归属媒体库，`LocalUsersv2.data` JSON 读取用户名、`UserDataKeys2` 关联 `UserDatas` 还原观看进度；Emby 密码为加密存储无法迁移，导入后统一使用默认密码并在预检报告中提示。
   - 备份支持 SQLite 在线备份（`.db`）、PostgreSQL `pg_dump`（有客户端时）或**通用可移植 JSON/ZIP 归档**（支持 SQLite ↔ PostgreSQL 跨库迁移）。

---

## 8. Vue 3 前端信息架构与路由规划

前端分为 **媒体消费端**（暗色影视风，类似 Emby/Infuse 体验）和 **管理控制台**（基于 Naive UI 的清晰多页签后台），通过 `go:embed` 静态嵌入后端。

### 8.1 路由结构

| 分区 | 路由 | 页面功能 |
| --- | --- | --- |
| **引导与认证** | `/setup`、`/login` | 首启管理员初始化向导（仅空库可见）、登录页 |
| **媒体消费端** | `/` | 首页（继续观看、我的媒体库/虚拟库、最近添加、下一集） |
| | `/library/:id` | 海报墙（多维筛选：类型/年份/标签/已看状态、排序、字母索引、虚拟滚动/分页） |
| | `/item/:id` | 电影/剧集详情（海报/背景、演职员、多版本源信息、季/集切换、收藏/已看、播放与外部播放器调起） |
| | `/play/:id` | Web 原生 `<video>` 直出播放器（支持浏览器原生格式 + VTT/ASS 字幕、进度心跳、片头跳过按钮、不兼容时引导调起外部播放器或复制直链） |
| | `/search`、`/favorites`、`/person/:id`、`/profile` | 全局搜索、我的收藏、人物作品页、个人资料/密码/我的设备 |
| **管理控制台** | `/admin/dashboard` | 系统概览、媒体库统计、实时任务流（SSE）、活跃播放会话监控 |
| | `/admin/libraries` | 物理媒体库 CRUD、多路径选择器、虚拟库/组合库管理、封面拼贴预览与上传 |
| | `/admin/tasks` | 扫库计划、全量/增量扫描控制、后台任务队列与失败重试 |
| | `/admin/scraper` | TMDB 配置与连接测试、刮削策略、**人工匹配队列**（候选海报并列对比） |
| | `/admin/metadata/:id` | 元数据精细编辑、**字段级锁定开关**、图片上传/切换、片头片尾时间点编辑 |
| | `/admin/users` | 用户管理、**权限组模板**、库级可见性矩阵、下载开关、历史设备与踢下线 |
| | `/admin/files` | 受限 Web 文件管理器（目录树、重命名、移动、新建目录、上传/下载） |
| | `/admin/migration` | Emby 旧 SQLite 迁移向导（上传/指定路径 → 预检 → 导入）与数据备份/恢复 |
| | `/admin/settings` | 服务名称/端口/代理、API Key 管理、图片与探测策略、缓存状态（Redis/内存）、审计日志与实时应用日志 |

---

## 9. 部署档位与配置优先级

配置加载顺序：**内置默认值 < 配置文件（`config.yaml`）< 环境变量（`DB_TYPE` / `DSN` / `PORT` / `DATA_DIR` 等无前缀命名，兼容旧 `EMBY_LITE_*` 回退）< 后台动态设置（数据库 `settings` 表）**。

| 档位 | Compose 文件 | 组件 | 适用场景 |
| --- | --- | --- | --- |
| **零依赖单二进制** | 直接运行 `./emby-lite serve` | 单文件 + 本地 SQLite | Windows / Linux / macOS 裸机部署 |
| **单容器档（默认推荐入门）** | `deploy/docker-compose.sqlite.yml` | `emby-lite`（内置 SQLite） | 个人 NAS、极低内存占用、一键启动 |
| **标准多用户档** | `deploy/docker-compose.pg.yml` | `emby-lite` + PostgreSQL 17 | 家庭/小圈子共享、大媒体库检索（`pg_trgm`） |
| **高并发缓存档** | `deploy/docker-compose.redis.yml` | `emby-lite` + PostgreSQL 17 + Redis 7 | 多客户端高频刷新、并发播放租约加速 |

---

## 10. 分阶段 Milestone 落地计划

| 阶段 | 目标 | 核心交付物与验收标准 |
| --- | --- | --- |
| **M1：工程骨架与 Emby 最小播放闭环（已完成）** | 跑通「建库 → 扫描本地/STRM → 客户端登录 → 浏览海报 → 直连/302 播放 → 进度保存」 | `emby-lite/` 工程脚手架、Gin + GORM（SQLite/PG 双库迁移）、`CacheStore`（内存 + Redis）、认证与权限基础、基础扫描器（本地视频 + `.strm` + 本地 NFO/图片）、Emby 核心 API（登录/Views/Items/详情/图片/PlaybackInfo/Range 直出 & 302/进度）、Vue 3 初始化向导 + 登录 + 简易海报墙与库管理；**已验收**：端到端测试与单元测试全部通过，`CGO_ENABLED=0` Windows（20.78MB）/ Linux amd64（20.22MB）内嵌前端单二进制编译通过 |
| **M2：增量扫描 v2、原生探测与图片管线（已完成）** | 补齐生产级扫描体验与媒体元信息 | `scan_v2` 增量与移动识别（保留 ItemID 与观看进度）、`internal/nativeprobe`（MP4/MKV + STRM 有界 HTTP Range + 可选 `ffprobe` 兜底）、纯 Go 图片按需缩放 + 内容寻址磁盘缓存 + 清理接口、DB 持久化任务队列 + SSE 实时进度推送、SQLite FTS5 / PG `pg_trgm` 搜索与 HMAC 签名游标分页、可视化目录选择器与 Vue 3 任务/缓存面板；**已验收**：6 个测试包全通过，`CGO_ENABLED=0` 静态单二进制编译通过 |
| **M3：TMDB 刮削、人工干预与元数据体系（已完成）** | 完善的刮削与人工修图/纠偏体验 | TMDB v3/v4 客户端（AES-GCM 加密凭据、镜像/代理支持、`provider_cache` 持久化缓存）、双语二元组匹配打分器 + 低分人工匹配队列（候选海报对比 + 手工搜索 TMDB）、元数据手工编辑 + 字段级锁定（扫描与重刮均严格遵守）+ 可选 NFO 回写、远程图片按需回源缓存、Emby `/Persons` 列表/详情/头像、Vue 3 刮削管理与元数据编辑弹窗；**已验收**：7 个测试包全通过，`CGO_ENABLED=0` 静态单二进制编译通过 |
| **M4：多用户权限、虚拟库、封面与片头跳过（已完成）** | 完整的多用户共享与客户端增强体验 | 权限组 CRUD 与库级白名单、用户权限编辑、设备历史封禁/踢下线与活跃会话终止、虚拟库/组合库（同步接入 Emby Views/Items 并遵守库隔离）、纯 Go 媒体库 16:9 拼贴封面自动生成与自定义封面上传、片头/片尾跳过标记（Emby `Chapters` 兼容 + Web 播放器悬浮跳过按钮）；**已验收**：7 个测试包全通过，`CGO_ENABLED=0` 静态单二进制编译通过 |
| **M5：文件管理、迁移、备份与发布打磨（已完成）** | 完整运维闭环与生产可交付物 | 受限 Web 文件管理（`/api/files/*`，防路径越界/符号链接逃逸）、纯 Go Emby 旧 SQLite 只读迁移向导（预检+导入）、SQLite 在线快照 + 跨库可移植 ZIP 备份恢复（SQLite ↔ PostgreSQL）、API Key 最小权限管理与管理员操作审计日志、多架构 `Dockerfile` + 三档 `docker-compose` 与 Vue 3 文件/运维界面；**已验收**：10 个测试包全通过，`CGO_ENABLED=0` 静态单二进制编译通过 |
