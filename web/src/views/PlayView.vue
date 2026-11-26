<template>
  <div class="space-y-4">
    <!-- Glass Player Control Header -->
    <div class="glass-panel rounded-2xl px-4 py-3 flex flex-wrap items-center justify-between gap-3">
      <div class="flex items-center gap-3 min-w-0">
        <n-button size="small" secondary @click="router.back()">← 返回</n-button>
        <h1 class="text-base sm:text-lg font-bold text-ink truncate">{{ itemName }}</h1>
      </div>
      <div class="flex flex-wrap items-center gap-1.5">
        <template v-if="mediaSources.length > 1">
          <button
            v-for="(src, idx) in mediaSources"
            :key="src.Id"
            type="button"
            class="px-2.5 py-1 rounded-xl text-xs font-semibold transition"
            :class="
              mediaSourceId === src.Id
                ? 'accent-gradient text-white shadow'
                : 'glass-pill text-ink-secondary hover:text-sky-600 dark:hover:text-sky-400'
            "
            @click="switchSource(src)"
          >
            {{ sourceShortLabel(src, idx) }}
          </button>
        </template>
        <template v-if="extPlay">
          <n-button size="small" secondary @click="copyUrl">复制直链</n-button>
          <n-button size="small" quaternary tag="a" :href="extPlay.players.iina">IINA</n-button>
          <n-button size="small" quaternary tag="a" :href="extPlay.players.potplayer">PotPlayer</n-button>
          <n-button size="small" quaternary tag="a" :href="extPlay.players.infuse">Infuse</n-button>
          <n-button v-if="extPlay.players.vlc" size="small" quaternary tag="a" :href="extPlay.players.vlc">VLC</n-button>
        </template>
      </div>
    </div>

    <!-- Cinema Dark Theater Container (always dark for true video contrast) -->
    <div class="rounded-3xl overflow-hidden bg-slate-950 border border-white/20 shadow-2xl aspect-video relative max-h-[80vh] mx-auto w-full">
      <video
        v-if="streamUrl"
        ref="videoRef"
        :src="streamUrl"
        controls
        autoplay
        class="w-full h-full bg-black"
        @loadedmetadata="onLoadedMetadata"
        @timeupdate="onTimeUpdate"
        @play="report('start')"
        @pause="report('progress')"
        @ended="report('stop')"
        @error="onVideoError"
      />
      <div v-else class="w-full h-full flex items-center justify-center text-slate-400 text-sm">
        正在准备直连播放流...
      </div>

      <!-- Skip Intro Floating Glass Button -->
      <div v-if="showSkipIntro" class="absolute right-6 bottom-16 z-20">
        <button
          type="button"
          class="px-5 py-2.5 rounded-2xl bg-white/90 hover:bg-white text-slate-900 font-semibold text-sm shadow-2xl backdrop-blur-md transition"
          @click="skipIntro"
        >
          跳过片头 →
        </button>
      </div>
      <div v-if="inCredits" class="absolute right-6 bottom-16 z-20">
        <span class="px-3.5 py-2 rounded-xl bg-black/70 backdrop-blur-md border border-white/20 text-xs text-slate-100">
          片尾已开始
        </span>
      </div>

      <!-- Direct-play decode fallback notice -->
      <div v-if="playbackError" class="absolute inset-0 bg-slate-950/90 backdrop-blur-md flex flex-col items-center justify-center p-6 text-center">
        <p class="text-amber-400 font-semibold text-base mb-2">
          浏览器无法直接解码当前视频编码（本服务为纯直连 / 302 模式，不转码）
        </p>
        <p class="text-xs text-slate-300 mb-5 max-w-md">
          建议使用上方外部原生播放器（IINA / PotPlayer / Infuse / VLC）或复制直链获得无损硬解播放体验。
        </p>
        <n-button v-if="extPlay" type="primary" @click="copyUrl">复制直连播放地址</n-button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NButton, useMessage } from 'naive-ui'
import { apiFetch, invalidateApiCache } from '../api/client'
import { useAuthStore } from '../stores/auth'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const message = useMessage()

const itemId = String(route.params.id || '')
const itemName = ref('播放中')
const streamUrl = ref('')
const playSessionId = ref('')
const mediaSourceId = ref('')
const mediaSources = ref<any[]>([])
const resumeSeconds = ref(0)
const extPlay = ref<any>(null)
const playbackError = ref(false)
const videoRef = ref<HTMLVideoElement | null>(null)
const introMarker = ref<{ has_marker: boolean; intro_start_secs: number; intro_end_secs: number; credits_start_secs: number } | null>(null)
const showSkipIntro = ref(false)
const inCredits = ref(false)
let timer: number | undefined

