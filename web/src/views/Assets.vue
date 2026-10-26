<template>
  <div>
    <n-card title="主机资产">
      <div class="filters">
        <n-input v-model:value="filters.ip" placeholder="IP" clearable style="width: 140px" @keyup.enter="search" />
        <n-input v-model:value="filters.systemName" placeholder="关联业务" clearable style="width: 140px" @keyup.enter="search" />
        <n-input v-model:value="filters.owner" placeholder="负责人" clearable style="width: 110px" @keyup.enter="search" />
        <n-select v-model:value="filters.department" :options="deptOptions" placeholder="部门" clearable style="width: 140px" />
        <n-select v-model:value="filters.source" :options="sourceOptions" placeholder="来源" clearable style="width: 110px" />
        <n-select v-model:value="filters.status" :options="statusOptions" placeholder="状态" clearable style="width: 110px" />
        <n-select v-model:value="filters.hasWeb" :options="hasWebOptions" placeholder="是否有WEB" clearable style="width: 130px" />
        <n-button type="primary" @click="search">查询</n-button>
        <n-button @click="reset">重置</n-button>
        <n-button secondary @click="selectAllFiltered">全选当前筛选</n-button>
        <n-button type="error" :disabled="checkedRows.length === 0" @click="batchDelete">
          批量删除{{ checkedRows.length ? `(${checkedRows.length})` : '' }}
        </n-button>
        <ExportButton type="assets" title="主机资产" :columns="exportCols" :filters="exportFilters" />
        <n-space style="margin-left: auto">
          <div v-if="importing" style="display:flex;align-items:center;gap:8px;min-width:200px">
            <n-progress type="line" :percentage="Math.round(importProgress)" :height="10"
              :show-indicator="false" status="success" style="flex:1" />
            <span style="font-size:12px;color:#16a34a">{{ Math.round(importProgress) }}%</span>
          </div>
          <n-button @click="downloadTemplate">下载模板</n-button>
          <n-upload :show-file-list="false" :custom-request="handleImport">
            <n-button type="primary">导入台账</n-button>
          </n-upload>
        </n-space>
      </div>

      <n-data-table :columns="columns" :data="items" :loading="loading" :pagination="{ ...pagination, showSizePicker: true, pageSizes: [10, 20, 50, 100], onUpdatePageSize: onPageSize }"
        :row-key="rowKey" checkable @update:checked-row-keys="onChecked" remote @update:page="onPage" />
    </n-card>

    <!-- 标注归属 -->
    <n-modal v-model:show="showEdit" preset="card" title="标注归属" style="width: 500px">
      <n-form label-placement="left" label-width="90">
        <n-form-item label="IP"><n-input :value="editForm.ip" disabled /></n-form-item>
        <n-form-item label="关联业务"><n-input v-model:value="editForm.systemName" placeholder="业务/系统名称" /></n-form-item>
        <n-form-item label="部门">
          <n-select v-model:value="editForm.department" :options="deptOptions" tag filterable placeholder="选择部门" @update:value="onEditDeptChange" />
        </n-form-item>
        <n-form-item label="负责人">
          <n-select v-model:value="editForm.owner" :options="editOwnerOptions" tag filterable placeholder="选择或输入负责人" />
        </n-form-item>
      </n-form>
      <p class="edit-tip">该 IP 若有 web 服务，标注后会自动确认为已登记。</p>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showEdit = false">取消</n-button>
          <n-button type="primary" @click="saveOwner">保存</n-button>
        </n-space>
      </template>
    </n-modal>

    <!-- 右侧端口抽屉 -->
    <n-drawer v-model:show="showPorts" :width="520" placement="right">
      <n-drawer-content :title="`${portIP} 端口详情`" closable>
        <n-empty v-if="ports.length === 0" description="暂无端口记录" />
        <n-data-table v-else :columns="portColumns" :data="ports" size="small" :max-height="600" />
      </n-drawer-content>
    </n-drawer>

    <n-modal v-model:show="showHistory" preset="card" title="历史态势" style="width: 700px">
      <p class="history-ip">{{ historyIP }}</p>
      <n-data-table :columns="historyColumns" :data="history" size="small" />
    </n-modal>
  </div>
</template>

<script setup>
import { ref, h, computed, onMounted } from 'vue'
import { NTag, NButton, NPopconfirm, useMessage } from 'naive-ui'
import api from '../api'
import ExportButton from '../components/ExportButton.vue'

const message = useMessage()
const loading = ref(false)
const importing = ref(false)
const importProgress = ref(0)
const items = ref([])
const departments = ref([])
const page = ref(1)
const pageSize = ref(20)
const pagination = ref({ page: 1, pageSize: 20, itemCount: 0 })
const filters = ref({ ip: '', systemName: '', owner: '', department: null, source: null, status: 'confirmed', hasWeb: null })
const checkedRows = ref([])

