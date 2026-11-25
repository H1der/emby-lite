<template>
  <router-link
    :to="{ name: 'item', params: { id: item.Id } }"
    class="poster-card group block rounded-lg overflow-hidden bg-cinema-card border border-cinema-border/60"
  >
    <div class="relative aspect-[2/3] bg-slate-900 overflow-hidden">
      <img
        v-if="posterUrl"
        :src="posterUrl"
        :alt="item.Name"
        loading="lazy"
        class="w-full h-full object-cover group-hover:scale-105 transition duration-300"
      />
      <div v-else class="w-full h-full flex items-center justify-center text-slate-500 text-xs px-3 text-center">
        {{ item.Name }}
      </div>
      <div
        v-if="item.CommunityRating"
        class="absolute top-2 right-2 px-1.5 py-0.5 rounded bg-black/75 text-amber-400 text-xs font-semibold"
      >
        ★ {{ item.CommunityRating.toFixed(1) }}
      </div>
      <div
        v-if="item.UserData?.Played"
        class="absolute top-2 left-2 px-1.5 py-0.5 rounded bg-emerald-500/90 text-slate-950 text-[10px] font-bold"
      >
        已看
      </div>
      <div
        v-if="progressPct > 0 && !item.UserData?.Played"
        class="absolute bottom-0 inset-x-0 h-1 bg-slate-800"
      >
        <div class="h-full bg-emerald-400" :style="{ width: `${progressPct}%` }"></div>
      </div>
    </div>
    <div class="p-2.5">
      <div class="text-sm font-medium truncate text-slate-100">{{ item.Name }}</div>
      <div class="text-xs text-slate-400 mt-0.5 flex items-center justify-between">
        <span>{{ item.ProductionYear || (item.Type === 'Series' ? '剧集' : '') }}</span>
        <span v-if="item.Type === 'Episode' && item.SeriesName" class="truncate max-w-[110px]">
          {{ item.SeriesName }}
        </span>
      </div>
    </div>
  </router-link>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { itemImageUrl } from '../api/client'

const props = defineProps<{ item: any }>()

const posterUrl = computed(() => {
  const tag = props.item.ImageTags?.Primary
  if (tag) return itemImageUrl(props.item.Id, 'Primary', tag)
  if (props.item.SeriesId && props.item.SeriesPrimaryImageTag) {
    return itemImageUrl(props.item.SeriesId, 'Primary', props.item.SeriesPrimaryImageTag)
  }
  return ''
})

const progressPct = computed(() => {
  const p = props.item.UserData?.PlayedPercentage
  return typeof p === 'number' && p > 0 ? Math.min(100, Math.round(p)) : 0
})
</script>
