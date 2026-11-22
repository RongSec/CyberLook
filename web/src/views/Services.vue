<template>
  <div>
    <n-card title="应用服务">
      <p class="tip">本页只展示<b>已确认</b>的 Web 资产（在「资产处置」确认过的）；待处置的请到「资产处置」处理。</p>
      <SearchBar :fields="splFieldsPlus" @search="onSplSearch" @reset="onSplReset" />
      <div class="filters" style="margin-top:12px">
        <n-tag round>{{ pagination.itemCount }} 条</n-tag>
        <n-button secondary :loading="selectingAll" @click="toggleSelectAll">
          {{ allSelected ? '取消全选' : '全选当前筛选' }}{{ checked.length ? ` (${checked.length})` : '' }}
        </n-button>
        <ExportButton type="services" title="应用服务" :columns="exportCols" :filters="filters" :conds="splConds" />
        <n-button :loading="exporting" @click="exportUrls">导出 URL</n-button>
        <n-button type="info" :disabled="checked.length === 0" @click="copyCheckedUrls">
          复制 URL{{ checked.length ? `(${checked.length})` : '' }}
        </n-button>
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

const selectingAll = ref(false)
const items = ref([])
const page = ref(1)
const pageSize = ref(20)
const pagination = ref({ page: 1, pageSize: 20, itemCount: 0 })
const splConds = ref([])

function onSplSearch({ conditions }) {
  splConds.value = conditions || []
  page.value = 1
  checked.value = [] // 筛选条件变了，之前的选中可能已不在结果里，清掉避免误删
  load()
}
function onSplReset() {
  splConds.value = []
  filters.value = { keyword: '', statusCodes: [], ip: '', port: '', service: '' }
  page.value = 1
  checked.value = []
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
  // 端口 / 协议 / 响应码 / 响应长度 合并为一列：上行端口+协议，下行状态码+长度
  {
    title: '端口 / 协议', key: 'endpoint', width: 150,
    render: row => {
      const proto = (row.protocol || '').toLowerCase()
      const code = row.statusCode || ''
      const n = parseInt(code)
      const codeColor = !code ? '#9ca3af' : n >= 200 && n < 300 ? '#15803d'
        : n >= 300 && n < 400 ? '#2563eb' : n >= 400 ? '#dc2626' : '#6b7280'
      const line2 = [
        code ? h('span', { style: `color:${codeColor}` }, code) : null,
        h('span', { style: 'color:#6b7280' }, fmtLen(row.contentLength))
      ].filter(Boolean)
      return h('div', { style: 'font-size:12px;line-height:1.5' }, [
        h('div', { style: 'color:#111827;font-weight:600' },
          `${row.port || '-'}${proto ? ' · ' + proto : ''}`),
        h('div', { style: 'font-size:11px;display:flex;gap:6px' }, line2)
      ])
    }
  },
  { title: '网站标题', key: 'title', ellipsis: { tooltip: true }, render: r => r.title || '-' },
  // 指纹与 Banner 同源（Banner 是原始响应头，指纹是从中提取的产品名），合并为一列：
  // 有指纹用指纹，提取失败时回退显示原始 Banner，信息不丢
  {
    title: '指纹', key: 'fingerprint', ellipsis: { tooltip: true },
    render: r => r.fingerprint || r.banner || '-'
  },
  { title: '台账登记', key: 'ledger', width: 200,
    render: r => h('div', { style: 'font-size:12px;line-height:1.5' }, [
      h('div', { style: r.systemName ? 'color:#15803d' : 'color:#c0c4cc' }, r.systemName || '—'),
      h('div', { style: 'color:#6b7280;font-size:11px' },
        [r.owner || '', r.department || ''].filter(Boolean).join(' · ') || '—')
    ]) },
  {
    title: '活跃状态', key: 'activity', width: 100,
    render: r => h(NTag, { size: 'small', type: r.activity === 'sleeping' ? 'default' : 'success', bordered: false },
      { default: () => r.activity === 'sleeping' ? '睡眠' : '活跃' })
  },
  { title: '最近发现', key: 'lastSeen', width: 160, render: r => fmt(r.lastSeen) }
]

