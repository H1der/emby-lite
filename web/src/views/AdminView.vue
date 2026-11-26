<template>
  <div class="space-y-6">
    <!-- Frosted Glass Admin Header -->
    <div class="glass-panel rounded-3xl p-4 sm:p-5 flex flex-wrap items-center justify-between gap-4">
      <div>
        <h1 class="text-xl sm:text-2xl font-bold text-ink tracking-tight">管理控制台</h1>
        <p class="text-xs text-ink-muted mt-0.5">媒体库调度、并发扫描引擎、权限治理与系统运维</p>
      </div>
      <div class="flex items-center gap-2">
        <n-button size="small" secondary @click="refreshActiveTab">刷新当前页</n-button>
        <n-button size="small" type="primary" @click="scanAll">⚡ 扫描全部媒体库</n-button>
      </div>
    </div>

    <!-- Responsive Glass Pill Navigation (horizontal scroll on mobile, clean bar on desktop) -->
    <div class="flex items-center gap-1.5 overflow-x-auto pb-1">
      <button
        v-for="tab in tabs"
        :key="tab.key"
        type="button"
        class="px-3.5 py-2 rounded-2xl text-xs sm:text-sm font-semibold whitespace-nowrap transition flex items-center gap-1.5 shrink-0"
        :class="
          activeTab === tab.key
            ? 'accent-gradient text-white shadow-md'
            : 'glass-pill text-ink-secondary hover:text-sky-600 dark:hover:text-sky-400'
        "
        @click="selectTab(tab.key)"
      >
        <span>{{ tab.icon }}</span>
        <span>{{ tab.label }}</span>
        <span
          v-if="tab.key === 'scraper' && matchQueue.length > 0"
          class="px-1.5 py-0.2 rounded-full text-[10px] bg-amber-400 text-slate-950 font-bold"
        >
          {{ matchQueue.length }}
        </span>
        <span
          v-if="tab.key === 'tasks' && runningTaskCount > 0"
          class="w-2 h-2 rounded-full bg-emerald-400 animate-pulse"
        ></span>
      </button>
    </div>

    <!-- Tab: Dashboard -->
    <div v-if="activeTab === 'dashboard'" class="space-y-5">
      <div class="grid grid-cols-2 lg:grid-cols-4 gap-4">
        <div class="glass-card rounded-2xl p-4 sm:p-5 space-y-1">
          <div class="text-xs text-ink-muted font-medium">媒体库总数</div>
          <div class="text-2xl sm:text-3xl font-bold text-ink">{{ dash.libraries ?? 0 }}</div>
        </div>
        <div class="glass-card rounded-2xl p-4 sm:p-5 space-y-1">
          <div class="text-xs text-ink-muted font-medium">电影条目</div>
          <div class="text-2xl sm:text-3xl font-bold text-sky-600 dark:text-sky-400">{{ dash.movies ?? 0 }}</div>
        </div>
        <div class="glass-card rounded-2xl p-4 sm:p-5 space-y-1">
          <div class="text-xs text-ink-muted font-medium">剧集 / 单集</div>
          <div class="text-2xl sm:text-3xl font-bold text-indigo-600 dark:text-indigo-400">
            {{ dash.series ?? 0 }} <span class="text-base font-medium text-ink-muted">/ {{ dash.episodes ?? 0 }}</span>
          </div>
        </div>
        <div class="glass-card rounded-2xl p-4 sm:p-5 space-y-1">
          <div class="text-xs text-ink-muted font-medium">用户 / 活跃播放</div>
          <div class="text-2xl sm:text-3xl font-bold text-emerald-600 dark:text-emerald-400">
            {{ dash.users ?? 0 }} <span class="text-base font-medium text-ink-muted">/ {{ dash.active_streams ?? 0 }}</span>
          </div>
        </div>
      </div>

      <n-card size="small" title="运行环境与高性能引擎能力">
        <div class="text-sm text-ink-secondary space-y-2.5">
          <div>
            服务器名称：<strong class="text-ink">{{ dash.server_name }}</strong>
            <span class="text-xs text-ink-muted ml-2 font-mono">ID: {{ dash.server_id }}</span>
          </div>
          <div class="flex flex-wrap items-center gap-2">
            <span>主数据库：<strong class="text-ink uppercase">{{ dash.database }}</strong></span>
            <n-tag v-if="dash.database === 'sqlite'" size="tiny" type="info">WAL 读写分离池</n-tag>
            <n-tag v-if="dash.has_fts5" size="tiny" type="success">FTS5 全文检索</n-tag>
            <n-tag v-if="dash.has_trgm" size="tiny" type="success">pg_trgm 模糊搜索</n-tag>
            <span class="text-ink-muted">·</span>
            <span>热缓存后端：<strong class="text-ink uppercase">{{ dash.cache }}</strong></span>
            <span class="text-ink-muted">·</span>
            <span>扫描并发：<strong class="text-ink">{{ dash.scan_concurrency ?? 4 }} 线程</strong></span>
          </div>
          <div class="flex flex-wrap items-center gap-2">
            <span>媒体探测：<strong class="text-ink">纯 Go 原生容器头并发快探 (MP4/MKV/Range)</strong></span>
            <n-tag size="tiny" :type="dash.ffprobe_available ? 'info' : 'default'">
              {{ dash.ffprobe_available ? '异步 ffprobe 兜底可用' : '纯静态零外部依赖' }}
            </n-tag>
          </div>
          <div class="flex flex-wrap items-center gap-3 pt-1">
            <span>
              图片缩略图磁盘缓存：<strong class="text-ink">{{ dash.image_cache_files ?? 0 }}</strong> 个文件（{{ formatBytes(dash.image_cache_bytes || 0) }}）
            </span>
            <n-button size="tiny" secondary type="warning" @click="clearCache">清理图片与热缓存</n-button>
          </div>
        </div>
      </n-card>
    </div>

    <!-- Tab: Libraries -->
    <div v-if="activeTab === 'libraries'" class="space-y-4">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <span class="text-xs text-ink-muted">拖动媒体库卡片或点击上下箭头可调整全局默认排序（同时生效于 Web 端与 Emby 客户端）</span>
        <n-button type="primary" size="small" @click="openCreateLibrary">+ 新建媒体库</n-button>
      </div>
      <div v-if="libraries.length === 0" class="glass-panel rounded-2xl p-10 text-center text-sm text-ink-muted">
        暂无媒体库，点击右上角创建。
      </div>
      <div v-else class="space-y-3">
        <div
          v-for="(lib, idx) in libraries"
          :key="lib.id"
          draggable="true"
          class="rounded-2xl transition border-2"
          :class="libDragOverIdx === idx ? 'border-sky-400/70' : 'border-transparent'"
          @dragstart="onLibDragStart(idx)"
          @dragover.prevent="libDragOverIdx = idx"
          @dragleave="onLibDragLeave(idx)"
          @drop.prevent="onLibDrop(idx)"
          @dragend="onLibDragEnd"
        >
          <n-card size="small">
            <div class="flex flex-wrap items-center justify-between gap-3">
              <div class="flex items-start gap-2.5 min-w-0">
                <div class="flex flex-col items-center gap-0.5 pt-0.5 shrink-0">
                  <span class="text-ink-muted select-none cursor-grab active:cursor-grabbing text-sm leading-none" title="拖动排序">⠿</span>
                  <div class="flex items-center gap-0.5 mt-1">
                    <n-button size="tiny" quaternary :disabled="idx === 0 || reorderingLibs" title="上移" @click="moveLibrary(idx, -1)">↑</n-button>
                    <n-button size="tiny" quaternary :disabled="idx === libraries.length - 1 || reorderingLibs" title="下移" @click="moveLibrary(idx, 1)">↓</n-button>
                  </div>
                </div>
                <div class="min-w-0">
                  <div class="font-semibold text-ink flex items-center flex-wrap gap-1.5">
                    <span>{{ lib.name }}</span>
                    <n-tag size="tiny" type="info">{{ lib.kind }}</n-tag>
                    <n-tag v-if="lib.auto_merge_versions && lib.kind !== 'boxsets'" size="tiny" type="default">多版本合并</n-tag>
                    <n-tag v-if="lib.auto_group_series && (lib.kind === 'tvshows' || lib.kind === 'mixed')" size="tiny" type="default">跨目录剧集合并</n-tag>
                    <n-tag v-if="lib.hidden" size="tiny" type="warning">隐藏</n-tag>
                    <n-tag v-if="lib.show_in_my_media === false" size="tiny" type="warning">不显示:我的媒体库</n-tag>
                    <n-tag v-if="lib.show_in_resume === false" size="tiny" type="warning">不显示:继续观看</n-tag>
                    <n-tag v-if="lib.show_in_next_up === false" size="tiny" type="warning">不显示:正在观看</n-tag>
                    <n-tag v-if="lib.show_in_latest === false" size="tiny" type="warning">不显示:最近添加</n-tag>
                    <n-tag v-if="lib.scanning" size="tiny" type="success">扫描中</n-tag>
                  </div>
                  <div class="text-xs text-ink-muted mt-1 break-all">
                    路径：{{ (lib.paths || []).join(' ; ') }} · {{ lib.item_count }} 项 · 状态：{{ lib.scanning ? '扫描中' : lib.status }}
                  </div>
                </div>
              </div>
              <div class="flex flex-wrap items-center gap-1.5">
                <n-button size="small" secondary @click="generateLibCover(lib.id)">拼贴封面</n-button>
                <n-button size="small" secondary @click="triggerCoverUpload(lib.id)">传封面</n-button>
                <n-button size="small" secondary @click="openEditLibrary(lib)">编辑</n-button>
                <n-button v-if="lib.kind !== 'boxsets'" size="small" secondary @click="consolidateLibrary(lib.id)">合并整理</n-button>
                <n-button size="small" secondary @click="probeLibrary(lib.id)">探测</n-button>
                <n-button size="small" type="info" secondary @click="scrapeLibrary(lib.id)">刮削</n-button>
                <n-button size="small" type="primary" @click="scanLibrary(lib.id)">扫描</n-button>
                <n-button size="small" type="error" quaternary @click="deleteLibrary(lib.id)">删除</n-button>
              </div>
            </div>
          </n-card>
        </div>
      </div>
    </div>

    <!-- Tab: Virtual Libraries & Intro Skip -->
    <div v-if="activeTab === 'virtual'">
      <AdminVirtualAndIntro :libraries="libraries" />
    </div>

    <!-- Tab: TMDB & Match Queue -->
    <div v-if="activeTab === 'scraper'" class="grid grid-cols-1 lg:grid-cols-2 gap-6">
      <n-card size="small" title="TMDB 刮削配置（凭据加密存储，只写不回显）">
        <n-form label-placement="top" size="small" @submit.prevent="saveTMDB">
          <n-form-item>
            <template #label>
              <span>TMDB v3 API Key</span>
              <n-tag v-if="tmdbStatus.has_api_key" size="tiny" type="success" class="ml-2">已加密保存</n-tag>
            </template>
            <n-input v-model:value="tmdbForm.api_key" type="password" show-password-on="click" placeholder="留空表示保持现有密钥不变" />
          </n-form-item>
          <n-form-item>
            <template #label>
              <span>TMDB v4 Read Access Token（与上方二选一）</span>
              <n-tag v-if="tmdbStatus.has_read_token" size="tiny" type="success" class="ml-2">已加密保存</n-tag>
            </template>
            <n-input v-model:value="tmdbForm.read_token" type="password" show-password-on="click" placeholder="留空表示保持现有 Token 不变" />
          </n-form-item>
          <div class="grid grid-cols-2 gap-3">
            <n-form-item label="API Base URL（可填镜像）">
              <n-input v-model:value="tmdbForm.api_base_url" />
            </n-form-item>
            <n-form-item label="图片 Base URL">
              <n-input v-model:value="tmdbForm.image_base_url" />
            </n-form-item>
            <n-form-item label="语言">
              <n-input v-model:value="tmdbForm.language" placeholder="zh-CN" />
            </n-form-item>
            <n-form-item label="自动匹配分数阈值 (0-100)">
              <n-input-number v-model:value="tmdbForm.auto_threshold" :min="10" :max="100" class="w-full" />
            </n-form-item>
          </div>
          <n-form-item>
            <template #label>
              <span>HTTP / SOCKS 代理（可选）</span>
              <n-tag v-if="tmdbStatus.has_proxy" size="tiny" type="info" class="ml-2">已配置</n-tag>
            </template>
            <n-input v-model:value="tmdbForm.proxy_url" placeholder="http://127.0.0.1:7890（填 __clear__ 可清除）" />
          </n-form-item>
          <n-form-item>
            <n-checkbox v-model:checked="tmdbForm.write_nfo">刮削成功后自动回写本地 NFO</n-checkbox>
          </n-form-item>
          <div class="flex items-center gap-2">
            <n-button type="primary" attr-type="submit">保存配置</n-button>
            <n-button secondary :loading="testingTMDB" @click="testTMDB">测试连接</n-button>
          </div>
        </n-form>
      </n-card>

      <n-card size="small" title="待人工匹配队列（低置信度候选并列对比）">
        <template #header-extra>
          <n-button size="tiny" secondary @click="loadMatchQueue">刷新</n-button>
        </template>
        <div v-if="matchQueue.length === 0" class="py-10 text-center text-sm text-ink-muted">
          当前没有待人工确认的刮削条目
        </div>
        <div v-else class="space-y-4 max-h-[520px] overflow-y-auto pr-1">
          <div
            v-for="entry in matchQueue"
            :key="entry.item_id"
            class="rounded-2xl neu-inset p-3.5 space-y-3"
          >
            <div class="flex items-center justify-between gap-2">
              <div>
                <div class="text-sm font-semibold text-ink">
                  {{ entry.item_name }}
                  <span v-if="entry.item_year" class="text-ink-muted font-normal">({{ entry.item_year }})</span>
                </div>
                <div class="text-xs text-ink-muted">
                  类型：{{ entry.item_type }} · 最高匹配分：{{ entry.best_score }}
                </div>
              </div>
              <div class="flex items-center gap-1.5">
                <n-button size="tiny" secondary @click="openManualSearch(entry)">手动搜 TMDB</n-button>
                <n-button size="tiny" type="warning" quaternary @click="ignoreMatch(entry.item_id)">忽略</n-button>
              </div>
            </div>

            <div v-if="entry.candidates && entry.candidates.length > 0" class="grid grid-cols-1 sm:grid-cols-2 gap-2">
              <div
                v-for="c in entry.candidates.slice(0, 4)"
                :key="c.provider_id"
                class="flex gap-2.5 p-2 rounded-xl glass-card"
              >
                <img
                  v-if="c.poster_url"
                  :src="c.poster_url"
                  class="w-12 h-18 object-cover rounded-lg shrink-0 bg-slate-200 dark:bg-slate-800"
                />
                <div class="min-w-0 flex-1 space-y-1">
                  <div class="text-xs font-semibold text-ink truncate">{{ c.title }} ({{ c.year || '?' }})</div>
                  <div class="text-[11px] text-sky-600 dark:text-sky-400 font-medium">匹配分 {{ c.score }} · ★ {{ c.rating }}</div>
                  <div class="text-[10px] text-ink-muted truncate">{{ (c.reasons || []).join(' / ') }}</div>
                  <n-button size="tiny" type="primary" secondary @click="resolveMatch(entry.item_id, c.media_type, c.provider_id)">
                    选用此匹配
                  </n-button>
                </div>
              </div>
            </div>
            <div v-else class="text-xs text-ink-muted">无自动候选，请点击“手动搜 TMDB”定位。</div>
          </div>
        </div>
      </n-card>
    </div>

    <!-- Tab: Tasks -->
    <div v-if="activeTab === 'tasks'" class="space-y-3">
      <div class="flex items-center justify-between">
        <div class="text-xs text-ink-muted flex items-center gap-2 glass-pill px-3 py-1.5 rounded-full">
          <span class="inline-block w-2 h-2 rounded-full" :class="sseConnected ? 'bg-emerald-500' : 'bg-slate-400'"></span>
          <span>{{ sseConnected ? 'SSE 实时推送已连接' : 'SSE 未连接' }}</span>
        </div>
        <n-button size="small" secondary @click="loadTasks">刷新任务列表</n-button>
      </div>
      <div v-if="tasks.length === 0" class="glass-panel rounded-2xl text-sm text-ink-muted py-10 text-center">
        暂无后台任务记录
      </div>
      <div v-else class="space-y-2.5">
        <n-card v-for="t in tasks" :key="t.id" size="small">
          <div class="space-y-2">
            <div class="flex flex-wrap items-center justify-between gap-2">
              <div class="flex items-center gap-2 flex-wrap min-w-0">
                <n-tag size="tiny" :type="taskStatusType(t.status)">{{ taskStatusLabel(t.status) }}</n-tag>
                <span class="text-sm font-semibold text-ink">{{ taskTypeLabel(t.type) }}</span>
                <n-tag v-if="taskLibraryName(t)" size="tiny" type="info">{{ taskLibraryName(t) }}</n-tag>
                <span class="text-xs text-ink-secondary">{{ formatTaskMessage(t) }}</span>
              </div>
              <div class="flex items-center gap-2 flex-wrap shrink-0">
                <n-tag v-if="taskDurationLabel(t)" size="tiny" type="default">
                  耗时 {{ taskDurationLabel(t) }}
                </n-tag>
                <span class="text-xs text-ink-muted">
                  {{ t.finished_at ? `完成于 ${formatTime(t.finished_at)}` : `创建于 ${formatTime(t.created_at)}` }}
                </span>
                <n-button
                  v-if="t.status === 'pending' || t.status === 'running'"
                  size="tiny"
                  type="error"
                  secondary
                  @click="cancelTask(t.id)"
                >
                  取消
                </n-button>
              </div>
            </div>
            <n-progress
              v-if="t.total > 0"
              type="line"
              :percentage="Math.min(100, Math.round((t.progress / t.total) * 100))"
              :status="t.status === 'failed' ? 'error' : t.status === 'completed' ? 'success' : 'default'"
              :height="6"
            />
            <div v-if="t.error" class="text-xs text-rose-500">错误：{{ t.error }}</div>
          </div>
        </n-card>
      </div>
    </div>

    <!-- Tab: Users -->
    <div v-if="activeTab === 'users'" class="space-y-4">
      <div class="flex justify-end">
        <n-button type="primary" size="small" @click="openCreateUser">+ 新建用户</n-button>
      </div>
      <div class="space-y-3">
        <n-card v-for="u in users" :key="u.id" size="small">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <div>
              <div class="font-semibold text-ink flex items-center gap-1.5 flex-wrap">
                <span>{{ u.username }}</span>
                <n-tag v-if="u.is_admin" size="tiny" type="success">管理员</n-tag>
                <n-tag v-if="u.can_download" size="tiny" type="info">允许下载</n-tag>
                <n-tag v-if="u.disabled" size="tiny" type="error">已禁用</n-tag>
              </div>
              <div class="text-xs text-ink-muted mt-1">
                可见库：{{ u.is_admin ? '全部' : (u.library_ids?.length ? `${u.library_ids.length} 个指定库` : '默认非隐藏库') }}
              </div>
            </div>
            <div class="flex items-center gap-2">
              <n-button size="small" secondary @click="openUserHomeSettings(u)">
                主屏幕设置
              </n-button>
              <n-button v-if="!u.is_primary_admin" size="small" secondary @click="openEditUser(u)">
                编辑权限
              </n-button>
              <n-button v-if="!u.is_primary_admin" size="small" secondary @click="toggleDownload(u)">
                {{ u.can_download ? '关闭下载' : '开启下载' }}
              </n-button>
              <n-button v-if="!u.is_primary_admin" size="small" type="error" quaternary @click="deleteUser(u.id)">
                删除
              </n-button>
            </div>
          </div>
        </n-card>
      </div>

      <AdminAccessPanel :libraries="libraries" @changed="loadUsersTab" />
    </div>

    <!-- Tab: Files -->
    <div v-if="activeTab === 'files'">
      <AdminFilesPanel />
    </div>

    <!-- Tab: Ops -->
    <div v-if="activeTab === 'ops'">
      <AdminOpsPanel />
    </div>

    <!-- Tab: Settings -->
    <div v-if="activeTab === 'settings'">
      <n-card size="small" class="max-w-xl" title="全局基础配置">
        <n-form label-placement="top" @submit.prevent="saveSettings">
          <n-form-item label="服务器显示名称">
            <n-input v-model:value="settingsForm.server_name" />
          </n-form-item>
          <n-form-item label="Emby 元数据目录（共享/复用 metadata/people 演员头像）">
            <div class="w-full space-y-2">
              <div class="flex items-center gap-2">
                <n-button size="small" type="primary" secondary @click="showMetaDirPicker = true">
                  📁 选择目录
                </n-button>
                <n-button v-if="settingsForm.emby_metadata_dir" size="small" quaternary @click="settingsForm.emby_metadata_dir = ''">
                  清除
                </n-button>
                <n-tag v-if="settingsForm.emby_metadata_dir && settingsStatus.exists" size="tiny" :type="settingsStatus.writable ? 'success' : 'info'">
                  {{ settingsStatus.writable ? '可读写 (:rw)' : '仅只读 (:ro)' }}
                </n-tag>
                <n-tag v-else-if="settingsForm.emby_metadata_dir && !settingsStatus.exists" size="tiny" type="warning">
                  目录不存在
                </n-tag>
              </div>
              <div class="px-3 py-2 rounded-xl neu-inset text-xs font-mono text-ink-secondary truncate">
                {{ settingsForm.emby_metadata_dir || '未配置（可挂载原 Emby 的 metadata 或 metadata/people 目录）' }}
              </div>
            </div>
          </n-form-item>
          <n-form-item>
            <n-checkbox v-model:checked="settingsForm.emby_metadata_writeback">
              目录可写时，将 TMDB 新获取的演员头像按 Emby 规范补写到 metadata/people（只补缺、绝不覆盖原文件）
            </n-checkbox>
          </n-form-item>
          <n-button type="primary" attr-type="submit">保存设置</n-button>
        </n-form>
      </n-card>
    </div>

    <!-- Manual TMDB Search Modal -->
    <n-modal v-model:show="showManualSearchModal" preset="card" :title="`手动搜索 TMDB：${manualSearchTarget?.item_name || ''}`" class="max-w-xl">
      <div class="space-y-3">
        <div class="flex gap-2">
          <n-input v-model:value="manualQuery" placeholder="输入影视名称..." @keyup.enter="runManualSearch" />
          <n-input-number v-model:value="manualYear" placeholder="年份" :min="0" :max="2100" class="w-28" />
          <n-button type="primary" :loading="searchingManual" @click="runManualSearch">搜索</n-button>
        </div>
        <div v-if="manualResults.length === 0" class="text-xs text-ink-muted py-6 text-center">输入关键词搜索 TMDB 候选条目</div>
        <div v-else class="space-y-2 max-h-80 overflow-y-auto">
          <div
            v-for="c in manualResults"
            :key="c.provider_id"
            class="flex items-center justify-between gap-3 p-3 rounded-xl neu-inset"
          >
            <div class="min-w-0">
              <div class="text-sm font-semibold text-ink truncate">{{ c.title }} ({{ c.year || '?' }})</div>
              <div class="text-xs text-ink-muted truncate">{{ c.original_title }} · TMDB #{{ c.provider_id }}</div>
            </div>
            <n-button
              size="small"
              type="primary"
              @click="resolveMatch(manualSearchTarget.item_id, c.media_type, c.provider_id)"
            >
              应用
            </n-button>
          </div>
        </div>
      </div>
    </n-modal>

    <!-- Create / Edit Library Modal -->
    <n-modal v-model:show="showLibModal" preset="card" :title="editingLibId ? '编辑媒体库' : '新建媒体库'" class="max-w-lg">
      <n-form @submit.prevent="saveLibrary">
        <n-form-item label="媒体库名称">
          <n-input v-model:value="libForm.name" placeholder="例如：电影 / 动漫" />
        </n-form-item>
        <n-form-item label="类型">
          <n-select v-model:value="libForm.kind" :options="kindOptions" />
        </n-form-item>
        <n-form-item label="媒体目录">
          <div class="w-full space-y-2.5">
            <div class="flex items-center justify-between gap-2">
              <n-button type="primary" secondary size="small" @click="showDirPicker = true">
                📁 浏览并选择目录
              </n-button>
              <span class="text-xs text-ink-muted">支持添加多个根目录</span>
            </div>

            <div
              v-if="libForm.paths.length === 0"
              class="rounded-xl border border-dashed border-slate-300 dark:border-white/20 p-3 text-xs text-ink-muted text-center"
            >
              暂未选择目录，请点击上方“浏览并选择目录”
            </div>
            <div v-else class="space-y-1.5">
              <div
                v-for="(p, idx) in libForm.paths"
                :key="p"
                class="flex items-center justify-between gap-2 px-3 py-1.5 rounded-xl neu-inset text-xs"
              >
                <span class="font-mono text-ink truncate" :title="p">{{ p }}</span>
                <n-button size="tiny" type="error" quaternary @click="removeLibPath(idx)">移除</n-button>
              </div>
            </div>
          </div>
        </n-form-item>
        <n-form-item v-if="libForm.kind !== 'boxsets'" label="自动组合">
          <div class="w-full space-y-2.5">
            <div>
              <n-checkbox v-model:checked="libForm.auto_merge_versions">自动合并多版本影片与单集</n-checkbox>
              <div class="text-[11px] text-ink-muted pl-6 mt-0.5">
                同目录多版本命名（如 4K / 1080p）或相同 TMDB/IMDb ID 的影片与同集多版本将自动合并为一个条目。
              </div>
            </div>
            <div v-if="libForm.kind === 'tvshows' || libForm.kind === 'mixed'">
              <n-checkbox v-model:checked="libForm.auto_group_series">自动合并分布在多个文件夹中的剧集</n-checkbox>
              <div class="text-[11px] text-ink-muted pl-6 mt-0.5">
                如果启用，分布在此媒体库中多个文件夹中的剧集（含不同季目录）将自动合并为一部。
              </div>
            </div>
          </div>
        </n-form-item>
        <n-form-item label="首页展示范围（全局默认）">
          <div class="w-full space-y-1.5">
            <div class="grid grid-cols-2 gap-2">
              <n-checkbox v-model:checked="libForm.show_in_my_media">在「我的媒体库」中展示</n-checkbox>
              <n-checkbox v-model:checked="libForm.show_in_resume">在「继续观看」中展示</n-checkbox>
              <n-checkbox v-model:checked="libForm.show_in_next_up">在「正在观看」中展示</n-checkbox>
              <n-checkbox v-model:checked="libForm.show_in_latest">在「最近添加」中展示</n-checkbox>
            </div>
            <div class="text-[11px] text-ink-muted">
              关闭后该媒体库将不会在对应首页板块出现；用户也可以在各自的主屏幕设置中单独隐藏。
            </div>
          </div>
        </n-form-item>
        <n-form-item label="可见性">
          <n-checkbox v-model:checked="libForm.hidden">设为隐藏媒体库（仅管理员或授权用户可见）</n-checkbox>
        </n-form-item>
        <n-button type="primary" block attr-type="submit">{{ editingLibId ? '保存修改' : '创建媒体库' }}</n-button>
      </n-form>
    </n-modal>

    <DirectoryPicker v-model:show="showDirPicker" :selected-paths="libForm.paths" @select="addLibPath" />
    <DirectoryPicker
      v-model:show="showMetaDirPicker"
      :selected-paths="settingsForm.emby_metadata_dir ? [settingsForm.emby_metadata_dir] : []"
      @select="selectMetaDir"
    />

    <!-- Create / Edit User Modal -->
    <n-modal v-model:show="showUserModal" preset="card" :title="editingUserId ? '编辑用户权限' : '新建用户'" class="max-w-md">
      <n-form @submit.prevent="saveUser">
        <n-form-item label="用户名">
          <n-input v-model:value="userForm.username" :disabled="Boolean(editingUserId)" />
        </n-form-item>
        <n-form-item :label="editingUserId ? '重置密码（留空不修改）' : '密码'">
          <n-input v-model:value="userForm.password" type="password" show-password-on="click" />
        </n-form-item>
        <n-form-item label="所属权限组">
          <n-select v-model:value="userForm.permission_group_id" clearable :options="groupOptions" placeholder="默认策略" />
        </n-form-item>
        <n-form-item label="用户独立可见库白名单（留空继承权限组）">
          <n-select v-model:value="userForm.library_ids" multiple :options="libraryOptions" />
        </n-form-item>
        <n-form-item label="权限开关">
          <div class="flex flex-wrap gap-4">
            <n-checkbox v-model:checked="userForm.is_admin">管理员</n-checkbox>
            <n-checkbox v-model:checked="userForm.can_download">允许媒体下载</n-checkbox>
            <n-checkbox v-if="editingUserId" v-model:checked="userForm.disabled">禁用账号</n-checkbox>
          </div>
        </n-form-item>
        <n-button type="primary" block attr-type="submit">{{ editingUserId ? '保存修改' : '创建用户' }}</n-button>
      </n-form>
    </n-modal>

    <HomeScreenSettingsModal
      v-model:show="showUserHomeSettings"
      :user-id="homeSettingsUser?.id"
      :username="homeSettingsUser?.username"
    />

    <input ref="coverFileInput" type="file" accept="image/*" class="hidden" @change="onCoverFileSelected" />
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  NButton, NCard, NCheckbox, NForm, NFormItem, NInput, NInputNumber, NModal, NProgress, NSelect, NTag, useMessage,
} from 'naive-ui'
import { apiFetch, getToken, invalidateApiCache } from '../api/client'
import { useAuthStore } from '../stores/auth'
import DirectoryPicker from '../components/DirectoryPicker.vue'
import AdminVirtualAndIntro from '../components/AdminVirtualAndIntro.vue'
import AdminAccessPanel from '../components/AdminAccessPanel.vue'
import AdminFilesPanel from '../components/AdminFilesPanel.vue'
import AdminOpsPanel from '../components/AdminOpsPanel.vue'
import HomeScreenSettingsModal from '../components/HomeScreenSettingsModal.vue'

