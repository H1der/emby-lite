<template>
  <router-link
    :to="{ name: 'item', params: { id: item.Id } }"
    class="glass-card glass-card-interactive group block rounded-2xl overflow-hidden p-1.5 select-none"
    :class="compact ? 'p-1' : 'p-1.5'"
  >
    <div class="relative aspect-[2/3] rounded-xl overflow-hidden bg-slate-200/70 dark:bg-slate-800/80">
      <img
        v-if="posterUrl"
        :src="posterUrl"
        :alt="item.Name"
        loading="lazy"
        decoding="async"
        class="w-full h-full object-cover group-hover:scale-[1.05] transition duration-500 ease-out"
      />
      <div
        v-else
        class="w-full h-full flex flex-col items-center justify-center text-ink-muted text-xs px-3 text-center gap-2 bg-gradient-to-br from-sky-100/70 via-indigo-50/70 to-teal-100/60 dark:from-slate-800 dark:via-slate-900 dark:to-slate-800"
      >
        <span class="text-2xl opacity-55">🎬</span>
        <span class="line-clamp-3 font-medium">{{ item.Name }}</span>
      </div>

      <!-- Subtle bottom vignette for progress bar readability -->
      <div
        v-if="progressPct > 0 || item.Type === 'Episode'"
        class="absolute inset-x-0 bottom-0 h-16 bg-gradient-to-t from-black/65 via-black/20 to-transparent pointer-events-none"
      ></div>

      <!-- Hover frosted play orb -->
      <div
        class="absolute inset-0 bg-slate-900/15 dark:bg-black/30 opacity-0 group-hover:opacity-100 transition-opacity duration-300 flex items-center justify-center pointer-events-none"
      >
        <div
          class="w-11 h-11 rounded-full bg-white/85 dark:bg-slate-900/80 backdrop-blur-md shadow-lg border border-white/80 dark:border-white/15 flex items-center justify-center text-sky-600 dark:text-sky-400 scale-90 group-hover:scale-100 transition-transform duration-300"
        >
          <svg class="w-5 h-5 translate-x-0.5" viewBox="0 0 24 24" fill="currentColor">
            <path d="M8 5.14v14l11-7-11-7z" />
          </svg>
        </div>
      </div>

      <!-- Rating pill -->
      <div
        v-if="item.CommunityRating"
        class="absolute top-2 right-2 px-1.5 py-0.5 rounded-lg bg-white/82 dark:bg-slate-900/80 backdrop-blur-md border border-white/70 dark:border-white/15 text-amber-600 dark:text-amber-400 text-[11px] font-bold shadow-sm"
      >
        ★ {{ Number(item.CommunityRating).toFixed(1) }}
      </div>

      <!-- Watched / Favorite badges -->
      <div class="absolute top-2 left-2 flex items-center gap-1">
        <span
          v-if="item.UserData?.Played"
          class="px-1.5 py-0.5 rounded-lg bg-emerald-500/90 text-white text-[10px] font-semibold shadow-sm backdrop-blur-sm"
        >
          已看
        </span>
        <span
          v-if="item.UserData?.IsFavorite"
          class="w-5 h-5 rounded-lg bg-rose-500/90 text-white text-[10px] flex items-center justify-center shadow-sm backdrop-blur-sm"
          title="已收藏"
        >
          ♥
        </span>
      </div>

      <!-- Episode pill -->
      <div
        v-if="item.Type === 'Episode' && item.ParentIndexNumber !== undefined && item.IndexNumber !== undefined"
        class="absolute bottom-2 left-2 px-1.5 py-0.5 rounded-md bg-black/60 backdrop-blur-sm text-white text-[10px] font-medium"
      >
        S{{ String(item.ParentIndexNumber).padStart(2, '0') }}E{{ String(item.IndexNumber).padStart(2, '0') }}
      </div>

      <!-- Playback progress bar -->
      <div
        v-if="progressPct > 0 && !item.UserData?.Played"
        class="absolute bottom-0 inset-x-0 h-1.5 bg-black/35 backdrop-blur-sm"
      >
        <div
          class="h-full rounded-r-full bg-gradient-to-r from-sky-400 via-cyan-400 to-indigo-500"
          :style="{ width: `${progressPct}%` }"
        ></div>
      </div>
    </div>

    <div class="px-1.5 pt-2 pb-1">
      <div class="text-[13px] sm:text-sm font-semibold truncate text-ink leading-snug" :title="item.Name">
        {{ item.Name }}
      </div>
      <div class="text-[11px] text-ink-muted mt-0.5 flex items-center justify-between gap-1.5">
        <span class="truncate">
          {{ subtitleLeft }}
        </span>
        <span
          v-if="typeBadge"
          class="shrink-0 px-1.5 py-0.2 rounded-md text-[10px] font-medium bg-sky-500/10 text-sky-700 dark:text-sky-300"
        >
          {{ typeBadge }}
        </span>
      </div>
    </div>
  </router-link>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { itemImageUrl } from '../api/client'

const props = withDefaults(defineProps<{ item: any; compact?: boolean }>(), {
  compact: false,
})

const posterUrl = computed(() => {
  const tag = props.item.ImageTags?.Primary
  if (tag) return itemImageUrl(props.item.Id, 'Primary', tag, 360)
  if (props.item.SeriesId && props.item.SeriesPrimaryImageTag) {
    return itemImageUrl(props.item.SeriesId, 'Primary', props.item.SeriesPrimaryImageTag, 360)
  }
  return ''
})

const progressPct = computed(() => {
  const p = props.item.UserData?.PlayedPercentage
  return typeof p === 'number' && p > 0 ? Math.min(100, Math.round(p)) : 0
})

const subtitleLeft = computed(() => {
  if (props.item.Type === 'Episode' && props.item.SeriesName) {
    return props.item.SeriesName
  }
  if (props.item.ProductionYear) return String(props.item.ProductionYear)
  if (props.item.Type === 'Series') return '剧集'
  if (props.item.Type === 'Movie') return '电影'
  return ''
})

const typeBadge = computed(() => {
  if (props.item.Type === 'Series') return '剧集'
  if (props.item.Type === 'Episode') return '单集'
  return ''
})
</script>
