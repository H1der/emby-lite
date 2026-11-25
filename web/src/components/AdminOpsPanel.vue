<template>
  <div class="space-y-6 mt-2">
    <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
      <!-- Backups & Restore -->
      <n-card size="small" title="数据库备份与跨库可移植恢复">
        <template #header-extra>
          <div class="flex gap-1.5">
            <n-button size="tiny" type="primary" @click="createBackup('portable-zip')">+ 可移植 ZIP 备份</n-button>
            <n-button v-if="backupDialect === 'sqlite'" size="tiny" secondary @click="createBackup('sqlite-db')">
              + SQLite 快照
            </n-button>
          </div>
        </template>
        <div class="text-xs text-slate-400 mb-3">
          可移植 ZIP 归档支持 <strong>SQLite ↔ PostgreSQL</strong> 跨库迁移恢复。目录：<code>{{ backupDir }}</code>
        </div>
        <div v-if="backups.length === 0" class="py-6 text-center text-xs text-slate-500">暂无备份归档</div>
        <div v-else class="space-y-2 max-h-72 overflow-y-auto">
          <div
            v-for="b in backups"
            :key="b.filename"
            class="rounded-lg border border-cinema-border bg-cinema-bg p-2.5 flex items-center justify-between gap-2"
          >
            <div class="min-w-0">
              <div class="text-xs font-mono text-white truncate">{{ b.filename }}</div>
              <div class="text-[11px] text-slate-400">{{ b.format }} · {{ (b.size / 1024).toFixed(1) }} KB</div>
            </div>
            <div class="flex items-center gap-1 shrink-0">
              <n-button size="tiny" secondary @click="downloadBackup(b.filename)">下载</n-button>
              <n-button v-if="b.format === 'portable-zip'" size="tiny" type="warning" secondary @click="restoreBackup(b.filename)">
                恢复
              </n-button>
              <n-button size="tiny" type="error" quaternary @click="deleteBackup(b.filename)">删</n-button>
            </div>
          </div>
        </div>
      </n-card>

      <!-- Emby Full Migration Wizard -->
      <n-card size="small" title="从旧 Emby 完整迁移（媒体库 / 元数据 / 用户 / 观看进度）">
        <div class="text-xs text-slate-400 mb-3">
          纯 Go 只读读取原 Emby 配置目录，自动识别 <code>root/default</code>（媒体库）、<code>data/library.db</code>（影视元数据）与 <code>data/users.db</code>（用户与观看记录）。
        </div>
        <n-form label-placement="top" size="small">
          <n-form-item label="原 Emby 配置根目录（推荐，自动识别下方所有文件）">
            <div class="flex gap-2 w-full">
              <n-input
                v-model:value="migForm.emby_config_dir"
                placeholder="例如：/emby-config 或 D:\emby\programdata"
              />
              <n-button secondary @click="openPicker('config')">📁 选择目录</n-button>
            </div>
          </n-form-item>

          <!-- Import Scope Toggles -->
          <n-form-item label="迁移内容">
            <div class="flex flex-wrap gap-x-4 gap-y-1.5">
              <n-checkbox v-model:checked="migForm.import_libraries">媒体库定义</n-checkbox>
              <n-checkbox v-model:checked="migForm.import_items">影视元数据与源路径</n-checkbox>
              <n-checkbox v-model:checked="migForm.import_users">用户账号</n-checkbox>
              <n-checkbox v-model:checked="migForm.import_watch">观看进度与收藏</n-checkbox>
            </div>
          </n-form-item>

          <!-- Path Prefix Mappings -->
          <n-form-item label="路径前缀映射（旧 Emby 路径 → 本机/容器新路径，可选）">
            <div class="w-full space-y-2">
              <div
                v-for="(m, idx) in migForm.path_mappings"
                :key="idx"
                class="flex items-center gap-1.5"
              >
                <n-input v-model:value="m.from" size="small" placeholder="旧路径前缀，如 /mnt/old" />
                <span class="text-slate-400 text-xs shrink-0">→</span>
                <n-input v-model:value="m.to" size="small" placeholder="新路径前缀，如 /media" />
                <n-button size="tiny" secondary @click="openPicker('mapping', idx)" title="选择新路径目录">📁</n-button>
                <n-button size="tiny" type="error" quaternary @click="removePathMapping(idx)">删</n-button>
              </div>
              <n-button size="tiny" dashed @click="addPathMapping">+ 添加路径映射</n-button>
            </div>
          </n-form-item>

          <n-form-item label="新导入用户的默认密码">
            <n-input
              v-model:value="migForm.default_password"
              type="password"
              show-password-on="click"
              placeholder="留空则默认使用 EmbyMigrate123!"
            />
          </n-form-item>

          <!-- Advanced Manual Overrides -->
          <div class="mb-3">
            <button
              type="button"
              class="text-xs text-emerald-400 hover:underline"
              @click="showAdvancedMig = !showAdvancedMig"
            >
              {{ showAdvancedMig ? '▾ 收起手动指定路径与高级选项' : '▸ 高级选项（手动指定 db / root 路径、覆盖策略）' }}
            </button>
            <div v-if="showAdvancedMig" class="mt-2 p-2.5 rounded-lg bg-cinema-bg/70 border border-cinema-border/70 space-y-2">
              <n-form-item label="root/default 目录（可选覆盖）" :show-feedback="false">
                <div class="flex gap-2 w-full">
                  <n-input v-model:value="migForm.root_default_dir" placeholder="例如：/emby-config/root/default" />
                  <n-button size="small" secondary @click="openPicker('root')">📁</n-button>
                </div>
              </n-form-item>
              <n-form-item label="users.db 文件路径（可选覆盖）" :show-feedback="false">
                <n-input v-model:value="migForm.users_db_path" placeholder="例如：/emby-config/data/users.db" />
              </n-form-item>
              <n-form-item label="library.db 文件路径（可选覆盖）" :show-feedback="false">
                <n-input v-model:value="migForm.library_db_path" placeholder="例如：/emby-config/data/library.db" />
              </n-form-item>
              <n-checkbox v-model:checked="migForm.overwrite" class="pt-1">
                允许覆盖已有同路径条目 / 观看记录
              </n-checkbox>
            </div>
          </div>

          <div class="flex items-center gap-2">
            <n-button secondary :loading="migLoading" @click="previewMigration">1. 预检迁移内容</n-button>
            <n-button type="primary" :loading="migLoading" @click="runMigration">2. 确认执行迁移</n-button>
          </div>
        </n-form>

        <!-- Preview Report -->
        <div
          v-if="migPreview"
          class="mt-3 p-3 rounded-lg bg-cinema-bg border border-cinema-border text-xs text-slate-300 space-y-2"
        >
          <div class="font-medium text-emerald-400">预检报告</div>
          <div v-if="migPreview.resolved" class="text-[11px] text-slate-400 space-y-0.5 font-mono break-all">
            <div v-if="migPreview.resolved.root_default_dir">root/default: {{ migPreview.resolved.root_default_dir }}</div>
            <div v-if="migPreview.resolved.library_db_path">library.db: {{ migPreview.resolved.library_db_path }}</div>
            <div v-if="migPreview.resolved.users_db_path">users.db: {{ migPreview.resolved.users_db_path }}</div>
          </div>

          <div class="grid grid-cols-2 gap-2 pt-1">
            <div class="rounded bg-cinema-card/70 px-2.5 py-1.5">
              媒体库：<strong>{{ migPreview.libraries_found }}</strong>（新建 {{ migPreview.new_libraries_to_create }}）
            </div>
            <div class="rounded bg-cinema-card/70 px-2.5 py-1.5">
              影视条目：<strong>{{ migPreview.items_found }}</strong>
              <span class="text-[11px] text-slate-400 ml-1">
                (电影 {{ migPreview.movies_found }} / 剧集 {{ migPreview.series_found }} / 季 {{ migPreview.seasons_found }} / 集 {{ migPreview.episodes_found }})
              </span>
            </div>
            <div class="rounded bg-cinema-card/70 px-2.5 py-1.5">
              旧用户：<strong>{{ migPreview.users_found }}</strong>（新建 {{ migPreview.new_users_to_create }}）
            </div>
            <div class="rounded bg-cinema-card/70 px-2.5 py-1.5">
              观看记录：<strong>{{ migPreview.watch_records_found }}</strong>（预计可关联 <strong>{{ migPreview.matched_watch_records }}</strong>）
            </div>
          </div>

          <div v-if="migPreview.libraries && migPreview.libraries.length > 0" class="space-y-1 pt-1">
            <div class="text-[11px] text-slate-400">识别到的媒体库（路径已应用前缀映射）：</div>
            <div class="max-h-36 overflow-y-auto space-y-1">
              <div
                v-for="lib in migPreview.libraries"
                :key="lib.name"
                class="rounded border border-cinema-border/60 px-2 py-1 flex flex-col gap-0.5"
              >
                <div class="flex items-center justify-between gap-2">
                  <span class="font-medium text-white">{{ lib.name }}</span>
                  <div class="flex items-center gap-1.5">
                    <span class="text-[10px] px-1.5 py-0.5 rounded bg-slate-800 text-slate-300">{{ lib.kind }}</span>
                    <span
                      class="text-[10px] px-1.5 py-0.5 rounded"
                      :class="lib.exists ? 'bg-amber-950 text-amber-300' : 'bg-emerald-950 text-emerald-300'"
                    >
                      {{ lib.exists ? '已存在（合并路径）' : '将新建' }}
                    </span>
                  </div>
                </div>
                <div class="text-[11px] font-mono text-slate-400 break-all">
                  {{ (lib.paths || []).join(' ; ') || '（无目录路径）' }}
                </div>
              </div>
            </div>
          </div>

          <div v-if="migPreview.warnings && migPreview.warnings.length > 0" class="pt-1 space-y-0.5 text-amber-300">
            <div class="font-medium">注意事项：</div>
            <div v-for="(w, i) in migPreview.warnings" :key="i">• {{ w }}</div>
          </div>
        </div>
      </n-card>
    </div>

    <!-- API Keys & Audit Logs -->
    <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
      <n-card size="small" title="API Key 管理（供外部脚本/工具调用标准接口）">
        <div class="flex gap-2 mb-3">
          <n-input v-model:value="newKeyName" size="small" placeholder="输入密钥备注名（如 MoviePilot）" />
          <n-button size="small" type="primary" @click="createKey">生成 Key</n-button>
        </div>
        <div v-if="createdPlainKey" class="mb-3 p-2.5 rounded-lg bg-emerald-950/60 border border-emerald-600/60 text-xs space-y-1">
          <div class="text-emerald-300 font-medium">新 API Key（仅展示一次，请立即复制保存）：</div>
          <div class="font-mono text-white break-all">{{ createdPlainKey }}</div>
        </div>
        <div v-if="apiKeys.length === 0" class="py-4 text-center text-xs text-slate-500">暂无 API Key</div>
        <div v-else class="space-y-2">
          <div
            v-for="k in apiKeys"
            :key="k.id"
            class="rounded-lg border border-cinema-border bg-cinema-bg px-3 py-2 flex items-center justify-between"
          >
            <div>
              <div class="text-xs font-medium text-white">{{ k.name }}</div>
              <div class="text-[11px] text-slate-400">创建于 {{ formatTime(k.created_at) }}</div>
            </div>
            <n-button size="tiny" type="error" quaternary @click="deleteKey(k.id)">吊销</n-button>
          </div>
        </div>
      </n-card>

      <n-card size="small" title="管理员操作审计日志">
        <template #header-extra>
          <n-button size="tiny" secondary @click="load">刷新</n-button>
        </template>
        <div v-if="auditLogs.length === 0" class="py-6 text-center text-xs text-slate-500">暂无审计日志</div>
        <div v-else class="space-y-1.5 max-h-72 overflow-y-auto">
          <div
            v-for="log in auditLogs"
            :key="log.id"
            class="rounded border border-cinema-border/60 bg-cinema-bg px-2.5 py-1.5 text-xs flex items-center justify-between gap-2"
          >
            <div class="min-w-0 truncate">
              <span class="text-emerald-400 font-mono">{{ log.action }}</span>
              <span class="text-slate-300 ml-1.5">{{ log.username || 'admin' }}</span>
              <span class="text-slate-500 ml-1.5">{{ log.target_id }}</span>
            </div>
            <span class="text-[10px] text-slate-500 shrink-0">{{ formatTime(log.created_at) }}</span>
          </div>
        </div>
      </n-card>
    </div>

    <DirectoryPicker
      v-model:show="pickerOpen"
      :title="pickerTitle"
      :selected-paths="pickerSelected"
      @select="onPickerSelect"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NCard, NCheckbox, NForm, NFormItem, NInput, useMessage } from 'naive-ui'
