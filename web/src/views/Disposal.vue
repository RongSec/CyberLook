<template>
  <div>
    <n-card title="资产处置（待确认 Web 资产）">
      <SearchBar :fields="splFieldsPlus" @search="onSplSearch" @reset="onSplReset" />

      <!-- 快捷分类筛选：存量里「主机新出现」往往上千条，容易把真正紧急的「端口新出现」淹没 -->
      <div class="cat-chips">
        <span class="cat-label">快捷筛选</span>
        <n-button size="small" :type="filters.category === null ? 'primary' : 'default'"
          :secondary="filters.category !== null" @click="toggleCategory(null)">
          全部 {{ items.length }}
        </n-button>
        <n-button v-for="c in categoryOptions" :key="c.value" size="small"
          :type="filters.category === c.value ? catType(c.value) : 'default'"
          :secondary="filters.category !== c.value" @click="toggleCategory(c.value)">
          {{ c.label }} {{ catCounts[c.value] }}
        </n-button>
      </div>

      <div class="toolbar" style="flex-wrap:wrap">
        <n-tooltip trigger="hover" placement="bottom">
          <template #trigger>
            <n-button type="success" :disabled="ledgerChecked.length === 0" @click="batchConfirmLedger">
              按台账确认{{ ledgerChecked.length ? `(${ledgerChecked.length})` : '' }}
            </n-button>
          </template>
          只确认**勾选中的那些端口行**（不再连坐同 IP 的其它端口）。
          「台账有」= 承认这个端口现状；「新变更」需要你逐行看过页面后再确认（全部/批量确认会显式提示）。
          「新出现」台账里没有信息，请用「批量处置」填写部门/负责人。
        </n-tooltip>
        <n-tooltip trigger="hover" placement="bottom">
          <template #trigger>
            <n-button type="primary" ghost @click="openBatchConfirm">批量处置</n-button>
          </template>
          选中端口后点击：填写部门/负责人，把这些端口登记到该归属；
          未选中时打开空白处置框，手动填写 IP 后同样可提交。
        </n-tooltip>
        <n-button @click="checkAllFiltered">{{ allFilteredSelected ? '取消全选当前筛选' : '全选当前筛选' }}</n-button>
        <n-button type="info" :disabled="checked.length === 0" @click="copyUrls">
          复制 URL{{ checked.length ? `(${checked.length})` : '' }}
        </n-button>
        <n-popconfirm positive-text="删除" negative-text="取消" @positive-click="batchDelete">
          <template #trigger>
            <n-button type="error" :disabled="checked.length === 0">
              删除选中{{ checked.length ? `(${checked.length})` : '' }}
            </n-button>
          </template>
          确认删除选中的 {{ checked.length }} 个端口记录？只删选中行，
          不再连带同 IP 的其它端口。
        </n-popconfirm>
        <ExportButton type="disposal" title="资产处置" :columns="exportCols" :conds="splConds" />
        <n-tag type="warning" round>{{ filteredItems.length }} 项待处理（共 {{ items.length }}）</n-tag>
        <span class="tip">红=主机新出现 · 橙=端口新出现 · 黄=新变更（确认过但页面已变）·
          勾选精确到端口；点 IP 旁「N 端口」一次选中整个 IP；台账登记列仅作参考</span>
      </div>
      <n-data-table :columns="columns" :data="filteredItems" :loading="loading" size="small"
        :row-key="r => r.ip + ':' + r.webPort" v-model:checked-row-keys="checked" @update:checked-row-keys="onCheckedChange"
        :pagination="pager" />
    </n-card>

    <n-modal v-model:show="showConfirm" preset="card" :title="batchMode ? '批量处置' : '确认资产归属'" style="width: 520px">
      <n-form label-placement="left" label-width="90">
        <n-form-item label="IP">
          <n-input v-model:value="form.ip" type="textarea" :rows="3" :disabled="!batchMode"
            placeholder="多个 IP 用换行或逗号分隔" />
        </n-form-item>
        <n-form-item v-if="form.statusCode" label="响应码">
          <n-input :value="form.statusCode" disabled />
        </n-form-item>
        <n-form-item v-if="form.title || form.prevTitle" label="网站标题">
          <div style="width:100%">
            <div v-if="form.prevTitle && form.prevTitle !== form.title" style="margin-bottom:6px">
              <n-tag size="small" type="warning">旧</n-tag>
              <span style="color:#999;text-decoration:line-through;margin-left:6px">{{ form.prevTitle }}</span>
            </div>
            <div>
              <n-tag size="small" :type="form.prevTitle && form.prevTitle !== form.title ? 'success' : 'default'">
                {{ form.prevTitle && form.prevTitle !== form.title ? '新' : '当前' }}
              </n-tag>
              <span style="margin-left:6px;font-weight:600">{{ form.title || '-' }}</span>
            </div>
          </div>
        </n-form-item>
        <n-form-item v-if="form.fingerprint || form.prevFingerprint" label="指纹">
          <div style="width:100%">
            <div v-if="form.prevFingerprint && form.prevFingerprint !== form.fingerprint" style="margin-bottom:6px">
              <n-tag size="small" type="warning">旧</n-tag>
              <span style="color:#999;text-decoration:line-through;margin-left:6px">{{ form.prevFingerprint }}</span>
            </div>
            <div>
              <n-tag size="small" :type="form.prevFingerprint && form.prevFingerprint !== form.fingerprint ? 'success' : 'default'">
                {{ form.prevFingerprint && form.prevFingerprint !== form.fingerprint ? '新' : '当前' }}
              </n-tag>
              <span style="margin-left:6px">{{ form.fingerprint || '-' }}</span>
            </div>
          </div>
        </n-form-item>
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
import { NTag, NButton, NPopconfirm, useMessage, useDialog } from 'naive-ui'
import api from '../api'
import SearchBar from '../components/SearchBar.vue'
import ExportButton from '../components/ExportButton.vue'

