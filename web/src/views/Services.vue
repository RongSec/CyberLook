<template>
  <div>
    <n-card title="应用服务">
      <p class="tip">本页只展示<b>已确认</b>的 Web 资产（在「资产处置」确认过的）；待处置的请到「资产处置」处理。</p>
      <div class="filters">
        <n-input v-model:value="filters.keyword" placeholder="搜索全部字段（IP/端口/服务/标题/响应码）" clearable style="width: 320px" @keyup.enter="search" />
        <n-select v-model:value="filters.statusCodes" multiple :options="statusCodeOptions" placeholder="响应码（可多选）" clearable style="width: 220px" />
        <n-input v-model:value="filters.ip" placeholder="IP" clearable style="width: 130px" @keyup.enter="search" />
        <n-input v-model:value="filters.port" placeholder="端口" clearable style="width: 90px" @keyup.enter="search" />
        <n-input v-model:value="filters.service" placeholder="服务名" clearable style="width: 110px" @keyup.enter="search" />
        <n-button type="primary" @click="search">查询</n-button>
        <n-button @click="reset">重置</n-button>
        <n-tag round>{{ pagination.itemCount }} 条</n-tag>
        <n-tag round>{{ pagination.itemCount }} 条</n-tag>
        <ExportButton type="services" title="应用服务" :columns="exportCols" :filters="filters" />
        <n-button @click="exportUrls">导出 URL</n-button>
        <n-popconfirm @positive-click="batchDelete" positive-text="删除" negative-text="取消">
          <template #trigger>
            <n-button type="error" :disabled="checked.length === 0">
              批量删除{{ checked.length ? '(' + checked.length + ')' : '' }}
            </n-button>
          </template>
          确认删除选中的 {{ checked.length }} 条服务记录？
        </n-popconfirm>
      </div>
      <n-data-table :columns="columns" :data="items" :loading="loading" :row-key="r => r.id"
        :checked-row-keys="checked" @update:checked-row-keys="keys => checked = keys"
        :pagination="{ ...pagination, showSizePicker: true, pageSizes: [10, 20, 50, 100], onUpdatePageSize: onPageSize }"
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
const checked = ref([])
const items = ref([])
const page = ref(1)
const pageSize = ref(20)
const pagination = ref({ page: 1, pageSize: 20, itemCount: 0 })
const filters = ref({ keyword: '', statusCodes: [], ip: '', port: '', service: '', protocol: '' })
const statusCodeOptions = [
  { label: '200 正常', value: '200' }, { label: '301 永久跳转', value: '301' },
  { label: '302 临时跳转', value: '302' }, { label: '401 未授权', value: '401' },
  { label: '403 禁止访问', value: '403' }, { label: '404 不存在', value: '404' },
  { label: '500 服务器错误', value: '500' }, { label: '502 网关错误', value: '502' },
  { label: '503 服务不可用', value: '503' }
]

const columns = [
  { type: 'selection', width: 44, fixed: 'left' },
  { title: 'IP', key: 'ip', width: 140 },
  { title: '部门', key: 'department', width: 130, ellipsis: { tooltip: true }, render: r => r.department || '—' },
  { title: '负责人', key: 'owner', width: 110, ellipsis: { tooltip: true }, render: r => r.owner || '—' },
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
    title: '操作', key: 'actions', width: 170,
    render: row => h('div', { style: 'display:flex;gap:6px' }, [
      h(NButton, { size: 'small', text: true, type: 'primary', onClick: () => copyUrl(row) }, { default: () => '复制URL' }),
      h(NPopconfirm, { onPositiveClick: () => del(row) },
        { trigger: () => h(NButton, { size: 'small', text: true, type: 'error' }, { default: () => '删除' }),
          default: () => `确认删除 ${row.ip}:${row.port} ？` })
    ])
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
      params: {
        page: page.value, size: pageSize.value, webOnly: 'true',
        ...filters.value,
        statusCodes: (filters.value.statusCodes || []).join(','),
        confirmedOnly: 'true', // 应用服务只展示已确认资产

      }
    })
    items.value = res.items
    pagination.value = { page: res.page, pageSize: res.size, itemCount: res.total }
  } finally {
    loading.value = false
  }
}

function copyUrl(row) {
  const proto = (row.protocol || '').toLowerCase().includes('https') ? 'https' : 'http'
  const url = proto + '://' + row.ip + (row.port && row.port !== 80 && row.port !== 443 ? ':' + row.port : '')
  navigator.clipboard.writeText(url).then(
    () => message.success('已复制：' + url),
    () => message.warning('复制失败，请手动复制：' + url)
  )
}

function search() { page.value = 1; load() }

async function batchDelete() {
  if (checked.value.length === 0) { message.warning('请先选择服务'); return }
  try {
    await api.post('/services/batch-delete', { ids: checked.value })
    message.success(`已删除 ${checked.value.length} 条`)
    checked.value = []
    load()
  } catch (e) { message.error(e.response?.data?.error || '删除失败') }
}

function onPage(p) { page.value = p; load() }
function onPageSize(sz) { pageSize.value = sz; page.value = 1; load() }
function reset() {
  filters.value = { keyword: '', statusCodes: [], ip: '', port: '', service: '', protocol: '' }
  load()
}

onMounted(load)
</script>

<style scoped>
.filters { display: flex; gap: 10px; align-items: center; margin-bottom: 16px; flex-wrap: wrap; }
.tip { color: var(--text-muted); font-size: 12px; margin: 0 0 12px; }
</style>
