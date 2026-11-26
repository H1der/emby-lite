<template>
  <div class="ambient-canvas flex min-h-screen text-ink">
    <!-- Desktop & Tablet Floating Glass Sidebar -->
    <aside
      class="hidden md:flex flex-col sticky top-3 h-[calc(100vh-1.5rem)] ml-3 my-3 z-30 glass-panel rounded-3xl transition-all duration-300 ease-out shrink-0 overflow-hidden"
      :class="collapsed ? 'w-[74px]' : 'w-60'"
    >
      <!-- Brand -->
      <div class="h-16 px-4 flex items-center justify-between border-b border-slate-200/60 dark:border-white/10">
        <router-link to="/" class="flex items-center gap-2.5 min-w-0">
          <div class="w-9 h-9 rounded-2xl accent-gradient flex items-center justify-center text-white font-bold shadow-md shrink-0">
            E
          </div>
          <div v-if="!collapsed" class="min-w-0">
            <div class="font-bold text-sm tracking-tight text-ink truncate">{{ auth.serverName }}</div>
            <div class="text-[10px] text-ink-muted truncate">直连影视库</div>
          </div>
        </router-link>
        <button
          v-if="!collapsed"
          type="button"
          class="w-7 h-7 rounded-xl flex items-center justify-center text-ink-muted hover:text-ink hover:bg-white/60 dark:hover:bg-white/10 transition"
          title="收起侧边栏"
          @click="themeStore.toggleSidebar()"
        >
          <svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <path d="m15 18-6-6 6-6" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
        </button>
      </div>

      <!-- Primary Nav -->
      <div class="flex-1 overflow-y-auto px-2.5 py-3 space-y-5">
        <nav class="space-y-1">
          <router-link
            to="/"
            class="nav-item"
            :class="{ 'nav-item-active': route.name === 'home', 'justify-center px-0': collapsed }"
            :title="collapsed ? '首页发现' : undefined"
          >
            <svg class="w-[18px] h-[18px] shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="m3 9 9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" stroke-linecap="round" stroke-linejoin="round" />
              <polyline points="9 22 9 12 15 12 15 22" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
            <span v-if="!collapsed" class="truncate">首页发现</span>
          </router-link>

          <router-link
            :to="{ name: 'library', params: { id: 'all' } }"
            class="nav-item"
            :class="{ 'nav-item-active': route.name === 'library' && route.params.id === 'all' && !route.query.fav, 'justify-center px-0': collapsed }"
            :title="collapsed ? '全部影视' : undefined"
          >
            <svg class="w-[18px] h-[18px] shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <rect x="2" y="3" width="20" height="14" rx="2" />
              <path d="M8 21h8M12 17v4" stroke-linecap="round" />
            </svg>
            <span v-if="!collapsed" class="truncate">全部影视</span>
          </router-link>

          <router-link
            :to="{ name: 'library', params: { id: 'all' }, query: { fav: '1' } }"
            class="nav-item"
            :class="{ 'nav-item-active': route.name === 'library' && route.query.fav === '1', 'justify-center px-0': collapsed }"
            :title="collapsed ? '我的收藏' : undefined"
          >
            <svg class="w-[18px] h-[18px] shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M19 14c1.49-1.46 3-3.21 3-5.5A5.5 5.5 0 0 0 16.5 3c-1.76 0-3 .5-4.5 2-1.5-1.5-2.74-2-4.5-2A5.5 5.5 0 0 0 2 8.5c0 2.3 1.5 4.05 3 5.5l7 7Z" />
            </svg>
            <span v-if="!collapsed" class="truncate">我的收藏</span>
          </router-link>
        </nav>

        <!-- Dynamic Libraries Section -->
        <div v-if="libraries.length > 0" class="space-y-1">
          <div v-if="!collapsed" class="px-3 text-[11px] font-semibold uppercase tracking-wider text-ink-muted">
            媒体库
          </div>
          <div v-else class="border-t border-slate-200/60 dark:border-white/10 mx-2 my-1"></div>
          <router-link
            v-for="lib in libraries"
            :key="lib.Id"
            :to="{ name: 'library', params: { id: lib.Id } }"
            class="nav-item"
            :class="{ 'nav-item-active': route.name === 'library' && route.params.id === lib.Id, 'justify-center px-0': collapsed }"
            :title="collapsed ? lib.Name : undefined"
          >
            <span class="text-base leading-none shrink-0">{{ libraryIcon(lib) }}</span>
            <span v-if="!collapsed" class="truncate flex-1">{{ lib.Name }}</span>
            <span
              v-if="!collapsed && lib.ChildCount !== undefined"
              class="text-[11px] px-1.5 py-0.5 rounded-md bg-slate-200/60 dark:bg-white/10 text-ink-muted font-medium"
            >
              {{ lib.ChildCount }}
            </span>
          </router-link>
        </div>

        <!-- Admin Console -->
        <div v-if="auth.isAdmin" class="space-y-1">
          <div v-if="!collapsed" class="px-3 text-[11px] font-semibold uppercase tracking-wider text-ink-muted">
            系统管理
          </div>
          <div v-else class="border-t border-slate-200/60 dark:border-white/10 mx-2 my-1"></div>
          <router-link
            to="/admin"
            class="nav-item"
            :class="{ 'nav-item-active': route.name === 'admin', 'justify-center px-0': collapsed }"
            :title="collapsed ? '管理控制台' : undefined"
          >
            <svg class="w-[18px] h-[18px] shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <circle cx="12" cy="12" r="3" />
              <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9c.26.604.852.997 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1Z" />
            </svg>
            <span v-if="!collapsed" class="truncate">管理控制台</span>
          </router-link>
        </div>
      </div>

      <!-- Bottom Expand Toggle when Collapsed -->
      <div class="p-2.5 border-t border-slate-200/60 dark:border-white/10 flex items-center justify-center">
        <button
          v-if="collapsed"
          type="button"
          class="w-10 h-10 rounded-2xl flex items-center justify-center text-ink-muted hover:text-ink hover:bg-white/60 dark:hover:bg-white/10 transition"
          title="展开侧边栏"
          @click="themeStore.toggleSidebar()"
        >
          <svg class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <path d="m9 18 6-6-6-6" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
        </button>
        <div v-else class="w-full flex items-center justify-between px-2 py-1 text-xs text-ink-muted">
          <span class="truncate">{{ auth.user?.Name }}</span>
          <button
            type="button"
            class="px-2 py-1 rounded-lg hover:bg-rose-500/10 hover:text-rose-600 dark:hover:text-rose-400 transition"
            @click="handleLogout"
          >
            退出
          </button>
        </div>
      </div>
    </aside>

    <!-- Main Column -->
    <div class="flex-1 min-w-0 flex flex-col">
      <!-- Floating Top Glass Header -->
      <header class="sticky top-0 z-20 px-3 sm:px-6 lg:px-8 pt-3 pb-1">
        <div class="max-w-[1920px] mx-auto glass-panel rounded-2xl h-14 px-3 sm:px-4 flex items-center justify-between gap-3">
          <!-- Mobile Brand + Drawer trigger -->
          <div class="flex items-center gap-2 md:hidden min-w-0">
            <button
              type="button"
              class="w-9 h-9 rounded-xl flex items-center justify-center text-ink-secondary hover:bg-white/60 dark:hover:bg-white/10 transition"
              @click="mobileDrawerOpen = true"
            >
              <svg class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <path d="M4 6h16M4 12h16M4 18h16" stroke-linecap="round" />
              </svg>
            </button>
            <router-link to="/" class="font-bold text-sm text-ink truncate">{{ auth.serverName }}</router-link>
          </div>

          <!-- Global Search Pill Trigger -->
          <button
            type="button"
            class="neu-inset flex-1 max-w-md h-9 px-3.5 rounded-xl flex items-center justify-between gap-2 text-xs sm:text-sm text-ink-muted hover:text-ink transition cursor-pointer"
            @click="searchModalOpen = true"
          >
            <span class="flex items-center gap-2 truncate">
              <svg class="w-4 h-4 text-sky-600 dark:text-sky-400 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <circle cx="11" cy="11" r="7" />
                <path d="m20 20-3.5-3.5" stroke-linecap="round" />
              </svg>
              <span class="truncate">搜索电影、剧集、拼音首字母...</span>
            </span>
            <kbd class="hidden sm:inline-flex items-center gap-0.5 px-1.5 py-0.5 rounded-md bg-white/75 dark:bg-slate-800/80 border border-slate-200/70 dark:border-white/10 text-[10px] font-mono text-ink-muted shrink-0">
              Ctrl K
            </kbd>
          </button>

          <!-- Right Actions -->
          <div class="flex items-center gap-2 shrink-0">
            <!-- Admin Live SSE Task Progress Capsule -->
            <router-link
              v-if="auth.isAdmin && activeTask"
              to="/admin?tab=tasks"
              class="hidden sm:flex items-center gap-2 px-3 py-1.5 rounded-xl bg-sky-500/12 border border-sky-400/30 text-sky-700 dark:text-sky-300 text-xs font-medium hover:bg-sky-500/20 transition max-w-[260px]"
            >
              <span class="w-2 h-2 rounded-full bg-sky-500 animate-pulse shrink-0"></span>
              <span class="truncate">{{ formatActiveTaskLabel(activeTask) }}</span>
              <span v-if="activeTask.total > 0" class="font-mono shrink-0">
                {{ Math.min(100, Math.round((activeTask.progress / activeTask.total) * 100)) }}%
              </span>
            </router-link>

            <!-- Theme Toggle -->
            <button
              type="button"
              class="w-9 h-9 rounded-xl glass-pill flex items-center justify-center text-ink-secondary hover:text-sky-600 dark:hover:text-sky-400 transition"
              :title="themeStore.isDark ? '切换为浅色拟态毛玻璃主题' : '切换为深色沉浸主题'"
              @click="themeStore.toggleMode()"
            >
              <svg v-if="themeStore.isDark" class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <circle cx="12" cy="12" r="4" />
                <path d="M12 2v2m0 16v2M4.93 4.93l1.41 1.41m11.32 11.32 1.41 1.41M2 12h2m16 0h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41" stroke-linecap="round" />
              </svg>
              <svg v-else class="w-4 h-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z" stroke-linecap="round" stroke-linejoin="round" />
              </svg>
            </button>

            <!-- User Chip -->
            <div class="hidden sm:flex items-center gap-2 pl-2 pr-1 py-1 rounded-xl glass-pill">
              <div class="w-6 h-6 rounded-lg accent-gradient text-white text-xs font-bold flex items-center justify-center">
                {{ (auth.user?.Name || 'U').slice(0, 1).toUpperCase() }}
              </div>
              <span class="text-xs font-medium text-ink pr-1 max-w-[96px] truncate">{{ auth.user?.Name }}</span>
              <button
                type="button"
                class="text-[11px] px-2 py-1 rounded-lg text-ink-muted hover:text-rose-600 dark:hover:text-rose-400 hover:bg-rose-500/10 transition"
                @click="handleLogout"
              >
                退出
              </button>
            </div>
          </div>
        </div>
      </header>

      <!-- Fluid Responsive Main Viewport -->
      <main class="flex-1 w-full max-w-[1920px] mx-auto px-3 sm:px-6 lg:px-8 pt-4 pb-24 md:pb-10">
        <router-view />
      </main>
    </div>

    <!-- Mobile Bottom Floating Glass Tab Bar -->
    <nav class="md:hidden fixed bottom-3 inset-x-3 z-30 glass-strong rounded-2xl px-2 py-1.5 flex items-center justify-around">
      <router-link to="/" class="mobile-tab" :class="{ 'mobile-tab-active': route.name === 'home' }">
        <svg class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <path d="m3 9 9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />
        </svg>
        <span>首页</span>
      </router-link>
      <router-link :to="{ name: 'library', params: { id: 'all' } }" class="mobile-tab" :class="{ 'mobile-tab-active': route.name === 'library' && !route.query.fav }">
        <svg class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <rect x="2" y="3" width="20" height="14" rx="2" />
          <path d="M8 21h8M12 17v4" />
        </svg>
        <span>片库</span>
      </router-link>
      <button type="button" class="mobile-tab" @click="searchModalOpen = true">
        <svg class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <circle cx="11" cy="11" r="7" />
          <path d="m20 20-3.5-3.5" />
        </svg>
        <span>搜索</span>
      </button>
      <router-link :to="{ name: 'library', params: { id: 'all' }, query: { fav: '1' } }" class="mobile-tab" :class="{ 'mobile-tab-active': route.query.fav === '1' }">
        <svg class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <path d="M19 14c1.49-1.46 3-3.21 3-5.5A5.5 5.5 0 0 0 16.5 3c-1.76 0-3 .5-4.5 2-1.5-1.5-2.74-2-4.5-2A5.5 5.5 0 0 0 2 8.5c0 2.3 1.5 4.05 3 5.5l7 7Z" />
        </svg>
        <span>收藏</span>
      </router-link>
      <router-link v-if="auth.isAdmin" to="/admin" class="mobile-tab" :class="{ 'mobile-tab-active': route.name === 'admin' }">
        <svg class="w-5 h-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <circle cx="12" cy="12" r="3" />
          <path d="M12 2v3m0 14v3M4.93 4.93l2.12 2.12m9.9 9.9 2.12 2.12M2 12h3m14 0h3" />
        </svg>
        <span>管理</span>
      </router-link>
    </nav>

    <!-- Mobile Slide-out Libraries Drawer -->
    <teleport to="body">
      <div
        v-if="mobileDrawerOpen"
        class="fixed inset-0 z-50 md:hidden flex bg-slate-900/30 dark:bg-black/60 backdrop-blur-sm"
        @click.self="mobileDrawerOpen = false"
      >
        <div class="w-72 max-w-[82vw] h-full glass-strong p-4 flex flex-col gap-4 shadow-2xl">
          <div class="flex items-center justify-between pb-3 border-b border-slate-200/70 dark:border-white/10">
            <div class="flex items-center gap-2.5">
              <div class="w-8 h-8 rounded-xl accent-gradient text-white font-bold flex items-center justify-center">E</div>
              <span class="font-bold text-ink">{{ auth.serverName }}</span>
            </div>
            <button type="button" class="p-1.5 rounded-lg text-ink-muted" @click="mobileDrawerOpen = false">✕</button>
          </div>
          <div class="flex-1 overflow-y-auto space-y-1">
            <div class="px-2 pb-1 text-xs font-semibold text-ink-muted">我的媒体库</div>
            <router-link
              v-for="lib in libraries"
              :key="lib.Id"
              :to="{ name: 'library', params: { id: lib.Id } }"
              class="nav-item"
              @click="mobileDrawerOpen = false"
            >
              <span>{{ libraryIcon(lib) }}</span>
              <span class="flex-1 truncate">{{ lib.Name }}</span>
              <span v-if="lib.ChildCount !== undefined" class="text-xs text-ink-muted">{{ lib.ChildCount }}</span>
            </router-link>
          </div>
          <div class="pt-3 border-t border-slate-200/70 dark:border-white/10 flex items-center justify-between">
            <span class="text-xs text-ink-secondary">{{ auth.user?.Name }}</span>
            <button type="button" class="text-xs text-rose-600 dark:text-rose-400 font-medium" @click="handleLogout">
              退出登录
            </button>
          </div>
        </div>
      </div>
    </teleport>

    <!-- Global Ctrl+K Command Search -->
    <CommandSearchModal v-model:show="searchModalOpen" />
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { apiFetchCached, getToken, invalidateApiCache } from '../api/client'
import { useAuthStore } from '../stores/auth'
import { useThemeStore } from '../stores/theme'
import CommandSearchModal from '../components/CommandSearchModal.vue'

