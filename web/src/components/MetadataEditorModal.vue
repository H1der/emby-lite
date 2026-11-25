<template>
  <n-modal
    :show="show"
    preset="card"
    title="编辑元数据与字段锁定"
    class="max-w-2xl w-full"
    @update:show="emit('update:show', $event)"
  >
    <div v-if="loading" class="py-10 text-center text-slate-400">正在加载元数据...</div>
    <n-form v-else label-placement="top" size="small" @submit.prevent="save">
      <div class="text-xs text-slate-400 mb-4">
        勾选字段右侧的 <strong>🔒 锁定</strong> 后，后续重新扫描 NFO 或 TMDB 刮削将不会覆盖该字段。
      </div>

      <div class="grid grid-cols-1 md:grid-cols-2 gap-x-4">
        <n-form-item>
          <template #label>
            <div class="flex items-center justify-between w-full">
              <span>标题</span>
              <n-checkbox :checked="isLocked('name')" @update:checked="toggleLock('name', $event)">🔒 锁定</n-checkbox>
            </div>
          </template>
          <n-input v-model:value="form.name" />
        </n-form-item>

        <n-form-item>
          <template #label>
            <div class="flex items-center justify-between w-full">
              <span>原标题</span>
              <n-checkbox :checked="isLocked('original_name')" @update:checked="toggleLock('original_name', $event)">🔒 锁定</n-checkbox>
            </div>
          </template>
          <n-input v-model:value="form.original_name" />
        </n-form-item>

        <n-form-item>
          <template #label>
            <div class="flex items-center justify-between w-full">
              <span>年份</span>
              <n-checkbox :checked="isLocked('year')" @update:checked="toggleLock('year', $event)">🔒 锁定</n-checkbox>
            </div>
          </template>
          <n-input-number v-model:value="form.year" :min="0" :max="2100" class="w-full" />
        </n-form-item>

        <n-form-item>
          <template #label>
            <div class="flex items-center justify-between w-full">
              <span>首映日期 (YYYY-MM-DD)</span>
              <n-checkbox :checked="isLocked('premiere_date')" @update:checked="toggleLock('premiere_date', $event)">🔒 锁定</n-checkbox>
            </div>
          </template>
          <n-input v-model:value="form.premiere_date" placeholder="2024-05-01" />
        </n-form-item>

        <n-form-item>
          <template #label>
            <div class="flex items-center justify-between w-full">
              <span>评分</span>
              <n-checkbox :checked="isLocked('community_rating')" @update:checked="toggleLock('community_rating', $event)">🔒 锁定</n-checkbox>
            </div>
          </template>
          <n-input-number v-model:value="form.community_rating" :min="0" :max="10" :step="0.1" class="w-full" />
        </n-form-item>

        <n-form-item>
          <template #label>
            <div class="flex items-center justify-between w-full">
              <span>内容分级 (MPAA)</span>
              <n-checkbox :checked="isLocked('official_rating')" @update:checked="toggleLock('official_rating', $event)">🔒 锁定</n-checkbox>
            </div>
          </template>
          <n-input v-model:value="form.official_rating" placeholder="PG-13 / TV-MA" />
        </n-form-item>
      </div>

      <n-form-item>
        <template #label>
          <div class="flex items-center justify-between w-full">
            <span>类型（逗号分隔）</span>
            <n-checkbox :checked="isLocked('genres')" @update:checked="toggleLock('genres', $event)">🔒 锁定</n-checkbox>
          </div>
        </template>
        <n-input v-model:value="form.genresText" placeholder="科幻, 冒险, 剧情" />
      </n-form-item>

      <n-form-item label="标签（逗号分隔）">
        <n-input v-model:value="form.tagsText" placeholder="高分, 4K, 收藏" />
      </n-form-item>

      <n-form-item>
        <template #label>
          <div class="flex items-center justify-between w-full">
            <span>简介</span>
            <n-checkbox :checked="isLocked('overview')" @update:checked="toggleLock('overview', $event)">🔒 锁定</n-checkbox>
          </div>
        </template>
        <n-input v-model:value="form.overview" type="textarea" :rows="4" />
      </n-form-item>

      <div class="flex flex-wrap items-center justify-between gap-3 pt-2">
        <n-checkbox v-model:checked="form.writeNFO">同时回写本地 NFO 文件</n-checkbox>
        <div class="flex items-center gap-2">
          <n-button secondary :loading="scraping" @click="rescrape">重新刮削</n-button>
          <n-button type="primary" :loading="saving" attr-type="submit">保存元数据</n-button>
        </div>
      </div>
    </n-form>
  </n-modal>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { NButton, NCheckbox, NForm, NFormItem, NInput, NInputNumber, NModal, useMessage } from 'naive-ui'