const showPorts = ref(false)
const portIP = ref('')
const ports = ref([])

const showEdit = ref(false)
const editForm = ref({ id: 0, ip: '', systemName: '', owner: null, department: null })

const showHistory = ref(false)
const history = ref([])
const historyIP = ref('')

const deptOptions = computed(() => departments.value.map(d => ({ label: d.name, value: d.name })))
const editOwnerOptions = computed(() => {
  const dept = departments.value.find(d => d.name === editForm.value.department)
  if (!dept || !dept.owners) return []
  return dept.owners.split(',').map(o => o.trim()).filter(Boolean).map(o => ({ label: o, value: o }))
})

const statusOptions = [
  { label: '已登记', value: 'confirmed' },
  { label: '待确认', value: 'pending' }
]
const sourceOptions = [
  { label: '台账导入', value: 'ledger' },
  { label: '扫描发现', value: 'scan' }
]
const hasWebOptions = [
  { label: '有', value: 'true' },
  { label: '无', value: 'false' }
]

// 导出列（与表格列一一对应）
const exportCols = [
  { key: 'ip', label: 'IP' },
  { key: 'hasWeb', label: '是否有WEB' },
  { key: 'systemName', label: '关联业务' },
  { key: 'owner', label: '负责人' },
  { key: 'department', label: '隶属部门' },
  { key: 'source', label: '来源' },
  { key: 'status', label: '状态' },
  { key: 'createdAt', label: '创建时间' }
]
const exportFilters = computed(() => ({
  ip: filters.value.ip, systemName: filters.value.systemName, owner: filters.value.owner,
  department: filters.value.department, source: filters.value.source, status: filters.value.status,
  hasWeb: filters.value.hasWeb
}))

const columns = [
  { type: 'selection' },
  {
    title: 'IP', key: 'ip', width: 150,
    render: row => h(NButton, { text: true, type: 'primary', onClick: () => openPorts(row) }, { default: () => row.ip })
  },
  {
    title: '是否有WEB', key: 'hasWeb', width: 110,
    render: row => h(NTag, { size: 'small', type: row.hasWeb ? 'success' : 'default' }, { default: () => row.hasWeb ? '有' : '无' })
  },
  { title: '关联业务', key: 'systemName' },
  { title: '负责人', key: 'owner', width: 100 },
  { title: '隶属部门', key: 'department', width: 130 },
  {
    title: '来源', key: 'source', width: 90,
    render: row => h(NTag, { type: row.source === 'ledger' ? 'info' : 'warning', size: 'small' },
      { default: () => row.source === 'ledger' ? '台账' : '扫描' })
  },
  {
    title: '状态', key: 'status', width: 100,
    render: row => h(NTag, { type: row.status === 'confirmed' ? 'success' : 'warning', size: 'small' },
      { default: () => row.status === 'confirmed' ? '已登记' : '待确认' })
  },
  {
    title: '操作', key: 'actions', width: 130,
    render: row => h('div', { style: 'display:flex;gap:8px' }, [
      h(NButton, { size: 'small', text: true, type: 'primary', onClick: () => openEdit(row) }, { default: () => '标注' }),
      h(NButton, { size: 'small', text: true, onClick: () => showSnapshots(row) }, { default: () => '态势' }),
      h(NPopconfirm, {
        onPositiveClick: () => delAsset(row),
        positiveText: '删除',
        negativeText: '取消'
      }, {
        trigger: () => h(NButton, { size: 'small', text: true, type: 'error' }, { default: () => '删除' }),
        default: () => `确认删除资产 ${row.ip} ？其端口与违规记录会一并删除。`
      })
    ])
  }
]

const portColumns = [
  { title: '端口', key: 'port', width: 80 },
  { title: '协议', key: 'protocol', width: 80 },
  { title: '服务', key: 'service', width: 100 },
  { title: '响应码', key: 'statusCode', width: 80, render: r => r.statusCode || '-' },
  { title: '网站标题', key: 'title', ellipsis: { tooltip: true }, render: r => r.title || '-' },
  { title: '最近发现', key: 'lastSeen', width: 150, render: r => fmt(r.lastSeen) }
]

const historyColumns = [
  { title: '扫描时间', key: 'scannedAt', render: r => fmt(r.scannedAt) },
  { title: '存活', key: 'isAlive', render: r => r.isAlive ? '是' : '否' },
  { title: '开放端口', key: 'openPorts', render: r => parsePorts(r.openPorts) }
]

function rowKey(row) { return row.id }
function fmt(t) { return t ? new Date(t).toLocaleString('zh-CN') : '-' }

async function loadDepartments() { departments.value = await api.get('/departments') }

