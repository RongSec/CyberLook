<template>
  <div>
    <n-card>
      <n-tabs type="line" animated>
        <n-tab-pane name="results" tab="检测结果">
          <SearchBar ref="searchBar" :fields="splFields" @search="onSplSearch" @reset="onSplReset" />

          <div class="toolbar" style="margin-top:12px">
            <n-button type="primary" :loading="checking" @click="runCheck([])">
              一键检测（全量）
            </n-button>
            <n-button type="warning" secondary :loading="checking" :disabled="checked.length === 0" @click="runCheck(checked)">
              检测选中{{ checked.length ? `(${checked.length})` : '' }}
            </n-button>
            <n-checkbox v-model:checked="ignoreWhitelist">忽略白名单</n-checkbox>
            <ExportButton type="compliance" title="合规检测" :columns="exportCols"
              :filters="{ status: '' }" :conds="splConds" />
            <n-button @click="checkAllResults">全选本页</n-button>
            <n-button quaternary :disabled="checked.length === 0" @click="checked = []">清空</n-button>
            <n-button type="error" :disabled="checked.length === 0" @click="batchDeleteResults">
              删除选中{{ checked.length ? `(${checked.length})` : '' }}
            </n-button>
          </div>

          <n-alert v-if="lastSummary" type="default" :bordered="false" style="margin-bottom:12px">
            上次检测：实连 {{ lastSummary.checked }} 个端口 · 疑似确认 {{ lastSummary.confirmed || 0 }}
            · 新增违规 {{ lastSummary.newViolation }} · 复现 {{ lastSummary.reopened }}
            · 确认已关闭 {{ lastSummary.fixed }} · 不可达 {{ lastSummary.uncertain }}
          </n-alert>

          <n-data-table :columns="resultColumns" :data="results" :loading="loading"
            :row-key="r => r.id" v-model:checked-row-keys="checked"
            :pagination="{ ...resultPagination, showSizePicker: true, pageSizes: [10, 20, 50, 100], onUpdatePageSize: onResultSize }"
            remote @update:page="onResultPage" size="small" />
        </n-tab-pane>

        <n-tab-pane name="rules" tab="规则管理">
          <div class="toolbar">
            <n-button type="primary" @click="openRuleModal()">添加规则</n-button>
            <ExportButton type="rules" title="合规规则" :columns="ruleExportCols" />
          </div>
          <n-data-table :columns="ruleColumns" :data="rules" size="small" />
        </n-tab-pane>
      </n-tabs>
    </n-card>

    <n-modal v-model:show="showRuleModal" preset="card" :title="ruleForm.id ? '编辑规则' : '添加规则'" style="width: 420px">
      <n-form label-placement="left" label-width="80">
        <n-form-item label="名称"><n-input v-model:value="ruleForm.name" placeholder="如 MySQL 直连" /></n-form-item>
        <n-form-item label="端口"><n-input-number v-model:value="ruleForm.port" :min="1" :max="65535" /></n-form-item>
        <n-form-item label="协议">
          <n-select v-model:value="ruleForm.protocol" :options="[{ label: 'tcp', value: 'tcp' }, { label: 'udp', value: 'udp' }]" />
        </n-form-item>
        <n-form-item label="启用">
          <n-switch v-model:value="ruleForm.enabled" />
          <span class="tip">停用后不参与合规判定</span>
        </n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showRuleModal = false">取消</n-button>
          <n-button type="primary" @click="saveRule">保存</n-button>
        </n-space>
      </template>
    </n-modal>

    <n-modal v-model:show="showEdit" preset="card" :title="'编辑检测结果 — ' + editForm.ip + ':' + editForm.port" style="width: 460px">
      <n-form label-placement="left" label-width="80">
        <n-form-item label="标签">
          <n-input v-model:value="editForm.label" :placeholder="editForm.ruleName || '留空则跟随规则名'" clearable />
        </n-form-item>
        <n-form-item label="状态">
          <n-select v-model:value="editForm.status" :options="[
            { label: '疑似违规', value: 'suspected' },
            { label: '违规', value: 'violation' },
            { label: '已整改', value: 'fixed' }
          ]" />
        </n-form-item>
      </n-form>
      <p class="tip" style="margin:0">标签只影响列表/导出里的显示名，不改规则归属，也不影响检测匹配。</p>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showEdit = false">取消</n-button>
          <n-button type="primary" @click="saveEdit">保存</n-button>
        </n-space>
      </template>
    </n-modal>

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
import SearchBar from '../components/SearchBar.vue'
import ExportButton from '../components/ExportButton.vue'

const message = useMessage()
const loading = ref(false)
const checking = ref(false)
const ignoreWhitelist = ref(true)
const lastSummary = ref(null)
const results = ref([])
const checked = ref([])
const splConds = ref([])
const searchBar = ref(null)
const resultPagination = ref({ page: 1, pageSize: 20, itemCount: 0 })
const resultPage = ref(1)
const resultSize = ref(20)

const rules = ref([])
const showRuleModal = ref(false)
const ruleForm = ref({ id: 0, name: '', port: 80, protocol: 'tcp', enabled: true })

