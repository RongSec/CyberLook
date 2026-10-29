<template>
  <div>
    <n-card title="资产处置（待确认 Web 资产）">
      <SearchBar :fields="splFields" @search="onSplSearch" @reset="onSplReset" />

      <div class="toolbar" style="flex-wrap:wrap">
        <n-button type="success" :disabled="ledgerChecked.length === 0" @click="batchConfirmLedger">
          按台账确认{{ ledgerChecked.length ? `(${ledgerChecked.length})` : '' }}
        </n-button>
        <n-button @click="checkAllLedger">{{ allLedgerSelected ? '取消选中台账有的' : '全选台账有的' }}</n-button>
        <n-button @click="checkAllFiltered">{{ allFilteredSelected ? '取消全选当前筛选' : '全选当前筛选' }}</n-button>
        <n-tooltip trigger="hover" placement="bottom">
          <template #trigger>
            <n-button type="primary" ghost @click="openBatchConfirm">批量处置</n-button>
          </template>
          选中资产后点击，会自动把 IP 填入处置框（可编辑）；
          未选中时打开空白处置框，手动填写 IP 后同样可提交。
        </n-tooltip>
        <n-button type="info" :disabled="checked.length === 0" @click="copyUrls">
          复制 URL{{ checked.length ? `(${checked.length})` : '' }}
        </n-button>
        <n-button type="error" :disabled="checked.length === 0" @click="batchDelete">
          删除选中{{ checked.length ? `(${checked.length})` : '' }}
        </n-button>
        <ExportButton type="disposal" title="资产处置" :columns="exportCols" :conds="splConds" />
        <n-tag type="warning" round>{{ filteredItems.length }} 项待处理（共 {{ items.length }}）</n-tag>
        <span class="tip">红=台账没有 · 绿=台账有 · 黄=确认过但页面相似度变更</span>
      </div>
      <n-data-table :columns="columns" :data="filteredItems" :loading="loading" size="small"
        :row-key="r => r.ip" v-model:checked-row-keys="checked"
        :pagination="pager" />
    </n-card>

    <n-modal v-model:show="showConfirm" preset="card" :title="batchMode ? '批量处置' : '确认资产归属'" style="width: 520px">
      <n-form label-placement="left" label-width="90">
        <n-form-item label="IP">
          <n-input v-model:value="form.ip" type="textarea" :rows="3" :disabled="!batchMode"
            placeholder="多个 IP 用换行或逗号分隔" />
        </n-form-item>
        <n-form-item label="网站标题"><n-input :value="form.title || '-'" disabled /></n-form-item>
        <n-form-item label="指纹"><n-input :value="form.fingerprint || '-'" disabled /></n-form-item>
        <n-form-item label="部门">
          <div style="display:flex;gap:8px;width:100%">
            <n-select v-model:value="form.department" :options="deptOptions" placeholder="必选" @update:value="onDeptChange" style="flex:1" />
            <n-button size="small" @click="openNewDept">新建</n-button>
          </div>
        </n-form-item>
        <n-form-item label="负责人">
          <div style="display:flex;gap:8px;width:100%">
            <n-select v-model:value="form.owner" :options="ownerOptions" tag filterable placeholder="必选（可从部门预设中选）" style="flex:1" />
            <n-button size="small" @click="openNewOwner">新建</n-button>
          </div>
        </n-form-item>
        <n-form-item label="系统名"><n-input v-model:value="form.systemName" placeholder="可选，如 门户网站" /></n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showConfirm = false">取消</n-button>
          <n-button type="primary" @click="doConfirm">确认入库</n-button>
        </n-space>
      </template>
    </n-modal>

    <!-- 新建部门 -->
    <n-modal v-model:show="showNewDept" preset="card" title="新建部门" style="width: 380px">
      <n-input v-model:value="newDeptName" placeholder="部门名称" @keyup.enter="saveNewDept" />
      <template #footer>
        <n-space justify="end">
          <n-button @click="showNewDept = false">取消</n-button>
          <n-button type="primary" @click="saveNewDept">保存</n-button>
        </n-space>
      </template>
    </n-modal>

    <!-- 新建负责人 -->
    <n-modal v-model:show="showNewOwner" preset="card" title="新建负责人" style="width: 380px">
      <n-input v-model:value="newOwnerName" placeholder="负责人姓名" @keyup.enter="saveNewOwner" />
      <template #footer>
        <n-space justify="end">
          <n-button @click="showNewOwner = false">取消</n-button>
          <n-button type="primary" @click="saveNewOwner">保存</n-button>
        </n-space>
      </template>
    </n-modal>
  </div>
