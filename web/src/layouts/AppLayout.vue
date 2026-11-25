<template>
  <div class="min-h-screen bg-cinema-bg text-slate-100 flex flex-col">
    <header class="sticky top-0 z-30 border-b border-cinema-border/80 bg-cinema-surface/90 backdrop-blur">
      <div class="max-w-7xl mx-auto px-4 h-14 flex items-center justify-between gap-4">
        <div class="flex items-center gap-6">
          <router-link to="/" class="flex items-center gap-2 font-semibold text-lg tracking-tight text-emerald-400">
            <span class="inline-block w-2.5 h-2.5 rounded-full bg-emerald-400"></span>
            <span>{{ auth.serverName }}</span>
          </router-link>
          <nav class="hidden sm:flex items-center gap-4 text-sm">
            <router-link to="/" class="hover:text-emerald-400 transition" active-class="text-emerald-400 font-medium">
              媒体库
            </router-link>
            <router-link
              v-if="auth.isAdmin"
              to="/admin"
              class="hover:text-emerald-400 transition"
              active-class="text-emerald-400 font-medium"
            >
              管理后台
            </router-link>
          </nav>
        </div>

        <div class="flex items-center gap-3">
          <n-input
            v-model:value="searchQuery"
            size="small"
            placeholder="搜索影片 / 剧集 / 拼音首字母..."
            clearable
            class="w-44 sm:w-64"
            @keyup.enter="goSearch"
          />
          <n-button v-if="auth.isAdmin" size="small" quaternary @click="router.push('/admin')" class="sm:hidden">
            管理
          </n-button>
          <span class="text-xs text-slate-400 hidden md:inline">{{ auth.user?.Name }}</span>
          <n-button size="small" secondary @click="handleLogout">退出</n-button>
        </div>
      </div>
    </header>

    <main class="flex-1 max-w-7xl w-full mx-auto px-4 py-6">
      <router-view />
    </main>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { NButton, NInput } from 'naive-ui'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
const router = useRouter()
const searchQuery = ref('')

function goSearch() {
  const q = searchQuery.value.trim()
  if (!q) return
  router.push({ name: 'library', params: { id: 'all' }, query: { q } })
}

async function handleLogout() {
  await auth.logout()
  router.push({ name: 'login' })
}
</script>
