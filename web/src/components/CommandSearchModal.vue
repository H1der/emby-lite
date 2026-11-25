<template>
  <teleport to="body">
    <transition name="fade">
      <div
        v-if="show"
        class="fixed inset-0 z-50 flex items-start justify-center pt-[10vh] px-4 bg-slate-900/25 dark:bg-black/55 backdrop-blur-md"
        @click.self="close"
      >
        <div class="glass-strong w-full max-w-2xl rounded-3xl overflow-hidden shadow-2xl">
          <!-- Input Header -->
          <div class="flex items-center gap-3 px-5 py-4 border-b border-slate-200/70 dark:border-white/10">
            <svg class="w-5 h-5 text-sky-600 dark:text-sky-400 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <circle cx="11" cy="11" r="7" />
              <path d="m20 20-3.5-3.5" stroke-linecap="round" />
            </svg>
            <input
              ref="inputRef"
              v-model="query"
              type="text"
              placeholder="实时搜索电影、剧集、拼音首字母..."
              class="flex-1 bg-transparent outline-none text-base text-ink placeholder:text-ink-muted"
              @keydown.down.prevent="moveActive(1)"
              @keydown.up.prevent="moveActive(-1)"
              @keydown.enter.prevent="confirmSelection"
              @keydown.esc="close"
            />
            <button
              v-if="query"
              type="button"
              class="text-xs px-2 py-1 rounded-lg text-ink-muted hover:text-ink hover:bg-slate-200/60 dark:hover:bg-white/10 transition"
              @click="query = ''"
            >
              清空
            </button>
            <kbd class="hidden sm:inline-flex px-2 py-0.5 text-[11px] font-mono rounded-md neu-inset text-ink-muted">ESC</kbd>
          </div>

          <!-- Results Body -->
          <div class="max-h-[60vh] overflow-y-auto p-3">
            <div v-if="!query.trim()" class="py-10 text-center space-y-2">
              <div class="text-sm font-medium text-ink-secondary">输入关键词或中文拼音首字母快速定位影视</div>
              <div class="text-xs text-ink-muted">支持 ↑ / ↓ 切换，Enter 打开详情，或按 Enter 进入完整搜索页</div>
            </div>

            <div v-else-if="loading" class="space-y-2 p-2">
              <div v-for="i in 4" :key="i" class="flex items-center gap-3 p-2 rounded-2xl">
                <div class="w-11 h-16 rounded-lg skeleton-shimmer shrink-0"></div>
                <div class="flex-1 space-y-2">
                  <div class="h-4 w-2/3 rounded skeleton-shimmer"></div>
                  <div class="h-3 w-1/3 rounded skeleton-shimmer"></div>
                </div>
              </div>
            </div>

            <div v-else-if="results.length === 0" class="py-10 text-center text-sm text-ink-muted">
              没有找到与 “{{ query }}” 匹配的影视内容
            </div>

            <div v-else class="space-y-1">
              <button
                v-for="(item, idx) in results"
                :key="item.Id"
                type="button"
                class="w-full flex items-center gap-3.5 p-2.5 rounded-2xl text-left transition"
                :class="
                  idx === activeIndex
                    ? 'bg-sky-500/14 dark:bg-sky-400/20 ring-1 ring-sky-500/30'
                    : 'hover:bg-white/60 dark:hover:bg-white/5'
                "
                @mouseenter="activeIndex = idx"
                @click="openItem(item)"
              >
                <div class="w-11 h-16 rounded-xl overflow-hidden bg-slate-200/70 dark:bg-slate-800 shrink-0 shadow-sm">
                  <img
                    v-if="posterOf(item)"
                    :src="posterOf(item)"
                    :alt="item.Name"
                    class="w-full h-full object-cover"
                  />
                  <div v-else class="w-full h-full flex items-center justify-center text-xs text-ink-muted">🎬</div>
                </div>
                <div class="min-w-0 flex-1">
                  <div class="flex items-center gap-2">
                    <span class="font-semibold text-sm text-ink truncate">{{ item.Name }}</span>
                    <span v-if="item.ProductionYear" class="text-xs text-ink-muted shrink-0">{{ item.ProductionYear }}</span>
                  </div>
                  <div class="text-xs text-ink-muted truncate mt-0.5">
                    {{ item.OriginalTitle || (item.Genres || []).slice(0, 3).join(' · ') || '媒体条目' }}
                  </div>
                </div>
                <div class="flex items-center gap-2 shrink-0">
                  <span v-if="item.CommunityRating" class="text-xs font-semibold text-amber-600 dark:text-amber-400">
                    ★ {{ Number(item.CommunityRating).toFixed(1) }}
                  </span>
                  <span class="px-2 py-0.5 rounded-lg text-[11px] font-medium bg-sky-500/10 text-sky-700 dark:text-sky-300">
                    {{ item.Type === 'Series' ? '剧集' : '电影' }}
                  </span>
                </div>
              </button>
            </div>
          </div>

          <!-- Footer -->
          <div class="px-5 py-2.5 border-t border-slate-200/70 dark:border-white/10 flex items-center justify-between text-xs text-ink-muted">
            <span>{{ results.length ? `已匹配 ${total} 项` : '全局即时检索' }}</span>
            <button
              v-if="query.trim()"
              type="button"
              class="text-sky-600 dark:text-sky-400 font-medium hover:underline"
              @click="goFullSearch"
            >
              在媒体库中查看全部结果 →
            </button>
          </div>
        </div>
      </div>
    </transition>
  </teleport>