import { apiFetch, getToken } from '../api/client'
import DirectoryPicker from './DirectoryPicker.vue'

const message = useMessage()
const backups = ref<any[]>([])
const backupDir = ref('')
const backupDialect = ref('sqlite')
const apiKeys = ref<any[]>([])
const auditLogs = ref<any[]>([])

const newKeyName = ref('')
const createdPlainKey = ref('')

const showAdvancedMig = ref(false)
const migForm = reactive({
  emby_config_dir: '',
  users_db_path: '',
  library_db_path: '',
  root_default_dir: '',
  default_password: '',
  import_libraries: true,
  import_items: true,
  import_users: true,
  import_watch: true,
  overwrite: false,
  path_mappings: [] as { from: string; to: string }[],
})
const migPreview = ref<any>(null)
const migLoading = ref(false)

// Visual directory picker state
const pickerOpen = ref(false)
const pickerTarget = ref<'config' | 'root' | 'mapping'>('config')
const pickerMappingIdx = ref(-1)

const pickerTitle = computed(() => {
  if (pickerTarget.value === 'config') return '选择原 Emby 配置根目录'
  if (pickerTarget.value === 'root') return '选择 Emby root/default 目录'
  return '选择映射后的本机目录'
})

const pickerSelected = computed(() => {
  if (pickerTarget.value === 'config' && migForm.emby_config_dir) return [migForm.emby_config_dir]
  if (pickerTarget.value === 'root' && migForm.root_default_dir) return [migForm.root_default_dir]
  if (pickerTarget.value === 'mapping' && pickerMappingIdx.value >= 0) {
    const val = migForm.path_mappings[pickerMappingIdx.value]?.to
    return val ? [val] : []
  }
  return []
})