import { apiFetch } from '../api/client'

const props = defineProps<{
  show: boolean
  itemId: string
}>()

const emit = defineEmits<{
  (e: 'update:show', val: boolean): void
  (e: 'saved'): void
}>()

const message = useMessage()
const loading = ref(false)
const saving = ref(false)
const scraping = ref(false)

const form = reactive({
  name: '',
  original_name: '',
  sort_name: '',
  overview: '',
  tagline: '',
  year: 0,
  premiere_date: '',
  community_rating: 0,
  official_rating: '',
  genresText: '',
  tagsText: '',
  lockedFields: [] as string[],
  writeNFO: false,
})

function isLocked(field: string) {
  return form.lockedFields.includes(field)
}

function toggleLock(field: string, checked: boolean) {
  const idx = form.lockedFields.indexOf(field)
  if (checked && idx < 0) form.lockedFields.push(field)
  if (!checked && idx >= 0) form.lockedFields.splice(idx, 1)
}

async function load() {
  if (!props.itemId) return
  loading.value = true
  try {
    const data = await apiFetch(`/api/admin/metadata/${props.itemId}`)
    form.name = data.name || ''
    form.original_name = data.original_name || ''
    form.sort_name = data.sort_name || ''
    form.overview = data.overview || ''
    form.tagline = data.tagline || ''
    form.year = data.year || 0
    form.premiere_date = data.premiere_date || ''
    form.community_rating = data.community_rating || 0
    form.official_rating = data.official_rating || ''
    form.genresText = (data.genres || []).join(', ')
    form.tagsText = (data.tags || []).join(', ')
    form.lockedFields = [...(data.locked_fields || [])]
    form.writeNFO = false
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  try {
    const genres = form.genresText.split(/[,，]/).map((s) => s.trim()).filter(Boolean)
    const tags = form.tagsText.split(/[,，]/).map((s) => s.trim()).filter(Boolean)
    await apiFetch(`/api/admin/metadata/${props.itemId}`, {
      method: 'PUT',
      body: JSON.stringify({
        name: form.name,
        original_name: form.original_name,
        sort_name: form.sort_name,
        overview: form.overview,
        tagline: form.tagline,
        year: form.year,
        premiere_date: form.premiere_date,
        community_rating: form.community_rating,
        official_rating: form.official_rating,
        genres,
        update_genres: true,
        tags,
        update_tags: true,
        locked_fields: form.lockedFields,
        write_nfo: form.writeNFO,
      }),
    })
    message.success('元数据与字段锁已保存')
    emit('saved')
    emit('update:show', false)
  } catch (e: any) {
    message.error(e.message || '保存失败')
  } finally {
    saving.value = false
  }
}

async function rescrape() {
  scraping.value = true
  try {
    const res = await apiFetch(`/api/admin/items/${props.itemId}/scrape?force=true`, { method: 'POST' })
    message.success(res.outcome === 'queued' ? '低置信度，已进入后台人工匹配队列' : '重新刮削完成（已跳过锁定字段）')
    await load()
    emit('saved')
  } catch (e: any) {
    message.error(e.message || '刮削失败')
  } finally {
    scraping.value = false
  }
}

watch(
  () => props.show,
  (visible) => {
    if (visible) load()
  },
)
</script>