</template>

<script setup>
import { ref, h, computed, onMounted } from 'vue'
import { NTag, NButton, NPopconfirm, useMessage } from 'naive-ui'
import api from '../api'
import SearchBar from '../components/SearchBar.vue'
import ExportButton from '../components/ExportButton.vue'

// 导出列

const message = useMessage()
const items = ref([])
const checked = ref([])

// SPL 条件 → 原有 filters（处置页是前端过滤，直接映射）
// 条件直接存下来，由 filteredItems 里的通用匹配器处理（覆盖页面所有列）
const splConds = ref([])

function onSplSearch({ conditions }) {
  splConds.value = (conditions || []).filter(c => {
    const v = c.value
    if (Array.isArray(v)) return v.length > 0
    return v !== '' && v !== null && v !== undefined
  })
}

// 单行数据取列值（处理布尔/空/时间）
function cellValue(it, field) {
  if (field === 'inLedger') return it.inLedger ? '是' : '否'
  const v = it[field]
  if (v === null || v === undefined) return ''
  if (typeof v === 'boolean') return v ? '是' : '否'
  return String(v)
}

// 通用条件匹配
function matchCond(it, c) {
  const s = cellValue(it, c.field)
  // 多选：值可能是数组，命中任一即可
  if (Array.isArray(c.value)) {
    if (c.value.length === 0) return true
    return c.value.some(x => s === String(x) || s.toLowerCase() === String(x).toLowerCase())
  }
  const v = String(c.value).trim()
  const sl = s.toLowerCase()
  const vl = v.toLowerCase()
  switch (c.op) {
    case 'eq': return sl === vl
    case 'neq': return sl !== vl
    case 'gt': { const a = parseFloat(s), b = parseFloat(v); return !isNaN(a) && !isNaN(b) && a > b }
    case 'lt': { const a = parseFloat(s), b = parseFloat(v); return !isNaN(a) && !isNaN(b) && a < b }
    case 'prefix': return sl.startsWith(vl)
    default: return sl.includes(vl)
  }
}
function onSplReset() {
  splConds.value = []
}

const filters = ref({ ip: '', category: null, title: '', ledger: '', port: '', statusCodes: [] })
const statusCodeOptions = [
  { label: '200 正常', value: '200' }, { label: '301 永久跳转', value: '301' },
  { label: '302 临时跳转', value: '302' }, { label: '401 未授权', value: '401' },
  { label: '403 禁止访问', value: '403' }, { label: '404 不存在', value: '404' },
  { label: '500 服务器错误', value: '500' }, { label: '502 网关错误', value: '502' },
  { label: '503 服务不可用', value: '503' }
]
const page = ref(1)
const pageSize = ref(20)

const categoryOptions = [
  { label: '新出现', value: 'new' },
  { label: '台账有', value: 'in_ledger' },
  { label: '新变更', value: 'changed' }
]

