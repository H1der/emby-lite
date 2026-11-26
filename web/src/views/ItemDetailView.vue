<template>
  <div class="space-y-8">
    <!-- Loading Skeleton -->
    <div v-if="loading && !item" class="glass-panel rounded-3xl p-6 md:p-8 flex flex-col md:flex-row gap-6">
      <div class="w-44 sm:w-52 shrink-0 aspect-[2/3] rounded-2xl skeleton-shimmer mx-auto md:mx-0"></div>
      <div class="flex-1 space-y-4">
        <div class="h-8 w-2/3 rounded-xl skeleton-shimmer"></div>
        <div class="h-4 w-1/3 rounded-lg skeleton-shimmer"></div>
        <div class="h-24 w-full rounded-xl skeleton-shimmer"></div>
        <div class="flex gap-2">
          <div class="h-10 w-32 rounded-xl skeleton-shimmer"></div>
          <div class="h-10 w-24 rounded-xl skeleton-shimmer"></div>
        </div>
      </div>
    </div>

    <template v-else-if="item">
      <!-- Frosted Glass Hero -->
      <div class="relative rounded-3xl overflow-hidden glass-panel">
        <div
          v-if="backdropUrl"
          class="absolute inset-0 bg-cover bg-center scale-105 opacity-25 dark:opacity-30 pointer-events-none"
          :style="{ backgroundImage: `url(${backdropUrl})` }"
        ></div>
        <div
          class="absolute inset-0 bg-gradient-to-br from-white/80 via-white/70 to-sky-50/65 dark:from-slate-950/85 dark:via-slate-900/75 dark:to-slate-950/85 backdrop-blur-md pointer-events-none"
        ></div>

        <div class="relative z-10 p-5 sm:p-7 lg:p-9 flex flex-col md:flex-row gap-6 lg:gap-8">
          <!-- Poster Frame -->
          <div class="w-40 sm:w-48 lg:w-56 shrink-0 mx-auto md:mx-0">
            <div class="rounded-2xl overflow-hidden glass-card p-1.5 shadow-xl aspect-[2/3]">
              <img
                v-if="posterUrl"
                :src="posterUrl"
                :alt="item.Name"
                class="w-full h-full object-cover rounded-xl"
              />
              <div
                v-else
                class="w-full h-full rounded-xl flex items-center justify-center text-xs text-ink-muted p-4 text-center bg-gradient-to-br from-sky-100/70 to-indigo-100/70 dark:from-slate-800 dark:to-slate-900"
              >
                {{ item.Name }}
              </div>
            </div>
          </div>

          <!-- Metadata Column -->
          <div class="flex-1 min-w-0 space-y-4">
            <div class="space-y-1">
              <div class="flex flex-wrap items-baseline gap-2.5">
                <h1 class="text-2xl sm:text-3xl lg:text-4xl font-bold text-ink tracking-tight leading-tight">
                  {{ item.Name }}
                </h1>
                <span v-if="item.ProductionYear" class="text-lg font-medium text-ink-muted">
                  ({{ item.ProductionYear }})
                </span>
              </div>
              <div v-if="item.OriginalTitle && item.OriginalTitle !== item.Name" class="text-sm text-ink-muted">
                {{ item.OriginalTitle }}
              </div>
              <div v-if="item.Taglines?.length" class="text-xs italic text-sky-700 dark:text-sky-300 pt-0.5">
                “{{ item.Taglines[0] }}”
              </div>
            </div>

            <!-- Ratings, Genres & Media Spec Badges -->
            <div class="flex flex-wrap items-center gap-2 text-xs">
              <span
                v-if="item.CommunityRating"
                class="px-2.5 py-1 rounded-xl bg-amber-400/20 border border-amber-500/30 text-amber-700 dark:text-amber-300 font-bold"
              >
                ★ {{ Number(item.CommunityRating).toFixed(1) }}
              </span>
              <span
                v-if="item.OfficialRating"
                class="px-2.5 py-1 rounded-xl glass-pill text-ink-secondary font-semibold"
              >
                {{ item.OfficialRating }}
              </span>
              <span
                v-if="runtimeLabel"
                class="px-2.5 py-1 rounded-xl glass-pill text-ink-secondary font-medium"
              >
                ⏱ {{ runtimeLabel }}
              </span>
              <span
                v-for="spec in mediaSpecs"
                :key="spec"
                class="px-2.5 py-1 rounded-xl bg-sky-500/12 border border-sky-500/25 text-sky-700 dark:text-sky-300 font-semibold"
              >
                {{ spec }}
              </span>
              <span
                v-for="g in item.Genres || []"
                :key="g"
                class="px-2.5 py-1 rounded-xl neu-inset text-ink-secondary font-medium"
              >
                {{ g }}
              </span>
            </div>

            <p v-if="item.Overview" class="text-sm text-ink-secondary leading-relaxed max-w-4xl">
              {{ item.Overview }}
            </p>

            <!-- Action Buttons -->
            <div class="flex flex-wrap items-center gap-2.5 pt-2">
              <button
                v-if="item.Type === 'Movie' || item.Type === 'Episode'"
                type="button"
                class="px-5 py-2.5 rounded-2xl accent-gradient text-white text-sm font-semibold shadow-lg hover:brightness-110 active:scale-[0.98] transition flex items-center gap-2"
                @click="router.push({ name: 'play', params: { id: item.Id } })"
              >
                <span>▶</span>
                <span>{{ item.UserData?.PlaybackPositionTicks > 0 ? '继续播放' : '网页直出播放' }}</span>
              </button>

              <template v-if="externalPlay">
                <n-button secondary @click="copyDirectUrl">复制直链</n-button>
                <n-button quaternary tag="a" :href="externalPlay.players.iina">IINA</n-button>
                <n-button quaternary tag="a" :href="externalPlay.players.potplayer">PotPlayer</n-button>
                <n-button quaternary tag="a" :href="externalPlay.players.infuse">Infuse</n-button>
                <n-button v-if="externalPlay.players.vlc" quaternary tag="a" :href="externalPlay.players.vlc">VLC</n-button>
              </template>

              <n-button :type="item.UserData?.IsFavorite ? 'error' : 'default'" secondary @click="toggleFavorite">
                {{ item.UserData?.IsFavorite ? '♥ 已收藏' : '♡ 收藏' }}
              </n-button>
              <n-button :type="item.UserData?.Played ? 'success' : 'default'" secondary @click="togglePlayed">
                {{ item.UserData?.Played ? '✓ 已观看' : '标记已看' }}
              </n-button>
              <n-button v-if="auth.isAdmin" secondary type="info" @click="showMetaEditor = true">
                ✎ 编辑元数据
              </n-button>
            </div>
          </div>
        </div>
      </div>

      <!-- TV Seasons & Episodes -->
      <section v-if="item.Type === 'Series' && seasons.length > 0" class="space-y-4">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <h2 class="text-lg sm:text-xl font-bold text-ink tracking-tight">分季剧集</h2>
          <div class="flex items-center gap-1.5 flex-wrap">
            <button
              v-for="s in seasons"
              :key="s.Id"
              type="button"
              class="px-3.5 py-1.5 rounded-xl text-xs font-semibold transition"
              :class="
                activeSeasonId === s.Id
                  ? 'accent-gradient text-white shadow-md'
                  : 'glass-pill text-ink-secondary hover:text-sky-600 dark:hover:text-sky-400'
              "
              @click="selectSeason(s.Id)"
            >
              {{ s.Name }}
            </button>
          </div>
        </div>

        <div class="grid grid-cols-1 lg:grid-cols-2 3xl:grid-cols-3 gap-4">
          <div
            v-for="ep in episodes"
            :key="ep.Id"
            class="glass-card glass-card-interactive rounded-2xl p-3 flex gap-3.5 items-center"
          >
            <!-- Episode 16:9 Thumbnail -->
            <div
              class="relative w-32 sm:w-40 aspect-video rounded-xl overflow-hidden bg-slate-200/80 dark:bg-slate-800 shrink-0 cursor-pointer group"
              @click="router.push({ name: 'play', params: { id: ep.Id } })"
            >
              <img
                v-if="episodeThumbUrl(ep)"
                :src="episodeThumbUrl(ep)"
                :alt="ep.Name"
                loading="lazy"
                class="w-full h-full object-cover group-hover:scale-105 transition duration-300"
              />
              <div v-else class="w-full h-full flex items-center justify-center text-xs font-semibold text-ink-muted">
                E{{ ep.IndexNumber ?? '?' }}
              </div>
              <div class="absolute inset-0 bg-black/25 opacity-0 group-hover:opacity-100 transition flex items-center justify-center">
                <div class="w-9 h-9 rounded-full bg-white/90 text-sky-600 flex items-center justify-center shadow">▶</div>
              </div>
              <div
                v-if="ep.UserData?.Played"
                class="absolute top-1.5 left-1.5 px-1.5 py-0.5 rounded-md bg-emerald-500/90 text-white text-[10px] font-semibold"
              >
                已看
              </div>
              <div
                v-if="ep.UserData?.PlayedPercentage > 0 && !ep.UserData?.Played"
                class="absolute bottom-0 inset-x-0 h-1 bg-black/40"
              >
                <div
                  class="h-full bg-gradient-to-r from-sky-400 to-indigo-500"
                  :style="{ width: `${Math.min(100, Math.round(ep.UserData.PlayedPercentage))}%` }"
                ></div>
              </div>
            </div>

            <div class="min-w-0 flex-1 space-y-1">
              <div class="flex items-center justify-between gap-2">
                <div class="text-sm font-semibold text-ink truncate">
                  E{{ ep.IndexNumber ?? '?' }} · {{ ep.Name }}
                </div>
                <span v-if="ep.RunTimeTicks" class="text-[11px] text-ink-muted shrink-0">
                  {{ formatRuntime(ep.RunTimeTicks) }}
                </span>
              </div>
              <div v-if="ep.Overview" class="text-xs text-ink-muted line-clamp-2 leading-relaxed">
                {{ ep.Overview }}
              </div>
              <div class="flex items-center gap-2 pt-1">
                <n-button size="tiny" type="primary" @click="router.push({ name: 'play', params: { id: ep.Id } })">
                  ▶ 播放
                </n-button>
                <n-button size="tiny" secondary @click="router.push({ name: 'item', params: { id: ep.Id } })">
                  详情
                </n-button>
              </div>
            </div>
          </div>
        </div>
      </section>

      <!-- Cast & Crew -->
      <section v-if="item.People && item.People.length > 0" class="space-y-3">
        <div class="flex items-center justify-between gap-3">
          <h2 class="text-lg sm:text-xl font-bold text-ink tracking-tight">演职人员</h2>
          <n-button
            v-if="item.People.length > 20"
            size="tiny"
            secondary
            @click="showAllPeople = !showAllPeople"
          >
            {{ showAllPeople ? '收起' : `显示全部 ${item.People.length} 人` }}
          </n-button>
        </div>
        <div class="glass-rail">
          <div
            v-for="person in displayedPeople"
            :key="person.Id + person.Type"
            class="glass-card rounded-2xl p-3 w-28 sm:w-32 shrink-0 text-center snap-start"
          >
            <div class="w-16 h-16 mx-auto rounded-full overflow-hidden neu-inset mb-2">
              <img
                v-if="person.PrimaryImageTag"
                :src="personImageUrl(person.Id)"
                :alt="person.Name"
                loading="lazy"
                class="w-full h-full object-cover"
              />
              <div v-else class="w-full h-full flex items-center justify-center text-sm font-semibold text-ink-muted">
                {{ person.Name.slice(0, 1) }}
              </div>
            </div>
            <div class="text-xs font-semibold text-ink truncate">{{ person.Name }}</div>
            <div class="text-[11px] text-ink-muted truncate mt-0.5">{{ personRoleLabel(person) }}</div>
          </div>
        </div>
      </section>

      <!-- BoxSet Collections -->
      <section v-if="item.Type === 'BoxSet'" class="space-y-3.5">
        <h2 class="text-lg sm:text-xl font-bold text-ink tracking-tight">合集内容</h2>
        <div v-if="boxsetItems.length > 0" class="poster-grid">
          <PosterCard v-for="member in boxsetItems" :key="member.Id" :item="member" />
        </div>
        <p v-else class="text-sm text-ink-muted">这个合集里还没有影片。</p>
      </section>

      <!-- Similar Recommendations -->
      <section v-if="similarItems.length > 0" class="space-y-3.5">
        <h2 class="text-lg sm:text-xl font-bold text-ink tracking-tight">相关推荐</h2>
        <div class="poster-grid">
          <PosterCard v-for="sim in similarItems" :key="sim.Id" :item="sim" />
        </div>
      </section>

      <!-- Admin Metadata Editor Modal -->
      <MetadataEditorModal
        v-if="auth.isAdmin && item"
        v-model:show="showMetaEditor"
        :item-id="item.Id"
        @saved="onMetadataSaved"
      />
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NButton, useMessage } from 'naive-ui'
import { apiFetch, getToken, invalidateApiCache, itemImageUrl } from '../api/client'
import { useAuthStore } from '../stores/auth'
import MetadataEditorModal from '../components/MetadataEditorModal.vue'
import PosterCard from '../components/PosterCard.vue'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const message = useMessage()