const auth = useAuthStore()
const themeStore = useThemeStore()
const route = useRoute()
const router = useRouter()

const libraries = ref<any[]>([])
const searchModalOpen = ref(false)
const mobileDrawerOpen = ref(false)
const activeTask = ref<any>(null)
let sse: EventSource | null = null

const collapsed = computed(() => themeStore.sidebarCollapsed)

function libraryIcon(lib: any) {
  if (String(lib.Id || '').startsWith('vlib-')) return '✨'
  if (lib.CollectionType === 'movies') return '🎞️'
  if (lib.CollectionType === 'tvshows') return '📺'
  if (lib.CollectionType === 'boxsets') return '🎬'
  return '🗂️'
}

function formatActiveTaskLabel(t: any): string {
  const msg = String(t?.message || '后台任务执行中')
  const libName = String(t?.library_name || '').trim()
  if (libName && !msg.includes(libName)) {
    return `[${libName}] ${msg}`
  }
  return msg
}

async function loadLibraries(force = false) {
  const uid = auth.user?.Id
  if (!uid) return
  try {
    const res = await apiFetchCached(`/emby/Users/${uid}/Views?IncludeHidden=true`, {
      ttlMs: 30000,
      force,
      onUpdate: (fresh) => {
        libraries.value = fresh?.Items || []
      },
    })
    libraries.value = res?.Items || []
  } catch {
    // ignore sidebar load error
  }
}

