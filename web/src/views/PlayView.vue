<template>
  <div class="space-y-4">
    <div class="flex items-center justify-between gap-4">
      <div class="flex items-center gap-3">
        <n-button size="small" secondary @click="router.back()">← 返回</n-button>
        <h1 class="text-lg font-semibold truncate">{{ itemName }}</h1>
      </div>
      <div v-if="extPlay" class="flex items-center gap-2">
        <n-button size="small" secondary @click="copyUrl">复制直链</n-button>
        <n-button size="small" quaternary tag="a" :href="extPlay.players.iina">IINA</n-button>
        <n-button size="small" quaternary tag="a" :href="extPlay.players.potplayer">PotPlayer</n-button>
        <n-button size="small" quaternary tag="a" :href="extPlay.players.infuse">Infuse</n-button>
      </div>
    </div>

    <div class="rounded-xl overflow-hidden bg-black border border-cinema-border aspect-video relative">
      <video
        v-if="streamUrl"
        ref="videoRef"
        :src="streamUrl"
        controls
        autoplay
        class="w-full h-full"
        @loadedmetadata="onLoadedMetadata"
        @timeupdate="onTimeUpdate"
        @play="report('start')"
        @pause="report('progress')"
        @ended="report('stop')"
        @error="onVideoError"
      />
      <!-- Skip Intro Floating Button -->
      <div v-if="showSkipIntro" class="absolute right-6 bottom-16 z-20">
        <n-button type="primary" size="medium" class="shadow-lg" @click="skipIntro">
          跳过片头 →
        </n-button>
      </div>
      <div v-if="inCredits" class="absolute right-6 bottom-16 z-20">
        <span class="px-3 py-1.5 rounded-lg bg-black/75 border border-cinema-border text-xs text-slate-200">
          片尾已开始
        </span>
      </div>
      <div v-if="playbackError" class="absolute inset-0 bg-black/85 flex flex-col items-center justify-center p-6 text-center">
        <p class="text-amber-400 font-medium mb-2">浏览器无法直接解码当前视频格式（本服务为纯直连/302 模式，不转码）</p>
        <p class="text-xs text-slate-400 mb-4">建议使用上方外部播放器（IINA / PotPlayer / Infuse）或复制直链播放。</p>
        <n-button v-if="extPlay" type="primary" size="small" @click="copyUrl">复制直连播放地址</n-button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NButton, useMessage } from 'naive-ui'
import { apiFetch } from '../api/client'
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

onMounted(async () => {
  const uid = auth.user?.Id
  if (!uid || !itemId) return
  const [detail, pb, ext, intro] = await Promise.all([
    apiFetch(`/emby/Users/${uid}/Items/${itemId}`),
    apiFetch(`/emby/Items/${itemId}/PlaybackInfo`, { method: 'POST', body: '{}' }),
    apiFetch(`/api/web/external-play/${itemId}`).catch(() => null),
    apiFetch(`/api/web/intro/${itemId}`).catch(() => null),
  ])
  itemName.value = detail?.Name || '播放中'
  resumeSeconds.value = (detail?.UserData?.PlaybackPositionTicks || 0) / 1e7
  playSessionId.value = pb?.PlaySessionId || ''
  const src = pb?.MediaSources?.[0]
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
