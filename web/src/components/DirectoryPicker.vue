<template>
  <n-modal
    :show="show"
    preset="card"
    :title="title || '选择媒体目录'"
    class="max-w-2xl w-full"
    @update:show="emit('update:show', $event)"
  >
    <div class="space-y-3">
      <!-- Quick Roots (Drives / Allowed Media Roots) -->
      <div v-if="roots.length > 0" class="flex flex-wrap items-center gap-1.5">
        <span class="text-xs text-ink-muted mr-1">根目录：</span>
        <n-button
          v-for="r in roots"
          :key="r.path"
          size="tiny"
          :type="current === r.path ? 'primary' : 'default'"
          secondary
          @click="browse(r.path)"
        >
          {{ r.name }}
        </n-button>
      </div>

      <!-- Current Path & Navigation -->
      <div class="flex items-center gap-2">
        <n-button size="small" secondary :disabled="!current" @click="browse(parent)">
          ↑ 上级目录
        </n-button>
        <div
          class="flex-1 px-3 py-1.5 rounded-xl neu-inset text-xs text-ink font-mono truncate"
          :title="current || '根目录'"
        >
          {{ current || '请选择磁盘或根目录' }}
        </div>
        <n-button size="small" type="primary" :disabled="!current" @click="selectPath(current)">
          选择当前目录
        </n-button>
      </div>

      <!-- Filter + Show hidden toggle -->
      <div class="flex items-center justify-between gap-2">
        <n-input v-model:value="filterText" size="small" placeholder="筛选当前目录下的子文件夹..." clearable />
        <n-checkbox v-model:checked="showHidden" class="shrink-0 text-xs" @update:checked="browse(current)">
          显示隐藏目录
        </n-checkbox>
      </div>

      <!-- Directory Listing -->
      <div class="neu-inset rounded-2xl h-72 overflow-y-auto divide-y divide-slate-200/60 dark:divide-white/10">
        <div v-if="loading" class="h-full flex items-center justify-center text-sm text-ink-muted">
          正在读取目录...
        </div>
        <div v-else-if="errorMsg" class="h-full flex items-center justify-center text-sm text-rose-500 px-4 text-center">
          {{ errorMsg }}
        </div>
        <div v-else-if="filteredEntries.length === 0" class="h-full flex items-center justify-center text-sm text-ink-muted">
          此目录下没有子文件夹（可点击右上角“选择当前目录”）
        </div>
        <div
          v-else
          v-for="entry in filteredEntries"
          :key="entry.path"
          class="flex items-center justify-between gap-2 px-3.5 py-2.5 hover:bg-white/60 dark:hover:bg-white/5 transition cursor-pointer"
          @click="browse(entry.path)"
        >
          <div class="flex items-center gap-2 min-w-0">
            <span class="text-amber-500 shrink-0">📁</span>
            <span class="text-sm text-ink truncate">{{ entry.name }}</span>
          </div>
          <div class="flex items-center gap-1.5 shrink-0" @click.stop>
            <n-button
              size="tiny"
              :type="isSelected(entry.path) ? 'success' : 'primary'"
              secondary
              @click="selectPath(entry.path)"
            >
              {{ isSelected(entry.path) ? '已选 ✓' : '添加' }}
            </n-button>
          </div>
        </div>
      </div>

      <!-- Selected Summary Footer -->
      <div class="flex items-center justify-between pt-1">
        <div class="text-xs text-ink-muted">
          已选 {{ selectedPaths.length }} 个目录
        </div>
        <n-button type="primary" size="small" @click="emit('update:show', false)">
          完成选择
        </n-button>
      </div>
    </div>
  </n-modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NButton, NCheckbox, NInput, NModal } from 'naive-ui'
import { apiFetch } from '../api/client'

interface DirEntry {
  name: string
  path: string
}

const props = defineProps<{
  show: boolean
  selectedPaths: string[]
  title?: string
}>()

const emit = defineEmits<{
  (e: 'update:show', val: boolean): void
  (e: 'select', path: string): void
}>()

const current = ref('')
const parent = ref('')
const roots = ref<DirEntry[]>([])
const entries = ref<DirEntry[]>([])
const filterText = ref('')
const showHidden = ref(false)
const loading = ref(false)
const errorMsg = ref('')

const filteredEntries = computed(() => {
  const q = filterText.value.trim().toLowerCase()
  if (!q) return entries.value
  return entries.value.filter((e) => e.name.toLowerCase().includes(q))
})

function isSelected(path: string): boolean {
  return props.selectedPaths.includes(path)
}

function selectPath(path: string) {
  if (!path) return
  emit('select', path)
}

async function browse(targetPath: string) {
  loading.value = true
  errorMsg.value = ''
  filterText.value = ''
  try {
    const params = new URLSearchParams()
    if (targetPath) params.set('path', targetPath)
    if (showHidden.value) params.set('show_hidden', 'true')
    const res = await apiFetch<{
      current: string
      parent: string
      roots: DirEntry[]
      entries: DirEntry[]
    }>(`/api/admin/directories?${params.toString()}`)
    current.value = res.current || ''
    parent.value = res.parent || ''
    roots.value = res.roots || []
    entries.value = res.entries || []
  } catch (e: any) {
    errorMsg.value = e.message || '读取目录失败'
  } finally {
    loading.value = false
  }
}

watch(
  () => props.show,
  (visible) => {
    if (visible) {
      browse(current.value)
    }
  },
)
</script>