// 态势（历史截图）
const showHistory = ref(false)
const history = ref([])
const historyIP = ref('')

// 编辑检测结果（标签 / 状态）
const showEdit = ref(false)
const editForm = ref({ id: 0, ip: '', port: 0, ruleName: '', label: '', status: 'suspected' })

function openEdit(row) {
  editForm.value = {
    id: row.id, ip: row.ip, port: row.port,
    ruleName: row.ruleName || '',
    label: row.label || '',
    status: row.status || 'suspected'
  }
  showEdit.value = true
}

async function saveEdit() {
  try {
    await api.put(`/compliance/results/${editForm.value.id}`, {
      label: editForm.value.label || '',
      status: editForm.value.status
    })
    message.success('已保存')
    showEdit.value = false
    loadResults()
  } catch (e) {
    message.error(e.response?.data?.error || '保存失败')
  }
}

const resultColumns = [
  { type: 'selection', width: 40, fixed: 'left' },
  { title: 'IP', key: 'ip', width: 130 },
  { title: '端口', key: 'port', width: 70 },
  { title: '规则', key: 'ruleName', width: 130, ellipsis: { tooltip: true }, render: r => r.ruleName || '-' },
  { title: '部门', key: 'department', width: 130, ellipsis: { tooltip: true }, render: r => r.department || '-' },
  { title: '负责人', key: 'owner', width: 110, ellipsis: { tooltip: true }, render: r => r.owner || '-' },
  { title: '系统', key: 'systemName', width: 160, ellipsis: { tooltip: true }, render: r => r.systemName || '-' },
  { title: '服务', key: 'service', width: 110, ellipsis: { tooltip: true }, render: r => r.service || '-' },
  {
    title: '状态', key: 'status', width: 96,
    render: row => {
      const map = {
        violation: { type: 'error', text: '违规' },
        suspected: { type: 'warning', text: '疑似违规' },
        fixed: { type: 'success', text: '已整改' }
      }
      const s = map[row.status] || { type: 'default', text: row.status || '-' }
      return h(NTag, { type: s.type, size: 'small' }, { default: () => s.text })
    }
  },
  { title: '确认响应', key: 'evidence', width: 200, ellipsis: { tooltip: true }, render: r => r.evidence || '-' },
  { title: '首次发现', key: 'detectedAt', width: 150, render: r => fmt(r.detectedAt) },
  { title: '最近发现', key: 'lastSeen', width: 150, render: r => fmt(r.lastSeen) },
  {
    title: '操作', key: 'actions', width: 160, fixed: 'right',
    render: row => h('div', { style: 'display:flex;gap:8px' }, [
      h(NButton, { size: 'small', text: true, type: 'primary', onClick: () => openEdit(row) }, { default: () => '编辑' }),
      h(NButton, { size: 'small', text: true, type: 'primary', onClick: () => showSnapshots(row) }, { default: () => '态势' }),
      h(NPopconfirm, { onPositiveClick: () => delResult(row) }, {
        trigger: () => h(NButton, { size: 'small', text: true, type: 'error' }, { default: () => '删除' }),
        default: () => `确认删除 ${row.ip}:${row.port} 的检测记录？`
      })
    ])
  }
]

// 检索字段与表格列保持一致（新增列自动多一项）
const SPL_FIXED_OPTIONS = {
  status: [
    { label: '疑似违规', value: 'suspected' },
    { label: '违规', value: 'violation' },
    { label: '已整改', value: 'fixed' }
  ]
}
const splFields = computed(() =>
  resultColumns
    .filter(c => c.key && !['actions', 'op'].includes(c.key))
    .map(c => {
      const f = SPL_FIXED_OPTIONS[c.key]
      return f ? { key: c.key, label: c.title, options: f } : { key: c.key, label: c.title }
    })
)

// 导出列 = 表格所有列（排除操作/勾选列）
const exportCols = computed(() =>
  resultColumns
    .filter(c => c.key && !['actions', 'op', 'a'].includes(c.key))
    .map(c => ({ key: c.key, label: c.title }))
)

const historyColumns = [
  { title: '扫描时间', key: 'scannedAt', render: r => fmt(r.scannedAt) },
  { title: '存活', key: 'isAlive', render: r => r.isAlive ? '是' : '否' },
  { title: '开放端口', key: 'openPorts', render: r => parsePorts(r.openPorts) }
]

const ruleColumns = [
  { title: '端口', key: 'port', width: 100 },
  { title: '名称', key: 'name' },
  { title: '协议', key: 'protocol', width: 100 },
  {
    title: '启用', key: 'enabled', width: 100,
    render: row => h(NTag, { type: row.enabled ? 'success' : 'default', size: 'small' },
      { default: () => row.enabled ? '启用' : '停用' })
  },
  {
    title: '操作', key: 'actions', width: 130,
    render: row => h('div', { style: 'display:flex;gap:8px' }, [
      h(NButton, { size: 'small', text: true, type: 'primary', onClick: () => openRuleModal(row) }, { default: () => '编辑' }),
      h(NPopconfirm, { onPositiveClick: () => delRule(row.id) },
        { trigger: () => h(NButton, { size: 'small', text: true, type: 'error' }, { default: () => '删除' }), default: () => '确认删除该规则？' })
    ])
  }
]