function openPicker(target: 'config' | 'root' | 'mapping', idx = -1) {
  pickerTarget.value = target
  pickerMappingIdx.value = idx
  pickerOpen.value = true
}

function onPickerSelect(path: string) {
  if (pickerTarget.value === 'config') {
    migForm.emby_config_dir = path
  } else if (pickerTarget.value === 'root') {
    migForm.root_default_dir = path
  } else if (pickerTarget.value === 'mapping' && pickerMappingIdx.value >= 0) {
    migForm.path_mappings[pickerMappingIdx.value].to = path
  }
  pickerOpen.value = false
}

function addPathMapping() {
  migForm.path_mappings.push({ from: '', to: '' })
}

function removePathMapping(idx: number) {
  migForm.path_mappings.splice(idx, 1)
}

function buildMigrationPayload() {
  return {
    emby_config_dir: migForm.emby_config_dir.trim(),
    users_db_path: migForm.users_db_path.trim(),
    library_db_path: migForm.library_db_path.trim(),
    root_default_dir: migForm.root_default_dir.trim(),
    default_password: migForm.default_password,
    import_libraries: migForm.import_libraries,
    import_items: migForm.import_items,
    import_users: migForm.import_users,
    import_watch: migForm.import_watch,
    overwrite: migForm.overwrite,
    path_mappings: migForm.path_mappings
      .map((m) => ({ from: m.from.trim(), to: m.to.trim() }))
      .filter((m) => m.from && m.to),
  }
}