async function report(kind: 'start' | 'progress' | 'stop') {
  const v = videoRef.value
  const posTicks = v ? Math.floor(v.currentTime * 1e7) : 0
  const durTicks = v && Number.isFinite(v.duration) ? Math.floor(v.duration * 1e7) : 0
  const path =
    kind === 'start'
      ? '/emby/Sessions/Playing'
      : kind === 'stop'
        ? '/emby/Sessions/Playing/Stopped'
        : '/emby/Sessions/Playing/Progress'
  await apiFetch(path, {
    method: 'POST',
    body: JSON.stringify({
      ItemId: itemId,
      MediaSourceId: mediaSourceId.value,
      PlaySessionId: playSessionId.value,
      PositionTicks: posTicks,
      RunTimeTicks: durTicks > 0 ? durTicks : undefined,
      IsPaused: v ? v.paused : false,
    }),
  }).catch(() => {})
  if (kind === 'stop') {
    invalidateApiCache('/api/web/home')
  }
}

function onLoadedMetadata() {
  if (videoRef.value && resumeSeconds.value > 5 && resumeSeconds.value < videoRef.value.duration - 10) {
    videoRef.value.currentTime = resumeSeconds.value
  }
}

function onTimeUpdate() {
  const v = videoRef.value
  const m = introMarker.value
  if (!v || !m || !m.has_marker) return
  const t = v.currentTime
  showSkipIntro.value = m.intro_end_secs > m.intro_start_secs && t >= m.intro_start_secs && t < m.intro_end_secs
  inCredits.value = m.credits_start_secs > 0 && t >= m.credits_start_secs
}

function skipIntro() {
  if (videoRef.value && introMarker.value?.intro_end_secs) {
    videoRef.value.currentTime = introMarker.value.intro_end_secs
    showSkipIntro.value = false
  }
}

function onVideoError() {
  playbackError.value = true
}

async function copyUrl() {
  if (!extPlay.value?.direct_url) return
  await navigator.clipboard.writeText(extPlay.value.direct_url)
  message.success('已复制直连地址')
}

function sourceShortLabel(src: any, idx: number): string {
  const full = String(src?.Name || '').trim()
  const prefix = itemName.value + ' - '
  if (full.startsWith(prefix) && full.length > prefix.length) {
    return full.slice(prefix.length)
  }
  const parts: string[] = []
  const w = Number(src?.Width || 0)
  const h = Number(src?.Height || 0)
  if (w >= 3800 || h >= 2100) parts.push('4K')
  else if (w >= 1900 || h >= 1000) parts.push('1080P')
  else if (w >= 1200 || h >= 700) parts.push('720P')
  if (src?.HDRType && src.HDRType !== 'SDR') parts.push(String(src.HDRType).toUpperCase())
  if (src?.VideoCodec) parts.push(String(src.VideoCodec).toUpperCase())
  return parts.length > 0 ? parts.join(' ') : `版本 ${idx + 1}`
}

async function switchSource(src: any) {
  if (!src || src.Id === mediaSourceId.value) return
  if (videoRef.value && Number.isFinite(videoRef.value.currentTime)) {
    resumeSeconds.value = videoRef.value.currentTime
  }
  playbackError.value = false
  mediaSourceId.value = src.Id
  streamUrl.value = '/emby' + src.DirectStreamUrl
  extPlay.value = await apiFetch(`/api/web/external-play/${itemId}?media_source_id=${encodeURIComponent(src.Id)}`).catch(() => extPlay.value)
}

onMounted(async () => {
  const uid = auth.user?.Id
  if (!uid || !itemId) return
  const querySourceId = typeof route.query.source === 'string' ? route.query.source : ''
  const [detail, pb, ext, intro] = await Promise.all([
    apiFetch(`/emby/Users/${uid}/Items/${itemId}`),
    apiFetch(`/emby/Items/${itemId}/PlaybackInfo`, { method: 'POST', body: '{}' }),
    apiFetch(`/api/web/external-play/${itemId}${querySourceId ? `?media_source_id=${encodeURIComponent(querySourceId)}` : ''}`).catch(() => null),
    apiFetch(`/api/web/intro/${itemId}`).catch(() => null),
  ])
  itemName.value = detail?.Name || '播放中'
  resumeSeconds.value = (detail?.UserData?.PlaybackPositionTicks || 0) / 1e7
  playSessionId.value = pb?.PlaySessionId || ''
  mediaSources.value = pb?.MediaSources || []
  const src = (querySourceId && mediaSources.value.find((s) => s.Id === querySourceId)) || mediaSources.value[0]
  if (src) {
    mediaSourceId.value = src.Id
    streamUrl.value = '/emby' + src.DirectStreamUrl
  }
  extPlay.value = ext
  introMarker.value = intro
  timer = window.setInterval(() => {
    if (videoRef.value && !videoRef.value.paused) {
      report('progress')
    }
  }, 10000)
})

onBeforeUnmount(() => {
  if (timer) window.clearInterval(timer)
  report('stop')
})
</script>
