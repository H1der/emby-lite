<template>
  <div class="ambient-canvas min-h-screen flex items-center justify-center p-4 relative">
    <button
      type="button"
      class="fixed top-4 right-4 w-10 h-10 rounded-2xl glass-pill flex items-center justify-center text-ink-secondary hover:text-sky-600 dark:hover:text-sky-400 transition"
      :title="themeStore.isDark ? '切换浅色主题' : '切换深色主题'"
      @click="themeStore.toggleMode()"
    >
      {{ themeStore.isDark ? '☀️' : '🌙' }}
    </button>

    <div class="glass-strong w-full max-w-sm rounded-3xl p-7 sm:p-8 space-y-6">
      <div class="text-center space-y-2">
        <div class="w-14 h-14 mx-auto rounded-2xl accent-gradient text-white text-2xl font-bold flex items-center justify-center shadow-lg">
          E
        </div>
        <div class="text-2xl font-bold text-ink tracking-tight">{{ auth.serverName }}</div>
        <div class="text-xs text-ink-muted">Emby 兼容轻量媒体服务器 · 纯直连 / 302</div>
      </div>

      <n-form @submit.prevent="submit" label-placement="top">
        <n-form-item label="用户名">
          <n-input v-model:value="username" placeholder="请输入用户名" size="large" />
        </n-form-item>
        <n-form-item label="密码">
          <n-input v-model:value="password" type="password" show-password-on="click" placeholder="请输入密码" size="large" />
        </n-form-item>
        <n-button type="primary" block size="large" :loading="loading" attr-type="submit" class="mt-2">
          登录进入媒体库
        </n-button>
      </n-form>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NButton, NForm, NFormItem, NInput, useMessage } from 'naive-ui'
import { useAuthStore } from '../stores/auth'
import { useThemeStore } from '../stores/theme'

const auth = useAuthStore()
const themeStore = useThemeStore()
const router = useRouter()
const route = useRoute()
const message = useMessage()

const username = ref('')
const password = ref('')
const loading = ref(false)

async function submit() {
  loading.value = true
  try {
    await auth.login(username.value.trim(), password.value)
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/'
    router.push(redirect)
  } catch (e: any) {
    message.error(e.message || '登录失败')
  } finally {
    loading.value = false
  }
}
</script>
