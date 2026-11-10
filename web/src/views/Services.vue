<template>
  <div>
    <n-card title="应用服务">
      <p class="tip">本页只展示<b>已确认</b>的 Web 资产（在「资产处置」确认过的）；待处置的请到「资产处置」处理。</p>
      <SearchBar :fields="splFieldsPlus" @search="onSplSearch" @reset="onSplReset" />
      <div class="filters" style="margin-top:12px">
        <n-tag round>{{ pagination.itemCount }} 条</n-tag>
        <ExportButton type="services" title="应用服务" :columns="exportCols" :filters="filters" :conds="splConds" />
        <n-button :loading="exporting" @click="exportUrls">导出 URL</n-button>
        <n-button :loading="aiBatch" @click="reidentifyBatch">AI 批量识别</n-button>
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
  { title: '台账登记', key: 'ledger', width: 200,
    render: r => h('div', { style: 'font-size:12px;line-height:1.5' }, [
      h('div', { style: 'color:#15803d' }, r.systemName || (r.confirmed ? '（台账未填系统名）' : '—')),
      h('div', { style: 'color:#6b7280;font-size:11px' },
        [r.owner || '', r.department || ''].filter(Boolean).join(' · ') || '—')
    ]) },
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

const splFieldsPlus = computed(() => [
  // 台账登记是聚合列，不能整体筛；拆成三个独立条件给后端
  ...splFields.value.filter(f => f.key !== 'ledger' && f.key !== 'systemName' && f.key !== 'owner' && f.key !== 'department'),
  { key: 'systemName', label: '系统名' },
  { key: 'owner', label: '负责人' },
  { key: 'department', label: '部门' },
])

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
    // 后端只返回 {total, items}，没有 page/size。之前取 res.page/res.size 恒为 undefined，
    // 分页器变成「非受控」：点查询后数据已回到第 1 页，页码却还停在第 2 页。
    // 这里以本地的 page/pageSize 为准（它们才是真正发给后端的值）。
    pagination.value = { page: page.value, pageSize: pageSize.value, itemCount: res.total }
  } finally {
    loading.value = false
  }
}

const aiBatch = ref(false)

// 历史「不清晰」标题批量重识别：AI 只在入库时触发，存量的靠这个补
async function reidentifyBatch() {
  aiBatch.value = true
  try {
    const r = await api.post('/services/reidentify?limit=100')
    message.success(`已入队 ${r.queued} 条（候选 ${r.scanned} 条），后台识别中，稍后刷新即可看到更准确的标题与新变更标记`)
  } catch (e) {
    message.error(e.response?.data?.error || '提交失败')
  } finally {
    aiBatch.value = false
  }
}

// 单条服务 → URL 文本（复制 URL 与导出 URL 共用同一口径，避免两处规则漂移）
// 协议：protocol 含 https 则 https，否则 http；端口：非 80/443 才带 :port；结尾不加多余斜杠
function buildUrl(row) {
  const proto = (row.protocol || '').toLowerCase().includes('https') ? 'https' : 'http'
  return proto + '://' + row.ip + (row.port && row.port !== 80 && row.port !== 443 ? ':' + row.port : '')
}

function copyUrl(row) {
  const url = buildUrl(row)
  // 内网多为 http 访问，navigator.clipboard 只在安全上下文（https/localhost）可用，
  // 之前直接调用会抛错导致按钮"失效"。这里先试 Clipboard API，失败回退 textarea + execCommand。
  const fallback = () => {
    try {
      const ta = document.createElement('textarea')
      ta.value = url
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      const ok = document.execCommand('copy')
      document.body.removeChild(ta)
      if (ok) message.success('已复制：' + url)
      return ok
    } catch (e) {
      return false
    }
  }
  // 直接走 execCommand（http 环境也能用）；Clipboard API 仅作为备选，不做安全上下文限制
  if (!fallback()) {
    if (navigator.clipboard) {
      navigator.clipboard.writeText(url).then(() => message.success('已复制：' + url), () => message.warning('复制失败，请手动复制：' + url))
    }
  }
}

function search() { page.value = 1; load() }

// ---------- 导出 URL（当前筛选条件下的全部 web 服务） ----------
const exporting = ref(false)

// 时间戳 YYYYMMDD-HHmm，用于文件名
function tsStamp(d = new Date()) {
  const p = n => String(n).padStart(2, '0')
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}`
}

// IPv4 按数值比较（否则 10.0.0.10 会排在 10.0.0.2 前面）；非 IPv4 退回原字符串
function ipSortKey(ip) {
  const s = String(ip || '')
  const parts = s.split('.')
  if (parts.length === 4 && parts.every(x => /^\d{1,3}$/.test(x))) {
    return parts.map(x => x.padStart(3, '0')).join('.')
  }
  return s
}

// Blob 下载纯文本。内容带 UTF-8 BOM：URL 本身是纯 ASCII，理论上不需要，
// 但用记事本/Excel 打开后再加工成含中文的清单时不至于乱码，成本仅 3 字节，故保留。
function downloadText(text, filename) {
  const blob = new Blob(['\ufeff' + text], { type: 'text/plain;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.style.display = 'none'
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  // 立即 revoke 在部分浏览器会打断下载，延迟释放
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

// 列表是后端分页（remote），只导出当前页会漏数据：
// 用与 load() 完全相同的查询参数另取一次全量（后端 size 上限 5000）。
async function exportUrls() {
  if (exporting.value) return
  exporting.value = true
  try {
    const res = await api.get('/services', {
      params: {
        page: 1, size: 5000, webOnly: 'true',
        ...filters.value,
        statusCodes: (filters.value.statusCodes || []).join(','),
        confirmedOnly: 'true', // 与列表一致：应用服务只含已确认资产
        conds: JSON.stringify(splConds.value)
      }
    })
    const rows = res.items || []
    const total = Number(res.total) || 0

    // 去重（同一 URL 只留一条）后按 IP、端口升序
    const seen = new Set()
    const list = []
    for (const r of rows) {
      const url = buildUrl(r)
      if (seen.has(url)) continue
      seen.add(url)
      list.push({ url, ip: ipSortKey(r.ip), port: Number(r.port) || 0 })
    }
    list.sort((a, b) => (a.ip === b.ip ? a.port - b.port : (a.ip < b.ip ? -1 : 1)))

    if (list.length === 0) {
      message.warning('当前筛选条件下没有可导出的 URL')
      return
    }
    // 一行一条，CRLF 兼容 Windows 记事本
    downloadText(list.map(x => x.url).join('\r\n'), `应用服务-URL-${tsStamp()}.txt`)
    if (total > rows.length) {
      message.warning(`已导出 ${list.length} 条 URL（共 ${total} 条，超出单次 5000 条上限，请缩小筛选范围）`)
    } else {
      message.success(`已导出 ${list.length} 条 URL`)
    }
  } catch (e) {
    message.error(e.response?.data?.error || '导出失败')
  } finally {
    exporting.value = false
  }
}

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
