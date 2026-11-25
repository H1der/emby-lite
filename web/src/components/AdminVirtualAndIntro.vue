<template>
  <div class="grid grid-cols-1 lg:grid-cols-2 gap-6 mt-2">
    <!-- Virtual Libraries -->
    <n-card size="small" title="虚拟库 / 组合库">
      <template #header-extra>
        <n-button size="tiny" type="primary" @click="openCreateVLib">+ 新建虚拟库</n-button>
      </template>
      <div v-if="vlibs.length === 0" class="py-8 text-center text-sm text-slate-400">
        暂无虚拟库（可按类型、评分、年份、多库组合动态聚合展示，并同步呈现在 Emby 客户端）
      </div>
      <div v-else class="space-y-3">
        <div
          v-for="vl in vlibs"
          :key="vl.id"
          class="rounded-xl border border-cinema-border bg-cinema-bg p-3 flex flex-wrap items-center justify-between gap-2"
        >
          <div class="min-w-0">
            <div class="font-medium text-white">
              {{ vl.name }}
              <n-tag size="tiny" :type="vl.enabled ? 'success' : 'default'" class="ml-1.5">
                {{ vl.enabled ? '启用' : '停用' }}
              </n-tag>
            </div>
            <div class="text-xs text-slate-400 mt-0.5 truncate">
              类型：{{ (vl.rule?.genres || []).join('/') || '不限' }} ·
              最低评分：{{ vl.rule?.min_rating || '不限' }} ·
              年份：{{ vl.rule?.min_year || '*' }}–{{ vl.rule?.max_year || '*' }}
            </div>
          </div>
          <div class="flex items-center gap-1.5">
            <n-button size="tiny" secondary @click="generateCover(vl.id)">拼贴封面</n-button>
            <n-button size="tiny" secondary @click="openEditVLib(vl)">编辑</n-button>
            <n-button size="tiny" type="error" quaternary @click="deleteVLib(vl.id)">删除</n-button>
          </div>
        </div>
      </div>
    </n-card>

    <!-- Intro / Credits Markers -->
    <n-card size="small" title="片头 / 片尾跳过标记（Emby 客户端 + Web 播放器）">
      <template #header-extra>
        <n-button size="tiny" type="primary" @click="showIntroModal = true">+ 新增标记</n-button>
      </template>
      <div v-if="markers.length === 0" class="py-8 text-center text-sm text-slate-400">
        暂无跳过标记（可按剧集 ID + 季号批量配置，或按单集 ID 精确配置）
      </div>
      <div v-else class="space-y-2.5 max-h-96 overflow-y-auto">
        <div
          v-for="m in markers"
          :key="m.id"
          class="rounded-xl border border-cinema-border bg-cinema-bg p-3 flex items-center justify-between gap-3"
        >
          <div class="text-xs space-y-0.5 min-w-0">
            <div class="text-slate-100 font-medium truncate">
              {{ m.item_id ? `单集 ID: ${m.item_id}` : `剧集 ID: ${m.series_id} · 第 ${m.season_num} 季` }}
            </div>
            <div class="text-slate-400">
              片头：{{ (m.intro_start_ticks / 1e7).toFixed(1) }}s → {{ (m.intro_end_ticks / 1e7).toFixed(1) }}s
              <span v-if="m.credits_start_ticks > 0"> · 片尾：{{ (m.credits_start_ticks / 1e7).toFixed(1) }}s</span>
            </div>
          </div>
          <n-button size="tiny" type="error" quaternary @click="deleteMarker(m.id)">删除</n-button>
        </div>
      </div>
    </n-card>

    <!-- Virtual Library Modal -->
    <n-modal v-model:show="showVLibModal" preset="card" :title="editingVLibId ? '编辑虚拟库' : '新建虚拟库'" class="max-w-lg">
      <n-form label-placement="top" size="small" @submit.prevent="saveVLib">
        <n-form-item label="虚拟库名称">
          <n-input v-model:value="vlibForm.name" placeholder="例如：高分科幻 / 豆瓣精选" />
        </n-form-item>
        <n-form-item label="来源物理库（不选表示全部可见库）">
          <n-select v-model:value="vlibForm.library_ids" multiple :options="libraryOptions" />
        </n-form-item>
        <div class="grid grid-cols-2 gap-3">
          <n-form-item label="条目类型">
            <n-select v-model:value="vlibForm.item_types" multiple :options="typeOptions" />
          </n-form-item>
          <n-form-item label="类型筛选（逗号分隔）">
            <n-input v-model:value="vlibForm.genresText" placeholder="科幻, 悬疑" />
          </n-form-item>
          <n-form-item label="最低评分">
            <n-input-number v-model:value="vlibForm.min_rating" :min="0" :max="10" :step="0.5" class="w-full" />
          </n-form-item>
          <n-form-item label="默认排序">
            <n-select v-model:value="vlibForm.sort_by" :options="sortOptions" />
          </n-form-item>
          <n-form-item label="起始年份">
            <n-input-number v-model:value="vlibForm.min_year" :min="0" :max="2100" class="w-full" />
          </n-form-item>
          <n-form-item label="截止年份">
            <n-input-number v-model:value="vlibForm.max_year" :min="0" :max="2100" class="w-full" />
          </n-form-item>
        </div>
        <n-form-item>
          <n-checkbox v-model:checked="vlibForm.enabled">在首页与 Emby 客户端中启用</n-checkbox>
        </n-form-item>
        <n-button type="primary" block attr-type="submit">保存虚拟库</n-button>
      </n-form>
    </n-modal>

    <!-- Intro Marker Modal -->
    <n-modal v-model:show="showIntroModal" preset="card" title="新增片头 / 片尾跳过标记" class="max-w-md">
      <n-form label-placement="top" size="small" @submit.prevent="saveIntro">
        <n-form-item label="剧集 ID（按整季生效，与单集 ID 二选一）">
          <n-input v-model:value="introForm.series_id" placeholder="可在剧集详情页 URL 获取" />
        </n-form-item>
        <n-form-item label="季号（按剧集生效时填写）">
          <n-input-number v-model:value="introForm.season_num" :min="0" :max="99" class="w-full" />
        </n-form-item>
        <n-form-item label="或指定单集 / 电影 ID（优先于整季规则）">
          <n-input v-model:value="introForm.item_id" placeholder="留空表示按剧集+季生效" />
        </n-form-item>
        <div class="grid grid-cols-3 gap-2">
          <n-form-item label="片头开始(秒)">
            <n-input-number v-model:value="introForm.intro_start_secs" :min="0" class="w-full" />
          </n-form-item>
          <n-form-item label="片头结束(秒)">
            <n-input-number v-model:value="introForm.intro_end_secs" :min="0" class="w-full" />
          </n-form-item>
          <n-form-item label="片尾开始(秒)">
            <n-input-number v-model:value="introForm.credits_start_secs" :min="0" class="w-full" />
          </n-form-item>
        </div>
        <n-button type="primary" block attr-type="submit">保存跳过标记</n-button>
      </n-form>
    </n-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import {
  NButton, NCard, NCheckbox, NForm, NFormItem, NInput, NInputNumber, NModal, NSelect, NTag, useMessage,
} from 'naive-ui'
import { apiFetch } from '../api/client'

