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
        <n-button type="success" :loading="capturing" @click="captureNow">立刻截取全部 Web 服务</n-button>
      </div>
      <p class="tip">立刻截图会对当前库内所有 web 服务逐一截图；定期截图按设定天数自动执行；超期快照自动删除。</p>
    </n-card>

    <!-- 快照列表 -->
    <n-card title="页面快照">
      <div class="toolbar">
        <n-tag round>{{ snapshots.length }} 张</n-tag>
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
        <n-grid-item v-for="s in snapshots" :key="s.id" span="4 m:1 s:2">
          <div class="shot-card" :class="{ picked: checked.includes(s.id) }">
            <n-checkbox class="shot-check" :checked="checked.includes(s.id)"
              @update:checked="v => toggle(s.id, v)" />
            <div class="shot-img" @click="preview = s; showPreview = true">
              <img :src="`/api/snapshots/${s.id}/image?token=${token}`" :alt="s.title" loading="lazy" />
            </div>
            <div class="shot-meta">
              <div class="shot-title">{{ s.title || s.url }}</div>
              <div class="shot-url">{{ s.url }}</div>
              <div class="shot-time">{{ fmt(s.capturedAt) }}</div>
            </div>
            <n-button size="tiny" text type="error" class="shot-del" @click="delOne(s)">删除</n-button>
          </div>
        </n-grid-item>
      </n-grid>
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

async function captureNow() {
  capturing.value = true
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

onMounted(() => { load(); loadSettings() })
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