// 导出列

const message = useMessage()
const dialog = useDialog()
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
// 注意：「台账登记」是聚合列（key=ledger），后端并不返回 ledger 字段，
// 数据里是 ledgerSystem / ledgerOwner / ledgerDept。检索栏已把这一列拆成三个独立字段，
// 这里把检索字段名映射回真实字段，通用 matchCond 的 等于/包含/开头是 等操作符才能取到值。
const CELL_FIELD_MAP = {
  systemName: 'ledgerSystem',
  owner: 'ledgerOwner',
  department: 'ledgerDept'
}

function cellValue(it, field) {
  if (field === 'inLedger') return (it.ledgerSystem || it.ledgerOwner || it.ledgerDept) ? '是' : '否'
  const realField = CELL_FIELD_MAP[field] || field
  const v = it[realField]
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

const filters = ref({ ip: '', category: null, title: '', port: '', statusCodes: [] })
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
  { label: '主机新出现', value: 'host_new' },
  { label: '端口新出现', value: 'port_new' },
  { label: '新变更', value: 'changed' }
]

// 单行是否命中当前筛选（category 单独传，用于统计各类别数量）
function matchItem(i, f, skipCategory) {
  // SPL 条件（覆盖所有列）
  for (const c of splConds.value) {
    if (!matchCond(i, c)) return false
  }
  if (f.ip && !i.ip.includes(f.ip.trim())) return false
  if (!skipCategory && f.category && i.category !== f.category) return false
  if (f.title && !(i.title || '').toLowerCase().includes(f.title.trim().toLowerCase())) return false
  if (f.port) {
    const want = String(f.port).trim()
    const ports = (i.webPorts && i.webPorts.length ? i.webPorts : [i.webPort]).map(String)
    if (!ports.includes(want)) return false
  }
  if (f.statusCodes && f.statusCodes.length > 0) {
    if (!f.statusCodes.includes(String(i.statusCode || ''))) return false
  }
  return true
}

