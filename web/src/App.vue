<template>
  <n-config-provider :theme="themeStore.naiveTheme" :theme-overrides="themeStore.naiveThemeOverrides">
    <n-message-provider>
      <router-view />
    </n-message-provider>
  </n-config-provider>
</template>

<script setup lang="ts">
import { NConfigProvider, NMessageProvider } from 'naive-ui'
import { useThemeStore } from './stores/theme'

const themeStore = useThemeStore()
</script>
