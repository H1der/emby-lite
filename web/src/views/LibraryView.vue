<template>
  <div class="space-y-5">
    <!-- Frosted Glass Header & Multi-Facet Filter Bar -->
    <div class="glass-panel rounded-3xl p-4 sm:p-5 space-y-4">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="flex items-center gap-3 min-w-0">
          <h1 class="text-xl sm:text-2xl font-bold text-ink tracking-tight truncate">{{ title }}</h1>
          <span class="px-2.5 py-0.5 rounded-full text-xs font-semibold neu-inset text-ink-secondary shrink-0">
            {{ total }} 项
          </span>
        </div>

        <div class="flex flex-wrap items-center gap-2">
          <n-input
            v-model:value="search"
            size="small"
            placeholder="关键词 / 拼音首字母..."
            clearable
            class="w-44 sm:w-56"
            @keyup.enter="reload"
          />
          <n-select
            v-model:value="sortBy"
            size="small"
            :options="sortOptions"
            class="w-32"
            @update:value="reload"
          />
          <button
            type="button"
            class="h-7 px-2.5 rounded-lg glass-pill text-xs font-medium text-ink-secondary hover:text-sky-600 dark:hover:text-sky-400 transition"
            :title="sortOrder === 'Descending' ? '当前降序，点击切换升序' : '当前升序，点击切换降序'"
            @click="toggleSortOrder"
          >
            {{ sortOrder === 'Descending' ? '↓ 降序' : '↑ 升序' }}
          </button>
          <button
            type="button"
            class="h-7 px-2.5 rounded-lg glass-pill text-xs font-medium text-ink-secondary hover:text-sky-600 dark:hover:text-sky-400 transition"
            @click="compactGrid = !compactGrid"
          >
            {{ compactGrid ? '⊞ 舒适网格' : '⊟ 紧凑网格' }}
          </button>
        </div>
      </div>

      <!-- Facets Row: Type / Genre / Year / Quick Filters -->
      <div class="flex flex-wrap items-center gap-2">
        <!-- Media Type Filter (useful in All / Mixed views) -->
        <div class="flex items-center gap-1 p-1 rounded-xl neu-inset">
          <button
            v-for="opt in typeFilterOptions"
            :key="opt.value"
            type="button"
            class="px-2.5 py-1 rounded-lg text-xs font-medium transition"
            :class="
              typeFilter === opt.value
                ? 'bg-white dark:bg-slate-800 text-sky-600 dark:text-sky-400 shadow-sm'
                : 'text-ink-muted hover:text-ink'
            "
            @click="selectType(opt.value)"
          >
            {{ opt.label }}
          </button>
        </div>

        <n-select
          v-if="genreOptions.length > 0"
          v-model:value="selectedGenre"
          size="small"
          clearable
          filterable
          placeholder="全部类型"
          :options="genreOptions"
          class="w-32"
          @update:value="reload"
        />

        <n-select
          v-if="yearOptions.length > 0"
          v-model:value="selectedYear"
          size="small"
          clearable
          filterable
          placeholder="全部年份"
          :options="yearOptions"
          class="w-28"
          @update:value="reload"
        />

        <button
          type="button"
          class="filter-chip"
          :class="{ 'filter-chip-active': favOnly }"
          @click="toggleFav"
        >
          {{ favOnly ? '♥ 仅看收藏' : '♡ 仅看收藏' }}
        </button>

        <button
          type="button"
          class="filter-chip"
          :class="{ 'filter-chip-active': unplayedOnly }"
          @click="toggleUnplayed"
        >
          {{ unplayedOnly ? '● 仅看未看' : '○ 仅看未看' }}
        </button>

        <button
          v-if="hasActiveFilters"
          type="button"
          class="px-2.5 py-1 rounded-xl text-xs font-medium text-rose-600 dark:text-rose-400 hover:bg-rose-500/10 transition"
          @click="resetFilters"
        >
          重置筛选
        </button>
      </div>

      <!-- A-Z Alphabet Quick Jump Strip -->
      <div class="flex items-center gap-1 overflow-x-auto pb-0.5 scrollbar-none">
        <button
          type="button"
          class="alpha-pill"
          :class="{ 'alpha-pill-active': !letter }"
          @click="selectLetter('')"
        >
          全部
        </button>
        <button
          v-for="ch in alphabet"
          :key="ch"
          type="button"
          class="alpha-pill"
          :class="{ 'alpha-pill-active': letter === ch }"
          @click="selectLetter(ch)"
        >
          {{ ch }}
        </button>
      </div>
    </div>

    <!-- Content Area -->
    <PosterSkeleton v-if="loading && items.length === 0" :count="18" :compact="compactGrid" />

    <div v-else-if="items.length === 0" class="glass-panel rounded-3xl py-16 text-center space-y-2">
      <div class="text-3xl">🔍</div>
      <div class="text-sm font-medium text-ink-secondary">没有匹配的影视内容</div>
      <button v-if="hasActiveFilters" type="button" class="text-xs text-sky-600 dark:text-sky-400 hover:underline" @click="resetFilters">
        清除当前筛选条件
      </button>
    </div>

    <template v-else>
      <div class="poster-grid" :class="{ 'poster-grid-compact': compactGrid }">
        <PosterCard v-for="item in items" :key="item.Id" :item="item" :compact="compactGrid" />
      </div>

      <!-- Infinite Scroll Sentinel & Status -->
      <div ref="sentinelRef" class="py-6 flex flex-col items-center justify-center gap-2">
        <div v-if="loadingMore" class="flex items-center gap-2 text-xs text-ink-muted glass-pill px-4 py-2 rounded-full">
          <span class="w-2 h-2 rounded-full bg-sky-500 animate-ping"></span>
          <span>正在加载更多...</span>
        </div>
        <button
          v-else-if="hasMore"
          type="button"
          class="px-5 py-2 rounded-2xl glass-pill text-xs font-semibold text-sky-600 dark:text-sky-400 hover:brightness-105 transition"
          @click="loadMore"
        >
          加载更多（已显示 {{ items.length }} / {{ total }}）
        </button>
        <div v-else class="text-xs text-ink-muted">
          已展示全部 {{ total }} 项内容
        </div>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { NInput, NSelect } from 'naive-ui'