function formatTime(iso: string) {
  return iso ? new Date(iso).toLocaleString() : ''
}

async function load() {
  const [b, k, a] = await Promise.all([
    apiFetch('/api/admin/backups'),
    apiFetch('/api/admin/keys'),
    apiFetch('/api/admin/audit-logs'),
  ])
  backups.value = b.items || []
  backupDir.value = b.backup_dir || ''
  backupDialect.value = b.dialect || 'sqlite'
  apiKeys.value = k.items || []
  auditLogs.value = a.items || []
}

async function createBackup(format: string) {
  try {
    await apiFetch('/api/admin/backups', { method: 'POST', body: JSON.stringify({ format }) })
    message.success('备份已创建')
    await load()
  } catch (e: any) {
    message.error(e.message || '备份失败')
  }
}

function downloadBackup(filename: string) {
  window.open(`/api/admin/backups/${encodeURIComponent(filename)}/download?api_key=${encodeURIComponent(getToken())}`, '_blank')
}

async function restoreBackup(filename: string) {
  if (!window.confirm(`确认从 ${filename} 恢复数据到当前数据库？`)) return
  try {
    const res = await apiFetch(`/api/admin/backups/${encodeURIComponent(filename)}/restore`, {
      method: 'POST',
      body: JSON.stringify({ confirm: true }),
    })
    message.success(`恢复完成（用户 ${res.counts?.users ?? 0}，条目 ${res.counts?.items ?? 0}）`)
    await load()
  } catch (e: any) {
    message.error(e.message || '恢复失败')
  }
}

