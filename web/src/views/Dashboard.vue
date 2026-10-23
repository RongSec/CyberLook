<template>
  <div class="dash">
    <!-- 统计卡片 -->
    <n-grid :cols="6" :x-gap="14" :y-gap="14" responsive="screen" item-responsive>
      <n-grid-item v-for="c in cards" :key="c.label" span="6 m:2 s:3">
        <div class="stat-card">
          <div class="stat-glow" :style="{ background: c.color }"></div>
          <div class="stat-icon" :style="{ background: c.bg, borderColor: c.border }">
            <n-icon size="20" :color="c.color"><component :is="c.icon" /></n-icon>
          </div>
          <div class="stat-body">
            <div class="stat-label">{{ c.label }}</div>
            <div class="stat-value" :style="{ backgroundImage: `linear-gradient(120deg, ${c.color}, #e2e8f0)` }">
              {{ c.value }}
            </div>
          </div>
        </div>
      </n-grid-item>
    </n-grid>

    <!-- 趋势折线图 -->
    <div class="panel" style="margin-top: 16px">
      <div class="panel-head">
        <span class="panel-title">资产态势趋势</span>
        <n-radio-group v-model:value="trendRange" size="small" @update:value="loadAll" style="margin-left: 12px">
          <n-radio-button value="7d">近 7 天</n-radio-button>
          <n-radio-button value="30d">近 30 天</n-radio-button>
          <n-radio-button value="365d">近一年</n-radio-button>
        </n-radio-group>
        <span class="panel-sub">Web资产 · 已登记 · 待确认 · 导入资产 · 违规（与卡片同源）</span>
      </div>
      <div ref="trendRef" class="chart chart-tall"></div>
    </div>

    <n-grid :cols="3" :x-gap="16" style="margin-top: 16px" responsive="screen" item-responsive>
      <!-- 新存活主机 -->
      <n-grid-item span="3 m:1">
        <div class="panel panel-fixed">
          <div class="panel-head">
            <span class="panel-title"><i class="dot live"></i>新存活主机</span>
            <span class="panel-sub">{{ (summary.newAlive || []).length }} 台</span>
          </div>
          <div class="panel-body">
            <div class="tag-list">
              <span v-for="ip in (summary.newAlive || [])" :key="ip" class="ip-chip">{{ ip }}</span>
            </div>
          </div>
        </div>
      </n-grid-item>

      <!-- 新出现应用 -->
      <n-grid-item span="3 m:1">
        <div class="panel panel-fixed">
          <div class="panel-head">
            <span class="panel-title"><i class="dot new"></i>新出现应用</span>
            <span class="panel-sub">{{ (summary.newService || []).length }} 个</span>
          </div>
          <div class="panel-body">
            <div class="svc-list">
              <div v-for="s in (summary.newService || []).slice(0, 8)" :key="s.id" class="svc-row">
                <span class="svc-ip">{{ s.ip }}</span>
                <span class="svc-port">:{{ s.port }}</span>
                <span class="svc-name">{{ s.service || s.banner || '-' }}</span>
              </div>
            </div>
          </div>
        </div>
      </n-grid-item>

      <!-- 资产状态分布（右） -->
      <n-grid-item span="3 m:1">
        <div class="panel panel-fixed">
          <div class="panel-head"><span class="panel-title">资产状态分布</span></div>
          <div ref="pieRef" class="chart-flex"></div>
        </div>
      </n-grid-item>
    </n-grid>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { NIcon } from 'naive-ui'
import { ServerOutline, CheckmarkCircleOutline, TimeOutline, LayersOutline, PulseOutline, AlertCircleOutline } from '@vicons/ionicons5'
import * as echarts from 'echarts'
import api from '../api'

const summary = ref({
  assetTotal: 0, assetConfirmed: 0, assetPending: 0,
  serviceTotal: 0, aliveTotal: 0, violationTotal: 0,
  newAlive: [], newService: []
})
const pieRef = ref(null)
const trendRef = ref(null)
const trend = ref([])
const trendRange = ref('7d')

