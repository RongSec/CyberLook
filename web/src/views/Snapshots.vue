<template>
  <div>
    <!-- 截图设置 / 任务下发 -->
    <n-card title="截图设置" style="margin-bottom: 16px">
      <div class="settings">
        <span class="label">定期截图</span>
        <n-switch v-model:value="settings.enabled" />
        <template v-if="settings.enabled">
          <span class="label">每</span>
          <n-input-number v-model:value="settings.intervalDays" :min="1" :max="365" style="width: 100px" />
          <span class="label">天</span>
        </template>
        <span class="label" style="margin-left: 12px">保留</span>
        <n-input-number v-model:value="settings.retentionDays" :min="1" :max="365" style="width: 100px" />
        <span class="label">天</span>
        <n-button type="primary" @click="saveSettings">保存设置</n-button>
        <n-button type="success" :loading="capturing" @click="captureNow">立刻截取已确认 Web 资产</n-button>
      </div>
      <div v-if="capturing || shotTotal > 0" style="margin-top: 12px">
        <n-progress type="line" :percentage="shotPct" :status="shotStatus" :height="18">
          {{ shotDone }} / {{ shotTotal }}（成功 {{ shotOK }}，失败 {{ shotFail }}）
        </n-progress>
      </div>
      <p class="tip">立刻截图只针对「已确认」的 Web 资产（资产处置确认过的），避免给待处置目标发请求；定期截图按设定天数自动执行；超期快照自动删除。</p>
    </n-card>

    <!-- 快照列表 -->
    <n-card title="页面快照">
      <div class="toolbar">
        <n-input v-model:value="keyword" placeholder="搜索 IP / 端口" clearable style="width: 220px" />
        <n-tag round>{{ groupedSnapshots.length }} 个 URL / {{ filteredSnapshots.length }} 张</n-tag>
        <ExportButton type="snapshots" title="页面快照" :columns="exportCols" />
        <n-button @click="toggleAll">
          {{ allChecked ? '取消全选' : '全选' }}
        </n-button>
        <n-button quaternary :disabled="checked.length === 0" @click="checked = []">清空选择</n-button>
        <n-button type="error" :disabled="checked.length === 0" @click="batchDelete">
          批量删除{{ checked.length ? `(${checked.length})` : '' }}
        </n-button>
      </div>
      <n-empty v-if="snapshots.length === 0" description="暂无快照，点上方「立刻截取全部 Web 服务」" />
      <n-grid v-else :cols="4" :x-gap="14" :y-gap="14" responsive="screen" item-responsive>
        <n-grid-item v-for="g in groupedSnapshots" :key="g.url" span="4 m:1 s:2">
          <div class="shot-card" :class="{ picked: checked.includes(g.latest.id) }">
            <n-checkbox class="shot-check" :checked="checked.includes(g.latest.id)"
              @update:checked="v => toggle(g.latest.id, v)" />
            <div class="shot-img" @click="preview = g.latest; showPreview = true">
              <img :src="`/api/snapshots/${g.latest.id}/image?token=${token}`" :alt="g.latest.title" loading="lazy" />
            </div>
            <div class="shot-meta">
              <div class="shot-title">{{ g.latest.title || g.url }}</div>
              <div class="shot-url">{{ g.url }}</div>
              <div class="shot-time">
                {{ fmt(g.latest.capturedAt) }}
                <n-tag v-if="g.count > 1" size="tiny" type="info" round style="margin-left:6px">
                  {{ g.count }} 张
                </n-tag>
              </div>
            </div>
            <div class="shot-actions">
              <n-button size="tiny" type="primary" secondary @click="openTimeline(g.url)">查看态势</n-button>
              <n-button size="tiny" text type="error" @click="delOne(g.latest)">删除</n-button>
            </div>
          </div>
        </n-grid-item>
      </n-grid>

      <!-- 查看态势：同一 URL 的历史截图（按时间倒序） -->
      <n-modal v-model:show="showTimeline" preset="card" :title="'查看态势 — ' + timelineUrl" style="width: 1100px">
        <p class="tip">同一 URL 的历史截图，按时间倒序（最新在前）</p>
        <n-empty v-if="timelineList.length === 0" description="暂无历史截图" />
        <div v-else style="max-height: 65vh; overflow-y: auto">
          <div v-for="(s, idx) in timelineList" :key="s.id"
            style="display:flex;gap:14px;align-items:flex-start;padding:12px 0;border-bottom:1px solid #f1f5f9">
            <div style="width:46px;text-align:center;color:#94a3b8;font-size:12px;padding-top:4px">
              #{{ timelineList.length - idx }}
            </div>
            <img :src="`/api/snapshots/${s.id}/image?token=${token}`" :alt="s.title"
              style="width:280px;border-radius:6px;border:1px solid #e2e8f0;cursor:pointer"
              @click="preview = s; showPreview = true" />
            <div style="flex:1">
              <div style="font-weight:600">{{ s.title || '(无标题)' }}</div>
              <div style="color:#64748b;font-size:12px;margin-top:4px">{{ fmt(s.capturedAt) }}</div>
              <n-button size="tiny" text type="error" style="margin-top:8px" @click="delOne(s)">删除这张</n-button>
            </div>
          </div>
        </div>
      </n-modal>
    </n-card>

    <n-modal v-model:show="showPreview" preset="card" :title="preview?.title || preview?.url" style="width: 1100px">
      <img v-if="preview" :src="`/api/snapshots/${preview.id}/image?token=${token}`" style="width: 100%; border-radius: 8px" />
    </n-modal>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { useMessage } from 'naive-ui'