async function load() {
  loading.value = true
  try {
    const res = await api.get('/assets', {
      params: {
        page: page.value, size: pageSize.value,
        ip: filters.value.ip, systemName: filters.value.systemName, owner: filters.value.owner,
        department: filters.value.department, source: filters.value.source, status: filters.value.status,
        hasWeb: filters.value.hasWeb
      }
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
  filters.value = { ip: '', systemName: '', owner: '', department: null, source: null, status: 'confirmed', hasWeb: null }
  load()
}
function onChecked(keys) { checkedRows.value = keys }

async function selectAllFiltered() {
  const res = await api.get('/assets', {
    params: { page: 1, size: 5000, ...exportFilters.value }
  })
  checkedRows.value = res.items.map(a => a.id)
  message.success(`已全选当前筛选的 ${checkedRows.value.length} 条`)
}

async function delAsset(row) {
  try {
    await api.post('/assets/batch-delete', { ids: [row.id] })
    message.success(`已删除 ${row.ip}`)
    load()
  } catch (e) { message.error(e.response?.data?.error || '删除失败') }
}

async function syncOrg() {
  try {
    const r = await api.post('/departments/sync')
    message.success(`已同步组织架构：共 ${r.departments} 个部门，新建 ${r.created} 个`)
  } catch (e) { message.error('同步失败：' + (e?.response?.data?.error || e?.message || e)) }
}

async function batchDelete() {
  if (checkedRows.value.length === 0) { message.warning('请先选择资产'); return }
  try {
    await api.post('/assets/batch-delete', { ids: checkedRows.value })
    message.success('已删除')
    checkedRows.value = []
    load()
  } catch (e) { message.error(e.response?.data?.error || '删除失败') }
}

function openEdit(row) {
  editForm.value = { id: row.id, ip: row.ip, systemName: row.systemName || '', owner: row.owner || null, department: row.department || null }
  showEdit.value = true
}
function onEditDeptChange() {
  const opts = editOwnerOptions.value
  if (opts.length > 0 && !editForm.value.owner) editForm.value.owner = opts[0].value
}
async function saveOwner() {
  try {
    const res = await api.put(`/assets/${editForm.value.id}/owner`, {
      systemName: editForm.value.systemName,
      owner: editForm.value.owner || '',
      department: editForm.value.department || ''
    })
    message.success(res.confirmed ? '已更新（该 IP 的 web 服务已同步确认）' : '已更新')
    showEdit.value = false
    load()
  } catch (e) { message.error(e.response?.data?.error || '保存失败') }
}

async function openPorts(row) {
  portIP.value = row.ip
  ports.value = await api.get(`/assets/${row.ip}/ports`)
  showPorts.value = true
}

async function downloadTemplate() {
  const res = await api.get('/assets/template', { responseType: 'blob' })
  const url = URL.createObjectURL(res)
  const a = document.createElement('a')
  a.href = url
  a.download = '资产台账模板.xlsx'
  a.click()
  URL.revokeObjectURL(url)
}

async function handleImport({ file, onFinish, onError }) {
  const fd = new FormData()
  fd.append('file', file.file)
  importing.value = true
  importProgress.value = 0
  // 后端一次性返回，这里按时间推进模拟进度，避免界面无反馈
  const timer = setInterval(() => {
    if (importProgress.value < 88) {
      importProgress.value = Math.min(88, importProgress.value + Math.random() * 7)
    }
  }, 220)
  try {
    const res = await api.post('/assets/import', fd)
    clearInterval(timer)
    importProgress.value = 100
    message.success(`导入完成：新增 ${res.imported}，更新 ${res.updated}，失败 ${res.failed}`)
    setTimeout(() => { importing.value = false; importProgress.value = 0 }, 400)
    page.value = 1
    load()
    onFinish()
  } catch (e) {
    clearInterval(timer)
    importing.value = false
    importProgress.value = 0
    message.error(e.response?.data?.error || '导入失败')
    onError()
  }
}

async function showSnapshots(row) {
  historyIP.value = row.ip
  history.value = await api.get(`/assets/${row.ip}/snapshots`)
  showHistory.value = true
}

function parsePorts(openPorts) {
  if (!openPorts) return '-'
  try {
    const arr = typeof openPorts === 'string' ? JSON.parse(openPorts) : openPorts
    return arr.map(p => p.port).join(', ')
  } catch { return '-' }
}

onMounted(() => {
  loadDepartments()
  load()
})
</script>

<style scoped>
.filters { display: flex; gap: 10px; align-items: center; margin-bottom: 16px; flex-wrap: wrap; }
.history-ip { font-size: 16px; font-weight: 600; margin: 0 0 12px; }
.edit-tip { color: var(--text-muted); font-size: 12px; margin: 0 0 4px; }
</style>