const cards = computed(() => [
  { label: 'Web 资产', value: summary.value.assetTotal, color: '#f59e0b', bg: 'rgba(245,158,11,0.12)', border: 'rgba(245,158,11,0.3)', icon: ServerOutline },
  { label: '已登记资产', value: summary.value.assetConfirmed, color: '#84cc16', bg: 'rgba(132,204,22,0.12)', border: 'rgba(132,204,22,0.3)', icon: CheckmarkCircleOutline },
  { label: '待确认资产', value: summary.value.assetPending, color: '#fb923c', bg: 'rgba(251,146,60,0.12)', border: 'rgba(251,146,60,0.3)', icon: TimeOutline },
  { label: '导入资产', value: summary.value.importedTotal, color: '#eab308', bg: 'rgba(234,179,8,0.12)', border: 'rgba(234,179,8,0.3)', icon: LayersOutline },
  { label: '运行中任务', value: summary.value.runningTasks, color: '#06b6d4', bg: 'rgba(6,182,212,0.12)', border: 'rgba(6,182,212,0.3)', icon: PulseOutline },
  { label: '合规违规', value: summary.value.violationTotal, color: '#ef4444', bg: 'rgba(239,68,68,0.12)', border: 'rgba(239,68,68,0.3)', icon: AlertCircleOutline }
])

let pieChart = null
let trendChart = null
let dashTimer = null

async function loadAll() {
  try {
    summary.value = await api.get('/dashboard/summary')
    renderPie()
    trend.value = await api.get(`/dashboard/trend?range=${trendRange.value}`)
    renderTrend()
  } catch (e) { console.error(e) }
}

function onResize() {
  pieChart?.resize()
  trendChart?.resize()
}

onMounted(() => {
  loadAll()
  // 每 30 秒刷新一次，让首页数据跟随资产/任务变动
  dashTimer = setInterval(loadAll, 30000)
  window.addEventListener('resize', onResize)
})

onBeforeUnmount(() => {
  if (dashTimer) clearInterval(dashTimer)
  window.removeEventListener('resize', onResize)
  pieChart?.dispose()
  trendChart?.dispose()
})

const axisColor = '#4b5a72'
const splitColor = 'rgba(148,163,184,0.10)'

function renderPie() {
  if (!pieRef.value) return
  if (!pieChart) pieChart = echarts.init(pieRef.value)
  const chart = pieChart
  chart.setOption({
    tooltip: { trigger: 'item', backgroundColor: 'rgba(15,23,42,0.9)', borderColor: 'rgba(148,163,184,0.2)', textStyle: { color: '#e2e8f0' } },
    legend: { bottom: 0, textStyle: { color: '#8892a6' }, itemWidth: 9, itemHeight: 9 },
    series: [{
      type: 'pie',
      radius: ['52%', '74%'],
      center: ['50%', '44%'],
      itemStyle: { borderRadius: 8, borderColor: 'rgba(7,11,20,0.9)', borderWidth: 3 },
      label: { show: false },
      emphasis: { scale: true, scaleSize: 6 },
      data: [
        { value: summary.value.assetConfirmed, name: '已登记', itemStyle: { color: '#84cc16' } },
        { value: summary.value.assetPending, name: '待确认', itemStyle: { color: '#fb923c' } },
        { value: summary.value.violationTotal, name: '违规', itemStyle: { color: '#ef4444' } }
      ]
    }]
  }, true)
}

function renderTrend() {
  if (!trendRef.value) return
  if (!trendChart) trendChart = echarts.init(trendRef.value)
  const chart = trendChart
  const mk = (name, key, color, withArea) => ({
    name, type: 'line', smooth: 0.4, symbol: 'circle', symbolSize: 6,
    data: trend.value.map(p => p[key] || 0),
    lineStyle: { width: 2.4, color },
    itemStyle: { color, borderColor: '#0b1020', borderWidth: 1.5 },
    areaStyle: withArea ? {
      color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
        { offset: 0, color: 'rgba(59,130,246,0.26)' },
        { offset: 1, color: 'rgba(59,130,246,0.01)' }
      ])
    } : undefined
  })
  chart.setOption({
    tooltip: { trigger: 'axis', backgroundColor: 'rgba(15,23,42,0.9)', borderColor: 'rgba(148,163,184,0.2)', textStyle: { color: '#e2e8f0' } },
    legend: { bottom: 0, textStyle: { color: '#8892a6' }, itemWidth: 12, itemHeight: 8 },
    grid: { left: 44, right: 24, top: 24, bottom: 54 },
    xAxis: {
      type: 'category',
      boundaryGap: false,
      data: trend.value.map(p => p.time),
      axisLine: { lineStyle: { color: axisColor } },
      axisTick: { show: false },
      axisLabel: { color: '#64748b' }
    },
    yAxis: {
      type: 'value',
      minInterval: 1,
      splitLine: { lineStyle: { color: splitColor } },
      axisLabel: { color: '#64748b' }
    },
    series: [
      mk('Web资产', 'webAssets', '#f59e0b', true),
      mk('已登记', 'assetConfirmed', '#84cc16', false),
      mk('待确认', 'assetPending', '#fb923c', false),
      mk('导入资产', 'importedTotal', '#eab308', false),
      mk('违规', 'violations', '#ef4444', false)
    ]
  })
}
</script>