const filteredItems = computed(() => {
  const f = filters.value
  return items.value.filter(i => matchItem(i, f, false))
})

// 各类别在当前其它筛选条件下的条数（用于工具栏快捷筛选上的数字）
const catCounts = computed(() => {
  const m = { host_new: 0, port_new: 0, changed: 0 }
  const f = filters.value
  for (const i of items.value) {
    if (!matchItem(i, f, true)) continue
    if (m[i.category] !== undefined) m[i.category]++
  }
  return m
})

// 快捷筛选：点一下只看某一类，再点一次取消
function toggleCategory(v) {
  if (v === null) {
    filters.value.category = null
  } else {
    filters.value.category = filters.value.category === v ? null : v
  }
  page.value = 1
}

// 类别对应的按钮颜色（与表格里的类别标签保持一致：红/橙/蓝）
function catType(v) {
  return { host_new: 'error', port_new: 'warning', changed: 'info' }[v] || 'default'
}

const pager = computed(() => ({
  pageSize: pageSize.value,
  showSizePicker: true,
  pageSizes: [10, 20, 50, 100],
  onUpdatePageSize: s => { pageSize.value = s }
}))

function search() { page.value = 1 }
function resetFilters() {
  filters.value = { ip: '', category: null, title: '', port: '', statusCodes: [] }
  page.value = 1
}
const departments = ref([])
const loading = ref(false)
const showConfirm = ref(false)
const showNewDept = ref(false)
const showNewOwner = ref(false)
const newDeptName = ref('')
const newOwnerName = ref('')
const form = ref({ prevTitle: '', prevFingerprint: '',  prevTitle: '', prevFingerprint: '',  id: 0, ip: '', title: '', fingerprint: '', department: null, owner: null, systemName: '' })
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
      const m = {
        host_new: ['主机新出现', 'error'],
        port_new: ['端口新出现', 'warning'],
        changed: ['新变更', 'info']
      }[r.category] || ['-', 'default']
      return h(NTag, { size: 'small', type: m[1] }, { default: () => m[0] })
    }
  },
  { title: 'IP', key: 'ip', width: 178,
    render: r => {
      const n = items.value.filter(i => i.ip === r.ip).length
      if (n <= 1) return h('span', null, r.ip)
      // 同一 IP 有多个 web 端口时给个一键整选的入口（勾选框本身只选单行）
      return h('div', { style: 'display:flex;align-items:center;gap:6px' }, [
        h('span', null, r.ip),
        h(NButton, {
          size: 'tiny', secondary: true, type: 'primary',
          title: `选中/取消该 IP 的全部 ${n} 个 Web 端口`,
          onClick: () => toggleIp(r.ip)
        }, { default: () => `${n} 端口` })
      ])
    } },
  { title: 'Web端口', key: 'webPort', width: 140,
    render: r => (r.webPorts && r.webPorts.length ? r.webPorts.join(', ') : String(r.webPort || '')) },
  // 下面四列：上旧下新，一眼看出变化
  { title: '响应码', key: 'statusCode', width: 100, render: r => twoLine(r.prevStatusCode, r.statusCode) },
  { title: '响应长度', key: 'contentLength', width: 130, render: r => twoLine(fmtLen(r.prevContentLength), fmtLen(r.contentLength)) },
  { title: '网站标题', key: 'title', ellipsis: { tooltip: true }, render: r => twoLine(r.prevTitle, r.title) },
  { title: '指纹', key: 'fingerprint', ellipsis: { tooltip: true }, render: r => twoLine(r.prevFingerprint, r.fingerprint) },
  {
    title: '台账登记', key: 'ledger', width: 200,
    // 仅作参考：IP 登记过就展示登记内容，没登记就一个「—」，不再有"台账未填系统名"这种噪音
    render: r => {
      const sys = (r.ledgerSystem || '').trim()
      const rest = [r.ledgerOwner, r.ledgerDept].map(x => (x || '').trim()).filter(Boolean).join(' · ')
      if (!sys && !rest) return h('span', { style: 'color:#c0c4cc;font-size:12px' }, '—')
      return h('div', { style: 'font-size:12px;line-height:1.5' }, [
        sys ? h('div', { style: 'color:#15803d' }, sys) : null,
        rest ? h('div', { style: 'color:#6b7280;font-size:11px' }, rest) : null,
        r.ledgerCount > 1 ? h('div', { style: 'color:#d97706;font-size:11px' }, `多归属（${r.ledgerCount} 条记录）`) : null
      ])
    }
  },
  { title: '发现时间', key: 'createdAt', width: 160, render: r => fmt(r.createdAt) }
]