const filteredItems = computed(() => {
  const f = filters.value
  return items.value.filter(i => {
    // SPL 条件（覆盖所有列）
    for (const c of splConds.value) {
      if (!matchCond(i, c)) return false
    }
    if (f.ip && !i.ip.includes(f.ip.trim())) return false
    if (f.category && i.category !== f.category) return false
    if (f.title && !(i.title || '').toLowerCase().includes(f.title.trim().toLowerCase())) return false
    if (f.ledger) {
      const hay = `${i.ledgerSystem || ''}${i.ledgerOwner || ''}${i.ledgerDept || ''}`
      if (!hay.includes(f.ledger.trim())) return false
    }
    if (f.port && String(i.webPort) !== String(f.port).trim()) return false
    if (f.statusCodes && f.statusCodes.length > 0) {
      if (!f.statusCodes.includes(String(i.statusCode || ''))) return false
    }
    return true
  })
})

const pager = computed(() => ({
  pageSize: pageSize.value,
  showSizePicker: true,
  pageSizes: [10, 20, 50, 100],
  onUpdatePageSize: s => { pageSize.value = s }
}))

function search() { page.value = 1 }
function resetFilters() {
  filters.value = { ip: '', category: null, title: '', ledger: '', port: '', statusCodes: [] }
  page.value = 1
}
const departments = ref([])
const loading = ref(false)
const showConfirm = ref(false)
const showNewDept = ref(false)
const showNewOwner = ref(false)
const newDeptName = ref('')
const newOwnerName = ref('')
const form = ref({ id: 0, ip: '', title: '', fingerprint: '', department: null, owner: null, systemName: '' })
const batchMode = ref(false)

const deptOptions = computed(() => departments.value.map(d => ({ label: d.name, value: d.name })))
const ownerOptions = computed(() => {
  const dept = departments.value.find(d => d.name === form.value.department)
  if (!dept || !dept.owners) return []
  return dept.owners.split(',').map(o => o.trim()).filter(Boolean).map(o => ({ label: o, value: o }))
})

function fmtLen(n) {
  if (!n) return '-'
  return n >= 1024 ? (n / 1024).toFixed(1) + ' KB' : n + ' B'
}

const columns = [
  { type: 'selection', width: 40, fixed: 'left' },
  {
    title: '类别', key: 'category', width: 90,
    render: r => {
      const m = { new: ['新出现', 'error'], in_ledger: ['台账有', 'success'], changed: ['新变更', 'warning'] }[r.category] || ['-', 'default']
      return h(NTag, { size: 'small', type: m[1] }, { default: () => m[0] })
    }
  },
  { title: 'IP', key: 'ip', width: 140 },
  { title: 'Web端口', key: 'webPort', width: 80 },
  { title: '响应码', key: 'statusCode', width: 80, render: r => r.statusCode || '-' },
  { title: '响应长度', key: 'contentLength', width: 100, render: r => fmtLen(r.contentLength) },
  { title: '网站标题', key: 'title', ellipsis: { tooltip: true }, render: r => r.title || '-' },
  {
    title: '台账登记', key: 'ledger', width: 190,
    render: r => r.inLedger
      ? h('div', { style: 'font-size:12px;line-height:1.5' }, [
          h('div', { style: 'color:#15803d' }, r.ledgerSystem || '（台账未填系统名）'),
          h('div', { style: 'color:#6b7280;font-size:11px' },
            [r.ledgerOwner, r.ledgerDept].filter(Boolean).join(' · ') || '（台账未填负责人）')
        ])
      : h('span', { style: 'color:#9ca3af;font-size:12px' }, '台账未登记')
  },
  { title: '指纹', key: 'fingerprint', ellipsis: { tooltip: true }, render: r => r.fingerprint || '-' },
  { title: '发现时间', key: 'createdAt', width: 160, render: r => fmt(r.createdAt) },
  {
    title: '操作', key: 'actions', width: 170,
    render: row => h('div', { style: 'display:flex;gap:6px;flex-wrap:wrap' }, [
      row.inLedger
        ? h(NButton, { size: 'small', type: 'success', text: true, onClick: () => confirmLedger([row.ip]) }, { default: () => '按台账确认' })
        : null,
      h(NButton, { size: 'small', type: 'primary', onClick: () => openConfirm(row) }, { default: () => '确认处置' }),
      h(NPopconfirm, { onPositiveClick: () => delDisposal(row) },
        { trigger: () => h(NButton, { size: 'small', type: 'error', text: true }, { default: () => '删除' }),
          default: () => `确认删除 ${row.ip} 及其服务记录？` })
    ])
  }
]