const tabs = [
  { key: 'dashboard', label: '概览', icon: '📊' },
  { key: 'libraries', label: '媒体库', icon: '🎞️' },
  { key: 'virtual', label: '虚拟库与片头', icon: '✨' },
  { key: 'scraper', label: 'TMDB 与刮削', icon: '🔎' },
  { key: 'tasks', label: '后台任务', icon: '⚡' },
  { key: 'users', label: '用户与权限', icon: '👥' },
  { key: 'files', label: '文件管理', icon: '📁' },
  { key: 'ops', label: '运维与迁移', icon: '🛠️' },
  { key: 'settings', label: '基础设置', icon: '⚙️' },
]

const message = useMessage()
const auth = useAuthStore()
const route = useRoute()
const router = useRouter()

const validTabs = new Set(tabs.map((t) => t.key))
const activeTab = ref(typeof route.query.tab === 'string' && validTabs.has(route.query.tab) ? route.query.tab : 'dashboard')
const loadedTabs = reactive<Record<string, boolean>>({})

const dash = ref<any>({})
const libraries = ref<any[]>([])
const users = ref<any[]>([])
const tasks = ref<any[]>([])
const matchQueue = ref<any[]>([])
const sseConnected = ref(false)
let evtSource: EventSource | null = null

const runningTaskCount = computed(() => tasks.value.filter((t) => t.status === 'running' || t.status === 'pending').length)

