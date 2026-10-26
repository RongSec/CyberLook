<template>
  <div>
    <n-card>
      <n-tabs type="line" animated>
        <!-- 故障 -->
        <n-tab-pane name="faults" tab="故障">
          <div class="toolbar">
            <n-select v-model:value="kindFilter" :options="kindOptions" placeholder="全部类型"
              clearable style="width: 160px" @update:value="loadFaults" />
            <n-button secondary @click="loadFaults">刷新</n-button>
            <span class="hint">服务端中断、节点离线/熔断、分片失败均会记录在此</span>
            <span style="flex:1"></span>
            <span class="hint">故障保留</span>
            <n-input-number v-model:value="retentionDays" :min="1" :max="365" size="small" style="width: 90px" />
            <span class="hint">天</span>
            <n-button size="small" type="primary" :loading="savingRetention" @click="saveRetention">保存</n-button>
            <span style="flex:1"></span>
            <span class="hint">天</span>
            <n-button size="small" type="primary" :loading="savingRetention" @click="saveRetention">保存</n-button>
          </div>
          <n-data-table :columns="faultCols" :data="faults" :loading="loadingFaults" size="small"
            :pagination="{ pageSize: 20 }" :row-key="r => r.id" />
        </n-tab-pane>

        <!-- 日志 -->
        <n-tab-pane name="logs" tab="日志">
          <div class="toolbar">
            <n-button secondary @click="loadLogs">刷新</n-button>
            <span class="hint">故障与任务执行记录，按时间倒序</span>
          </div>
          <n-data-table :columns="logCols" :data="logs" :loading="loadingLogs" size="small"
            :pagination="{ pageSize: 50 }" :row-key="(_, i) => i" />
        </n-tab-pane>
      </n-tabs>
    </n-card>
  </div>
</template>

<script setup>
import { ref, h, onMounted } from 'vue'
import { NTag, NButton, NInputNumber, useMessage } from 'naive-ui'
import api from '../api'

const message = useMessage()

const kindFilter = ref(null)
const faults = ref([])
const loadingFaults = ref(false)

const logs = ref([])
const loadingLogs = ref(false)

const retentionDays = ref(30)
const savingRetention = ref(false)

async function loadRetention() {
  try {
    const r = await api.get('/faults/retention')
    retentionDays.value = r.days || 30
  } catch (e) { /* 用默认值 */ }
}

async function saveRetention() {
  savingRetention.value = true
  try {
    await api.post('/faults/retention', { days: retentionDays.value })
    message.success(`故障记录保留期已设为 ${retentionDays.value} 天，每日 03:00 自动清理`)
  } catch (e) {
    message.error(e.response?.data?.error || '保存失败')
  } finally {
    savingRetention.value = false
  }
}

const kindOptions = [
  { label: '服务端中断', value: 'server_restart' },
  { label: '节点离线', value: 'node_offline' },
  { label: '节点熔断', value: 'node_fused' },
  { label: '分片失败', value: 'shard_failed' }
]

const kindMap = {
  server_restart: ['error', '服务端中断'],
  node_offline: ['warning', '节点离线'],
  node_fused: ['error', '节点熔断'],
  shard_failed: ['error', '分片失败']
}

async function loadFaults() {
  loadingFaults.value = true
  try {
    const params = { page: 1, size: 200 }
    if (kindFilter.value) params.kind = kindFilter.value
    const res = await api.get('/faults', { params })
    faults.value = res.items || []
  } catch (e) {
    message.error('加载故障失败：' + (e?.message || e))
  } finally {
    loadingFaults.value = false
  }
}

async function loadLogs() {
  loadingLogs.value = true
  try {
    const res = await api.get('/system/logs', { params: { page: 1, size: 200 } })
    logs.value = res.items || []
  } catch (e) {
    message.error('加载日志失败：' + (e?.message || e))
  } finally {
    loadingLogs.value = false
  }
}

const faultCols = [
  { title: '时间', key: 'createdAt', width: 170, render: r => fmt(r.createdAt) },
  {
    title: '类型', key: 'kind', width: 110,
    render: r => {
      const [type, text] = kindMap[r.kind] || ['default', r.kind]
      return h(NTag, { type, size: 'small' }, { default: () => text })
    }
  },
  { title: '节点', key: 'nodeName', width: 150, render: r => r.nodeName || '-' },
  { title: '任务', key: 'taskId', width: 80, render: r => r.taskId ? `#${r.taskId}` : '-' },
  { title: '描述', key: 'message', ellipsis: { tooltip: true }, render: r => r.message }
]

const levelTag = {
  error: ['error', 'ERROR'],
  warn: ['warning', 'WARN'],
  info: ['info', 'INFO']
}

const logCols = [
  { title: '时间', key: 'time', width: 170, render: r => r.time },
  {
    title: '级别', key: 'level', width: 90,
    render: r => {
      const [type, text] = levelTag[r.level] || ['default', r.level]
      return h(NTag, { type, size: 'small' }, { default: () => text })
    }
  },
  { title: '来源', key: 'source', width: 180, render: r => r.source },
  { title: '内容', key: 'message', ellipsis: { tooltip: true }, render: r => r.message }
]

function fmt(t) {
  if (!t) return '-'
  const d = new Date(t)
  const p = n => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

onMounted(() => {
  loadFaults()
  loadLogs()
  loadRetention()
})
</script>

<style scoped>
.toolbar { display: flex; gap: 8px; align-items: center; margin-bottom: 12px; }
.hint { color: #888; font-size: 12px; }
</style>
