<template>
  <div class="space-y-6">
    <div class="flex items-center justify-between">
      <h1 class="text-xl font-semibold">管理控制台</h1>
      <n-button size="small" type="primary" secondary @click="scanAll">扫描全部媒体库</n-button>
    </div>

    <n-tabs v-model:value="activeTab" type="line" animated>
      <!-- Dashboard -->
      <n-tab-pane name="dashboard" tab="概览">
        <div class="grid grid-cols-2 sm:grid-cols-4 gap-4 mt-2">
          <n-card size="small" title="媒体库">{{ dash.libraries ?? 0 }}</n-card>
          <n-card size="small" title="电影">{{ dash.movies ?? 0 }}</n-card>
          <n-card size="small" title="剧集 / 单集">{{ dash.series ?? 0 }} / {{ dash.episodes ?? 0 }}</n-card>
          <n-card size="small" title="用户 / 活跃播放">{{ dash.users ?? 0 }} / {{ dash.active_streams ?? 0 }}</n-card>
        </div>
        <n-card size="small" title="运行环境与引擎能力" class="mt-4">
          <div class="text-sm text-slate-300 space-y-1.5">
            <div>服务器名称：{{ dash.server_name }}（ID: <code>{{ dash.server_id }}</code>）</div>
            <div>
              主数据库：<strong>{{ dash.database }}</strong>
              <n-tag v-if="dash.has_fts5" size="tiny" type="success" class="ml-1.5">FTS5 全文检索</n-tag>
              <n-tag v-if="dash.has_trgm" size="tiny" type="success" class="ml-1.5">pg_trgm 模糊搜索</n-tag>
              · 缓存后端：<strong>{{ dash.cache }}</strong>
            </div>
            <div>
              媒体探测：<strong>纯 Go 原生解析 (MP4/MKV/Range)</strong>
              <n-tag size="tiny" :type="dash.ffprobe_available ? 'info' : 'default'" class="ml-1.5">
                {{ dash.ffprobe_available ? 'ffprobe 兜底可用' : '纯静态零外部依赖' }}
              </n-tag>
            </div>
            <div class="flex flex-wrap items-center gap-3 pt-1">
              <span>图片缩略图磁盘缓存：{{ dash.image_cache_files ?? 0 }} 个文件（{{ formatBytes(dash.image_cache_bytes || 0) }}）</span>
              <n-button size="tiny" secondary type="warning" @click="clearCache">清理图片与热缓存</n-button>
            </div>
          </div>
        </n-card>
      </n-tab-pane>

      <!-- Libraries -->
      <n-tab-pane name="libraries" tab="媒体库">
        <div class="flex justify-end mb-3">
          <n-button type="primary" size="small" @click="openCreateLibrary">+ 新建媒体库</n-button>
        </div>
        <div class="space-y-3">
          <n-card v-for="lib in libraries" :key="lib.id" size="small">
            <div class="flex flex-wrap items-center justify-between gap-3">
              <div>
                <div class="font-medium text-white">
                  {{ lib.name }}
                  <n-tag size="tiny" class="ml-2">{{ lib.kind }}</n-tag>
                  <n-tag v-if="lib.hidden" size="tiny" type="warning" class="ml-1">隐藏</n-tag>
                </div>
                <div class="text-xs text-slate-400 mt-1">
                  路径：{{ (lib.paths || []).join(' ; ') }} · {{ lib.item_count }} 项 · 状态：{{ lib.scanning ? '扫描中' : lib.status }}
                </div>
              </div>
              <div class="flex items-center gap-2">
                <n-button size="small" secondary @click="generateLibCover(lib.id)">拼贴封面</n-button>
                <n-button size="small" secondary @click="triggerCoverUpload(lib.id)">传封面</n-button>
                <n-button size="small" secondary @click="openEditLibrary(lib)">编辑</n-button>
                <n-button size="small" secondary @click="probeLibrary(lib.id)">探测</n-button>
                <n-button size="small" type="info" secondary @click="scrapeLibrary(lib.id)">刮削</n-button>
                <n-button size="small" type="primary" secondary @click="scanLibrary(lib.id)">扫描</n-button>
                <n-button size="small" type="error" quaternary @click="deleteLibrary(lib.id)">删除</n-button>
              </div>
            </div>
          </n-card>
        </div>
      </n-tab-pane>

      <!-- Virtual Libraries & Intro Skip -->
      <n-tab-pane name="virtual" tab="虚拟库与片头跳过">
        <AdminVirtualAndIntro :libraries="libraries" />
      </n-tab-pane>

      <!-- TMDB & Manual Match Queue -->
      <n-tab-pane name="scraper" :tab="`TMDB 与刮削${matchQueue.length ? ` (${matchQueue.length})` : ''}`">
        <div class="grid grid-cols-1 lg:grid-cols-2 gap-6 mt-2">
          <!-- TMDB Config Card -->
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

          <!-- Manual Match Queue -->
          <n-card size="small" title="待人工匹配队列（低置信度候选并列对比）">
            <template #header-extra>
              <n-button size="tiny" secondary @click="loadMatchQueue">刷新</n-button>
            </template>
            <div v-if="matchQueue.length === 0" class="py-10 text-center text-sm text-slate-400">
              当前没有待人工确认的刮削条目
            </div>
            <div v-else class="space-y-4 max-h-[520px] overflow-y-auto pr-1">
              <div
                v-for="entry in matchQueue"
                :key="entry.item_id"
                class="rounded-xl border border-cinema-border bg-cinema-bg p-3 space-y-3"
              >
                <div class="flex items-center justify-between gap-2">
                  <div>
                    <div class="text-sm font-semibold text-white">
                      {{ entry.item_name }}
                      <span v-if="entry.item_year" class="text-slate-400 font-normal">({{ entry.item_year }})</span>
                    </div>
                    <div class="text-xs text-slate-400">
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
                    class="flex gap-2.5 p-2 rounded-lg border border-cinema-border/60 bg-cinema-card"
                  >
                    <img
                      v-if="c.poster_url"
                      :src="c.poster_url"
                      class="w-12 h-18 object-cover rounded shrink-0 bg-slate-900"
                    />
                    <div class="min-w-0 flex-1 space-y-1">
                      <div class="text-xs font-medium text-white truncate">{{ c.title }} ({{ c.year || '?' }})</div>
                      <div class="text-[11px] text-emerald-400">匹配分 {{ c.score }} · ★ {{ c.rating }}</div>
                      <div class="text-[10px] text-slate-400 truncate">{{ (c.reasons || []).join(' / ') }}</div>
                      <n-button size="tiny" type="primary" secondary @click="resolveMatch(entry.item_id, c.media_type, c.provider_id)">
                        选用此匹配
                      </n-button>
                    </div>
                  </div>
                </div>
                <div v-else class="text-xs text-slate-500">无自动候选，请点击“手动搜 TMDB”定位。</div>
              </div>
            </div>
          </n-card>
        </div>
      </n-tab-pane>

      <!-- Background Tasks (SSE Live) -->
      <n-tab-pane name="tasks" tab="后台任务 (SSE)">
        <div class="flex items-center justify-between mb-3">
          <div class="text-xs text-slate-400 flex items-center gap-2">
            <span class="inline-block w-2 h-2 rounded-full" :class="sseConnected ? 'bg-emerald-400' : 'bg-slate-500'"></span>
            <span>{{ sseConnected ? 'SSE 实时推送已连接' : 'SSE 未连接' }}</span>
          </div>
          <n-button size="small" secondary @click="loadTasks">刷新任务列表</n-button>
        </div>
        <div v-if="tasks.length === 0" class="text-sm text-slate-400 py-8 text-center">暂无后台任务记录</div>
        <div v-else class="space-y-2.5">
          <n-card v-for="t in tasks" :key="t.id" size="small">
            <div class="space-y-2">
              <div class="flex flex-wrap items-center justify-between gap-2">
                <div class="flex items-center gap-2">
                  <n-tag size="tiny" :type="taskStatusType(t.status)">{{ taskStatusLabel(t.status) }}</n-tag>
                  <span class="text-sm font-medium text-white">{{ taskTypeLabel(t.type) }}</span>
                  <span class="text-xs text-slate-400">{{ t.message }}</span>
                </div>
                <div class="flex items-center gap-2">
                  <span class="text-xs text-slate-500">{{ formatTime(t.created_at) }}</span>
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
              <div v-if="t.error" class="text-xs text-rose-400">错误：{{ t.error }}</div>
            </div>
          </n-card>
        </div>
      </n-tab-pane>

      <!-- Users -->
      <n-tab-pane name="users" tab="用户与权限">
        <div class="flex justify-end mb-3">
          <n-button type="primary" size="small" @click="openCreateUser">+ 新建用户</n-button>
        </div>
        <div class="space-y-3">
          <n-card v-for="u in users" :key="u.id" size="small">
            <div class="flex flex-wrap items-center justify-between gap-3">
              <div>
                <div class="font-medium text-white">
                  {{ u.username }}
                  <n-tag v-if="u.is_admin" size="tiny" type="success" class="ml-2">管理员</n-tag>
                  <n-tag v-if="u.can_download" size="tiny" type="info" class="ml-1">允许下载</n-tag>
                  <n-tag v-if="u.disabled" size="tiny" type="error" class="ml-1">已禁用</n-tag>
                </div>
                <div class="text-xs text-slate-400 mt-1">
                  可见库：{{ u.is_admin ? '全部' : (u.library_ids?.length ? `${u.library_ids.length} 个指定库` : '默认非隐藏库') }}
                </div>
              </div>
              <div class="flex items-center gap-2">
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

        <AdminAccessPanel :libraries="libraries" @changed="loadAll" />
      </n-tab-pane>

      <!-- File Manager -->
      <n-tab-pane name="files" tab="文件管理">
        <AdminFilesPanel />
      </n-tab-pane>

      <!-- Backups, Migration, API Keys & Audit Logs -->
      <n-tab-pane name="ops" tab="运维与迁移">
        <AdminOpsPanel />
      </n-tab-pane>

      <!-- Settings -->
      <n-tab-pane name="settings" tab="基础设置">
        <n-card size="small" class="max-w-xl mt-2">
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
                <div class="px-3 py-1.5 rounded bg-cinema-bg border border-cinema-border text-xs font-mono text-slate-300 truncate">
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
      </n-tab-pane>
    </n-tabs>

    <!-- Manual TMDB Search Modal -->
    <n-modal v-model:show="showManualSearchModal" preset="card" :title="`手动搜索 TMDB：${manualSearchTarget?.item_name || ''}`" class="max-w-xl">
      <div class="space-y-3">
        <div class="flex gap-2">
          <n-input v-model:value="manualQuery" placeholder="输入影视名称..." @keyup.enter="runManualSearch" />
          <n-input-number v-model:value="manualYear" placeholder="年份" :min="0" :max="2100" class="w-28" />
          <n-button type="primary" :loading="searchingManual" @click="runManualSearch">搜索</n-button>
        </div>
        <div v-if="manualResults.length === 0" class="text-xs text-slate-500 py-6 text-center">输入关键词搜索 TMDB 候选条目</div>
        <div v-else class="space-y-2 max-h-80 overflow-y-auto">
          <div
            v-for="c in manualResults"
            :key="c.provider_id"
            class="flex items-center justify-between gap-3 p-2.5 rounded-lg border border-cinema-border bg-cinema-bg"
          >
            <div class="min-w-0">
              <div class="text-sm font-medium text-white truncate">{{ c.title }} ({{ c.year || '?' }})</div>
              <div class="text-xs text-slate-400 truncate">{{ c.original_title }} · TMDB #{{ c.provider_id }}</div>
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
              <span class="text-xs text-slate-400">支持添加多个根目录</span>
            </div>

            <div
              v-if="libForm.paths.length === 0"
              class="rounded-lg border border-dashed border-cinema-border p-3 text-xs text-slate-500 text-center"
            >
              暂未选择目录，请点击上方“浏览并选择目录”
            </div>
            <div v-else class="space-y-1.5">
              <div
                v-for="(p, idx) in libForm.paths"
                :key="p"
                class="flex items-center justify-between gap-2 px-3 py-1.5 rounded bg-cinema-bg border border-cinema-border text-xs"
              >
                <span class="font-mono text-slate-200 truncate" :title="p">{{ p }}</span>
                <n-button size="tiny" type="error" quaternary @click="removeLibPath(idx)">移除</n-button>
              </div>
            </div>
          </div>
        </n-form-item>
        <n-form-item label="可见性">
          <n-checkbox v-model:checked="libForm.hidden">设为隐藏媒体库（仅管理员或授权用户可见）</n-checkbox>
        </n-form-item>
        <n-button type="primary" block attr-type="submit">{{ editingLibId ? '保存修改' : '创建媒体库' }}</n-button>
      </n-form>
    </n-modal>

    <!-- Directory Picker Modal (Libraries) -->
    <DirectoryPicker
      v-model:show="showDirPicker"
      :selected-paths="libForm.paths"
      @select="addLibPath"
    />

    <!-- Directory Picker Modal (Emby Metadata Dir) -->
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

    <input ref="coverFileInput" type="file" accept="image/*" class="hidden" @change="onCoverFileSelected" />
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import {
  NButton, NCard, NCheckbox, NForm, NFormItem, NInput, NInputNumber, NModal, NProgress, NSelect, NTabPane, NTabs, NTag, useMessage,
} from 'naive-ui'
import { apiFetch, getToken } from '../api/client'
import { useAuthStore } from '../stores/auth'
import DirectoryPicker from '../components/DirectoryPicker.vue'
import AdminVirtualAndIntro from '../components/AdminVirtualAndIntro.vue'
import AdminAccessPanel from '../components/AdminAccessPanel.vue'
import AdminFilesPanel from '../components/AdminFilesPanel.vue'
import AdminOpsPanel from '../components/AdminOpsPanel.vue'