const settingsForm = reactive({
  server_name: '',
  emby_metadata_dir: '',
  emby_metadata_writeback: true,
})
const settingsStatus = reactive({ exists: false, writable: false })
const showMetaDirPicker = ref(false)

const tmdbStatus = ref<any>({})
const tmdbForm = reactive({
  api_key: '',
  read_token: '',
  api_base_url: 'https://api.themoviedb.org/3',
  image_base_url: 'https://image.tmdb.org/t/p',
  proxy_url: '',
  language: 'zh-CN',
  auto_threshold: 75,
  write_nfo: false,
})
const testingTMDB = ref(false)

const showManualSearchModal = ref(false)
const manualSearchTarget = ref<any>(null)
const manualQuery = ref('')
const manualYear = ref<number | null>(null)
const manualResults = ref<any[]>([])
const searchingManual = ref(false)

const showLibModal = ref(false)
const showDirPicker = ref(false)
const editingLibId = ref('')
const libDragIdx = ref(-1)
const libDragOverIdx = ref(-1)
const reorderingLibs = ref(false)
const libForm = reactive({
  name: '',
  kind: 'movies',
  paths: [] as string[],
  hidden: false,
  show_in_my_media: true,
  show_in_resume: true,
  show_in_next_up: true,
  show_in_latest: true,
  auto_merge_versions: true,
  auto_group_series: true,
})
const kindOptions = [
  { label: '电影 (movies)', value: 'movies' },
  { label: '剧集 (tvshows)', value: 'tvshows' },
  { label: '混合 (mixed)', value: 'mixed' },
  { label: '合集 (boxsets)', value: 'boxsets' },
]

