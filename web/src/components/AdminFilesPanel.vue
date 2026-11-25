<template>
  <div class="space-y-4 mt-2">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <div class="flex flex-wrap items-center gap-1.5">
        <span class="text-xs text-slate-400">允许根目录：</span>
        <n-button
          v-for="r in roots"
          :key="r"
          size="tiny"
          :type="current === r ? 'primary' : 'default'"
          secondary
          @click="browse(r)"
        >
          {{ r }}
        </n-button>
      </div>
      <div class="flex items-center gap-2">
        <n-button size="small" secondary :disabled="!current" @click="browse(parent)">↑ 上级</n-button>
        <n-button size="small" secondary :disabled="!current" @click="showMkdirModal = true">+ 新建目录</n-button>
        <n-button size="small" type="primary" secondary :disabled="!current" @click="triggerUpload">↑ 上传文件</n-button>
      </div>
    </div>

    <div class="px-3 py-1.5 rounded bg-cinema-bg border border-cinema-border text-xs font-mono text-slate-300 truncate">
      {{ current || '请选择一个媒体根目录进行浏览（严格限制在已配置媒体目录范围内）' }}
    </div>

    <div class="border border-cinema-border rounded-xl bg-cinema-bg divide-y divide-cinema-border/40 min-h-[280px]">
      <div v-if="loading" class="py-12 text-center text-sm text-slate-400">加载中...</div>
      <div v-else-if="entries.length === 0" class="py-12 text-center text-sm text-slate-500">当前目录为空</div>
      <div
        v-else
        v-for="e in entries"
        :key="e.path"
        class="flex flex-wrap items-center justify-between gap-3 px-3.5 py-2.5 hover:bg-cinema-card/70 transition"
      >
        <div
          class="flex items-center gap-2.5 min-w-0 flex-1"
          :class="e.is_dir ? 'cursor-pointer' : ''"
          @click="e.is_dir && browse(e.path)"
        >
          <span>{{ e.is_dir ? '📁' : '📄' }}</span>
          <span class="text-sm text-slate-100 truncate">{{ e.name }}</span>
          <span v-if="!e.is_dir" class="text-xs text-slate-500 shrink-0">{{ formatSize(e.size) }}</span>
        </div>
        <div v-if="current" class="flex items-center gap-1.5 shrink-0">
          <n-button v-if="!e.is_dir" size="tiny" secondary @click="downloadFile(e.path)">下载</n-button>
          <n-button size="tiny" secondary @click="openRename(e)">重命名</n-button>
          <n-button size="tiny" type="error" quaternary @click="removeEntry(e)">删除</n-button>
        </div>
      </div>
    </div>

    <input ref="uploadInput" type="file" class="hidden" @change="onUploadFile" />

    <!-- Mkdir Modal -->
    <n-modal v-model:show="showMkdirModal" preset="card" title="新建子目录" class="max-w-sm">
      <n-form @submit.prevent="doMkdir">
        <n-form-item label="目录名">
          <n-input v-model:value="newDirName" placeholder="例如：Season 02" />
        </n-form-item>
        <n-button type="primary" block attr-type="submit">创建</n-button>
      </n-form>
    </n-modal>

    <!-- Rename Modal -->
    <n-modal v-model:show="showRenameModal" preset="card" title="重命名" class="max-w-sm">
      <n-form @submit.prevent="doRename">
        <n-form-item label="新名称">
          <n-input v-model:value="renameNewName" />
        </n-form-item>
        <n-button type="primary" block attr-type="submit">保存</n-button>
      </n-form>
    </n-modal>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NForm, NFormItem, NInput, NModal, useMessage } from 'naive-ui'
import { apiFetch, getToken } from '../api/client'

const message = useMessage()
const current = ref('')
const parent = ref('')
const roots = ref<string[]>([])
const entries = ref<any[]>([])
const loading = ref(false)

const showMkdirModal = ref(false)
const newDirName = ref('')
const showRenameModal = ref(false)
const renameTarget = ref('')
const renameNewName = ref('')
const uploadInput = ref<HTMLInputElement | null>(null)

function formatSize(b: number): string {
  if (b < 1024) return `${b} B`
  if (b < 1024 * 1024) return `${(b / 1024).toFixed(1)} KB`
  if (b < 1024 * 1024 * 1024) return `${(b / (1024 * 1024)).toFixed(1)} MB`
  return `${(b / (1024 * 1024 * 1024)).toFixed(2)} GB`
}

async function browse(path = '') {
  loading.value = true
  try {
    const q = path ? `?path=${encodeURIComponent(path)}` : ''
    const res = await apiFetch(`/api/files${q}`)
    current.value = res.current || ''
    parent.value = res.parent || ''
    roots.value = res.roots || []
    entries.value = res.entries || []
  } catch (e: any) {
    message.error(e.message || '读取目录失败')
  } finally {
    loading.value = false
  }
}

async function doMkdir() {
  if (!newDirName.value.trim()) return
  try {
    await apiFetch('/api/files/mkdir', {
      method: 'POST',
      body: JSON.stringify({ parent: current.value, name: newDirName.value.trim() }),
    })
    showMkdirModal.value = false
    newDirName.value = ''
    message.success('目录已创建')
    await browse(current.value)
  } catch (e: any) {
    message.error(e.message || '创建失败')
  }
}

function openRename(entry: any) {
  renameTarget.value = entry.path
  renameNewName.value = entry.name
  showRenameModal.value = true
}

async function doRename() {
  if (!renameNewName.value.trim()) return
  try {
    await apiFetch('/api/files/rename', {
      method: 'POST',
      body: JSON.stringify({ path: renameTarget.value, new_name: renameNewName.value.trim() }),
    })
    showRenameModal.value = false
    message.success('已重命名')
    await browse(current.value)
  } catch (e: any) {
    message.error(e.message || '重命名失败')
  }
}

function triggerUpload() {
  uploadInput.value?.click()
}

async function onUploadFile(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file || !current.value) return
  const fd = new FormData()
  fd.append('dir', current.value)
  fd.append('file', file)
  try {
    const res = await fetch('/api/files/upload', {
      method: 'POST',
      headers: { 'X-Emby-Token': getToken() },
      body: fd,
    })
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    message.success('上传成功')
    await browse(current.value)
  } catch (err: any) {
    message.error(err.message || '上传失败')
  } finally {
    input.value = ''
  }
}

function downloadFile(path: string) {
  const url = `/api/files/download?path=${encodeURIComponent(path)}&api_key=${encodeURIComponent(getToken())}`
  window.open(url, '_blank')
}

async function removeEntry(entry: any) {
  try {
    await apiFetch(`/api/files?path=${encodeURIComponent(entry.path)}`, { method: 'DELETE' })
    message.success('已删除')
    await browse(current.value)
  } catch (e: any) {
    message.error(e.message || '删除失败（非空目录请先清空）')
  }
}

onMounted(() => browse(''))
</script>
