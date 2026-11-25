<template>
  <div class="space-y-8">
    <!-- Libraries & Virtual Libraries -->
    <section>
      <div class="flex items-center justify-between mb-3">
        <h2 class="text-lg font-semibold">我的媒体库</h2>
        <router-link v-if="auth.isAdmin" to="/admin" class="text-xs text-emerald-400 hover:underline">
          管理媒体库 →
        </router-link>
      </div>
      <div v-if="views.length === 0" class="rounded-xl border border-dashed border-cinema-border p-8 text-center text-slate-400">
        <p class="mb-3">还没有可见媒体库。</p>
        <n-button v-if="auth.isAdmin" type="primary" size="small" @click="router.push('/admin')">
          前往后台添加媒体库
        </n-button>
      </div>
      <div v-else class="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 gap-4">
        <router-link
          v-for="lib in views"
          :key="lib.Id"
          :to="{ name: 'library', params: { id: lib.Id } }"
          class="poster-card relative rounded-xl overflow-hidden bg-cinema-card border border-cinema-border/70 aspect-video flex flex-col justify-end p-3.5"
        >
          <img
            v-if="lib.ImageTags?.Primary"
            :src="itemImageUrl(lib.Id, 'Primary', lib.ImageTags.Primary)"
            :alt="lib.Name"
            loading="lazy"
            class="absolute inset-0 w-full h-full object-cover opacity-60"
          />
          <div class="absolute inset-0 bg-gradient-to-t from-black/90 via-black/40 to-transparent"></div>
          <div class="relative z-10">
            <div class="text-base font-semibold text-white truncate">{{ lib.Name }}</div>
            <div class="text-xs text-slate-300 flex items-center justify-between mt-0.5">
              <span>{{ lib.Id.startsWith('vlib-') ? '虚拟库' : kindLabel(lib.CollectionType) }}</span>
              <span v-if="lib.ChildCount !== undefined">{{ lib.ChildCount }} 项</span>
            </div>
          </div>
        </router-link>
      </div>
    </section>

    <!-- Continue Watching -->
    <section v-if="resumeItems.length > 0">
      <h2 class="text-lg font-semibold mb-3">继续观看</h2>
      <div class="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-5 lg:grid-cols-6 gap-4">
        <PosterCard v-for="item in resumeItems" :key="item.Id" :item="item" />
      </div>
    </section>

    <!-- Latest Added -->
    <section>
      <h2 class="text-lg font-semibold mb-3">最近添加</h2>
      <div v-if="latestItems.length === 0" class="text-sm text-slate-400">暂无媒体内容，扫描媒体库后将在此展示。</div>
      <div v-else class="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-5 lg:grid-cols-6 gap-4">
        <PosterCard v-for="item in latestItems" :key="item.Id" :item="item" />
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { NButton } from 'naive-ui'
import { apiFetch, itemImageUrl } from '../api/client'
import { useAuthStore } from '../stores/auth'
import PosterCard from '../components/PosterCard.vue'

const auth = useAuthStore()
const router = useRouter()
const views = ref<any[]>([])
const resumeItems = ref<any[]>([])
const latestItems = ref<any[]>([])

function kindLabel(k: string) {
  if (k === 'movies') return '电影'
  if (k === 'tvshows') return '剧集'
  return '混合'
}

onMounted(async () => {
  const uid = auth.user?.Id
  if (!uid) return
  const [vRes, rRes, lRes] = await Promise.all([
    apiFetch(`/emby/Users/${uid}/Views`),
    apiFetch(`/emby/Users/${uid}/Items/Resume?Limit=12`),
    apiFetch(`/emby/Users/${uid}/Items/Latest?Limit=18`),
  ])
  views.value = vRes?.Items || []
  resumeItems.value = rRes?.Items || []
  latestItems.value = Array.isArray(lRes) ? lRes : []
})
</script>
