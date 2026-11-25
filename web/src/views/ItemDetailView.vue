<template>
  <div v-if="item" class="space-y-8">
    <!-- Hero -->
    <div class="relative rounded-2xl overflow-hidden border border-cinema-border bg-cinema-surface">
      <div
        v-if="backdropUrl"
        class="absolute inset-0 bg-cover bg-center opacity-20 pointer-events-none"
        :style="{ backgroundImage: `url(${backdropUrl})` }"
      ></div>
      <div class="relative p-6 md:p-8 flex flex-col md:flex-row gap-6">
        <div class="w-44 shrink-0 rounded-xl overflow-hidden bg-slate-900 border border-cinema-border aspect-[2/3]">
          <img v-if="posterUrl" :src="posterUrl" :alt="item.Name" class="w-full h-full object-cover" />
          <div v-else class="w-full h-full flex items-center justify-center text-xs text-slate-500 p-3 text-center">
            {{ item.Name }}
          </div>
        </div>

        <div class="flex-1 space-y-3">
          <div class="flex flex-wrap items-center gap-2">
            <h1 class="text-2xl md:text-3xl font-bold text-white">{{ item.Name }}</h1>
            <span v-if="item.ProductionYear" class="text-slate-400 text-lg">({{ item.ProductionYear }})</span>
          </div>
          <div v-if="item.OriginalTitle && item.OriginalTitle !== item.Name" class="text-sm text-slate-400">
            {{ item.OriginalTitle }}
          </div>
          <div class="flex flex-wrap items-center gap-2 text-xs">
            <n-tag v-if="item.CommunityRating" type="warning" size="small">★ {{ item.CommunityRating.toFixed(1) }}</n-tag>
            <n-tag v-if="item.OfficialRating" size="small">{{ item.OfficialRating }}</n-tag>
            <n-tag v-for="g in item.Genres || []" :key="g" size="small" :bordered="false">{{ g }}</n-tag>
          </div>
          <p v-if="item.Overview" class="text-sm text-slate-300 leading-relaxed max-w-3xl">
            {{ item.Overview }}
          </p>

          <!-- Actions -->
          <div class="flex flex-wrap items-center gap-2 pt-2">
            <n-button
              v-if="item.Type === 'Movie' || item.Type === 'Episode'"
              type="primary"
              @click="router.push({ name: 'play', params: { id: item.Id } })"
            >
              ▶ 网页直出播放
            </n-button>
            <n-button v-if="externalPlay" secondary @click="copyDirectUrl">复制直链</n-button>
            <n-button v-if="externalPlay" quaternary tag="a" :href="externalPlay.players.iina">IINA</n-button>
            <n-button v-if="externalPlay" quaternary tag="a" :href="externalPlay.players.potplayer">PotPlayer</n-button>
            <n-button v-if="externalPlay" quaternary tag="a" :href="externalPlay.players.infuse">Infuse</n-button>
            <n-button secondary @click="toggleFavorite">
              {{ item.UserData?.IsFavorite ? '♥ 已收藏' : '♡ 收藏' }}
            </n-button>
            <n-button secondary @click="togglePlayed">
              {{ item.UserData?.Played ? '✓ 已观看' : '标记已看' }}
            </n-button>
            <n-button v-if="auth.isAdmin" secondary type="info" @click="showMetaEditor = true">
              ✎ 编辑元数据
            </n-button>
          </div>
        </div>
      </div>
    </div>

    <!-- Cast & Crew -->
    <section v-if="item.People && item.People.length > 0" class="space-y-3">
      <h2 class="text-lg font-semibold">演职人员</h2>
      <div class="grid grid-cols-3 sm:grid-cols-5 md:grid-cols-7 lg:grid-cols-8 gap-3">
        <div
          v-for="person in item.People.slice(0, 16)"
          :key="person.Id + person.Type"
          class="rounded-xl bg-cinema-card border border-cinema-border/60 p-2 text-center"
        >
          <div class="w-16 h-16 mx-auto rounded-full overflow-hidden bg-slate-800 mb-2">
            <img
              v-if="person.PrimaryImageTag"
              :src="personImageUrl(person.Id)"
              :alt="person.Name"
              loading="lazy"
              class="w-full h-full object-cover"
            />
            <div v-else class="w-full h-full flex items-center justify-center text-xs text-slate-500">
              {{ person.Name.slice(0, 1) }}
            </div>
          </div>
          <div class="text-xs font-medium text-slate-100 truncate">{{ person.Name }}</div>
          <div class="text-[11px] text-slate-400 truncate">{{ person.Role || person.Type }}</div>
        </div>
      </div>
    </section>

    <!-- TV Seasons & Episodes -->
    <section v-if="item.Type === 'Series' && seasons.length > 0" class="space-y-4">
      <div class="flex items-center gap-2 flex-wrap">
        <n-button
          v-for="s in seasons"
          :key="s.Id"
          size="small"
          :type="activeSeasonId === s.Id ? 'primary' : 'default'"
          @click="selectSeason(s.Id)"
        >
          {{ s.Name }}
        </n-button>
      </div>
      <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
        <div
          v-for="ep in episodes"
          :key="ep.Id"
          class="rounded-xl bg-cinema-card border border-cinema-border/70 p-4 flex items-center justify-between gap-4"
        >
          <div class="min-w-0">
            <div class="text-sm font-medium text-white truncate">
              E{{ ep.IndexNumber ?? '?' }} · {{ ep.Name }}
            </div>
            <div v-if="ep.Overview" class="text-xs text-slate-400 line-clamp-2 mt-1">{{ ep.Overview }}</div>
          </div>
          <div class="flex items-center gap-2 shrink-0">
            <n-button size="small" type="primary" @click="router.push({ name: 'play', params: { id: ep.Id } })">
              播放
            </n-button>
            <n-button size="small" secondary @click="router.push({ name: 'item', params: { id: ep.Id } })">
              详情
            </n-button>
          </div>
        </div>
      </div>
    </section>

    <!-- Admin Metadata Editor Modal -->
    <MetadataEditorModal
      v-if="auth.isAdmin && item"
      v-model:show="showMetaEditor"
      :item-id="item.Id"
      @saved="load"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NButton, NTag, useMessage } from 'naive-ui'
