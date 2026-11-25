<template>
  <n-config-provider :theme="darkTheme" :theme-overrides="themeOverrides">
    <n-message-provider>
      <router-view />
    </n-message-provider>
  </n-config-provider>
</template>

<script setup lang="ts">
import { NConfigProvider, NMessageProvider, darkTheme, type GlobalThemeOverrides } from 'naive-ui'

const themeOverrides: GlobalThemeOverrides = {
  common: {
    primaryColor: '#10b981',
    primaryColorHover: '#34d399',
    primaryColorPressed: '#059669',
    bodyColor: '#0b0e14',
    cardColor: '#141923',
    modalColor: '#141923',
    popoverColor: '#1a2230',
  },
}
</script>