import { apiFetch, apiFetchCached } from '../api/client'
import { useAuthStore } from '../stores/auth'
import PosterCard from '../components/PosterCard.vue'
import PosterSkeleton from '../components/PosterSkeleton.vue'

const PAGE_SIZE = 48
const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ'.split('')

const route = useRoute()
const auth = useAuthStore()

const title = ref('媒体库')
const items = ref<any[]>([])
const total = ref(0)
const hasMore = ref(false)
const loading = ref(false)
const loadingMore = ref(false)

const search = ref(typeof route.query.q === 'string' ? route.query.q : '')
const sortBy = ref('DateCreated')
const sortOrder = ref<'Ascending' | 'Descending'>('Descending')
const favOnly = ref(route.query.fav === '1')
const unplayedOnly = ref(false)
const typeFilter = ref<string>('all')
const collectionType = ref('')
const selectedGenre = ref<string | null>(null)
const selectedYear = ref<number | null>(null)
const letter = ref('')
const compactGrid = ref(false)

const genreOptions = ref<{ label: string; value: string }[]>([])
const yearOptions = ref<{ label: string; value: number }[]>([])

const sentinelRef = ref<HTMLElement | null>(null)
let observer: IntersectionObserver | null = null
let searchTimer: number | undefined
let loadSeq = 0

const sortOptions = [
  { label: '最近添加', value: 'DateCreated' },
  { label: '按名称', value: 'SortName' },
  { label: '按年份', value: 'ProductionYear' },
  { label: '按评分', value: 'CommunityRating' },
  { label: '随机发现', value: 'Random' },
]

