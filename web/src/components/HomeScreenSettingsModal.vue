<template>
  <n-modal
    :show="show"
    preset="card"
    :title="modalTitle"
    class="max-w-3xl"
    @update:show="emit('update:show', $event)"
  >
    <div class="space-y-4">
      <div class="text-xs text-ink-muted leading-relaxed">
        拖动或点击上下按钮调整媒体库展示顺序，并勾选每个媒体库是否在「我的媒体库」「继续观看」「正在观看（接下来观看）」「最近添加」中显示。设置会同步生效于 Web 端与 Emby 客户端。
      </div>

      <div v-if="loading" class="py-10 text-center text-sm text-ink-muted">
        正在加载主屏幕设置...
      </div>

      <div v-else-if="items.length === 0" class="py-10 text-center text-sm text-ink-muted">
        当前没有可配置的可见媒体库。
      </div>

      <div v-else class="space-y-2.5 max-h-[56vh] overflow-y-auto pr-1">
        <!-- Header Row (Desktop) -->
        <div class="hidden sm:grid sm:grid-cols-[1fr_repeat(4,76px)_68px] items-center gap-2 px-3 py-1.5 text-[11px] font-semibold text-ink-muted">
          <span>媒体库（拖拽排序）</span>
          <span class="text-center">我的媒体库</span>
          <span class="text-center">继续观看</span>
          <span class="text-center" title="控制首页「接下来观看 / 正在观看」板块">正在观看</span>
          <span class="text-center">最近添加</span>
          <span class="text-center">排序</span>
        </div>

        <div
          v-for="(lib, idx) in items"
          :key="lib.id"
          draggable="true"
          class="rounded-2xl neu-inset px-3 py-2.5 transition border border-transparent"
          :class="dragOverIdx === idx ? 'border-sky-400/70 bg-sky-500/10' : ''"
          @dragstart="onDragStart(idx)"
          @dragover.prevent="dragOverIdx = idx"
          @dragleave="onDragLeave(idx)"
          @drop.prevent="onDrop(idx)"
          @dragend="onDragEnd"
        >
          <div class="grid grid-cols-1 sm:grid-cols-[1fr_repeat(4,76px)_68px] items-center gap-2.5">
            <!-- Name + Drag Grip -->
            <div class="flex items-center gap-2 min-w-0 cursor-grab active:cursor-grabbing">
              <span class="text-ink-muted select-none text-sm leading-none" title="拖动调整顺序">⠿</span>
              <span class="text-sm font-semibold text-ink truncate">{{ lib.name }}</span>
              <n-tag v-if="lib.is_virtual" size="tiny" type="info">虚拟库</n-tag>
            </div>

            <!-- Mobile labels + Desktop centered checkboxes -->
            <div class="flex sm:justify-center items-center justify-between gap-1.5">
              <span class="sm:hidden text-xs text-ink-secondary">我的媒体库</span>
              <div class="flex items-center gap-1" :title="!lib.global_show_in_my_media ? '管理员已全局关闭此项' : undefined">
                <n-checkbox
                  v-model:checked="lib.show_in_my_media"
                  :disabled="!lib.global_show_in_my_media"
                />
                <span v-if="!lib.global_show_in_my_media" class="text-[10px] text-amber-500">全局关</span>
              </div>
            </div>

            <div class="flex sm:justify-center items-center justify-between gap-1.5">
              <span class="sm:hidden text-xs text-ink-secondary">继续观看</span>
              <div class="flex items-center gap-1" :title="!lib.global_show_in_resume ? '管理员已全局关闭此项' : undefined">
                <n-checkbox
                  v-model:checked="lib.show_in_resume"
                  :disabled="lib.is_virtual || !lib.global_show_in_resume"
                />
                <span v-if="!lib.is_virtual && !lib.global_show_in_resume" class="text-[10px] text-amber-500">全局关</span>
              </div>
            </div>

            <div class="flex sm:justify-center items-center justify-between gap-1.5">
              <span class="sm:hidden text-xs text-ink-secondary">正在观看</span>
              <div class="flex items-center gap-1" :title="!lib.global_show_in_next_up ? '管理员已全局关闭此项' : undefined">
                <n-checkbox
                  v-model:checked="lib.show_in_next_up"
                  :disabled="lib.is_virtual || !lib.global_show_in_next_up"
                />
                <span v-if="!lib.is_virtual && !lib.global_show_in_next_up" class="text-[10px] text-amber-500">全局关</span>
              </div>
            </div>

            <div class="flex sm:justify-center items-center justify-between gap-1.5">
              <span class="sm:hidden text-xs text-ink-secondary">最近添加</span>
              <div class="flex items-center gap-1" :title="!lib.global_show_in_latest ? '管理员已全局关闭此项' : undefined">
                <n-checkbox
                  v-model:checked="lib.show_in_latest"
                  :disabled="lib.is_virtual || !lib.global_show_in_latest"
                />
                <span v-if="!lib.is_virtual && !lib.global_show_in_latest" class="text-[10px] text-amber-500">全局关</span>
              </div>
            </div>

            <!-- Up / Down buttons -->
            <div class="flex items-center justify-end sm:justify-center gap-1">
              <n-button size="tiny" secondary :disabled="idx === 0" title="上移" @click="moveItem(idx, -1)">
                ↑
              </n-button>
              <n-button size="tiny" secondary :disabled="idx === items.length - 1" title="下移" @click="moveItem(idx, 1)">
                ↓
              </n-button>
            </div>
          </div>
        </div>
      </div>

      <div class="pt-2 border-t border-slate-200/70 dark:border-white/10 flex flex-wrap items-center justify-between gap-3">
        <n-checkbox v-model:checked="hidePlayedInLatest">
          在「最近添加」中隐藏已观看内容
        </n-checkbox>

        <div class="flex items-center gap-2 ml-auto">
          <n-button
            v-if="hasCustomOrder || orderDirty"
            size="small"
            quaternary
            :loading="saving"
            @click="resetOrder"
          >
            恢复默认排序
          </n-button>
          <n-button size="small" secondary @click="emit('update:show', false)">取消</n-button>
          <n-button size="small" type="primary" :loading="saving" @click="save">保存设置</n-button>
        </div>
      </div>
    </div>
  </n-modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NButton, NCheckbox, NModal, NTag, useMessage } from 'naive-ui'