import api from '../api'
import ExportButton from '../components/ExportButton.vue'

// 导出列
const exportCols = [
  { key: 'id', label: 'ID' },
  { key: 'url', label: 'URL' },
  { key: 'title', label: '标题' },
  { key: 'capturedAt', label: '截图时间' }
]

const message = useMessage()
const snapshots = ref([])
const checked = ref([])
const settings = ref({ enabled: false, intervalDays: 7, retentionDays: 30 })
const capturing = ref(false)
const keyword = ref('')
const showTimeline = ref(false)
const timelineUrl = ref('')
const shotTotal = ref(0)
const shotDone = ref(0)
const shotOK = ref(0)
const shotFail = ref(0)
const filteredSnapshots = computed(() => {
  const k = keyword.value.trim().toLowerCase()
  if (!k) return snapshots.value
  return snapshots.value.filter(s => (s.url || '').toLowerCase().includes(k))
})

const groupedSnapshots = computed(() => {
  const m = new Map()
  for (const s of filteredSnapshots.value) {
    if (!m.has(s.url)) m.set(s.url, [])
    m.get(s.url).push(s)
  }
  const out = []
  for (const [url, list] of m) {
    list.sort((a, b) => new Date(b.capturedAt) - new Date(a.capturedAt))
    out.push({ url, latest: list[0], count: list.length, list })
  }
  // 最新截图时间靠前的排前面
  out.sort((a, b) => new Date(b.latest.capturedAt) - new Date(a.latest.capturedAt))
  return out
})

const timelineList = computed(() => {
  if (!timelineUrl.value) return []
  return snapshots.value
    .filter(s => s.url === timelineUrl.value)
    .sort((a, b) => new Date(b.capturedAt) - new Date(a.capturedAt))
})

function openTimeline(url) {
  timelineUrl.value = url
  showTimeline.value = true
}

const shotPct = computed(() => shotTotal.value > 0 ? Math.round(shotDone.value * 100 / shotTotal.value) : 0)
const shotStatus = computed(() => {
  if (shotTotal.value === 0) return 'default'
  if (shotDone.value < shotTotal.value) return 'default'
  return shotFail.value > 0 ? 'warning' : 'success'
})
const showPreview = ref(false)
const preview = ref(null)
const token = localStorage.getItem('token') || ''

function fmt(t) { return t ? new Date(t).toLocaleString('zh-CN') : '-' }