const typeFilterOptions = computed<{ label: string; value: string }[]>(() => {
  if (collectionType.value === 'boxsets') {
    return [
      { label: '全部', value: 'all' },
      { label: '合集', value: 'BoxSet' },
    ]
  }
  return [
    { label: '全部', value: 'all' },
    { label: '电影', value: 'Movie' },
    { label: '剧集', value: 'Series' },
  ]
})

const hasActiveFilters = computed(() =>
  Boolean(
    search.value.trim() ||
      favOnly.value ||
      unplayedOnly.value ||
      typeFilter.value !== 'all' ||
      selectedGenre.value ||
      selectedYear.value ||
      letter.value,
  ),
)

function buildQueryParams(startIndex: number) {
  const libId = String(route.params.id || '')
  const params = new URLSearchParams({
    StartIndex: String(startIndex),
    Limit: String(PAGE_SIZE),
    SortBy: sortBy.value,
    SortOrder: sortOrder.value,
  })
  if (libId && libId !== 'all') {
    params.set('ParentId', libId)
  } else {
    params.set('Recursive', 'true')
  }

  if (typeFilter.value !== 'all') {
    params.set('IncludeItemTypes', typeFilter.value)
    params.set('Recursive', 'true')
  } else if (!libId || libId === 'all') {
    params.set('IncludeItemTypes', 'Movie,Series')
  }

  if (search.value.trim()) {
    params.set('SearchTerm', search.value.trim())
    params.set('Recursive', 'true')
  }
  if (letter.value) {
    params.set('NameStartsWith', letter.value)
  }
  if (selectedGenre.value) {
    params.set('Genres', selectedGenre.value)
  }
  if (selectedYear.value) {
    params.set('Years', String(selectedYear.value))
  }
  if (favOnly.value) {
    params.set('IsFavorite', 'true')
  }
  if (unplayedOnly.value) {
    params.set('IsPlayed', 'false')
  }
  return params
}

async function loadMetaAndFilters() {
  const libId = String(route.params.id || '')
  if (libId && libId !== 'all') {
    const info = await apiFetchCached(`/emby/Items/${libId}`, { ttlMs: 60000 }).catch(() => null)
    title.value = info?.Name || '媒体库'
    collectionType.value = String(info?.CollectionType || '')
    if (collectionType.value === 'boxsets') {
      if (typeFilter.value !== 'all' && typeFilter.value !== 'BoxSet') typeFilter.value = 'all'
    } else if (typeFilter.value === 'BoxSet') {
      typeFilter.value = 'all'
    }
  } else if (favOnly.value) {
    title.value = '我的收藏'
  } else if (search.value.trim()) {
    title.value = `搜索：${search.value.trim()}`
  } else {
    title.value = '全部影视'
  }

  const filterUrl =
    libId && libId !== 'all' && !libId.startsWith('vlib-')
      ? `/emby/Items/Filters?ParentId=${encodeURIComponent(libId)}`
      : '/emby/Items/Filters'
  const f = await apiFetchCached(filterUrl, { ttlMs: 60000 }).catch(() => null)
  genreOptions.value = (f?.Genres || []).map((g: string) => ({ label: g, value: g }))
  yearOptions.value = (f?.Years || []).map((y: number) => ({ label: String(y), value: y }))
}