const showUserModal = ref(false)
const showUserHomeSettings = ref(false)
const homeSettingsUser = ref<{ id: string; username: string } | null>(null)
const editingUserId = ref('')
const permissionGroups = ref<any[]>([])
const userForm = reactive({
  username: '',
  password: '',
  is_admin: false,
  can_download: false,
  disabled: false,
  permission_group_id: null as string | null,
  library_ids: [] as string[],
})
const libraryOptions = computed(() => libraries.value.map((l) => ({ label: l.name, value: l.id })))
const groupOptions = computed(() => permissionGroups.value.map((g) => ({ label: g.name, value: g.id })))

const coverFileInput = ref<HTMLInputElement | null>(null)
const uploadingCoverLibId = ref('')

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(2)} MB`
}

function formatTime(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '0ms'
  if (ms < 1000) return `${Math.round(ms)}ms`
  if (ms < 10000) {
    const tenths = Math.round(ms / 100)
    return tenths % 10 === 0 ? `${tenths / 10}秒` : `${(tenths / 10).toFixed(1)}秒`
  }
  const totalSecs = Math.floor(ms / 1000)
  if (totalSecs < 60) return `${totalSecs}秒`
  const hours = Math.floor(totalSecs / 3600)
  const mins = Math.floor((totalSecs % 3600) / 60)
  const secs = totalSecs % 60
  if (hours === 0) {
    return secs === 0 ? `${mins}分` : `${mins}分${secs}秒`
  }
  if (mins === 0 && secs === 0) return `${hours}小时`
  if (secs === 0) return `${hours}小时${mins}分`
  return `${hours}小时${mins}分${secs}秒`
}

function taskLibraryName(t: any): string {
  if (t?.library_name) return String(t.library_name)
  let lid = t?.library_id ? String(t.library_id) : ''
  if (!lid && t?.payload_json) {
    try {
      const parsed = JSON.parse(t.payload_json)
      if (parsed?.library_id) lid = String(parsed.library_id)
    } catch {
      // ignore invalid payload json
    }
  }
  if (lid) {
    const matched = libraries.value.find((l) => l.id === lid)
    if (matched?.name) return matched.name
  }
  return ''
}

function taskDurationLabel(t: any): string {
  if (typeof t?.duration_ms === 'number' && t.duration_ms > 0) {
    return formatDuration(t.duration_ms)
  }
  if (t?.finished_at) {
    const startIso = t.started_at || t.created_at
    if (startIso) {
      const diff = new Date(t.finished_at).getTime() - new Date(startIso).getTime()
      if (diff >= 0) return formatDuration(diff)
    }
  }
  return ''
}

function formatTaskMessage(t: any): string {
  let msg = String(t?.message || '')
  const libName = taskLibraryName(t)
  if (libName && msg.startsWith(`【${libName}】`)) {
    msg = msg.slice(libName.length + 2)
  }
  // Format legacy historical scan completion messages like "完成 (4523000ms)：..."
  msg = msg.replace(/完成\s*\((\d+)ms\)/g, (_m, msStr) => `完成（耗时 ${formatDuration(Number(msStr))}）`)
  return msg
}

function taskTypeLabel(tp: string): string {
  if (tp === 'scan') return '媒体库扫描'
  if (tp === 'probe') return '媒体信息探测'
  if (tp === 'scrape') return 'TMDB 刮削'
  if (tp === 'migrate_emby') return '从旧 Emby 迁移'
  return tp
}

function taskStatusType(status: string): 'default' | 'info' | 'success' | 'warning' | 'error' {
  switch (status) {
    case 'running': return 'info'
    case 'completed': return 'success'
    case 'failed': return 'error'
    case 'cancelled': return 'warning'
    default: return 'default'
  }
}

function taskStatusLabel(status: string): string {
  switch (status) {
    case 'pending': return '排队中'
    case 'running': return '执行中'
    case 'completed': return '已完成'
    case 'failed': return '失败'
    case 'cancelled': return '已取消'
    default: return status
  }
}

// Per-tab lazy loaders so entering /admin only fetches what is visible!
async function loadDashboardTab() {
  dash.value = await apiFetch('/api/admin/dashboard')
}

async function loadLibrariesTab() {
  const res = await apiFetch('/api/admin/libraries')
  libraries.value = res.items || []
}

async function loadScraperTab() {
  const [tmdb, q] = await Promise.all([
    apiFetch('/api/admin/tmdb'),
    apiFetch('/api/admin/scraper/queue?status=pending'),
  ])
  tmdbStatus.value = tmdb
  tmdbForm.api_base_url = tmdb.api_base_url || 'https://api.themoviedb.org/3'
  tmdbForm.image_base_url = tmdb.image_base_url || 'https://image.tmdb.org/t/p'
  tmdbForm.language = tmdb.language || 'zh-CN'
  tmdbForm.auto_threshold = tmdb.auto_threshold || 75
  tmdbForm.write_nfo = Boolean(tmdb.write_nfo)
  matchQueue.value = q.items || []
}

async function loadTasks() {
  const res = await apiFetch('/api/admin/tasks')
  tasks.value = res.items || []
}

async function loadUsersTab() {
  const [u, pg, l] = await Promise.all([
    apiFetch('/api/admin/users'),
    apiFetch('/api/admin/permission-groups'),
    libraries.value.length ? Promise.resolve({ items: libraries.value }) : apiFetch('/api/admin/libraries'),
  ])
  users.value = u.items || []
  permissionGroups.value = pg.items || []
  if (l.items) libraries.value = l.items
}

async function loadSettingsTab() {
  const s = await apiFetch('/api/admin/settings')
  settingsForm.server_name = s.server_name || ''
  settingsForm.emby_metadata_dir = s.emby_metadata_dir || ''
  settingsForm.emby_metadata_writeback = Boolean(s.emby_metadata_writeback)
  settingsStatus.exists = Boolean(s.emby_metadata_exists)
  settingsStatus.writable = Boolean(s.emby_metadata_writable)
}

async function ensureTabLoaded(tabKey: string, force = false) {
  if (!force && loadedTabs[tabKey]) return
  loadedTabs[tabKey] = true
  switch (tabKey) {
    case 'dashboard':
      await loadDashboardTab()
      break
    case 'libraries':
      await loadLibrariesTab()
      break
    case 'virtual':
      if (!libraries.value.length) await loadLibrariesTab()
      break
    case 'scraper':
      await loadScraperTab()
      break
    case 'tasks':
      await loadTasks()
      break
    case 'users':
      await loadUsersTab()
      break
    case 'settings':
      await loadSettingsTab()
      break
  }
}

function selectTab(key: string) {
  activeTab.value = key
  router.replace({ query: { ...route.query, tab: key } })
  ensureTabLoaded(key)
}

async function refreshActiveTab() {
  await ensureTabLoaded(activeTab.value, true)
  message.success('已刷新')
}

async function loadMatchQueue() {
  const res = await apiFetch('/api/admin/scraper/queue?status=pending')
  matchQueue.value = res.items || []
}

function connectSSE() {
  const token = getToken()
  if (!token) return
  evtSource = new EventSource(`/api/admin/tasks/events?api_key=${encodeURIComponent(token)}`)
  evtSource.addEventListener('connected', () => {
    sseConnected.value = true
  })
  evtSource.addEventListener('task', (e: MessageEvent) => {
    try {
      const ev = JSON.parse(e.data)
      if (!ev?.task?.id) return
      const idx = tasks.value.findIndex((t) => t.id === ev.task.id)
      if (idx >= 0) {
        tasks.value[idx] = ev.task
      } else {
        tasks.value.unshift(ev.task)
      }
      if (ev.event === 'completed' || ev.event === 'failed') {
        invalidateApiCache('/api/web/home')
        invalidateApiCache('/emby/')
        if (activeTab.value === 'dashboard') loadDashboardTab()
        if (activeTab.value === 'libraries') loadLibrariesTab()
      }
    } catch {
      // ignore malformed frame
    }
  })
  evtSource.onerror = () => {
    sseConnected.value = false
  }
}

async function saveTMDB() {
  try {
    const res = await apiFetch('/api/admin/tmdb', { method: 'PUT', body: JSON.stringify(tmdbForm) })
    tmdbStatus.value = res
    tmdbForm.api_key = ''
    tmdbForm.read_token = ''
    tmdbForm.proxy_url = ''
    message.success('TMDB 配置已保存并立即生效')
  } catch (e: any) {
    message.error(e.message || '保存 TMDB 配置失败')
  }
}

async function testTMDB() {
  testingTMDB.value = true
  try {
    const res = await apiFetch('/api/admin/tmdb/test', { method: 'POST' })
    message.success(res.message || '连接成功')
  } catch (e: any) {
    message.error(e.message || '连接失败')
  } finally {
    testingTMDB.value = false
  }
}

function openManualSearch(entry: any) {
  manualSearchTarget.value = entry
  manualQuery.value = entry.item_name || ''
  manualYear.value = entry.item_year || null
  manualResults.value = []
  showManualSearchModal.value = true
}

async function runManualSearch() {
  if (!manualQuery.value.trim()) return
  searchingManual.value = true
  try {
    const mediaType = manualSearchTarget.value?.item_type === 'Series' ? 'tv' : 'movie'
    const res = await apiFetch('/api/admin/scraper/search', {
      method: 'POST',
      body: JSON.stringify({ query: manualQuery.value.trim(), year: manualYear.value || 0, media_type: mediaType }),
    })
    manualResults.value = res.candidates || []
  } catch (e: any) {
    message.error(e.message || '搜索失败')
  } finally {
    searchingManual.value = false
  }
}

async function resolveMatch(itemId: string, mediaType: string, tmdbId: number) {
  try {
    await apiFetch(`/api/admin/scraper/queue/${itemId}/resolve`, {
      method: 'POST',
      body: JSON.stringify({ tmdb_id: tmdbId, media_type: mediaType }),
    })
    showManualSearchModal.value = false
    invalidateApiCache('/api/web/home')
    message.success('已应用选中匹配')
    await loadMatchQueue()
  } catch (e: any) {
    message.error(e.message || '应用匹配失败')
  }
}

async function ignoreMatch(itemId: string) {
  await apiFetch(`/api/admin/scraper/queue/${itemId}/ignore`, { method: 'POST' })
  message.info('已忽略该条目')
  await loadMatchQueue()
}

function openCreateLibrary() {
  editingLibId.value = ''
  libForm.name = ''
  libForm.kind = 'movies'
  libForm.paths = []
  libForm.hidden = false
  libForm.show_in_my_media = true
  libForm.show_in_resume = true
  libForm.show_in_next_up = true
  libForm.show_in_latest = true
  libForm.auto_merge_versions = true
  libForm.auto_group_series = true
  showLibModal.value = true
}

function openEditLibrary(lib: any) {
  editingLibId.value = lib.id
  libForm.name = lib.name || ''
  libForm.kind = lib.kind || 'movies'
  libForm.paths = [...(lib.paths || [])]
  libForm.hidden = Boolean(lib.hidden)
  libForm.show_in_my_media = lib.show_in_my_media !== false
  libForm.show_in_resume = lib.show_in_resume !== false
  libForm.show_in_next_up = lib.show_in_next_up !== false
  libForm.show_in_latest = lib.show_in_latest !== false
  libForm.auto_merge_versions = lib.auto_merge_versions !== false
  libForm.auto_group_series = lib.auto_group_series !== false
  showLibModal.value = true
}

function onLibDragStart(idx: number) {
  libDragIdx.value = idx
}

function onLibDragLeave(idx: number) {
  if (libDragOverIdx.value === idx) {
    libDragOverIdx.value = -1
  }
}

function onLibDragEnd() {
  libDragIdx.value = -1
  libDragOverIdx.value = -1
}

async function onLibDrop(targetIdx: number) {
  const from = libDragIdx.value
  onLibDragEnd()
  if (from < 0 || from === targetIdx || reorderingLibs.value) return
  const arr = [...libraries.value]
  const [moved] = arr.splice(from, 1)
  arr.splice(targetIdx, 0, moved)
  await persistLibraryOrder(arr)
}

async function moveLibrary(idx: number, delta: number) {
  const target = idx + delta
  if (target < 0 || target >= libraries.value.length || reorderingLibs.value) return
  const arr = [...libraries.value]
  const [moved] = arr.splice(idx, 1)
  arr.splice(target, 0, moved)
  await persistLibraryOrder(arr)
}

async function persistLibraryOrder(nextLibs: any[]) {
  const prev = libraries.value
  libraries.value = nextLibs
  reorderingLibs.value = true
  try {
    await apiFetch('/api/admin/libraries/reorder', {
      method: 'PUT',
      body: JSON.stringify({ library_ids: nextLibs.map((l) => l.id) }),
    })
    invalidateApiCache('/api/web/home')
    invalidateApiCache('/emby/')
    window.dispatchEvent(new CustomEvent('emby-lite:home-settings-updated'))
    message.success('媒体库排序已保存')
  } catch (e: any) {
    libraries.value = prev
    message.error(e.message || '保存排序失败')
  } finally {
    reorderingLibs.value = false
  }
}

function openUserHomeSettings(u: any) {
  homeSettingsUser.value = { id: u.id, username: u.username }
  showUserHomeSettings.value = true
}

function addLibPath(path: string) {
  if (!path) return
  if (libForm.paths.includes(path)) {
    message.info('该目录已在列表中')
    return
  }
  libForm.paths.push(path)
  message.success('已添加目录')
}

function removeLibPath(idx: number) {
  libForm.paths.splice(idx, 1)
}

async function saveLibrary() {
  if (!libForm.name.trim() || (libForm.kind !== 'boxsets' && libForm.paths.length === 0)) {
    message.warning('请填写媒体库名称并至少选择一个目录')
    return
  }
  const payload = {
    name: libForm.name.trim(),
    kind: libForm.kind,
    paths: libForm.paths,
    hidden: libForm.hidden,
    show_in_my_media: libForm.show_in_my_media,
    show_in_resume: libForm.show_in_resume,
    show_in_next_up: libForm.show_in_next_up,
    show_in_latest: libForm.show_in_latest,
    auto_merge_versions: libForm.auto_merge_versions,
    auto_group_series: libForm.auto_group_series,
  }
  if (editingLibId.value) {
    const res = await apiFetch(`/api/admin/libraries/${editingLibId.value}`, { method: 'PUT', body: JSON.stringify(payload) })
    const c = res?.consolidated
    const merged = c ? (c.series_merged || 0) + (c.movies_merged || 0) + (c.episodes_merged || 0) : 0
    message.success(merged > 0 ? `媒体库已更新，并自动合并 ${merged} 个重复项` : '媒体库已更新')
  } else {
    await apiFetch('/api/admin/libraries', { method: 'POST', body: JSON.stringify(payload) })
    message.success('媒体库已创建')
  }
  showLibModal.value = false
  invalidateApiCache('/api/web/home')
  invalidateApiCache('/emby/')
  window.dispatchEvent(new CustomEvent('emby-lite:home-settings-updated'))
  await loadLibrariesTab()
}

async function consolidateLibrary(id: string) {
  try {
    const res = await apiFetch(`/api/admin/libraries/${id}/consolidate`, { method: 'POST' })
    const total = (res?.series_merged || 0) + (res?.seasons_merged || 0) + (res?.movies_merged || 0) + (res?.episodes_merged || 0)
    invalidateApiCache('/api/web/home')
    invalidateApiCache('/emby/')
    if (total > 0) {
      message.success(`合并整理完成：剧集 ${res.series_merged || 0}，季 ${res.seasons_merged || 0}，电影 ${res.movies_merged || 0}，单集 ${res.episodes_merged || 0}`)
    } else {
      message.info('当前媒体库没有可合并的多版本或重复剧集')
    }
    await loadLibrariesTab()
  } catch (e: any) {
    message.error(e.message || '合并整理失败')
  }
}

async function scanLibrary(id: string) {
  await apiFetch(`/api/admin/libraries/${id}/scan`, { method: 'POST' })
  message.success('并发扫描任务已加入队列')
  await loadTasks()
}

async function probeLibrary(id: string) {
  await apiFetch(`/api/admin/libraries/${id}/probe?include_strm=true`, { method: 'POST' })
  message.success('媒体探测任务已加入队列')
  await loadTasks()
}

async function scrapeLibrary(id: string) {
  await apiFetch(`/api/admin/libraries/${id}/scrape`, { method: 'POST' })
  message.success('TMDB 刮削任务已加入队列')
  await loadTasks()
}

async function scanAll() {
  await apiFetch('/api/admin/scan-all', { method: 'POST' })
  message.success('全库并发扫描任务已加入队列')
  await loadTasks()
}

async function cancelTask(id: string) {
  await apiFetch(`/api/admin/tasks/${id}/cancel`, { method: 'POST' })
  message.info('已请求取消任务')
}

async function clearCache() {
  const res = await apiFetch('/api/admin/cache/clear', { method: 'POST' })
  invalidateApiCache()
  message.success(`已清理 ${res.image_files_cleared ?? 0} 个缩略图缓存文件并清空热缓存`)
  await loadDashboardTab()
}

async function deleteLibrary(id: string) {
  await apiFetch(`/api/admin/libraries/${id}`, { method: 'DELETE' })
  invalidateApiCache('/api/web/home')
  invalidateApiCache('/emby/')
  message.success('已删除媒体库')
  await loadLibrariesTab()
}

async function generateLibCover(id: string) {
  const res = await apiFetch(`/api/admin/libraries/${id}/cover/generate`, { method: 'POST' })
  invalidateApiCache('/api/web/home')
  invalidateApiCache('/emby/')
  message.success(`拼贴封面已生成（使用了 ${res.poster_count ?? 0} 张海报）`)
  await loadLibrariesTab()
}

function triggerCoverUpload(id: string) {
  uploadingCoverLibId.value = id
  coverFileInput.value?.click()
}

async function onCoverFileSelected(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file || !uploadingCoverLibId.value) return
  const fd = new FormData()
  fd.append('file', file)
  try {
    const res = await fetch(`/api/admin/libraries/${uploadingCoverLibId.value}/cover`, {
      method: 'POST',
      headers: { 'X-Emby-Token': getToken() },
      body: fd,
    })
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    invalidateApiCache('/api/web/home')
    message.success('自定义封面上传成功')
    await loadLibrariesTab()
  } catch (err: any) {
    message.error(err.message || '封面上传失败')
  } finally {
    input.value = ''
  }
}

function openCreateUser() {
  editingUserId.value = ''
  userForm.username = ''
  userForm.password = ''
  userForm.is_admin = false
  userForm.can_download = false
  userForm.disabled = false
  userForm.permission_group_id = null
  userForm.library_ids = []
  showUserModal.value = true
}

function openEditUser(u: any) {
  editingUserId.value = u.id
  userForm.username = u.username
  userForm.password = ''
  userForm.is_admin = Boolean(u.is_admin)
  userForm.can_download = Boolean(u.can_download)
  userForm.disabled = Boolean(u.disabled)
  userForm.permission_group_id = u.permission_group_id || null
  userForm.library_ids = [...(u.library_ids || [])]
  showUserModal.value = true
}

async function saveUser() {
  if (editingUserId.value) {
    await apiFetch(`/api/admin/users/${editingUserId.value}`, {
      method: 'PUT',
      body: JSON.stringify({
        password: userForm.password,
        is_admin: userForm.is_admin,
        can_download: userForm.can_download,
        disabled: userForm.disabled,
        permission_group_id: userForm.permission_group_id || '',
        library_ids: userForm.library_ids,
        update_libraries: true,
      }),
    })
    editingUserId.value = ''
    showUserModal.value = false
    message.success('用户权限已更新')
    await loadUsersTab()
    return
  }
  if (!userForm.username.trim() || userForm.password.length < 4) {
    message.warning('请填写用户名且密码至少 4 位')
    return
  }
  await apiFetch('/api/admin/users', {
    method: 'POST',
    body: JSON.stringify({
      username: userForm.username.trim(),
      password: userForm.password,
      is_admin: userForm.is_admin,
      can_download: userForm.can_download,
      permission_group_id: userForm.permission_group_id || '',
      library_ids: userForm.library_ids,
    }),
  })
  showUserModal.value = false
  userForm.username = ''
  userForm.password = ''
  userForm.library_ids = []
  message.success('用户已创建')
  await loadUsersTab()
}

async function toggleDownload(u: any) {
  await apiFetch(`/api/admin/users/${u.id}`, { method: 'PUT', body: JSON.stringify({ can_download: !u.can_download }) })
  message.success('权限已更新')
  await loadUsersTab()
}

async function deleteUser(id: string) {
  await apiFetch(`/api/admin/users/${id}`, { method: 'DELETE' })
  message.success('用户已删除')
  await loadUsersTab()
}

function selectMetaDir(path: string) {
  settingsForm.emby_metadata_dir = path
  showMetaDirPicker.value = false
  message.success('已选择 Emby 元数据目录，请点击保存设置生效')
}

async function saveSettings() {
  await apiFetch('/api/admin/settings', {
    method: 'PUT',
    body: JSON.stringify({
      server_name: settingsForm.server_name,
      emby_metadata_dir: settingsForm.emby_metadata_dir,
      emby_metadata_writeback: settingsForm.emby_metadata_writeback,
    }),
  })
  auth.serverName = settingsForm.server_name
  message.success('设置已保存')
  await loadSettingsTab()
}

watch(
  () => route.query.tab,
  (t) => {
    if (typeof t === 'string' && validTabs.has(t) && t !== activeTab.value) {
      activeTab.value = t
      ensureTabLoaded(t)
    }
  },
)

onMounted(() => {
  ensureTabLoaded(activeTab.value)
  connectSSE()
})

onBeforeUnmount(() => {
  if (evtSource) {
    evtSource.close()
    evtSource = null
  }
})
</script>
