<template>
  <div class="ambient-canvas min-h-screen flex items-center justify-center p-4 relative">
    <button
      type="button"
      class="fixed top-4 right-4 w-10 h-10 rounded-2xl glass-pill flex items-center justify-center text-ink-secondary hover:text-sky-600 dark:hover:text-sky-400 transition"
      @click="themeStore.toggleMode()"
    >
      {{ themeStore.isDark ? '☀️' : '🌙' }}
    </button>

    <div class="glass-strong w-full max-w-md rounded-3xl p-7 sm:p-8 space-y-6">
      <div class="text-center space-y-2">
        <div class="w-14 h-14 mx-auto rounded-2xl accent-gradient text-white text-2xl font-bold flex items-center justify-center shadow-lg">
          E
        </div>
        <h1 class="text-2xl font-bold text-ink tracking-tight">初始化 Emby-Lite</h1>
        <p class="text-xs text-ink-muted leading-relaxed">
          欢迎使用轻量级 Emby 兼容媒体服务器，请创建首位管理员账号。
        </p>
      </div>

      <n-form @submit.prevent="submit" label-placement="top">
        <n-form-item label="服务器名称">
          <n-input v-model:value="serverName" placeholder="例如：家庭影院 NAS" size="large" />
        </n-form-item>
        <n-form-item label="管理员用户名">
          <n-input v-model:value="username" placeholder="admin" size="large" />
        </n-form-item>
        <n-form-item label="管理员密码（至少 4 位）">
          <n-input v-model:value="password" type="password" show-password-on="click" placeholder="输入强密码" size="large" />
        </n-form-item>
        <n-button type="primary" block size="large" :loading="loading" attr-type="submit" class="mt-2">
          完成初始化并登录
        </n-button>
      </n-form>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { NButton, NForm, NFormItem, NInput, useMessage } from 'naive-ui'
import { useAuthStore } from '../stores/auth'
import { useThemeStore } from '../stores/theme'

const auth = useAuthStore()
const themeStore = useThemeStore()
const router = useRouter()
const message = useMessage()

const serverName = ref(auth.serverName || 'Emby-Lite')
const username = ref('admin')
const password = ref('')
const loading = ref(false)

async function submit() {
  if (!username.value.trim() || password.value.length < 4) {
    message.warning('请填写用户名且密码至少 4 位')
    return
  }
  loading.value = true
  try {
    await auth.setup(username.value.trim(), password.value, serverName.value.trim())
    message.success('初始化完成')
    router.push({ name: 'admin' })
  } catch (e: any) {
    message.error(e.message || '初始化失败')
  } finally {
    loading.value = false
  }
}
</script>
