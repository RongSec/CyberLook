<template>
  <div>
    <div class="toolbar">
      <n-button type="primary" @click="openBind">新增定时任务</n-button>
      <n-button secondary @click="load">刷新</n-button>
      <span class="hint">定时任务到点自动执行，可在「扫描任务」里查看每次结果</span>
    </div>

    <n-data-table :columns="columns" :data="items" :loading="loading" size="small" :row-key="r => r.id" />

    <!-- 绑定/编辑周期 -->
    <n-modal v-model:show="showBind" preset="card" :title="bindForm.id ? '编辑定时任务' : '新增定时任务'" style="width: 520px">
      <n-form label-placement="left" label-width="90">
        <n-form-item label="任务">
          <n-select v-model:value="bindForm.id" :options="taskOptions" filterable placeholder="选择一个已有任务"
            :disabled="!!bindForm.id" @update:value="onPickTask" />
        </n-form-item>
        <n-form-item label="执行周期">
          <n-select v-model:value="bindForm.mode" :options="[
            { label: '每天', value: 'daily' },
            { label: '每隔 N 天', value: 'interval' }
          ]" style="width: 140px" />
          <n-input-number v-if="bindForm.mode === 'interval'" v-model:value="bindForm.days" :min="2" :max="30"
            style="width: 110px;margin-left:8px" />
          <span style="margin-left:8px">天</span>
        </n-form-item>
        <n-form-item label="执行时间">
          <n-input-number v-model:value="bindForm.hour" :min="0" :max="23" style="width: 110px" />
          <span style="margin-left:8px">点整</span>
        </n-form-item>
        <n-form-item label="预览">
          <n-tag type="info">{{ cronPreview }}</n-tag>
        </n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showBind = false">取消</n-button>
          <n-button v-if="bindForm.id" type="error" secondary @click="unbind">取消定时</n-button>
          <n-button type="primary" :loading="saving" @click="saveBind">保存</n-button>
        </n-space>
      </template>
    </n-modal>

    <!-- 详情：执行历史 -->
    <n-modal v-model:show="showDetail" preset="card" :title="`执行记录 · ${detailTask?.name || ''}`" style="width: 860px">
      <n-descriptions :column="4" size="small" bordered style="margin-bottom:12px">
        <n-descriptions-item label="目标 IP">{{ detailTask?.totalIps || 0 }}</n-descriptions-item>
        <n-descriptions-item label="存活 IP">{{ detailTask?.aliveCount || 0 }}</n-descriptions-item>
        <n-descriptions-item label="周期">{{ cronText(detailTask?.cron) }}</n-descriptions-item>
        <n-descriptions-item label="执行次数">{{ runs.length }}</n-descriptions-item>
      </n-descriptions>
      <n-data-table :columns="runCols" :data="runs" size="small" :max-height="380" />
    </n-modal>
  </div>
</template>

<script setup>
import { ref, h, computed, onMounted } from 'vue'
import { NButton, NTag, useMessage } from 'naive-ui'
import api from '../api'

const message = useMessage()
const items = ref([])
const loading = ref(false)
const saving = ref(false)

const showBind = ref(false)
const bindForm = ref({ id: null, mode: 'daily', days: 7, hour: 2 })
const taskOptions = ref([])

const showDetail = ref(false)
const detailTask = ref(null)
const runs = ref([])

function cronText(cron) {
  if (!cron) return '未设置'
  let m = String(cron).match(/^0 (\d+) \*\/(\d+) \* \*$/)
  if (m) return `每 ${m[2]} 天 ${String(m[1]).padStart(2, '0')}:00`
  m = String(cron).match(/^0 (\d+) \* \* \*$/)
  if (m) return `每天 ${String(m[1]).padStart(2, '0')}:00`
  return cron
}

const cronPreview = computed(() => {
  const { mode, days, hour } = bindForm.value
  const hh = String(hour ?? 2).padStart(2, '0')
  return mode === 'daily' ? `每天 ${hh}:00` : `每 ${days} 天 ${hh}:00`
})

function buildCron() {
  const { mode, days, hour } = bindForm.value
  return mode === 'daily' ? `0 ${hour} * * *` : `0 ${hour} */${days} * *`
}

async function load() {
  loading.value = true
  try {
    items.value = await api.get('/tasks/scheduled')
  } catch (e) {
    message.error('加载失败：' + (e?.message || e))
  } finally {
    loading.value = false
  }
}

async function openBind() {
  bindForm.value = { id: null, mode: 'daily', days: 7, hour: 2 }
  showBind.value = true
  try {
    const res = await api.get('/tasks', { params: { page: 1, size: 200 } })
    taskOptions.value = (res.items || []).map(t => ({
      label: `${t.name}（${t.ipRanges.length > 30 ? t.ipRanges.slice(0, 30) + '…' : t.ipRanges}）`,
      value: t.id,
      raw: t
    }))
  } catch (e) { console.error(e) }
}

function onPickTask(id) {
  const opt = taskOptions.value.find(o => o.value === id)
  if (!opt) return
  const cron = opt.raw.cron || ''
  let m = cron.match(/^0 (\d+) \*\/(\d+) \* \*$/)
  if (m) {
    bindForm.value.mode = 'interval'
    bindForm.value.days = parseInt(m[2])
    bindForm.value.hour = parseInt(m[1])
    return
  }
  m = cron.match(/^0 (\d+) \* \* \*$/)
  if (m) {
    bindForm.value.mode = 'daily'
    bindForm.value.hour = parseInt(m[1])
  }
}