async function load() {
  const res = await api.get('/snapshots')
  snapshots.value = res.items || []
}
async function loadSettings() { settings.value = await api.get('/snapshots/settings') }

async function saveSettings() {
  await api.post('/snapshots/settings', settings.value)
  message.success('设置已保存')
}

let shotTimer = null
async function pollProgress() {
  try {
    const p = await api.get('/snapshots/progress')
    shotTotal.value = p.total || 0
    shotDone.value = p.done || 0
    shotOK.value = p.ok || 0
    shotFail.value = p.fail || 0
    if (p.running) {
      // 后台正在截图：刷新/切换页面回来后也要恢复进度条
      capturing.value = true
      if (!shotTimer) shotTimer = setInterval(pollProgress, 1200)
    } else if (capturing.value) {
      // 截图结束
      clearInterval(shotTimer)
      shotTimer = null
      capturing.value = false
      load()
    }
  } catch (e) { /* 忽略轮询错误 */ }
}

async function captureNow() {
  capturing.value = true
  shotTotal.value = 0; shotDone.value = 0; shotOK.value = 0; shotFail.value = 0
  if (shotTimer) clearInterval(shotTimer)
  shotTimer = setInterval(pollProgress, 1200)
  try {
    const res = await api.post('/snapshots/capture')
    message.success(`截图完成：成功 ${res.success}，失败 ${res.failed}`)
    load()
  } catch (e) { message.error(e.response?.data?.error || '截图失败') } finally { capturing.value = false }
}

async function delOne(s) {
  await api.post('/snapshots/batch-delete', { ids: [s.id] })
  message.success('已删除')
  load()
}

const allChecked = computed(() =>
  snapshots.value.length > 0 && checked.value.length === snapshots.value.length)

function toggle(id, v) {
  if (v) {
    if (!checked.value.includes(id)) checked.value = [...checked.value, id]
  } else {
    checked.value = checked.value.filter(x => x !== id)
  }
}

function toggleAll() {
  checked.value = allChecked.value ? [] : snapshots.value.map(s => s.id)
}

async function batchDelete() {
  if (checked.value.length === 0) { message.warning('请先选择'); return }
  await api.post('/snapshots/batch-delete', { ids: checked.value })
  message.success('已删除')
  checked.value = []
  load()
}

onMounted(() => {
  load()
  loadSettings()
  // 页面刷新/切回时立即同步一次截图进度（后台可能还在跑）
  pollProgress()
})
</script>

<style scoped>
.shot-card { position: relative; }
.shot-check { position: absolute; top: 8px; left: 8px; z-index: 3; }
.shot-card.picked { outline: 2px solid #f59e0b; border-radius: 8px; }

.settings { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
.label { font-size: 13px; color: var(--text-muted); }
.tip { color: var(--text-muted); font-size: 12px; margin: 10px 0 0; }
.toolbar { display: flex; gap: 12px; align-items: center; margin-bottom: 16px; }
.shot-card {
  position: relative;
  border-radius: 12px;
  overflow: hidden;
  background: var(--bg-panel);
  border: 1px solid var(--border-subtle);
  transition: all 0.25s cubic-bezier(0.4, 0, 0.2, 1);
}
.shot-card:hover { transform: translateY(-3px); border-color: var(--border-glow); box-shadow: 0 12px 32px rgba(0,0,0,0.28); }
.shot-img { height: 170px; overflow: hidden; cursor: pointer; background: #0b1020; display: flex; align-items: center; justify-content: center; }
.shot-img img { max-width: 100%; max-height: 100%; width: auto; height: auto; object-fit: contain; transition: transform 0.4s; }

.shot-meta { padding: 10px 12px; }
.shot-title { font-size: 12.5px; font-weight: 600; color: var(--text-main); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.shot-url { font-size: 11px; color: var(--text-muted); margin-top: 2px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.shot-time { font-size: 11px; color: #4b5a72; margin-top: 4px; }
.shot-del { position: absolute; top: 8px; right: 8px; }
</style>