// 检索字段 = 表格所有列（排除操作列），新增列时自动多一项
// 固定选项字段（不给手输）
const SPL_FIXED_OPTIONS = {
  category: [{ label: '新出现', value: 'new' }, { label: '台账有', value: 'in_ledger' }, { label: '新变更', value: 'changed' }],
  inLedger: [{ label: '是', value: '是' }, { label: '否', value: '否' }],
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

async function delDisposal(row) {
  try {
    await api.delete(`/disposal/${encodeURIComponent(row.ip)}`)
    message.success('已删除')
    load()
  } catch (e) { message.error(e.response?.data?.error || '删除失败') }
}

// 按台账确认（用当前扫描到的标题作为基线）
async function confirmLedger(ips) {
  try {
    const res = await api.post('/disposal/confirm-ledger', { ips })
    message.success(`已按台账确认 ${res.confirmed} 项`)
    checked.value = []
    load()
  } catch (e) {
    message.error(e.response?.data?.error || '确认失败')
  }
}

const ledgerChecked = computed(() =>
  checked.value.filter(ip => items.value.find(i => i.ip === ip && i.inLedger)))

const allLedgerSelected = computed(() => {
  const ledgerIps = items.value.filter(i => i.inLedger).map(i => i.ip)
  return ledgerIps.length > 0 && ledgerIps.every(ip => checked.value.includes(ip))
})
const allFilteredSelected = computed(() => {
  const ips = filteredItems.value.map(i => i.ip)
  return ips.length > 0 && ips.every(ip => checked.value.includes(ip))
})

// 切换：已全选则取消，否则全选
function checkAllLedger() {
  const ledgerIps = items.value.filter(i => i.inLedger).map(i => i.ip)
  if (allLedgerSelected.value) {
    checked.value = checked.value.filter(ip => !ledgerIps.includes(ip))
  } else {
    const set = new Set(checked.value)
    ledgerIps.forEach(ip => set.add(ip))
    checked.value = [...set]
  }
}

async function copyUrls() {
  const rows = filteredItems.value.filter(i => checked.value.includes(i.ip))
  if (!rows.length) return message.warning('请先勾选资产')
  // 443/8443 走 https，其余按 http
  const lines = rows.map(r => {
    const scheme = (r.webPort === 443 || r.webPort === 8443) ? 'https' : 'http'
    return `${scheme}://${r.ip}:${r.webPort}/`
  })
  const text = lines.join('\n')
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    // 非 HTTPS 环境下 Clipboard API 不可用，用隐藏 textarea 兜底
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    document.body.removeChild(ta)
  }
  message.success(`已复制 ${lines.length} 条 URL`)
}

function checkAllFiltered() {
  const ips = filteredItems.value.map(i => i.ip)
  if (allFilteredSelected.value) {
    checked.value = checked.value.filter(ip => !ips.includes(ip))
  } else {
    const set = new Set(checked.value)
    ips.forEach(ip => set.add(ip))
    checked.value = [...set]
  }
}

async function batchDelete() {
  const ips = checked.value
  if (!ips.length) return message.warning('请先勾选要删除的资产')
  try {
    const res = await api.post('/disposal/batch-delete', { ips })
    message.success(`已删除 ${res.deleted || ips.length} 项`)
    checked.value = []
    load()
  } catch (e) {
    message.error(e.response?.data?.error || '删除失败')
  }
}

async function batchConfirmLedger() {
  const ips = ledgerChecked.value
  if (!ips.length) return message.warning('请先勾选台账中有的资产')
  await confirmLedger(ips)
}

async function load() {
  loading.value = true
  try {
    const res = await api.get('/disposal')
    items.value = res.items || []
  } finally { loading.value = false }
}

