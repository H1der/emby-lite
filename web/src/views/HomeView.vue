<template>
  <div class="space-y-8">
    <!-- Loading Skeleton State -->
    <template v-if="initialLoading">
      <div class="glass-panel rounded-3xl h-64 sm:h-80 lg:h-[380px] skeleton-shimmer"></div>
      <div class="space-y-3">
        <div class="h-6 w-36 rounded-lg skeleton-shimmer"></div>
        <div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5 3xl:grid-cols-6 gap-4">
          <div v-for="i in 5" :key="i" class="aspect-video rounded-2xl glass-card skeleton-shimmer"></div>
        </div>
      </div>
      <div class="space-y-3">
        <div class="h-6 w-32 rounded-lg skeleton-shimmer"></div>
        <PosterSkeleton :count="12" />
      </div>
    </template>

    <template v-else>
      <!-- Cinematic Frosted Glass Hero Showcase -->
      <section
        v-if="currentHero"
        class="relative rounded-3xl overflow-hidden glass-panel min-h-[260px] sm:min-h-[320px] lg:min-h-[380px] flex items-end group"
        @mouseenter="heroPaused = true"
        @mouseleave="heroPaused = false"
      >
        <img
          v-if="heroBackdropUrl"
          :key="currentHero.Id"
          :src="heroBackdropUrl"
          :alt="currentHero.Name"
          class="absolute inset-0 w-full h-full object-cover object-center transition-transform duration-700 group-hover:scale-[1.03]"
        />
        <div
          v-else
          class="absolute inset-0 bg-gradient-to-br from-sky-500/30 via-indigo-500/25 to-teal-400/25"
        ></div>

        <!-- Soft readable veil (always rich cinematic legibility without feeling heavy) -->
        <div
          class="absolute inset-0 bg-gradient-to-r from-slate-950/85 via-slate-900/55 to-slate-900/15"
        ></div>
        <div
          class="absolute inset-0 bg-gradient-to-t from-slate-950/85 via-transparent to-transparent"
        ></div>

        <div class="relative z-10 w-full p-5 sm:p-8 lg:p-10 flex flex-col md:flex-row md:items-end justify-between gap-6">
          <div class="max-w-2xl space-y-3">
            <div class="flex flex-wrap items-center gap-2">
              <span class="px-2.5 py-0.5 rounded-full text-[11px] font-semibold bg-white/20 backdrop-blur-md text-white border border-white/25">
                {{ currentHero.Type === 'Series' ? '精选剧集' : '精选电影' }}
              </span>
              <span
                v-if="currentHero.CommunityRating"
                class="px-2.5 py-0.5 rounded-full text-[11px] font-bold bg-amber-400/90 text-slate-950"
              >
                ★ {{ Number(currentHero.CommunityRating).toFixed(1) }}
              </span>
              <span v-if="currentHero.ProductionYear" class="text-xs text-slate-200 font-medium">
                {{ currentHero.ProductionYear }}
              </span>
              <span
                v-for="g in (currentHero.Genres || []).slice(0, 3)"
                :key="g"
                class="px-2 py-0.5 rounded-full text-[11px] bg-white/15 backdrop-blur-sm text-slate-100"
              >
                {{ g }}
              </span>
            </div>

            <h1 class="text-2xl sm:text-3xl lg:text-4xl font-bold text-white tracking-tight leading-tight drop-shadow">
              {{ currentHero.Name }}
            </h1>

            <p v-if="currentHero.Overview" class="text-xs sm:text-sm text-slate-200/90 line-clamp-2 sm:line-clamp-3 leading-relaxed">
              {{ currentHero.Overview }}
            </p>

            <div class="flex flex-wrap items-center gap-2.5 pt-2">
              <button
                type="button"
                class="px-5 py-2.5 rounded-2xl accent-gradient text-white text-sm font-semibold shadow-lg hover:brightness-110 active:scale-[0.98] transition flex items-center gap-2"
                @click="openHeroPrimary"
              >
                <span>▶</span>
                <span>{{ currentHero.Type === 'Movie' ? '立即播放' : '查看剧集' }}</span>
              </button>
              <button
                type="button"
                class="px-4 py-2.5 rounded-2xl bg-white/20 hover:bg-white/30 backdrop-blur-md border border-white/30 text-white text-sm font-medium transition"
                @click="router.push({ name: 'item', params: { id: currentHero.Id } })"
              >
                详情信息
              </button>
            </div>
          </div>

          <!-- Carousel Controls -->
          <div v-if="featured.length > 1" class="flex items-center gap-2 self-end">
            <button
              type="button"
              class="w-8 h-8 rounded-full bg-white/20 hover:bg-white/35 backdrop-blur-md border border-white/25 text-white flex items-center justify-center transition"
              @click="stepHero(-1)"
            >
              ‹
            </button>
            <div class="flex items-center gap-1.5 px-2">
              <button
                v-for="(item, idx) in featured"
                :key="item.Id"
                type="button"
                class="h-1.5 rounded-full transition-all duration-300"
                :class="idx === heroIndex ? 'w-6 bg-white' : 'w-1.5 bg-white/45 hover:bg-white/75'"
                @click="heroIndex = idx"
              ></button>
            </div>
            <button
              type="button"
              class="w-8 h-8 rounded-full bg-white/20 hover:bg-white/35 backdrop-blur-md border border-white/25 text-white flex items-center justify-center transition"
              @click="stepHero(1)"
            >
              ›
            </button>
          </div>
        </div>
      </section>

      <!-- Stats Summary Strip + My Libraries Bento -->
      <section class="space-y-3.5">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div class="flex items-center gap-3">
            <h2 class="text-lg sm:text-xl font-bold text-ink tracking-tight">我的媒体库</h2>
            <div v-if="counts && (counts.MovieCount || counts.SeriesCount)" class="hidden sm:flex items-center gap-1.5 text-xs text-ink-muted">
              <span class="px-2 py-0.5 rounded-full glass-pill">{{ counts.MovieCount || 0 }} 部电影</span>
              <span class="px-2 py-0.5 rounded-full glass-pill">{{ counts.SeriesCount || 0 }} 部剧集</span>
            </div>
          </div>
          <div class="flex items-center gap-3">
            <button
              type="button"
              class="text-xs font-medium px-2.5 py-1 rounded-xl glass-pill text-ink-secondary hover:text-sky-600 dark:hover:text-sky-400 transition"
              @click="showHomeSettings = true"
            >
              ⚙ 主屏幕设置
            </button>
            <router-link
              v-if="auth.isAdmin"
              to="/admin?tab=libraries"
              class="text-xs font-medium text-sky-600 dark:text-sky-400 hover:underline"
            >
              管理媒体库 →
            </router-link>
          </div>
        </div>

        <div v-if="views.length === 0" class="glass-panel rounded-3xl p-10 text-center space-y-3">
          <div class="text-3xl">📂</div>
          <p class="text-sm text-ink-secondary">当前「我的媒体库」暂无展示项，可在主屏幕设置中调整显示或新建媒体库。</p>
          <div class="flex items-center justify-center gap-2">
            <n-button secondary @click="showHomeSettings = true">主屏幕设置</n-button>
            <n-button v-if="auth.isAdmin" type="primary" @click="router.push('/admin?tab=libraries')">
              前往后台新建媒体库
            </n-button>
          </div>
        </div>

        <div v-else class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5 3xl:grid-cols-6 gap-4">
          <router-link
            v-for="lib in views"
            :key="lib.Id"
            :to="{ name: 'library', params: { id: lib.Id } }"
            class="glass-card glass-card-interactive group relative rounded-2xl overflow-hidden aspect-[16/10] flex flex-col justify-end p-4"
          >
            <img
              v-if="lib.ImageTags?.Primary"
              :src="itemImageUrl(lib.Id, 'Primary', lib.ImageTags.Primary, 640)"
              :alt="lib.Name"
              loading="lazy"
              class="absolute inset-0 w-full h-full object-cover group-hover:scale-105 transition duration-500"
            />
            <div
              v-else
              class="absolute inset-0 bg-gradient-to-br from-sky-400/35 via-indigo-400/25 to-teal-400/30"
            ></div>
            <div class="absolute inset-0 bg-gradient-to-t from-slate-950/80 via-slate-900/25 to-transparent"></div>
            <div class="relative z-10 min-w-0">
              <div class="text-base font-bold text-white truncate drop-shadow-sm">{{ lib.Name }}</div>
            </div>
          </router-link>
        </div>
      </section>

      <!-- Continue Watching Horizontal Glass Rail -->
      <section v-if="resumeItems.length > 0" class="space-y-3">
        <div class="flex items-center justify-between">
          <h2 class="text-lg sm:text-xl font-bold text-ink tracking-tight">继续观看</h2>
          <div class="hidden sm:flex items-center gap-1.5">
            <button type="button" class="rail-arrow" @click="scrollRail(resumeRailRef, -420)">‹</button>
            <button type="button" class="rail-arrow" @click="scrollRail(resumeRailRef, 420)">›</button>
          </div>
        </div>
        <div ref="resumeRailRef" class="glass-rail">
          <div v-for="item in resumeItems" :key="item.Id" class="w-36 sm:w-44 lg:w-48 shrink-0 snap-start">
            <PosterCard :item="item" />
          </div>
        </div>
      </section>

      <!-- Next Up Horizontal Glass Rail -->
      <section v-if="nextUpItems.length > 0" class="space-y-3">
        <div class="flex items-center justify-between">
          <h2 class="text-lg sm:text-xl font-bold text-ink tracking-tight">接下来观看</h2>
          <div class="hidden sm:flex items-center gap-1.5">
            <button type="button" class="rail-arrow" @click="scrollRail(nextUpRailRef, -420)">‹</button>
            <button type="button" class="rail-arrow" @click="scrollRail(nextUpRailRef, 420)">›</button>
          </div>
        </div>
        <div ref="nextUpRailRef" class="glass-rail">
          <div v-for="item in nextUpItems" :key="item.Id" class="w-36 sm:w-44 lg:w-48 shrink-0 snap-start">
            <PosterCard :item="item" />
          </div>
        </div>
      </section>

      <!-- Latest Added Adaptive Grid -->
      <section class="space-y-3.5">
        <div class="flex items-center justify-between">
          <h2 class="text-lg sm:text-xl font-bold text-ink tracking-tight">最近添加</h2>
          <router-link
            :to="{ name: 'library', params: { id: 'all' } }"
            class="text-xs font-medium text-sky-600 dark:text-sky-400 hover:underline"
          >
            浏览全部 →
          </router-link>
        </div>

        <div v-if="latestItems.length === 0" class="glass-panel rounded-2xl p-8 text-center text-sm text-ink-muted">
          暂无媒体内容，扫描媒体库后将在此展示。
        </div>
        <div v-else class="poster-grid">
          <PosterCard v-for="item in latestItems" :key="item.Id" :item="item" />
        </div>
      </section>
    </template>

    <HomeScreenSettingsModal v-model:show="showHomeSettings" @saved="refreshHome" />
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { NButton } from 'naive-ui'
import { apiFetchCached, getCachedData, itemImageUrl } from '../api/client'
import { useAuthStore } from '../stores/auth'
import PosterCard from '../components/PosterCard.vue'
import PosterSkeleton from '../components/PosterSkeleton.vue'
import HomeScreenSettingsModal from '../components/HomeScreenSettingsModal.vue'