import { apiFetch, getToken, itemImageUrl } from '../api/client'
import { useAuthStore } from '../stores/auth'
import MetadataEditorModal from '../components/MetadataEditorModal.vue'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const message = useMessage()

const item = ref<any>(null)
const seasons = ref<any[]>([])
const episodes = ref<any[]>([])
const activeSeasonId = ref('')
const externalPlay = ref<any>(null)
const showMetaEditor = ref(false)

const posterUrl = computed(() => {
  if (!item.value) return ''
  const tag = item.value.ImageTags?.Primary
  if (tag) return itemImageUrl(item.value.Id, 'Primary', tag)
  if (item.value.SeriesId && item.value.SeriesPrimaryImageTag) {
    return itemImageUrl(item.value.SeriesId, 'Primary', item.value.SeriesPrimaryImageTag)
  }
  return ''
})

const backdropUrl = computed(() => {
  const tags = item.value?.BackdropImageTags
  if (Array.isArray(tags) && tags.length > 0) {
    return itemImageUrl(item.value.Id, 'Backdrop', tags[0])
  }
  return ''
})

function personImageUrl(personId: string): string {
  const token = getToken()
  return `/emby/Persons/${encodeURIComponent(personId)}/Images/Primary?maxWidth=185&api_key=${encodeURIComponent(token)}`
}

async function load() {
  const id = String(route.params.id || '')
  const uid = auth.user?.Id
  if (!id || !uid) return
  item.value = await apiFetch(`/emby/Users/${uid}/Items/${id}`)
  seasons.value = []
  episodes.value = []
  externalPlay.value = null

  if (item.value.Type === 'Series') {
    const sRes = await apiFetch(`/emby/Shows/${id}/Seasons`)
    seasons.value = sRes?.Items || []
    if (seasons.value.length > 0) {
      await selectSeason(seasons.value[0].Id)
    }
  } else if (item.value.Type === 'Movie' || item.value.Type === 'Episode') {
    externalPlay.value = await apiFetch(`/api/web/external-play/${id}`).catch(() => null)
  }
}

async function selectSeason(seasonId: string) {
  activeSeasonId.value = seasonId
  const res = await apiFetch(`/emby/Shows/${item.value.Id}/Episodes?SeasonId=${seasonId}`)
  episodes.value = res?.Items || []
}

async function toggleFavorite() {
  const uid = auth.user?.Id
  if (!uid || !item.value) return
  const method = item.value.UserData?.IsFavorite ? 'DELETE' : 'POST'
  item.value.UserData = await apiFetch(`/emby/Users/${uid}/FavoriteItems/${item.value.Id}`, { method })
}

async function togglePlayed() {
  const uid = auth.user?.Id
  if (!uid || !item.value) return
  const method = item.value.UserData?.Played ? 'DELETE' : 'POST'
  item.value.UserData = await apiFetch(`/emby/Users/${uid}/PlayedItems/${item.value.Id}`, { method })
}

async function copyDirectUrl() {
  if (!externalPlay.value?.direct_url) return
  await navigator.clipboard.writeText(externalPlay.value.direct_url)
  message.success('已复制直连播放地址')
}

watch(() => route.params.id, load)
onMounted(load)
</script>