const loading = ref(true)
const item = ref<any>(null)
const seasons = ref<any[]>([])
const episodes = ref<any[]>([])
const boxsetItems = ref<any[]>([])
const similarItems = ref<any[]>([])
const activeSeasonId = ref('')
const externalPlay = ref<any>(null)
const showMetaEditor = ref(false)
const showAllPeople = ref(false)

const displayedPeople = computed(() => {
  const people = item.value?.People || []
  return showAllPeople.value ? people : people.slice(0, 20)
})

const posterUrl = computed(() => {
  if (!item.value) return ''
  const tag = item.value.ImageTags?.Primary
  if (tag) return itemImageUrl(item.value.Id, 'Primary', tag, 480)
  if (item.value.SeriesId && item.value.SeriesPrimaryImageTag) {
    return itemImageUrl(item.value.SeriesId, 'Primary', item.value.SeriesPrimaryImageTag, 480)
  }
  return ''
})

const backdropUrl = computed(() => {
  const tags = item.value?.BackdropImageTags
  if (Array.isArray(tags) && tags.length > 0) {
    return itemImageUrl(item.value.Id, 'Backdrop', tags[0], 1280)
  }
  return ''
})

function formatRuntime(ticks?: number): string {
  if (!ticks || ticks <= 0) return ''
  const mins = Math.round(ticks / 1e7 / 60)
  if (mins < 60) return `${mins} 分钟`
  const h = Math.floor(mins / 60)
  const m = mins % 60
  return m > 0 ? `${h}h ${m}m` : `${h}h`
}

