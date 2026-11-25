<template>
  <div class="space-y-5">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="flex items-center gap-3">
        <h1 class="text-xl font-semibold">{{ title }}</h1>
        <span class="text-xs text-slate-400">共 {{ total }} 项</span>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <n-input
          v-model:value="search"
          size="small"
          placeholder="筛选关键词 / 拼音..."
          clearable
          class="w-48"
          @keyup.enter="load"
        />
        <n-select v-model:value="sortBy" size="small" :options="sortOptions" class="w-36" @update:value="load" />
        <n-button size="small" :type="favOnly ? 'primary' : 'default'" @click="toggleFav">
          {{ favOnly ? '仅看收藏 ✓' : '仅看收藏' }}
        </n-button>
      </div>
    </div>

    <div v-if="loading" class="py-16 text-center text-slate-400">加载中...</div>
    <div v-else-if="items.length === 0" class="py-16 text-center text-slate-400">没有匹配的影视内容</div>
    <div v-else class="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-5 lg:grid-cols-6 gap-4">
      <PosterCard v-for="item in items" :key="item.Id" :item="item" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { NButton, NInput, NSelect } from 'naive-ui'
import { apiFetch } from '../api/client'
import { useAuthStore } from '../stores/auth'
import PosterCard from '../components/PosterCard.vue'

const route = useRoute()
const auth = useAuthStore()

const title = ref('媒体库')
const items = ref<any[]>([])
const total = ref(0)
const loading = ref(false)
const search = ref(typeof route.query.q === 'string' ? route.query.q : '')
const sortBy = ref('DateCreated')
const favOnly = ref(false)

const sortOptions = [
  { label: '最近添加', value: 'DateCreated' },
  { label: '按名称', value: 'SortName' },
  { label: '按年份', value: 'ProductionYear' },
  { label: '按评分', value: 'CommunityRating' },
]

function toggleFav() {
  favOnly.value = !favOnly.value
  load()
}

async function load() {
  const uid = auth.user?.Id
  if (!uid) return
  loading.value = true
  try {
    const libId = String(route.params.id || '')
    const params = new URLSearchParams({
      Limit: '120',
      SortBy: sortBy.value,
      SortOrder: sortBy.value === 'SortName' ? 'Ascending' : 'Descending',
    })
    if (libId && libId !== 'all') {
      params.set('ParentId', libId)
      const info = await apiFetch(`/emby/Items/${libId}`).catch(() => null)
      if (info?.Name) title.value = info.Name
    } else {
      title.value = search.value ? `搜索：${search.value}` : '全部影视'
      params.set('Recursive', 'true')
      params.set('IncludeItemTypes', 'Movie,Series')
    }
    if (search.value.trim()) {
      params.set('SearchTerm', search.value.trim())
      params.set('Recursive', 'true')
    }
    if (favOnly.value) {
      params.set('IsFavorite', 'true')
    }
    const res = await apiFetch(`/emby/Users/${uid}/Items?${params.toString()}`)
    items.value = res?.Items || []
    total.value = res?.TotalRecordCount || items.value.length
  } finally {
    loading.value = false
  }
}

watch(
  () => [route.params.id, route.query.q],
  () => {
    if (typeof route.query.q === 'string') {
      search.value = route.query.q
    }
    load()
  },
)

onMounted(load)
</script>
