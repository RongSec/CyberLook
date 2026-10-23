<template>
  <div>
    <n-card title="应用服务">
      <p class="tip">仅显示已完成处置（已登记资产）的 Web 服务；待处置的请到「资产处置」处理。</p>
      <div class="filters">
        <n-input v-model:value="filters.ip" placeholder="IP" clearable style="width: 150px" @keyup.enter="search" />
        <n-input v-model:value="filters.port" placeholder="端口" clearable style="width: 110px" @keyup.enter="search" />
        <n-input v-model:value="filters.service" placeholder="服务名" clearable style="width: 130px" @keyup.enter="search" />
        <n-input v-model:value="filters.protocol" placeholder="协议" clearable style="width: 110px" @keyup.enter="search" />
        <n-button type="primary" @click="search">查询</n-button>
        <n-button @click="reset">重置</n-button>
        <ExportButton type="services" title="应用服务" :columns="exportCols" :filters="filters" />
        <n-button @click="exportUrls">导出 URL</n-button>
      </div>
      <n-data-table :columns="columns" :data="items" :loading="loading" :pagination="{ ...pagination, showSizePicker: true, pageSizes: [10, 20, 50, 100], onUpdatePageSize: onPageSize }"
        remote @update:page="onPage" />
    </n-card>
  </div>
</template>

<script setup>
import { ref, h, onMounted } from 'vue'
import { NTag, NButton, NPopconfirm, useMessage } from 'naive-ui'
import api from '../api'

const message = useMessage()
import ExportButton from '../components/ExportButton.vue'

// 导出列
function fmtLen(n) {
  if (!n) return '-'
  return n >= 1024 ? (n / 1024).toFixed(1) + ' KB' : n + ' B'
}

const exportCols = [
  { key: 'ip', label: 'IP' },
  { key: 'port', label: '端口' },
  { key: 'protocol', label: '协议' },
  { key: 'service', label: '服务' },
  { key: 'statusCode', label: '响应码' },
  { key: 'contentLength', label: '响应长度' },
  { key: 'title', label: '网站标题' },
  { key: 'fingerprint', label: '指纹' },
  { key: 'lastSeen', label: '最近发现' }
]

const loading = ref(false)
const items = ref([])
const page = ref(1)
const pageSize = ref(20)
const pagination = ref({ page: 1, pageSize: 20, itemCount: 0 })
const filters = ref({ ip: '', port: '', service: '', protocol: '' })

const columns = [
  { title: 'IP', key: 'ip', width: 140 },
  { title: '端口', key: 'port', width: 80 },
  { title: '协议', key: 'protocol', width: 80 },
  {
    title: '响应码', key: 'statusCode', width: 90,
    render: r => {
      const code = r.statusCode || ''
      if (!code) return '-'
      const n = parseInt(code)
      const type = n >= 200 && n < 300 ? 'success' : n >= 300 && n < 400 ? 'info' : n >= 400 ? 'error' : 'default'
      return h(NTag, { size: 'small', type }, { default: () => code })
    }
  },
  { title: '响应长度', key: 'contentLength', width: 100, render: r => fmtLen(r.contentLength) },
  { title: '网站标题', key: 'title', ellipsis: { tooltip: true }, render: r => r.title || '-' },
  { title: '指纹', key: 'fingerprint', width: 150, ellipsis: { tooltip: true }, render: r => r.fingerprint || '-' },
  { title: 'Banner', key: 'banner', ellipsis: { tooltip: true } },
  { title: '最近发现', key: 'lastSeen', width: 160, render: r => fmt(r.lastSeen) },
  {
    title: '操作', key: 'actions', width: 90,
    render: row => h(NPopconfirm, { onPositiveClick: () => del(row) },
      { trigger: () => h(NButton, { size: 'small', text: true, type: 'error' }, { default: () => '删除' }),
        default: () => `确认删除 ${row.ip}:${row.port} ？` })
  }
]

function fmt(t) { return t ? new Date(t).toLocaleString('zh-CN') : '-' }

async function del(row) {
  try {
    await api.delete(`/services/${row.id}`)
    message.success('已删除')
    load()
  } catch (e) { message.error(e.response?.data?.error || '删除失败') }
}

async function load() {
  loading.value = true
  try {
    const res = await api.get('/services', {
      params: { page: page.value, size: pageSize.value, webOnly: 'true', confirmedOnly: 'true', ...filters.value }
    })
    items.value = res.items
    pagination.value = { page: res.page, pageSize: res.size, itemCount: res.total }
  } finally {
    loading.value = false
  }
}

function search() { page.value = 1; load() }
function onPage(p) { page.value = p; load() }
function onPageSize(sz) { pageSize.value = sz; page.value = 1; load() }
function reset() {
  filters.value = { ip: '', port: '', service: '', protocol: '' }
  load()
}

onMounted(load)
</script>

<style scoped>
.filters { display: flex; gap: 10px; align-items: center; margin-bottom: 16px; flex-wrap: wrap; }
.tip { color: var(--text-muted); font-size: 12px; margin: 0 0 12px; }
</style>