// 检索字段 = 表格所有列（排除操作列），新增列时自动多一项
// 固定选项字段（不给手输）
const SPL_FIXED_OPTIONS = {
  category: [{ label: '主机新出现', value: 'host_new' }, { label: '端口新出现', value: 'port_new' }, { label: '新变更', value: 'changed' }],
  activity: [{ label: '活跃', value: 'active' }, { label: '睡眠', value: 'sleeping' }],
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

// 检索栏字段：台账登记是聚合列（key=ledger，数据里没有该字段），不能整体筛；
// 拆成「系统名 / 负责人 / 部门」三个独立条件（AND 关系），cellValue 负责映射回 ledgerXxx。
const splFieldsPlus = computed(() => [
  ...splFields.value.filter(f => !['ledger', 'systemName', 'owner', 'department'].includes(f.key)),
  { key: 'systemName', label: '系统名' },
  { key: 'owner', label: '负责人' },
  { key: 'department', label: '部门' }
])

function fmt(t) { return t ? new Date(t).toLocaleString('zh-CN') : '-' }

async function delDisposal(row) {
  try {
    await api.delete(`/disposal/${encodeURIComponent(row.ip)}`)
    message.success('已删除')
    load()
  } catch (e) { message.error(e.response?.data?.error || '删除失败') }
}

// 按台账确认（用当前扫描到的标题作为基线）
// key = ip:port。**勾选精确到端口**：勾一行只选这一行，复制 URL 才能只拿这一条。
// 要整 IP 一起选，点 IP 列旁边的「N 端口」按钮（toggleIp），一次到位。
function keysToIps(keys) {
  return [...new Set((keys || []).map(k => String(k).split(':')[0]))]
}
function keysOfIp(ip) {
  return items.value.filter(i => i.ip === ip).map(i => i.ip + ':' + i.webPort)
}
// 勾选严格跟随被点的那一行，不再做同 IP 联动
function onCheckedChange(keys) {
  checked.value = keys || []
}
// 一键选中 / 取消整个 IP 的所有端口
function toggleIp(ip) {
  const keys = keysOfIp(ip)
  if (!keys.length) return
  const cur = new Set(checked.value)
  const allSelected = keys.every(k => cur.has(k))
  checked.value = allSelected
    ? [...cur].filter(k => !keys.includes(k))
    : [...new Set([...cur, ...keys])]
}

async function confirmLedger(keys) {
  try {
    const res = await api.post('/disposal/confirm-ledger', { keys })
    message.success(`已按台账确认 ${res.confirmed} 项`)
    checked.value = []
    load()
  } catch (e) {
    message.error(e.response?.data?.error || '确认失败')
  }
}

const checkedIps = computed(() => new Set(keysToIps(checked.value)))
// 上旧下新：旧值灰色小字，新值正常字重
function twoLine(prev, cur) {
  const oldV = (prev === undefined || prev === null || prev === '') ? '—' : String(prev)
  const newV = (cur === undefined || cur === null || cur === '') ? '—' : String(cur)
  return h('div', { style: 'line-height:1.35' }, [
    h('div', { style: 'color:#9ca3af;font-size:12px' }, oldV),
    h('div', { style: 'font-weight:600' }, newV)
  ])
}


// 选中的端口 key —— 精确到端口，不再按 IP 传
// 「按台账确认」只能确认台账里有信息的行。
//
// 主机新出现（host_new）= 台账里根本没有这个 IP，没有可用信息，
// 纳入确认只会得到「已确认 0 个」。之前这里直接取 checked，
// 于是全勾新出现时按钮仍显示 (1) 且可点，点完弹一句「都不在台账中」——纯误导。
//
// 按 IP 而非 ip:port 匹配：一个 IP 在列表里只占一行（webPort 是主端口），
// 但勾选精确到端口，用 ip:port 会漏掉同 IP 的副端口。
const ledgerChecked = computed(() => {
  const byIp = new Map(items.value.map(i => [i.ip, i]))
  return checked.value.filter(k => {
    const it = byIp.get(String(k).split(':')[0])
    return !!it && it.category !== 'host_new'
  })
})
// 选中里有多少是「新变更」：确认前需要二次确认，避免静默接受页面变化
const changedChecked = computed(() => {
  const byKey = new Map(items.value.map(i => [i.ip + ':' + i.webPort, i]))
  return checked.value.filter(k => byKey.get(k)?.category === 'changed')
})

const allFilteredSelected = computed(() => {
  const ips = filteredItems.value.map(i => i.ip)
  return ips.length > 0 && ips.every(ip => checkedIps.value.has(ip))
})

async function copyUrls() {
  const rows = filteredItems.value.filter(i => checked.value.includes(i.ip + ':' + i.webPort))
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
  const ips = [...new Set(filteredItems.value.map(i => i.ip))]
  const allKeys = filteredItems.value.map(i => i.ip + ':' + i.webPort)
  if (allFilteredSelected.value) {
    const drop = new Set(allKeys)
    checked.value = checked.value.filter(k => !drop.has(k))
  } else {
    checked.value = [...new Set([...checked.value, ...allKeys])]
  }
}

async function batchDelete() {
  if (!checked.value.length) return message.warning('请先勾选要删除的资产')
  try {
    // 精确到端口：只删选中的这些行，不再连带同 IP 的其它端口
    const res = await api.post('/disposal/batch-delete', { keys: checked.value })
    message.success(`已删除 ${res.deleted ?? checked.value.length} 个端口记录`)
    checked.value = []
    load()
  } catch (e) {
    message.error(e.response?.data?.error || '删除失败')
  }
}

async function batchConfirmLedger() {
  const keys = ledgerChecked.value
  if (!keys.length) {
    // 选中的全是「新出现」（台账里没这个 IP）：给明确出路，别让按钮变成"点了没反应"
    message.warning('所选资产都不在台账中（新出现），请用「批量处置」填写部门/负责人来登记归属')
    return
  }
  const changed = changedChecked.value.length
  if (!changed) return confirmLedger(keys)
  // 选中的含「新变更」：必须明确告知，避免像以前那样"确认台账有，变更也一起没了"
  dialog.warning({
    title: '选中包含页面已变更的端口',
    content: `其中 ${changed} 行是「新变更」（页面标题/内容与上次确认不一致）。\n继续确认 = 接受这些变化并写入新基线。建议先逐行核对页面。`,
    positiveText: `确认 ${keys.length} 项（含 ${changed} 项变更）`,
    negativeText: '返回核对',
    onPositiveClick: () => confirmLedger(keys),
  })
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
// 注意 checked 里存的是 "ip:port"（端口级选择用），后端 /disposal/confirm 的 ips 只认纯 IP，
// 直接 join 会提交成 "10.1.1.1:8080" → 一条都匹配不上（表现为「已确认 0 个，跳过 N」）。
// 所以这里去掉端口并按 IP 去重（同一 IP 多端口只提交一次）。
function openBatchConfirm() {
  batchMode.value = true
  const ips = [...new Set(checked.value.map(k => String(k).split(':')[0]).filter(Boolean))]
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
.cat-chips { display: flex; gap: 8px; align-items: center; margin: 12px 0 4px; flex-wrap: wrap; }
.cat-label { font-size: 12px; color: #94a3b8; margin-right: 2px; }
</style>
