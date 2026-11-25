<template>
  <div class="min-h-screen bg-cinema-bg flex items-center justify-center p-4">
    <n-card class="max-w-sm w-full border border-cinema-border bg-cinema-surface">
      <div class="text-center mb-6">
        <div class="text-2xl font-bold text-emerald-400">{{ auth.serverName }}</div>
        <div class="text-xs text-slate-400 mt-1">Emby 兼容媒体服务器 · 纯直连 / 302</div>
      </div>
      <n-form @submit.prevent="submit">
        <n-form-item label="用户名">
          <n-input v-model:value="username" placeholder="请输入用户名" />
        </n-form-item>
        <n-form-item label="密码">
          <n-input v-model:value="password" type="password" show-password-on="click" placeholder="请输入密码" />
        </n-form-item>
        <n-button type="primary" block :loading="loading" attr-type="submit">登录</n-button>
      </n-form>
    </n-card>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NButton, NCard, NForm, NFormItem, NInput, useMessage } from 'naive-ui'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
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