async function saveBind() {
  if (!bindForm.value.id) { message.warning('请选择任务'); return }
  saving.value = true
  try {
    const t = taskOptions.value.find(o => o.value === bindForm.value.id)?.raw
    await api.put(`/tasks/${bindForm.value.id}`, {
      name: t?.name || '',
      ipRanges: t?.ipRanges || '',
      modules: t?.modules || 'port',
      portMode: t?.portMode || 'top100',
      ports: t?.ports || '',
      batchSize: t?.batchSize || 100,
      thread: t?.thread || 600,
      timeout: t?.timeout || 3,
      finger: t?.finger || 'tiny',
      cron: buildCron(),
      nodeIds: t?.nodeIds || ''
    })
    message.success('已保存')
    showBind.value = false
    load()
  } catch (e) {
    message.error('保存失败：' + (e?.response?.data?.error || e?.message || e))
  } finally {
    saving.value = false
  }
}

async function unbind() {
  try {
    const t = taskOptions.value.find(o => o.value === bindForm.value.id)?.raw
    await api.put(`/tasks/${bindForm.value.id}`, {
      name: t?.name || '', ipRanges: t?.ipRanges || '', modules: t?.modules || 'port',
      portMode: t?.portMode || 'top100', ports: t?.ports || '', batchSize: t?.batchSize || 100,
      thread: t?.thread || 600, timeout: t?.timeout || 3, finger: t?.finger || 'tiny',
      cron: '', nodeIds: t?.nodeIds || ''
    })
    message.success('已取消定时')
    showBind.value = false
    load()
  } catch (e) { message.error('操作失败') }
}

async function openDetail(row) {
  detailTask.value = row
  showDetail.value = true
  try {
    runs.value = await api.get(`/tasks/${row.id}/runs`)
  } catch (e) { runs.value = [] }
}

const runCols = [
  { title: '开始时间', key: 'startedAt', width: 170, render: r => fmt(r.startedAt) },
  { title: '结束时间', key: 'finishedAt', width: 170, render: r => fmt(r.finishedAt) },
  { title: '耗时', key: 'durationSec', width: 90, render: r => dur(r.durationSec) },
  { title: '状态', key: 'status', width: 90, render: r => h(NTag, { size: 'small', type: r.status === 'done' ? 'success' : r.status === 'failed' ? 'error' : 'warning' }, { default: () => r.status }) },
  { title: 'IP/存活', width: 110, render: r => `${r.totalIps || 0} / ${r.aliveIps || 0}` },
  { title: '应用', width: 110, render: r => `${r.appsFound || 0}（新 ${r.appsNew || 0}）` },
  { title: '违规', width: 110, render: r => `${r.violations || 0}（新 ${r.violationsNew || 0}）` }
]

const columns = [
  { title: 'ID', key: 'id', width: 70 },
  { title: '任务名', key: 'name', width: 160, ellipsis: { tooltip: true } },
  { title: '扫描范围', key: 'ipRanges', ellipsis: { tooltip: true }, width: 200 },
  { title: '周期', key: 'cron', width: 140, render: r => h(NTag, { size: 'small', type: 'info' }, { default: () => cronText(r.cron) }) },
  { title: '状态', key: 'status', width: 90, render: r => statusTag(r.status) },
  {
    title: '上次执行', key: 'lastRun', width: 210,
    render: r => {
      const lr = r.lastRun
      if (!lr) return h('span', { style: 'color:#999' }, '尚未执行')
      return h('div', { style: 'font-size:12px;line-height:1.6' }, [
        h('div', fmt(lr.startedAt)),
        h('div', { style: 'color:#888' }, `耗时 ${dur(lr.durationSec)} · 存活 ${lr.aliveIps || 0}`)
      ])
    }
  },
  {
    title: '操作', key: 'actions', width: 200,
    render: row => h('div', { style: 'display:flex;gap:6px' }, [
      h(NButton, { size: 'small', text: true, type: 'info', onClick: () => openEdit(row) }, { default: () => '编辑' }),
      h(NButton, { size: 'small', text: true, onClick: () => openDetail(row) }, { default: () => '详情' })
    ])
  }
]

function openEdit(row) {
  openBind().then(() => {
    bindForm.value.id = row.id
    onPickTask(row.id)
  })
}

function statusTag(s) {
  const map = { done: ['success', '完成'], running: ['warning', '执行中'], failed: ['error', '失败'], pending: ['default', '待执行'], interrupted: ['warning', '中断'] }
  const [type, text] = map[s] || ['default', s || '-']
  return h(NTag, { size: 'small', type }, { default: () => text })
}

function fmt(t) {
  if (!t) return '-'
  const d = new Date(t)
  const p = n => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

function dur(sec) {
  if (!sec || sec <= 0) return '-'
  if (sec < 60) return `${sec}秒`
  const m = Math.floor(sec / 60), s = sec % 60
  if (m < 60) return `${m}分${s}秒`
  return `${Math.floor(m / 60)}时${m % 60}分`
}

onMounted(load)
</script>

<style scoped>
.toolbar { display: flex; gap: 8px; align-items: center; margin-bottom: 12px; }
.hint { color: #888; font-size: 12px; }
</style>
