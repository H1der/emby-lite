<template>
  <div class="min-h-screen bg-cinema-bg flex items-center justify-center p-4">
    <n-card class="max-w-md w-full border border-cinema-border bg-cinema-surface" title="初始化 Emby-Lite">
      <p class="text-sm text-slate-400 mb-5">
        欢迎使用轻量级 Emby 兼容媒体服务器。请先创建首位管理员账号。
      </p>
      <n-form @submit.prevent="submit">
        <n-form-item label="服务器名称">
          <n-input v-model:value="serverName" placeholder="例如：家庭影院 NAS" />
        </n-form-item>
        <n-form-item label="管理员用户名">
          <n-input v-model:value="username" placeholder="admin" />
        </n-form-item>
        <n-form-item label="管理员密码（至少 4 位）">
          <n-input v-model:value="password" type="password" show-password-on="click" placeholder="输入强密码" />
        </n-form-item>
        <n-button type="primary" block :loading="loading" attr-type="submit">
          完成初始化并登录
        </n-button>
      </n-form>
    </n-card>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { NButton, NCard, NForm, NFormItem, NInput, useMessage } from 'naive-ui'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
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