<style scoped>
.panel-fixed { height: 340px; display: flex; flex-direction: column; overflow: hidden; }
.panel-fixed .panel-body { flex: 1; overflow-y: auto; }
.chart-flex { flex: 1; width: 100%; min-height: 240px; }

.dash { animation: rise 0.4s cubic-bezier(0.4, 0, 0.2, 1); }
@keyframes rise { from { opacity: 0; transform: translateY(10px); } to { opacity: 1; transform: none; } }

.stat-card {
  position: relative;
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 16px 18px;
  border-radius: 16px;
  background: var(--bg-panel);
  border: 1px solid rgba(148, 163, 184, 0.12);
  backdrop-filter: blur(16px);
  -webkit-backdrop-filter: blur(16px);
  overflow: hidden;
  transition: transform 0.25s cubic-bezier(0.4, 0, 0.2, 1), border-color 0.25s, box-shadow 0.25s;
}
.stat-card:hover {
  transform: translateY(-3px);
  border-color: rgba(251, 191, 36, 0.35);
  box-shadow: 0 14px 40px rgba(0, 0, 0, 0.32);
}
.stat-glow {
  position: absolute;
  width: 120px; height: 120px;
  right: -40px; top: -50px;
  border-radius: 50%;
  filter: blur(46px);
  opacity: 0.18;
  pointer-events: none;
}
.stat-icon {
  width: 44px; height: 44px;
  border-radius: 13px;
  display: flex; align-items: center; justify-content: center;
  border: 1px solid;
  flex-shrink: 0;
}
.stat-label { font-size: 12px; color: var(--text-muted); letter-spacing: 0.4px; }
.stat-value {
  font-size: 26px;
  font-weight: 700;
  line-height: 1.15;
  -webkit-background-clip: text;
  background-clip: text;
  -webkit-text-fill-color: transparent;
}

.panel {
  border-radius: 16px;
  background: var(--bg-panel);
  border: 1px solid rgba(148, 163, 184, 0.12);
  backdrop-filter: blur(16px);
  -webkit-backdrop-filter: blur(16px);
  padding: 14px 18px 8px;
  transition: border-color 0.25s;
}
.panel:hover { border-color: rgba(251, 191, 36, 0.28); }
.panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding-bottom: 10px;
}
.panel-title { font-size: 14px; font-weight: 600; color: var(--text-main); letter-spacing: 0.3px; display: flex; align-items: center; gap: 7px; }
.panel-sub { font-size: 12px; color: var(--text-muted); }
.panel-body { padding: 8px 0 14px; min-height: 120px; }
.chart { height: 280px; }
.chart-tall { height: 250px; }

.dot { width: 7px; height: 7px; border-radius: 50%; display: inline-block; }
.dot.live { background: #34d399; box-shadow: 0 0 0 3px rgba(52, 211, 153, 0.16); animation: pulse 1.6s infinite; }
.dot.new { background: #a78bfa; box-shadow: 0 0 0 3px rgba(167, 139, 250, 0.16); }
@keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.4; } }

.tag-list { display: flex; flex-wrap: wrap; gap: 8px; }
.ip-chip {
  font-family: 'JetBrains Mono', Consolas, monospace;
  font-size: 12px;
  padding: 4px 10px;
  border-radius: 8px;
  color: #6ee7b7;
  background: rgba(52, 211, 153, 0.10);
  border: 1px solid rgba(52, 211, 153, 0.28);
  transition: all 0.2s;
}
.ip-chip:hover { background: rgba(52, 211, 153, 0.18); transform: translateY(-1px); }

.svc-list { display: flex; flex-direction: column; gap: 6px; }
.svc-row {
  display: flex; align-items: center; gap: 4px;
  padding: 6px 10px;
  border-radius: 8px;
  background: rgba(30, 41, 59, 0.35);
  border: 1px solid transparent;
  font-size: 12.5px;
  transition: all 0.2s;
}
.svc-row:hover { background: rgba(245, 158, 11, 0.10); border-color: rgba(251, 191, 36, 0.25); }
.svc-ip { font-family: 'JetBrains Mono', Consolas, monospace; color: #fcd34d; }
.svc-port { font-family: 'JetBrains Mono', Consolas, monospace; color: #a78bfa; }
.svc-name { margin-left: auto; color: #8892a6; }
</style>