const props = defineProps<{ libraries: any[] }>()
const message = useMessage()

const vlibs = ref<any[]>([])
const markers = ref<any[]>([])

const showVLibModal = ref(false)
const editingVLibId = ref('')
const vlibForm = reactive({
  name: '',
  library_ids: [] as string[],
  item_types: [] as string[],
  genresText: '',
  min_rating: 0,
  min_year: 0,
  max_year: 0,
  sort_by: 'DateCreated',
  enabled: true,
})

const showIntroModal = ref(false)
const introForm = reactive({
  series_id: '',
  season_num: 1,
  item_id: '',
  intro_start_secs: 0,
  intro_end_secs: 90,
  credits_start_secs: 0,
})

const libraryOptions = computed(() => props.libraries.map((l) => ({ label: l.name, value: l.id })))
const typeOptions = [
  { label: '电影 (Movie)', value: 'Movie' },
  { label: '剧集 (Series)', value: 'Series' },
]
const sortOptions = [
  { label: '最近添加', value: 'DateCreated' },
  { label: '评分最高', value: 'CommunityRating' },
  { label: '按年份', value: 'ProductionYear' },
  { label: '按名称', value: 'SortName' },
]

async function load() {
  const [vl, mk] = await Promise.all([
    apiFetch('/api/admin/virtual-libraries'),
    apiFetch('/api/admin/intro-markers'),
  ])
  vlibs.value = vl.items || []
  markers.value = mk.items || []
}

