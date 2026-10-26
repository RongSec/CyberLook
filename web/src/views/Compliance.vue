<template>
  <div>
    <n-card>
      <n-tabs type="line" animated>
        <n-tab-pane name="results" tab="检测结果">
          <div class="toolbar">
            <n-button type="primary" :loading="checking" @click="runCheck">一键检测当前结果</n-button>
            <n-radio-group v-model:value="resultStatus" @update:value="loadResults">
              <n-radio-button value="">全部</n-radio-button>
              <n-radio-button value="violation">违规</n-radio-button>
              <n-radio-button value="fixed">已整改</n-radio-button>
            </n-radio-group>
            <ExportButton type="compliance" title="合规检测" :columns="exportCols" :filters="{ status: resultStatus }" />
            <n-button @click="checkAllResults">全选本页</n-button>
            <n-button quaternary :disabled="checked.length === 0" @click="checked = []">清空</n-button>
            <n-button type="error" :disabled="checked.length === 0" @click="batchDeleteResults">
              删除选中{{ checked.length ? `(${checked.length})` : '' }}
            </n-button>
          </div>
          <n-data-table :columns="resultColumns" :data="results" :loading="loading"
            :row-key="r => r.id" v-model:checked-row-keys="checked"
            :pagination="{ ...resultPagination, showSizePicker: true, pageSizes: [10, 20, 50, 100], onUpdatePageSize: onResultSize }"
            remote @update:page="onResultPage" />
        </n-tab-pane>

        <n-tab-pane name="rules" tab="规则管理">
          <div class="toolbar">
            <n-button type="primary" @click="showRuleModal = true">添加规则</n-button>
            <ExportButton type="rules" title="合规规则" :columns="ruleExportCols" />
          </div>
          <n-data-table :columns="ruleColumns" :data="rules" size="small" />
        </n-tab-pane>
      </n-tabs>
    </n-card>

    <n-modal v-model:show="showRuleModal" preset="card" title="添加规则" style="width: 420px">
      <n-form label-placement="left" label-width="80">
        <n-form-item label="名称"><n-input v-model:value="ruleForm.name" placeholder="如 MySQL 直连" /></n-form-item>
        <n-form-item label="端口"><n-input-number v-model:value="ruleForm.port" :min="1" :max="65535" /></n-form-item>
        <n-form-item label="协议">
          <n-select v-model:value="ruleForm.protocol" :options="[{ label: 'tcp', value: 'tcp' }, { label: 'udp', value: 'udp' }]" />
        </n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showRuleModal = false">取消</n-button>
          <n-button type="primary" @click="saveRule">保存</n-button>
        </n-space>
      </template>
    </n-modal>
  </div>
</template>

<script setup>
import { ref, h, onMounted } from 'vue'
import { NTag, NButton, NPopconfirm, useMessage } from 'naive-ui'
import api from '../api'
import ExportButton from '../components/ExportButton.vue'

const message = useMessage()
const loading = ref(false)
const checking = ref(false)
const resultStatus = ref('')
const results = ref([])
const checked = ref([])
const resultPagination = ref({ page: 1, pageSize: 20, itemCount: 0 })
const resultPage = ref(1)
const resultSize = ref(20)

const rules = ref([])
const showRuleModal = ref(false)
const ruleForm = ref({ name: '', port: 80, protocol: 'tcp' })

// 导出列
const exportCols = [
  { key: 'ip', label: 'IP' },
  { key: 'port', label: '端口' },
  { key: 'status', label: '状态' },
  { key: 'ruleId', label: '规则ID' },
  { key: 'detectedAt', label: '首次发现' },
  { key: 'lastSeen', label: '最近发现' }
]
const ruleExportCols = [
  { key: 'name', label: '名称' },
  { key: 'port', label: '端口' },
  { key: 'protocol', label: '协议' },
  { key: 'enabled', label: '启用' }
]

const resultColumns = [
  { type: 'selection', width: 40, fixed: 'left' },
  { title: 'IP', key: 'ip', width: 140 },
  { title: '端口', key: 'port', width: 90 },
  { title: '服务', key: 'service', width: 140, ellipsis: { tooltip: true }, render: r => r.service || '-' },
  {
    title: '状态', key: 'status', width: 100,
    render: row => h(NTag, { type: row.status === 'violation' ? 'error' : 'success', size: 'small' },
      { default: () => row.status === 'violation' ? '违规' : '已整改' })
  },
  { title: '首次发现', key: 'detectedAt', render: r => fmt(r.detectedAt) },
  { title: '最近发现', key: 'lastSeen', render: r => fmt(r.lastSeen) },
  { title: '整改时间', key: 'fixedAt', render: r => fmt(r.fixedAt) },
  {
    title: '操作', key: 'actions', width: 90,
    render: row => h(NPopconfirm, { onPositiveClick: () => delResult(row) },
      { trigger: () => h(NButton, { size: 'small', text: true, type: 'error' }, { default: () => '删除' }),
        default: () => `确认删除 ${row.ip}:${row.port} 的检测记录？` })
  }
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
    title: '操作', key: 'actions', width: 90,
    render: row => h(NPopconfirm, { onPositiveClick: () => delRule(row.id) },
      { trigger: () => h(NButton, { size: 'small', text: true, type: 'error' }, { default: () => '删除' }), default: () => '确认删除该规则？' })
  }
]

function fmt(t) { return t ? new Date(t).toLocaleString('zh-CN') : '-' }

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
    const res = await api.get('/compliance/results', { params: { page: resultPage.value, size: resultSize.value, status: resultStatus.value } })
    results.value = res.items
    resultPagination.value = { page: resultPage.value, pageSize: resultSize.value, itemCount: res.total }
  } finally {
    loading.value = false
  }
}
function onResultPage(p) { resultPage.value = p; loadResults() }
function onResultSize(s) { resultSize.value = s; resultPage.value = 1; loadResults() }

async function runCheck() {
  checking.value = true
  try {
    const res = await api.post('/compliance/check')
    message.success(`检测完成：新增违规 ${res.newViolation}，已整改 ${res.fixed}`)
    loadResults()
  } catch (e) {
    message.error(e.response?.data?.error || '检测失败')
  } finally {
    checking.value = false
  }
}

async function loadRules() { rules.value = await api.get('/compliance/rules') }

async function saveRule() {
  try {
    await api.post('/compliance/rules', ruleForm.value)
    message.success('已保存')
    showRuleModal.value = false
    ruleForm.value = { name: '', port: 80, protocol: 'tcp' }
    loadRules()
  } catch (e) { message.error('保存失败') }
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
.toolbar { display: flex; gap: 12px; align-items: center; margin-bottom: 16px; }
</style>