const ruleExportCols = computed(() =>
  ruleColumns
    .filter(c => c.key && !['actions', 'op', 'a'].includes(c.key))
    .map(c => ({ key: c.key, label: c.title }))
)

function fmt(t) { return t ? new Date(t).toLocaleString('zh-CN') : '-' }
function parsePorts(openPorts) {
  if (!openPorts) return '-'
  try {
    const arr = typeof openPorts === 'string' ? JSON.parse(openPorts) : openPorts
    return arr.map(p => p.port).join(', ')
  } catch { return '-' }
}

async function showSnapshots(row) {
  historyIP.value = row.ip
  try {
    history.value = await api.get(`/assets/${row.ip}/snapshots`)
  } catch {
    history.value = []
  }
  showHistory.value = true
}

function onSplSearch({ conditions }) {
  splConds.value = conditions || []
  resultPage.value = 1
  loadResults()
}
function onSplReset() {
  splConds.value = []
  resultPage.value = 1
  loadResults()
}

async function delResult(row) {
  try {
    await api.delete(`/compliance/results/${row.id}`)
    message.success('已删除')
    checked.value = checked.value.filter(x => x !== row.id)
    loadResults()
  } catch (e) {
    message.error(e.response?.data?.error || '删除失败')
  }
}

function checkAllResults() {
  checked.value = results.value.map(r => r.id)
}

async function batchDeleteResults() {
  const ids = checked.value
  if (!ids.length) return message.warning('请先勾选要删除的记录')
  try {
    const res = await api.post('/compliance/results/batch-delete', { ids })
    message.success(`已删除 ${res.deleted || ids.length} 项`)
    checked.value = []
    loadResults()
  } catch (e) {
    message.error(e.response?.data?.error || '删除失败')
  }
}

async function loadResults() {
  loading.value = true
  try {
    const res = await api.get('/compliance/results', {
      params: {
        page: resultPage.value, size: resultSize.value,
        conds: JSON.stringify(splConds.value)
      }
    })
    results.value = res.items
    resultPagination.value = { page: resultPage.value, pageSize: resultSize.value, itemCount: res.total }
  } finally {
    loading.value = false
  }
}
function onResultPage(p) { resultPage.value = p; loadResults() }
function onResultSize(s) { resultSize.value = s; resultPage.value = 1; loadResults() }

// 手动检测：对每个候选端口实连一次，确认仍然开着才判违规
async function runCheck(ids) {
  checking.value = true
  try {
    const res = await api.post('/compliance/check', {
      ids: ids || [],
      ignoreWhitelist: ignoreWhitelist.value
    })
    lastSummary.value = res
    if (!res.checked) {
      message.warning(res.message || '没有需要检测的端口')
    } else {
      message.success(`检测完成：实连 ${res.checked} 个端口，疑似确认 ${res.confirmed || 0}，新增违规 ${res.newViolation}，复现 ${res.reopened}，确认关闭 ${res.fixed}，不可达 ${res.uncertain}`)
    }
    checked.value = []
    loadResults()
  } catch (e) {
    message.error(e.response?.data?.error || '检测失败')
  } finally {
    checking.value = false
  }
}

async function loadRules() { rules.value = await api.get('/compliance/rules') }

// 新增 / 编辑同一个弹窗：传 row 即编辑
function openRuleModal(row) {
  if (row) {
    ruleForm.value = {
      id: row.id,
      name: row.name || '',
      port: row.port || 80,
      protocol: row.protocol || 'tcp',
      enabled: row.enabled !== false
    }
  } else {
    ruleForm.value = { id: 0, name: '', port: 80, protocol: 'tcp', enabled: true }
  }
  showRuleModal.value = true
}

async function saveRule() {
  if (!ruleForm.value.name || !ruleForm.value.name.trim()) return message.warning('请填写规则名称')
  if (!ruleForm.value.port) return message.warning('请填写端口')
  try {
    await api.post('/compliance/rules', ruleForm.value)
    message.success(ruleForm.value.id ? '已保存，下次检测生效' : '已添加')
    showRuleModal.value = false
    ruleForm.value = { id: 0, name: '', port: 80, protocol: 'tcp', enabled: true }
    loadRules()
  } catch (e) { message.error(e.response?.data?.error || '保存失败') }
}
async function delRule(id) {
  await api.delete(`/compliance/rules/${id}`)
  message.success('已删除')
  loadRules()
}

onMounted(() => {
  loadResults()
  loadRules()
})
</script>

<style scoped>
.toolbar { display: flex; gap: 12px; align-items: center; margin-bottom: 16px; flex-wrap: wrap; }
.history-ip { font-size: 16px; font-weight: 600; margin: 0 0 12px; }
.tip { margin-left: 8px; font-size: 12px; color: #888; }
</style>
