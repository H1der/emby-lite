<template>
  <div class="space-y-6 mt-2">
    <!-- Permission Groups -->
    <n-card size="small" title="权限组模板">
      <template #header-extra>
        <n-button size="tiny" type="primary" @click="openCreateGroup">+ 新建权限组</n-button>
      </template>
      <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
        <div
          v-for="g in groups"
          :key="g.id"
          class="rounded-xl border border-cinema-border bg-cinema-bg p-3 flex items-start justify-between gap-3"
        >
          <div class="space-y-1 min-w-0">
            <div class="font-medium text-white">{{ g.name }}</div>
            <div class="text-xs text-slate-400">{{ g.description || '无描述' }}</div>
            <div class="flex flex-wrap gap-1.5 pt-1">
              <n-tag size="tiny" :type="g.allow_playback ? 'success' : 'error'">
                {{ g.allow_playback ? '允许播放' : '禁播' }}
              </n-tag>
              <n-tag size="tiny" :type="g.allow_download ? 'info' : 'default'">
                {{ g.allow_download ? '允许下载' : '禁下载' }}
              </n-tag>
              <n-tag size="tiny">设备上限 {{ g.max_devices || '不限' }}</n-tag>
              <n-tag size="tiny">并发播放 {{ g.max_concurrent_streams || '不限' }}</n-tag>
            </div>
          </div>
          <div class="flex items-center gap-1 shrink-0">
            <n-button size="tiny" secondary @click="openEditGroup(g)">编辑</n-button>
            <n-button size="tiny" type="error" quaternary @click="deleteGroup(g.id)">删除</n-button>
          </div>
        </div>
      </div>
    </n-card>

    <!-- Devices & Active Sessions -->
    <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
      <n-card size="small" title="历史登录设备">
        <template #header-extra>
          <n-button size="tiny" secondary @click="load">刷新</n-button>
        </template>
        <div v-if="devices.length === 0" class="py-6 text-center text-xs text-slate-500">暂无设备记录</div>
        <div v-else class="space-y-2 max-h-80 overflow-y-auto">
          <div
            v-for="d in devices"
            :key="d.user_id + ':' + d.device_id"
            class="rounded-lg border border-cinema-border bg-cinema-bg p-2.5 flex items-center justify-between gap-2"
          >
            <div class="min-w-0">
              <div class="text-xs font-medium text-white truncate">
                {{ d.username || d.user_id }} · {{ d.client }} ({{ d.device_name }})
                <n-tag v-if="d.blocked" size="tiny" type="error" class="ml-1">已封禁</n-tag>
              </div>
              <div class="text-[11px] text-slate-400">
                IP: {{ d.ip }} · 最近活跃：{{ formatTime(d.last_seen_at) }}
              </div>
            </div>
            <div class="flex items-center gap-1 shrink-0">
              <n-button size="tiny" secondary @click="toggleBlock(d)">
                {{ d.blocked ? '解封' : '封禁' }}
              </n-button>
              <n-button size="tiny" type="error" quaternary @click="removeDevice(d)">踢下线</n-button>
            </div>
          </div>
        </div>
      </n-card>

      <n-card size="small" title="活跃播放会话">
        <div v-if="sessions.length === 0" class="py-6 text-center text-xs text-slate-500">当前无活跃播放</div>
        <div v-else class="space-y-2 max-h-80 overflow-y-auto">
          <div
            v-for="s in sessions"
            :key="s.play_session_id"
            class="rounded-lg border border-cinema-border bg-cinema-bg p-2.5 flex items-center justify-between gap-2"
          >
            <div class="min-w-0">
              <div class="text-xs font-medium text-white truncate">
                {{ s.username }} 正在观看：{{ s.item_name || s.item_id }}
              </div>
              <div class="text-[11px] text-slate-400">
                {{ s.client }} ({{ s.device_name }}) · {{ s.is_paused ? '已暂停' : '播放中' }}
              </div>
            </div>
            <n-button size="tiny" type="error" secondary @click="killSession(s.play_session_id)">终止</n-button>
          </div>
        </div>
      </n-card>
    </div>

    <!-- Permission Group Modal -->
    <n-modal v-model:show="showGroupModal" preset="card" :title="editingGroupId ? '编辑权限组' : '新建权限组'" class="max-w-lg">
      <n-form label-placement="top" size="small" @submit.prevent="saveGroup">
        <n-form-item label="权限组名称">
          <n-input v-model:value="groupForm.name" />
        </n-form-item>
        <n-form-item label="描述">
          <n-input v-model:value="groupForm.description" />
        </n-form-item>
        <div class="grid grid-cols-2 gap-3">
          <n-form-item label="最大设备数（0 表示不限）">
            <n-input-number v-model:value="groupForm.max_devices" :min="0" class="w-full" />
          </n-form-item>
          <n-form-item label="同时播放流上限（0 不限）">
            <n-input-number v-model:value="groupForm.max_concurrent_streams" :min="0" class="w-full" />
          </n-form-item>
        </div>
        <n-form-item label="组级可见媒体库白名单（留空默认全部非隐藏库）">
          <n-select v-model:value="groupForm.library_ids" multiple :options="libraryOptions" />
        </n-form-item>
        <div class="flex flex-wrap gap-4 mb-4">
          <n-checkbox v-model:checked="groupForm.allow_playback">允许播放</n-checkbox>
          <n-checkbox v-model:checked="groupForm.allow_download">允许下载</n-checkbox>
          <n-checkbox v-model:checked="groupForm.can_view_hidden">可查看隐藏库</n-checkbox>
        </div>
        <n-button type="primary" block attr-type="submit">保存权限组</n-button>
      </n-form>
    </n-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import {
  NButton, NCard, NCheckbox, NForm, NFormItem, NInput, NInputNumber, NModal, NSelect, NTag, useMessage,
} from 'naive-ui'
import { apiFetch } from '../api/client'