interface WebHomePayload {
  views: any[]
  resume: any[]
  next_up: any[]
  latest: any[]
  featured: any[]
  counts: { MovieCount?: number; SeriesCount?: number; EpisodeCount?: number }
}

const auth = useAuthStore()
const router = useRouter()

const views = ref<any[]>([])
const resumeItems = ref<any[]>([])
const nextUpItems = ref<any[]>([])
const latestItems = ref<any[]>([])
const featured = ref<any[]>([])
const counts = ref<WebHomePayload['counts'] | null>(null)
const initialLoading = ref(true)
const showHomeSettings = ref(false)

const heroIndex = ref(0)
const heroPaused = ref(false)
let heroTimer: number | undefined

const resumeRailRef = ref<HTMLElement | null>(null)
const nextUpRailRef = ref<HTMLElement | null>(null)

const currentHero = computed(() => featured.value[heroIndex.value] || null)

const heroBackdropUrl = computed(() => {
  const it = currentHero.value
  if (!it) return ''
  const bgs = it.BackdropImageTags
  if (Array.isArray(bgs) && bgs.length > 0) {
    return itemImageUrl(it.Id, 'Backdrop', bgs[0], 1440)
  }
  if (it.ImageTags?.Primary) {
    return itemImageUrl(it.Id, 'Primary', it.ImageTags.Primary, 900)
  }
  return ''
})

