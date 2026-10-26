<template>
  <div>
    <n-card title="资产处置（待确认 Web 资产）">
      <div class="filters" style="display:flex;gap:10px;flex-wrap:wrap;margin-bottom:12px">
        <n-input v-model:value="filters.ip" placeholder="IP" clearable style="width:150px" @keyup.enter="search" />
        <n-select v-model:value="filters.category" :options="categoryOptions" placeholder="类别" clearable style="width:120px" />
        <n-input v-model:value="filters.title" placeholder="网站标题" clearable style="width:170px" @keyup.enter="search" />
        <n-input v-model:value="filters.ledger" placeholder="台账系统名/负责人" clearable style="width:190px" @keyup.enter="search" />
        <n-input v-model:value="filters.port" placeholder="端口" clearable style="width:100px" @keyup.enter="search" />
        <n-button type="primary" @click="search">查询</n-button>
        <n-button @click="resetFilters">重置</n-button>
      </div>

      <div class="toolbar" style="flex-wrap:wrap">
        <n-button type="success" :disabled="ledgerChecked.length === 0" @click="batchConfirmLedger">
          按台账确认{{ ledgerChecked.length ? `(${ledgerChecked.length})` : '' }}
        </n-button>
        <n-button @click="checkAllLedger">全选台账有的</n-button>
        <n-button @click="checkAllFiltered">全选当前筛选</n-button>
        <n-button type="info" :disabled="checked.length === 0" @click="copyUrls">
          复制 URL{{ checked.length ? `(${checked.length})` : '' }}
        </n-button>
        <n-button type="error" :disabled="checked.length === 0" @click="batchDelete">
          删除选中{{ checked.length ? `(${checked.length})` : '' }}
        </n-button>
        <ExportButton type="disposal" title="资产处置" :columns="exportCols" />
        <n-tag type="warning" round>{{ items.length }} 项待处理</n-tag>
        <span class="tip">红=台账没有 · 绿=台账有 · 黄=确认过但标题变了</span>
      </div>
      <n-data-table :columns="columns" :data="filteredItems" :loading="loading" size="small"
        :row-key="r => r.ip" v-model:checked-row-keys="checked"
        :pagination="pager" />
    </n-card>

    <n-modal v-model:show="showConfirm" preset="card" title="确认资产归属" style="width: 520px">
      <n-form label-placement="left" label-width="90">
        <n-form-item label="IP"><n-input :value="form.ip" disabled /></n-form-item>
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
import ExportButton from '../components/ExportButton.vue'

// 导出列
const exportCols = [
  { key: 'ip', label: 'IP' },
  { key: 'webPort', label: 'Web端口' },
  { key: 'statusCode', label: '响应码' },
  { key: 'title', label: '网站标题' },
  { key: 'fingerprint', label: '指纹' },
  { key: 'createdAt', label: '发现时间' }
]

const message = useMessage()
const items = ref([])
const checked = ref([])
const filters = ref({ ip: '', category: null, title: '', ledger: '', port: '' })
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
    if (f.ip && !i.ip.includes(f.ip.trim())) return false
    if (f.category && i.category !== f.category) return false
    if (f.title && !(i.title || '').toLowerCase().includes(f.title.trim().toLowerCase())) return false
    if (f.ledger) {
      const hay = `${i.ledgerSystem || ''}${i.ledgerOwner || ''}${i.ledgerDept || ''}`
      if (!hay.includes(f.ledger.trim())) return false
    }
    if (f.port && String(i.webPort) !== String(f.port).trim()) return false
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
  filters.value = { ip: '', category: null, title: '', ledger: '', port: '' }
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

function checkAllLedger() {
  checked.value = items.value.filter(i => i.inLedger).map(i => i.ip)
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
  checked.value = filteredItems.value.map(i => i.ip)
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

function saveNewOwner() {
  const name = (newOwnerName.value || '').trim()
  if (!name) { message.warning('请输入负责人姓名'); return }
  form.value.owner = name
  showNewOwner.value = false
  message.success(`负责人「${name}」已填入`)
}

function openConfirm(row) {
  form.value = { id: row.id, ip: row.ip, title: row.title, fingerprint: row.fingerprint, department: null, owner: null, systemName: '' }
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
    await api.post('/disposal/confirm', {
      ip: form.value.ip,
      systemName: form.value.systemName,
      owner: form.value.owner,
      department: form.value.department
    })
    message.success('已确认入库')
    showConfirm.value = false
    load()
  } catch (e) { message.error(e.response?.data?.error || '确认失败') }
}

onMounted(() => { load(); loadDepartments() })
</script>

<style scoped>
.toolbar { display: flex; gap: 12px; align-items: center; margin-bottom: 16px; flex-wrap: wrap; }
.tip { color: #94a3b8; font-size: 12px; }
</style>