async function reload() {
  const uid = auth.user?.Id
  if (!uid) return
  const seq = ++loadSeq
  loading.value = true
  try {
    const params = buildQueryParams(0)
    const res = await apiFetch(`/emby/Users/${uid}/Items?${params.toString()}`)
    if (seq !== loadSeq) return
    items.value = res?.Items || []
    total.value = res?.TotalRecordCount ?? items.value.length
    hasMore.value = Boolean(res?.HasMore) || items.value.length < total.value
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

async function loadMore() {
  const uid = auth.user?.Id
  if (!uid || loading.value || loadingMore.value || !hasMore.value) return
  const seq = loadSeq
  loadingMore.value = true
  try {
    const params = buildQueryParams(items.value.length)
    const res = await apiFetch(`/emby/Users/${uid}/Items?${params.toString()}`)
    if (seq !== loadSeq) return
    const next = res?.Items || []
    items.value = [...items.value, ...next]
    total.value = res?.TotalRecordCount ?? items.value.length
    hasMore.value = next.length > 0 && (Boolean(res?.HasMore) || items.value.length < total.value)
  } finally {
    loadingMore.value = false
  }
}

function toggleSortOrder() {
  sortOrder.value = sortOrder.value === 'Descending' ? 'Ascending' : 'Descending'
  reload()
}

function selectType(val: string) {
  typeFilter.value = val
  reload()
}

function toggleFav() {
  favOnly.value = !favOnly.value
  reload()
}

function toggleUnplayed() {
  unplayedOnly.value = !unplayedOnly.value
  reload()
}

function selectLetter(ch: string) {
  letter.value = ch
  if (ch && sortBy.value !== 'SortName') {
    sortBy.value = 'SortName'
    sortOrder.value = 'Ascending'
  }
  reload()
}

function resetFilters() {
  search.value = ''
  favOnly.value = false
  unplayedOnly.value = false
  typeFilter.value = 'all'
  selectedGenre.value = null
  selectedYear.value = null
  letter.value = ''
  sortBy.value = 'DateCreated'
  sortOrder.value = 'Descending'
  reload()
}

watch(search, () => {
  if (searchTimer) window.clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => {
    reload()
  }, 260)
})

watch(
  () => [route.params.id, route.query.q, route.query.fav],
  async () => {
    search.value = typeof route.query.q === 'string' ? route.query.q : ''
    favOnly.value = route.query.fav === '1'
    selectedGenre.value = null
    selectedYear.value = null
    letter.value = ''
    await Promise.all([loadMetaAndFilters(), reload()])
  },
)

onMounted(async () => {
  await Promise.all([loadMetaAndFilters(), reload()])
  observer = new IntersectionObserver(
    (entries) => {
      if (entries.some((e) => e.isIntersecting)) {
        loadMore()
      }
    },
    { rootMargin: '320px' },
  )
  if (sentinelRef.value) {
    observer.observe(sentinelRef.value)
  }
})

watch(sentinelRef, (el, prev) => {
  if (prev && observer) observer.unobserve(prev)
  if (el && observer) observer.observe(el)
})

onBeforeUnmount(() => {
  if (searchTimer) window.clearTimeout(searchTimer)
  observer?.disconnect()
})
</script>

<style scoped>
.filter-chip {
  padding: 0.25rem 0.75rem;
  border-radius: 0.75rem;
  font-size: 0.75rem;
  font-weight: 500;
  background: var(--glass-card);
  backdrop-filter: blur(14px);
  border: 1px solid var(--glass-border);
  box-shadow: var(--shadow-neu-sm);
  color: var(--text-secondary);
  cursor: pointer;
  transition: all 0.2s ease;
}
.filter-chip:hover {
  color: var(--accent);
  background: var(--surface-hover);
}
.filter-chip-active {
  background: var(--accent-soft);
  color: var(--accent);
  border-color: rgba(14, 165, 233, 0.38);
}
.alpha-pill {
  padding: 0.125rem 0.5rem;
  border-radius: 0.5rem;
  font-size: 11px;
  font-weight: 600;
  color: var(--text-muted);
  flex-shrink: 0;
  transition: all 0.18s ease;
}
.alpha-pill:hover {
  color: var(--text-primary);
  background: var(--surface-hover);
}
.alpha-pill-active {
  background: var(--accent);
  color: #ffffff;
  box-shadow: 0 2px 8px rgba(2, 132, 199, 0.28);
}
</style>