const props = defineProps<{ libraries: any[] }>()
const emit = defineEmits<{ (e: 'changed'): void }>()
const message = useMessage()

const groups = ref<any[]>([])
const devices = ref<any[]>([])
const sessions = ref<any[]>([])

const showGroupModal = ref(false)
const editingGroupId = ref('')
const groupForm = reactive({
  name: '',
  description: '',
  allow_playback: true,
  allow_download: false,
  max_devices: 5,
  max_concurrent_streams: 3,
  can_view_hidden: false,
  library_ids: [] as string[],
})

const libraryOptions = computed(() => props.libraries.map((l) => ({ label: l.name, value: l.id })))

function formatTime(iso: string) {
  return iso ? new Date(iso).toLocaleString() : ''
}

async function load() {
  const [g, d, s] = await Promise.all([
    apiFetch('/api/admin/permission-groups'),
    apiFetch('/api/admin/devices'),
    apiFetch('/api/admin/sessions'),
  ])
  groups.value = g.items || []
  devices.value = d.items || []
  sessions.value = s.items || []
}

function openCreateGroup() {
  editingGroupId.value = ''
  groupForm.name = ''
  groupForm.description = ''
  groupForm.allow_playback = true
  groupForm.allow_download = false
  groupForm.max_devices = 5
  groupForm.max_concurrent_streams = 3
  groupForm.can_view_hidden = false
  groupForm.library_ids = []
  showGroupModal.value = true
}

function openEditGroup(g: any) {
  editingGroupId.value = g.id
  groupForm.name = g.name || ''
  groupForm.description = g.description || ''
  groupForm.allow_playback = Boolean(g.allow_playback)
  groupForm.allow_download = Boolean(g.allow_download)
  groupForm.max_devices = g.max_devices || 0
  groupForm.max_concurrent_streams = g.max_concurrent_streams || 0
  groupForm.can_view_hidden = Boolean(g.can_view_hidden)
  groupForm.library_ids = [...(g.library_ids || [])]
  showGroupModal.value = true
}

async function saveGroup() {
  if (!groupForm.name.trim()) {
    message.warning('请输入权限组名称')
    return
  }
  if (editingGroupId.value) {
    await apiFetch(`/api/admin/permission-groups/${editingGroupId.value}`, { method: 'PUT', body: JSON.stringify(groupForm) })
    message.success('权限组已更新')
  } else {
    await apiFetch('/api/admin/permission-groups', { method: 'POST', body: JSON.stringify(groupForm) })
    message.success('权限组已创建')
  }
  showGroupModal.value = false
  await load()
  emit('changed')
}

async function deleteGroup(id: string) {
  await apiFetch(`/api/admin/permission-groups/${id}`, { method: 'DELETE' })
  message.success('权限组已删除')
  await load()
  emit('changed')
}

async function toggleBlock(d: any) {
  await apiFetch(`/api/admin/devices/${d.user_id}/${encodeURIComponent(d.device_id)}/block`, {
    method: 'POST',
    body: JSON.stringify({ blocked: !d.blocked }),
  })
  message.success(d.blocked ? '设备已解封' : '设备已封禁并注销登录')
  await load()
}

async function removeDevice(d: any) {
  await apiFetch(`/api/admin/devices/${d.user_id}/${encodeURIComponent(d.device_id)}`, { method: 'DELETE' })
  message.success('已踢下线并移除设备记录')
  await load()
}

async function killSession(sid: string) {
  await apiFetch(`/api/admin/sessions/${encodeURIComponent(sid)}`, { method: 'DELETE' })
  message.success('播放会话已终止')
  await load()
}

onMounted(load)
</script>
