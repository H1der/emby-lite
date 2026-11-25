<template>
  <div :class="rail ? 'glass-rail' : ['poster-grid', compact ? 'poster-grid-compact' : '']">
    <div
      v-for="n in count"
      :key="n"
      class="glass-card rounded-2xl p-1.5"
      :class="rail ? 'w-36 sm:w-44 shrink-0' : ''"
    >
      <div class="aspect-[2/3] rounded-xl skeleton-shimmer"></div>
      <div class="px-1.5 pt-2.5 pb-1 space-y-1.5">
        <div class="h-3.5 w-4/5 rounded-md skeleton-shimmer"></div>
        <div class="h-2.5 w-1/2 rounded-md skeleton-shimmer"></div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
withDefaults(defineProps<{ count?: number; rail?: boolean; compact?: boolean }>(), {
  count: 12,
  rail: false,
  compact: false,
})
</script>