function openCreateVLib() {
  editingVLibId.value = ''
  vlibForm.name = ''
  vlibForm.library_ids = []
  vlibForm.item_types = []
  vlibForm.genresText = ''
  vlibForm.min_rating = 0
  vlibForm.min_year = 0
  vlibForm.max_year = 0
  vlibForm.sort_by = 'DateCreated'
  vlibForm.enabled = true
  showVLibModal.value = true
}

function openEditVLib(vl: any) {
  editingVLibId.value = vl.id
  vlibForm.name = vl.name || ''
  vlibForm.library_ids = [...(vl.rule?.library_ids || [])]
  vlibForm.item_types = [...(vl.rule?.item_types || [])]
  vlibForm.genresText = (vl.rule?.genres || []).join(', ')
  vlibForm.min_rating = vl.rule?.min_rating || 0
  vlibForm.min_year = vl.rule?.min_year || 0
  vlibForm.max_year = vl.rule?.max_year || 0
  vlibForm.sort_by = vl.rule?.sort_by || 'DateCreated'
  vlibForm.enabled = Boolean(vl.enabled)
  showVLibModal.value = true
}

async function saveVLib() {
  if (!vlibForm.name.trim()) {
    message.warning('请输入虚拟库名称')
    return
  }
  const genres = vlibForm.genresText.split(/[,，]/).map((s) => s.trim()).filter(Boolean)
  const payload = {
    name: vlibForm.name.trim(),
    enabled: vlibForm.enabled,
    rule: {
      library_ids: vlibForm.library_ids,
      item_types: vlibForm.item_types,
      genres,
      min_rating: vlibForm.min_rating || 0,
      min_year: vlibForm.min_year || 0,
      max_year: vlibForm.max_year || 0,
      sort_by: vlibForm.sort_by,
    },
  }
  if (editingVLibId.value) {
    await apiFetch(`/api/admin/virtual-libraries/${editingVLibId.value}`, { method: 'PUT', body: JSON.stringify(payload) })
    message.success('虚拟库已更新')
  } else {
    await apiFetch('/api/admin/virtual-libraries', { method: 'POST', body: JSON.stringify(payload) })
    message.success('虚拟库已创建')
  }
  showVLibModal.value = false
  await load()
}

async function deleteVLib(id: string) {
  await apiFetch(`/api/admin/virtual-libraries/${id}`, { method: 'DELETE' })
  message.success('虚拟库已删除')
  await load()
}

async function generateCover(id: string) {
  const res = await apiFetch(`/api/admin/libraries/${id}/cover/generate`, { method: 'POST' })
  message.success(`拼贴封面已生成（使用了 ${res.poster_count ?? 0} 张海报）`)
  await load()
}

async function saveIntro() {
  if (!introForm.item_id.trim() && !introForm.series_id.trim()) {
    message.warning('请填写剧集 ID 或单集 ID')
    return
  }
  try {
    await apiFetch('/api/admin/intro-markers', {
      method: 'PUT',
      body: JSON.stringify({
        series_id: introForm.series_id.trim(),
        season_num: introForm.season_num,
        item_id: introForm.item_id.trim(),
        intro_start_secs: introForm.intro_start_secs,
        intro_end_secs: introForm.intro_end_secs,
        credits_start_secs: introForm.credits_start_secs,
      }),
    })
    showIntroModal.value = false
    message.success('跳过标记已保存')
    await load()
  } catch (e: any) {
    message.error(e.message || '保存失败')
  }
}

async function deleteMarker(id: string) {
  await apiFetch(`/api/admin/intro-markers/${id}`, { method: 'DELETE' })
  message.success('标记已删除')
  await load()
}

onMounted(load)
</script>