// 检索字段 = 表格所有列（排除操作列），新增列时自动多一项
// 固定选项字段（不给手输）
const SPL_FIXED_OPTIONS = {
  activity: [{ label: '活跃', value: 'active' }, { label: '睡眠', value: 'sleeping' }],
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

// 导出列 = 表格所有列（排除勾选列）；合并列导出时拆回原始字段
const exportCols = computed(() => {
  const out = []
  for (const c of columns) {
    if (!c.key || ['actions', 'op'].includes(c.key)) continue
    if (c.key === 'endpoint') {
      out.push({ key: 'port', label: '端口' })
      out.push({ key: 'protocol', label: '协议' })
      out.push({ key: 'statusCode', label: '响应码' })
      out.push({ key: 'contentLength', label: '响应长度' })
      continue
    }
    out.push({ key: c.key, label: c.title })
  }
  return out
})

// 检索字段：合并列拆回原始字段，台账登记列拆成三个独立条件
const splFieldsPlus = computed(() => [
  { key: 'port', label: '端口' },
  { key: 'protocol', label: '协议', options: SPL_FIXED_OPTIONS.protocol },
  { key: 'statusCode', label: '响应码', options: SPL_FIXED_OPTIONS.statusCode.options, multiple: true, filterable: true },
  { key: 'contentLength', label: '响应长度' },
  ...splFields.value.filter(f => !['ledger', 'systemName', 'owner', 'department', 'endpoint'].includes(f.key)),
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

async function load() {
  loading.value = true
  try {
    const res = await api.get('/services', { params: queryParams(page.value, pageSize.value) })
    items.value = res.items
    // 后端只返回 {total, items}，没有 page/size。之前取 res.page/res.size 恒为 undefined，
    // 分页器变成「非受控」：点查询后数据已回到第 1 页，页码却还停在第 2 页。
    // 这里以本地的 page/pageSize 为准（它们才是真正发给后端的值）。
    pagination.value = { page: page.value, pageSize: pageSize.value, itemCount: res.total }
  } finally {
    loading.value = false
  }
}

// 列表与「全选」共用同一套查询条件，避免全选选出与当前筛选不一致的数据
function queryParams(p, s) {
  return {
    page: p, size: s, webOnly: 'true',
    ...filters.value,
    statusCodes: (filters.value.statusCodes || []).join(','),
    confirmedOnly: 'true', // 应用服务只展示已确认资产
    conds: JSON.stringify(splConds.value)
  }
}

// 是否已把「当前筛选」全部选中（跨页计数，不是当前页）
const allSelected = computed(() =>
  pagination.value.itemCount > 0 && checked.value.length >= pagination.value.itemCount)

// 全选当前筛选：本页是服务端分页，勾选只覆盖当前页 20 条，
// 所以要按同样条件把结果集的 id 全部取回来再设为已选。
async function toggleSelectAll() {
  if (allSelected.value) {
    checked.value = []
    return
  }
  if (pagination.value.itemCount === 0) {
    message.warning('当前筛选没有数据')
    return
  }
  selectingAll.value = true
  try {
    const ids = []
    const size = 1000
    let p = 1
    while (p <= 100) { // 兜底，最多 10 万条
      const res = await api.get('/services', { params: queryParams(p, size) })
      const list = res.items || []
      ids.push(...list.map(x => x.id))
      if (list.length < size || ids.length >= (res.total || 0)) break
      p++
    }
    checked.value = ids
    if (ids.length > 0) message.success(`已全选当前筛选的 ${ids.length} 条`)
  } catch (e) {
    message.error('全选失败：' + (e.response?.data?.error || e.message))
  } finally {
    selectingAll.value = false
  }
}

// 单条服务 → URL 文本（复制 URL 与导出 URL 共用同一口径，避免两处规则漂移）
// 协议：protocol 含 https 则 https，否则 http；端口：非 80/443 才带 :port；结尾不加多余斜杠
function buildUrl(row) {
  const proto = (row.protocol || '').toLowerCase().includes('https') ? 'https' : 'http'
  return proto + '://' + row.ip + (row.port && row.port !== 80 && row.port !== 443 ? ':' + row.port : '')
}

function copyText(text) {
  // 内网多为 http 访问，navigator.clipboard 只在安全上下文（https/localhost）可用，
  // 之前直接调用会抛错导致按钮"失效"。这里先试 textarea + execCommand，失败再回退 Clipboard API。
  const fallback = () => {
    try {
      const ta = document.createElement('textarea')
      ta.value = text
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      const ok = document.execCommand('copy')
      document.body.removeChild(ta)
      if (ok) message.success('已复制到剪贴板')
      return ok
    } catch (e) {
      return false
    }
  }
  // 直接走 execCommand（http 环境也能用）；Clipboard API 仅作为备选，不做安全上下文限制
  if (!fallback()) {
    if (navigator.clipboard) {
      navigator.clipboard.writeText(text).then(() => message.success('已复制到剪贴板'), () => message.warning('复制失败，请手动复制：' + text))
    }
  }
}

// 复制选中行的 URL（操作列已移除，统一走工具栏）
function copyCheckedUrls() {
  const rows = items.value.filter(r => checked.value.includes(r.id))
  if (rows.length === 0) {
    message.warning('当前页没有选中行；跨页全选请用「导出 URL」')
    return
  }
  copyText(rows.map(buildUrl).join('\n'))
  if (rows.length < checked.value.length) {
    message.warning(`已复制当前页 ${rows.length} 条（共选 ${checked.value.length} 条），跨页请用「导出 URL」`)
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
  page.value = 1
  checked.value = []
  load()
}

onMounted(load)
</script>

<style scoped>
.filters { display: flex; gap: 10px; align-items: center; margin-bottom: 16px; flex-wrap: wrap; }
.tip { color: var(--text-muted); font-size: 12px; margin: 0 0 12px; }
</style>