function onHomeSettingsUpdated() {
  loadLibraries(true)
}

function connectGlobalSSE() {
  if (!auth.isAdmin) return
  const token = getToken()
  if (!token) return
  sse = new EventSource(`/api/admin/tasks/events?api_key=${encodeURIComponent(token)}`)
  sse.addEventListener('task', (e: MessageEvent) => {
    try {
      const ev = JSON.parse(e.data)
      if (!ev?.task) return
      if (ev.task.status === 'running' || ev.task.status === 'pending') {
        activeTask.value = ev.task
      } else if (activeTask.value?.id === ev.task.id) {
        activeTask.value = null
        invalidateApiCache('/api/web/home')
        invalidateApiCache('/emby/')
        loadLibraries()
      }
    } catch {
      // ignore malformed event
    }
  })
}

function onKeyDown(e: KeyboardEvent) {
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
    e.preventDefault()
    searchModalOpen.value = !searchModalOpen.value
  }
}

async function handleLogout() {
  invalidateApiCache()
  await auth.logout()
  router.push({ name: 'login' })
}

onMounted(() => {
  loadLibraries()
  connectGlobalSSE()
  window.addEventListener('keydown', onKeyDown)
  window.addEventListener('emby-lite:home-settings-updated', onHomeSettingsUpdated)
})

onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeyDown)
  window.removeEventListener('emby-lite:home-settings-updated', onHomeSettingsUpdated)
  sse?.close()
})
</script>

<style scoped>
.nav-item {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.625rem 0.75rem;
  border-radius: 1rem;
  font-size: 13px;
  font-weight: 500;
  color: var(--text-secondary);
  transition: all 0.2s ease;
}
.nav-item:hover {
  background: var(--surface-hover);
  color: var(--text-primary);
}
.nav-item-active {
  background: linear-gradient(135deg, rgba(14, 165, 233, 0.16) 0%, rgba(79, 70, 229, 0.14) 100%);
  color: var(--accent);
  box-shadow: var(--shadow-neu-sm);
  border: 1px solid rgba(14, 165, 233, 0.24);
}
.mobile-tab {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 0.125rem;
  padding: 0.25rem 0.75rem;
  border-radius: 0.75rem;
  font-size: 10px;
  font-weight: 500;
  color: var(--text-muted);
  transition: all 0.2s ease;
}
.mobile-tab-active {
  color: var(--accent);
  background: var(--accent-soft);
}
</style>