const runtimeLabel = computed(() => formatRuntime(item.value?.RunTimeTicks))

const mediaSpecs = computed(() => {
  const specs: string[] = []
  const src = item.value?.MediaSources?.[0]
  if (!src) return specs
  const w = Number(src.Width || 0)
  const h = Number(src.Height || 0)
  if (w >= 3800 || h >= 2100) specs.push('4K UHD')
  else if (w >= 1900 || h >= 1000) specs.push('1080P')
  else if (w >= 1200 || h >= 700) specs.push('720P')

  if (src.HDRType && src.HDRType !== 'SDR') specs.push(String(src.HDRType).toUpperCase())
  if (src.VideoCodec) specs.push(String(src.VideoCodec).toUpperCase())
  if (src.Container) specs.push(String(src.Container).toUpperCase())
  if (src.Size > 0) {
    const gb = src.Size / (1024 * 1024 * 1024)
    specs.push(gb >= 1 ? `${gb.toFixed(1)} GB` : `${(src.Size / (1024 * 1024)).toFixed(0)} MB`)
  }
  return specs
})

function episodeThumbUrl(ep: any): string {
  if (ep.ImageTags?.Primary) return itemImageUrl(ep.Id, 'Primary', ep.ImageTags.Primary, 420)
  if (ep.ImageTags?.Thumb) return itemImageUrl(ep.Id, 'Thumb', ep.ImageTags.Thumb, 420)
  return ''
}