import { apiFetch, invalidateApiCache } from '../api/client'

interface HomeLibrarySetting {
  id: string
  name: string
  kind: string
  is_virtual: boolean
  show_in_my_media: boolean
  show_in_resume: boolean
  show_in_next_up: boolean
  show_in_latest: boolean
  global_show_in_my_media: boolean
  global_show_in_resume: boolean
  global_show_in_next_up: boolean
  global_show_in_latest: boolean
}

const props = defineProps<{
  show: boolean
  userId?: string
  username?: string
}>()

const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
  (e: 'saved'): void
}>()

const message = useMessage()
const loading = ref(false)
const saving = ref(false)
const items = ref<HomeLibrarySetting[]>([])
const hidePlayedInLatest = ref(false)
const hasCustomOrder = ref(false)
const orderDirty = ref(false)
const dragIdx = ref(-1)
const dragOverIdx = ref(-1)

const modalTitle = computed(() =>
  props.username ? `主屏幕设置 — ${props.username}` : '主屏幕与媒体库排序设置',
)

function apiUrl() {
  if (props.userId) {
    return `/api/web/home-settings?user_id=${encodeURIComponent(props.userId)}`
  }
  return '/api/web/home-settings'
}

async function load() {
  loading.value = true
  try {
    const res = await apiFetch(apiUrl())
    items.value = (res.libraries || []).map((l: HomeLibrarySetting) => ({
      ...l,
      show_in_my_media: l.global_show_in_my_media ? Boolean(l.show_in_my_media) : false,
      show_in_resume: l.global_show_in_resume ? Boolean(l.show_in_resume) : false,
      show_in_next_up: l.global_show_in_next_up ? Boolean(l.show_in_next_up) : false,
      show_in_latest: l.global_show_in_latest ? Boolean(l.show_in_latest) : false,
    }))
    hidePlayedInLatest.value = Boolean(res.hide_played_in_latest)
    hasCustomOrder.value = Boolean(res.has_custom_order)
    orderDirty.value = false
  } catch (e: any) {
    message.error(e.message || '加载主屏幕设置失败')
  } finally {
    loading.value = false
  }
}

function moveItem(index: number, delta: number) {
  const target = index + delta
  if (target < 0 || target >= items.value.length) return
  const arr = [...items.value]
  const [moved] = arr.splice(index, 1)
  arr.splice(target, 0, moved)
  items.value = arr
  orderDirty.value = true
}

function onDragStart(idx: number) {
  dragIdx.value = idx
}

function onDragLeave(idx: number) {
  if (dragOverIdx.value === idx) {
    dragOverIdx.value = -1
  }
}

function onDrop(targetIdx: number) {
  if (dragIdx.value < 0 || dragIdx.value === targetIdx) {
    onDragEnd()
    return
  }
  const arr = [...items.value]
  const [moved] = arr.splice(dragIdx.value, 1)
  arr.splice(targetIdx, 0, moved)
  items.value = arr
  orderDirty.value = true
  onDragEnd()
}

function onDragEnd() {
  dragIdx.value = -1
  dragOverIdx.value = -1
}

async function resetOrder() {
  saving.value = true
  try {
    await apiFetch(apiUrl(), {
      method: 'PUT',
      body: JSON.stringify({ reset_order: true }),
    })
    invalidateApiCache('/api/web/home')
    invalidateApiCache('/emby/')
    window.dispatchEvent(new CustomEvent('emby-lite:home-settings-updated'))
    message.success('已恢复全局默认媒体库顺序')
    emit('saved')
    await load()
  } catch (e: any) {
    message.error(e.message || '恢复默认排序失败')
  } finally {
    saving.value = false
  }
}

async function save() {
  saving.value = true
  try {
    const payload: Record<string, any> = {
      hide_played_in_latest: hidePlayedInLatest.value,
      libraries: items.value.map((l) => ({
        id: l.id,
        show_in_my_media: l.global_show_in_my_media ? l.show_in_my_media : true,
        show_in_resume: l.global_show_in_resume ? l.show_in_resume : true,
        show_in_next_up: l.global_show_in_next_up ? l.show_in_next_up : true,
        show_in_latest: l.global_show_in_latest ? l.show_in_latest : true,
      })),
    }
    if (orderDirty.value || hasCustomOrder.value) {
      payload.ordered_views = items.value.map((l) => l.id)
    }
    await apiFetch(apiUrl(), {
      method: 'PUT',
      body: JSON.stringify(payload),
    })
    invalidateApiCache('/api/web/home')
    invalidateApiCache('/emby/')
    window.dispatchEvent(new CustomEvent('emby-lite:home-settings-updated'))
    message.success('主屏幕设置已保存')
    emit('saved')
    emit('update:show', false)
  } catch (e: any) {
    message.error(e.message || '保存失败')
  } finally {
    saving.value = false
  }
}

watch(
  () => props.show,
  (open) => {
    if (open) load()
  },
)
</script>