async function deleteBackup(filename: string) {
  await apiFetch(`/api/admin/backups/${encodeURIComponent(filename)}`, { method: 'DELETE' })
  message.success('备份已删除')
  await load()
}

async function previewMigration() {
  migLoading.value = true
  try {
    migPreview.value = await apiFetch('/api/admin/migration/emby/preview', {
      method: 'POST',
      body: JSON.stringify(buildMigrationPayload()),
    })
    message.success('预检完成')
  } catch (e: any) {
    message.error(e.message || '预检失败')
  } finally {
    migLoading.value = false
  }
}

async function runMigration() {
  migLoading.value = true
  try {
    const res = await apiFetch('/api/admin/migration/emby/import', {
      method: 'POST',
      body: JSON.stringify(buildMigrationPayload()),
    })
    message.success(
      `迁移完成：媒体库 +${res.libraries_created}，影视条目 +${res.items_imported}，用户 +${res.users_created}，观看记录 +${res.watch_imported}`,
    )
    await load()
  } catch (e: any) {
    message.error(e.message || '迁移失败')
  } finally {
    migLoading.value = false
  }
}

async function createKey() {
  if (!newKeyName.value.trim()) return
  const res = await apiFetch('/api/admin/keys', {
    method: 'POST',
    body: JSON.stringify({ name: newKeyName.value.trim() }),
  })
  createdPlainKey.value = res.key
  newKeyName.value = ''
  message.success('API Key 已生成')
  await load()
}

async function deleteKey(id: string) {
  await apiFetch(`/api/admin/keys/${id}`, { method: 'DELETE' })
  message.success('API Key 已吊销')
  await load()
}

onMounted(load)
</script>