async function loadDepartments() { departments.value = await api.get('/departments') }

function openNewDept() {
  newDeptName.value = ''
  showNewDept.value = true
}

async function saveNewDept() {
  const name = (newDeptName.value || '').trim()
  if (!name) { message.warning('请输入部门名称'); return }
  try {
    await api.post('/departments', { name })
    message.success(`部门「${name}」已创建`)
    showNewDept.value = false
    form.value.department = name
    await loadDepartments()
  } catch (e) { message.error(e.response?.data?.error || '创建失败') }
}

function openNewOwner() {
  newOwnerName.value = ''
  showNewOwner.value = true
}

async function saveNewOwner() {
  const name = (newOwnerName.value || '').trim()
  if (!name) { message.warning('请输入负责人姓名'); return }
  if (!form.value.department) { message.warning('请先选择部门，负责人需要归属到部门'); return }
  form.value.owner = name
  // 把新负责人追加到当前部门，保证后续选这个部门时能带出来
  const dept = departments.value.find(d => d.name === form.value.department)
  if (dept) {
    const owners = (dept.owners || '').split(',').map(o => o.trim()).filter(Boolean)
    if (!owners.includes(name)) {
      owners.push(name)
      try {
        await api.put(`/departments/${dept.id}`, {
          name: dept.name, owners: owners.join(','), parentId: dept.parentId || null
        })
        await loadDepartments()
        message.success(`负责人「${name}」已创建并绑定到「${dept.name}」`)
      } catch (e) {
        message.warning(`负责人已填入，但绑定部门失败：${e.response?.data?.error || e.message}`)
      }
    } else {
      message.success(`负责人「${name}」已存在并填入`)
    }
  }
  showNewOwner.value = false
}

function openConfirm(row) {
  batchMode.value = false
  form.value = { id: row.id, ip: row.ip, title: row.title, fingerprint: row.fingerprint, department: null, owner: null, systemName: '' }
  showConfirm.value = true
}

// 批量处置：选中则填 IP，未选中则空白
function openBatchConfirm() {
  batchMode.value = true
  const ips = checked.value.length > 0 ? checked.value.slice() : []
  form.value = { id: 0, ip: ips.join('\n'), title: '', fingerprint: '', department: null, owner: null, systemName: '' }
  showConfirm.value = true
}

function onDeptChange() {
  // 自动带出该部门第一个预设负责人
  const opts = ownerOptions.value
  form.value.owner = opts.length > 0 ? opts[0].value : null
}

async function doConfirm() {
  if (!form.value.department) { message.warning('请选择部门'); return }
  if (!form.value.owner) { message.warning('请选择负责人'); return }
  try {
    if (batchMode.value) {
      const ips = form.value.ip.split(/[\s,，;；]+/).map(x => x.trim()).filter(Boolean)
      if (ips.length === 0) { message.warning('请填写至少一个 IP'); return }
      const r = await api.post('/disposal/confirm', {
        ips,
        systemName: form.value.systemName,
        owner: form.value.owner,
        department: form.value.department
      })
      message.success(`已确认 ${r.ok || ips.length} 个资产` + (r.skip ? `，跳过 ${r.skip}` : ''))
    } else {
      if (!form.value.ip) { message.warning('缺少 IP'); return }
      await api.post('/disposal/confirm', {
        ip: form.value.ip,
        systemName: form.value.systemName,
        owner: form.value.owner,
        department: form.value.department
      })
      message.success('已确认入库')
    }
    showConfirm.value = false
    checked.value = []
    load()
  } catch (e) { message.error(e.response?.data?.error || '确认失败') }
}

onMounted(() => { load(); loadDepartments() })
</script>

<style scoped>
.toolbar { display: flex; gap: 12px; align-items: center; margin-bottom: 16px; flex-wrap: wrap; }
.tip { color: #94a3b8; font-size: 12px; }
</style>