</template>

<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { apiFetch, itemImageUrl } from '../api/client'
import { useAuthStore } from '../stores/auth'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ (e: 'update:show', val: boolean): void }>()

const router = useRouter()
const auth = useAuthStore()

const inputRef = ref<HTMLInputElement | null>(null)
const query = ref('')
const loading = ref(false)
const results = ref<any[]>([])
const total = ref(0)
const activeIndex = ref(0)
let debounceTimer: number | undefined

function close() {
  emit('update:show', false)
}

function posterOf(item: any): string {
  const tag = item.ImageTags?.Primary
  if (tag) return itemImageUrl(item.Id, 'Primary', tag, 180)
  return ''
}

function moveActive(delta: number) {
  if (results.value.length === 0) return
  activeIndex.value = (activeIndex.value + delta + results.value.length) % results.value.length
}

function openItem(item: any) {
  close()
  router.push({ name: 'item', params: { id: item.Id } })
}

function goFullSearch() {
  const q = query.value.trim()
  if (!q) return
  close()
  router.push({ name: 'library', params: { id: 'all' }, query: { q } })
}

function confirmSelection() {
  if (results.value[activeIndex.value]) {
    openItem(results.value[activeIndex.value])
    return
  }
  goFullSearch()
}

async function runSearch(q: string) {
  const uid = auth.user?.Id
  if (!uid || !q.trim()) {
    results.value = []
    total.value = 0
    return
  }
  loading.value = true
  try {
    const params = new URLSearchParams({
      Recursive: 'true',
      IncludeItemTypes: 'Movie,Series',
      Limit: '12',
      SearchTerm: q.trim(),
    })
    const res = await apiFetch(`/emby/Users/${uid}/Items?${params.toString()}`)
    results.value = res?.Items || []
    total.value = res?.TotalRecordCount ?? results.value.length
    activeIndex.value = 0
  } finally {
    loading.value = false
  }
}

watch(
  () => props.show,
  async (val) => {
    if (val) {
      await nextTick()
      inputRef.value?.focus()
    }
  },
)

watch(query, (val) => {
  if (debounceTimer) window.clearTimeout(debounceTimer)
  if (!val.trim()) {
    results.value = []
    total.value = 0
    loading.value = false
    return
  }
  debounceTimer = window.setTimeout(() => runSearch(val), 220)
})
</script>

<style scoped>
.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.18s ease;
}
.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}
</style>