function stepHero(delta: number) {
  if (featured.value.length <= 1) return
  heroIndex.value = (heroIndex.value + delta + featured.value.length) % featured.value.length
}

function openHeroPrimary() {
  if (!currentHero.value) return
  if (currentHero.value.Type === 'Movie') {
    router.push({ name: 'play', params: { id: currentHero.value.Id } })
  } else {
    router.push({ name: 'item', params: { id: currentHero.value.Id } })
  }
}

function scrollRail(el: HTMLElement | null, delta: number) {
  if (!el) return
  el.scrollBy({ left: delta, behavior: 'smooth' })
}

function applyHomeData(data: WebHomePayload) {
  views.value = data?.views || []
  resumeItems.value = data?.resume || []
  nextUpItems.value = data?.next_up || []
  latestItems.value = data?.latest || []
  featured.value = data?.featured || []
  counts.value = data?.counts || null
  if (heroIndex.value >= featured.value.length) {
    heroIndex.value = 0
  }
}

async function refreshHome() {
  try {
    const fresh = await apiFetchCached<WebHomePayload>('/api/web/home', { force: true })
    applyHomeData(fresh)
  } catch {
    // ignore refresh error
  }
}

onMounted(async () => {
  const cached = getCachedData<WebHomePayload>('/api/web/home')
  if (cached) {
    applyHomeData(cached)
    initialLoading.value = false
  }
  try {
    const fresh = await apiFetchCached<WebHomePayload>('/api/web/home', {
      ttlMs: 15000,
      onUpdate: (updated) => applyHomeData(updated),
    })
    applyHomeData(fresh)
  } finally {
    initialLoading.value = false
  }

  window.addEventListener('emby-lite:home-settings-updated', refreshHome)
  heroTimer = window.setInterval(() => {
    if (!heroPaused.value && featured.value.length > 1) {
      stepHero(1)
    }
  }, 7000)
})

onBeforeUnmount(() => {
  window.removeEventListener('emby-lite:home-settings-updated', refreshHome)
  if (heroTimer) window.clearInterval(heroTimer)
})
</script>

<style scoped>
.rail-arrow {
  width: 2rem;
  height: 2rem;
  border-radius: 0.75rem;
  background: var(--glass-card);
  backdrop-filter: blur(14px);
  border: 1px solid var(--glass-border);
  box-shadow: var(--shadow-neu-sm);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 1.125rem;
  color: var(--text-secondary);
  transition: all 0.2s ease;
}
.rail-arrow:hover {
  color: var(--accent);
  background: var(--surface-hover);
}
</style>
