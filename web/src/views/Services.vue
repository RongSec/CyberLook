<template>
  <div>
    <n-card title="应用服务">
      <p class="tip">本页只展示<b>已确认</b>的 Web 资产（在「资产处置」确认过的）；待处置的请到「资产处置」处理。</p>
      <SearchBar :fields="splFields" @search="onSplSearch" @reset="onSplReset" />
      <div class="filters" style="margin-top:12px">
        <n-tag round>{{ pagination.itemCount }} 条</n-tag>
        <n-tag round>{{ pagination.itemCount }} 条</n-tag>
        <ExportButton type="services" title="应用服务" :columns="exportCols" :filters="filters" :conds="splConds" />
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
import { ref, computed, h, onMounted } from 'vue'
import { NTag, NButton, NPopconfirm, useMessage } from 'naive-ui'
import api from '../api'
import SearchBar from '../components/SearchBar.vue'

const message = useMessage()

function renderOwners(raw) {
  const list = String(raw || '').split(',').map(x => x.trim()).filter(Boolean)
  if (list.length === 0) return '—'
  return h('div', { style: 'display:flex;flex-wrap:wrap;gap:4px' },
    list.map(n => h(NTag, { size: 'small', bordered: false, round: true }, { default: () => n })))
}
import ExportButton from '../components/ExportButton.vue'

// 导出列
function fmtLen(n) {
  if (!n) return '-'
  return n >= 1024 ? (n / 1024).toFixed(1) + ' KB' : n + ' B'
}


const loading = ref(false)
const checked = ref([])
const items = ref([])
const page = ref(1)
const pageSize = ref(20)
const pagination = ref({ page: 1, pageSize: 20, itemCount: 0 })
const splConds = ref([])

function onSplSearch({ conditions }) {
  splConds.value = conditions || []
  page.value = 1
  load()
}
function onSplReset() {
  splConds.value = []
  filters.value = { keyword: '', statusCodes: [], ip: '', port: '', service: '' }
  page.value = 1
  load()
}

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
  { title: '负责人', key: 'owner', width: 150, render: r => renderOwners(r.owner) },
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

// 检索字段 = 表格所有列（排除操作列），新增列时自动多一项
// 固定选项字段（不给手输）
const SPL_FIXED_OPTIONS = {
  protocol: [{ label: 'https', value: 'https' }, { label: 'http', value: 'http' }],
  statusCode: {
    multiple: true, filterable: true,
    options: [
      { label: '200', value: '200' }, { label: '201', value: '201' }, { label: '204', value: '204' },
      { label: '301', value: '301' }, { label: '302', value: '302' }, { label: '304', value: '304' },
      { label: '400', value: '400' }, { label: '401', value: '401' }, { label: '403', value: '403' },
      { label: '404', value: '404' }, { label: '405', value: '405' }, { label: '429', value: '429' },
      { label: '500', value: '500' }, { label: '502', value: '502' }, { label: '503', value: '503' }
    ]
  }
}

// 导出列 = 表格所有列（排除操作列），与页面显示保持一致
const exportCols = computed(() =>
  columns
    .filter(c => c.key && !['actions', 'op'].includes(c.key))
    .map(c => ({ key: c.key, label: c.title }))
)

const splFields = computed(() =>
  columns
    .filter(c => c.key && !['actions', 'op'].includes(c.key))
    .map(c => {
      const f = SPL_FIXED_OPTIONS[c.key]
      if (!f) return { key: c.key, label: c.title }
      // 简单选项数组，或 { options, multiple, filterable } 对象
      if (Array.isArray(f)) return { key: c.key, label: c.title, options: f }
      return { key: c.key, label: c.title, options: f.options, multiple: !!f.multiple, filterable: !!f.filterable }
    })
)

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
        conds: JSON.stringify(splConds.value)

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