const message = useMessage()
const auth = useAuthStore()

const activeTab = ref('dashboard')
const dash = ref<any>({})
const libraries = ref<any[]>([])
const users = ref<any[]>([])
const tasks = ref<any[]>([])
const matchQueue = ref<any[]>([])
const sseConnected = ref(false)
let evtSource: EventSource | null = null

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
const libForm = reactive({
  name: '',
  kind: 'movies',
  paths: [] as string[],
  hidden: false,
})
const kindOptions = [
  { label: '电影 (movies)', value: 'movies' },
  { label: '剧集 (tvshows)', value: 'tvshows' },
  { label: '混合 (mixed)', value: 'mixed' },
]

const showUserModal = ref(false)
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

function formatTime(iso: string): string {
  if (!iso) return ''
  return new Date(iso).toLocaleString()
}

function taskTypeLabel(tp: string): string {
  if (tp === 'scan') return '媒体库扫描'
  if (tp === 'probe') return '媒体信息探测'
  if (tp === 'scrape') return 'TMDB 刮削'
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

async function loadAll() {
  const [d, l, u, s, t, tmdb, q, pg] = await Promise.all([
    apiFetch('/api/admin/dashboard'),
    apiFetch('/api/admin/libraries'),
    apiFetch('/api/admin/users'),
    apiFetch('/api/admin/settings'),
    apiFetch('/api/admin/tasks'),
    apiFetch('/api/admin/tmdb'),
    apiFetch('/api/admin/scraper/queue?status=pending'),
    apiFetch('/api/admin/permission-groups'),
  ])
  dash.value = d
  libraries.value = l.items || []
  users.value = u.items || []
  permissionGroups.value = pg.items || []
  settingsForm.server_name = s.server_name || ''
  settingsForm.emby_metadata_dir = s.emby_metadata_dir || ''
  settingsForm.emby_metadata_writeback = Boolean(s.emby_metadata_writeback)
  settingsStatus.exists = Boolean(s.emby_metadata_exists)
  settingsStatus.writable = Boolean(s.emby_metadata_writable)
  tasks.value = t.items || []
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
        loadAll()
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
    const res = await apiFetch('/api/admin/tmdb', {
      method: 'PUT',
      body: JSON.stringify(tmdbForm),
    })
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
      body: JSON.stringify({
        query: manualQuery.value.trim(),
        year: manualYear.value || 0,
        media_type: mediaType,
      }),
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
  showLibModal.value = true
}

function openEditLibrary(lib: any) {
  editingLibId.value = lib.id
  libForm.name = lib.name || ''
  libForm.kind = lib.kind || 'movies'
  libForm.paths = [...(lib.paths || [])]
  libForm.hidden = Boolean(lib.hidden)
  showLibModal.value = true
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
  if (!libForm.name.trim() || libForm.paths.length === 0) {
    message.warning('请填写媒体库名称并至少选择一个目录')
    return
  }
  const payload = {
    name: libForm.name.trim(),
    kind: libForm.kind,
    paths: libForm.paths,
    hidden: libForm.hidden,
  }
  if (editingLibId.value) {
    await apiFetch(`/api/admin/libraries/${editingLibId.value}`, {
      method: 'PUT',
      body: JSON.stringify(payload),
    })
    message.success('媒体库已更新')
  } else {
    await apiFetch('/api/admin/libraries', {
      method: 'POST',
      body: JSON.stringify(payload),
    })
    message.success('媒体库已创建')
  }
  showLibModal.value = false
  await loadAll()
}

async function scanLibrary(id: string) {
  await apiFetch(`/api/admin/libraries/${id}/scan`, { method: 'POST' })
  message.success('扫描任务已加入队列')
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
  message.success('全库扫描任务已加入队列')
  await loadTasks()
}

async function cancelTask(id: string) {
  await apiFetch(`/api/admin/tasks/${id}/cancel`, { method: 'POST' })
  message.info('已请求取消任务')
}

async function clearCache() {
  const res = await apiFetch('/api/admin/cache/clear', { method: 'POST' })
  message.success(`已清理 ${res.image_files_cleared ?? 0} 个缩略图缓存文件并清空热缓存`)
  await loadAll()
}

async function deleteLibrary(id: string) {
  await apiFetch(`/api/admin/libraries/${id}`, { method: 'DELETE' })
  message.success('已删除媒体库')
  await loadAll()
}

async function generateLibCover(id: string) {
  const res = await apiFetch(`/api/admin/libraries/${id}/cover/generate`, { method: 'POST' })
  message.success(`拼贴封面已生成（使用了 ${res.poster_count ?? 0} 张海报）`)
  await loadAll()
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
    message.success('自定义封面上传成功')
    await loadAll()
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
    await loadAll()
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
  await loadAll()
}

async function toggleDownload(u: any) {
  await apiFetch(`/api/admin/users/${u.id}`, {
    method: 'PUT',
    body: JSON.stringify({ can_download: !u.can_download }),
  })
  message.success('权限已更新')
  await loadAll()
}

async function deleteUser(id: string) {
  await apiFetch(`/api/admin/users/${id}`, { method: 'DELETE' })
  message.success('用户已删除')
  await loadAll()
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
  await loadAll()
}

onMounted(() => {
  loadAll()
  connectSSE()
})

onBeforeUnmount(() => {
  if (evtSource) {
    evtSource.close()
    evtSource = null
  }
})
</script>