const personRoleLabels: Record<string, string> = {
  Actor: '演员',
  Director: '导演',
  Writer: '编剧',
  Producer: '制片人',
  GuestStar: '客串',
  Composer: '作曲',
  Conductor: '指挥',
  Lyricist: '作词',
  Arranger: '编曲',
  Engineer: '工程师',
  Mixer: '混音',
  Performer: '表演者',
  Author: '作者',
  Illustrator: '插画',
  Editor: '剪辑',
  Translator: '翻译',
  Other: '其他',
}

function personRoleLabel(person: any): string {
  const role = String(person?.Role || '').trim()
  if (role) return role
  return personRoleLabels[person?.Type] || person?.Type || ''
}

function personImageUrl(personId: string): string {
  const token = getToken()
  return `/emby/Persons/${encodeURIComponent(personId)}/Images/Primary?maxWidth=185&api_key=${encodeURIComponent(token)}`
}

async function load() {
  const id = String(route.params.id || '')
  const uid = auth.user?.Id
  if (!id || !uid) return
  loading.value = true
  seasons.value = []
  episodes.value = []
  boxsetItems.value = []
  similarItems.value = []
  externalPlay.value = null
  try {
    const [detail, simRes] = await Promise.all([
      apiFetch(`/emby/Users/${uid}/Items/${id}`),
      apiFetch(`/emby/Items/${id}/Similar?Limit=12`).catch(() => ({ Items: [] })),
    ])
    item.value = detail
    similarItems.value = simRes?.Items || []

    if (detail?.Type === 'Series') {
      const sRes = await apiFetch(`/emby/Shows/${id}/Seasons`)
      seasons.value = sRes?.Items || []
      if (seasons.value.length > 0) {
        await selectSeason(seasons.value[0].Id)
      }
    } else if (detail?.Type === 'BoxSet') {
      const members = await apiFetch(`/emby/Users/${uid}/Items?ParentId=${encodeURIComponent(id)}&Limit=200`).catch(() => null)
      boxsetItems.value = members?.Items || []
    } else if (detail?.Type === 'Movie' || detail?.Type === 'Episode') {
      externalPlay.value = await apiFetch(`/api/web/external-play/${id}`).catch(() => null)
    }
  } finally {
    loading.value = false
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
  invalidateApiCache('/api/web/home')
}

async function togglePlayed() {
  const uid = auth.user?.Id
  if (!uid || !item.value) return
  const method = item.value.UserData?.Played ? 'DELETE' : 'POST'
  item.value.UserData = await apiFetch(`/emby/Users/${uid}/PlayedItems/${item.value.Id}`, { method })
  invalidateApiCache('/api/web/home')
}

async function copyDirectUrl() {
  if (!externalPlay.value?.direct_url) return
  await navigator.clipboard.writeText(externalPlay.value.direct_url)
  message.success('已复制直连播放地址')
}

function onMetadataSaved() {
  invalidateApiCache('/api/web/home')
  invalidateApiCache('/emby/')
  load()
}

watch(() => route.params.id, load)
onMounted(load)
</script>
