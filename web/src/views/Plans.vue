<template>
  <div>
    <n-card>
      <n-tabs type="line" animated>
        <!-- 任务 tab -->
        <n-tab-pane name="tasks" tab="扫描任务">
          <div class="toolbar">
            <n-button type="primary" @click="openCreate">下发扫描任务</n-button>
            <n-button secondary @click="openTerminal()">
              <template #icon><n-icon><TerminalOutline /></n-icon></template>
              扫描引擎终端输出
            </n-button>
            <n-button secondary @click="selectAll">全选全部</n-button>
            <ExportButton type="tasks" title="扫描任务" :columns="exportCols" />
            <n-button type="error" :disabled="checkedRows.length === 0" @click="batchDelete">
              批量删除{{ checkedRows.length ? `(${checkedRows.length})` : '' }}
            </n-button>
            <span class="running-hint" v-if="runningTask">
              正在执行：<b>{{ runningTask.name }}</b>（#{{ runningTask.id }}）
            </span>
            <!-- 服务识别进度（v0.0.52）：扫描完的端口会被送进 AI 识别队列，
                 这里显示积压与预计耗时——识别不再由用户在「应用服务」里手动触发。 -->
            <n-tag v-if="aiStats && (aiStats.backlog > 0 || aiStats.queueLen > 0)" round type="info"
              :title="aiStatsTitle" style="margin-left:auto">
              服务识别：待识别 {{ aiStats.backlog }} 条<template v-if="aiStats.perMinute > 0">
                · {{ aiStats.perMinute }} 条/分 · 约 {{ aiEtaText }}</template>
            </n-tag>
          </div>
          <n-data-table :columns="columns" :data="items" :loading="loading" :pagination="{ ...pagination, showSizePicker: true, pageSizes: [10, 20, 50, 100], onUpdatePageSize: onPlanSize }"
            :row-key="rowKey" checkable @update:checked-row-keys="onChecked" remote @update:page="onPage" />
        </n-tab-pane>

        <!-- 模板 tab -->
        <n-tab-pane name="templates" tab="扫描模板">
          <div class="toolbar">
            <n-button type="primary" @click="openTplModal()">新建模板</n-button>
          </div>
          <n-data-table :columns="tplColumns" :data="templates" size="small" />
        </n-tab-pane>

        <!-- 定时任务 tab -->
        <n-tab-pane name="scheduled" tab="定时任务">
          <ScheduledTasks />
        </n-tab-pane>
      </n-tabs>
    </n-card>

    <!-- 创建任务弹窗 -->
    <n-modal v-model:show="showCreate" preset="card" title="下发扫描任务" style="width: 700px">
      <n-form label-placement="left" label-width="100">
        <n-form-item label="任务名称">
          <n-input v-model:value="form.name" placeholder="如 10.24 段巡检" />
        </n-form-item>

        <n-form-item label="扫描模板">
          <n-select v-model:value="form.templateId" clearable filterable :options="templateOptions"
            placeholder="可选，选后自动套用参数（可再手动改）" @update:value="applyTemplate" />
        </n-form-item>

        <n-form-item label="扫描模块 -m">
          <n-select v-model:value="form.modules" multiple :options="moduleOptions" placeholder="默认端口扫描" />
        </n-form-item>

        <n-form-item label="扫描范围">
          <div class="range-box">
            <div class="range-line">
              <n-select v-model:value="form.segments" multiple filterable :options="segmentOptions"
                placeholder="① 选择网段（可选，多选）" style="flex: 1" />
              <n-button size="tiny" @click="form.segments = segmentOptions.map(o => o.value)">全选</n-button>
              <n-button size="tiny" quaternary @click="form.segments = []">清空</n-button>
            </div>
            <div class="range-line">
              <n-select v-model:value="form.assets" multiple filterable :options="assetOptions"
                placeholder="② 选择资产（可选，多选）" style="flex: 1" />
              <n-button size="tiny" @click="form.assets = assetOptions.map(o => o.value)">全选</n-button>
              <n-button size="tiny" quaternary @click="form.assets = []">清空</n-button>
            </div>
            <div class="range-line">
              <n-input v-model:value="form.manual" type="textarea" :rows="2"
                placeholder="③ 手动补充（可选）：IP / CIDR / 范围，逗号或换行分隔" style="flex: 1" />
            </div>
            <div class="range-tip">三者可组合，平台自动展开去重并去除免扫描清单，通过 -hf 主机列表文件传给引擎。</div>
          </div>
        </n-form-item>

        <n-form-item label="执行节点">
          <div style="display:flex;gap:8px;width:100%">
            <n-select v-model:value="form.nodeIds" multiple filterable :options="nodeOptions"
              placeholder="默认本机执行；可多选节点" style="flex:1" />
            <n-button size="tiny" @click="form.nodeIds = ['all']">全部节点</n-button>
            <n-button size="tiny" quaternary @click="form.nodeIds = []">清空</n-button>
          </div>
        </n-form-item>

        <n-form-item label="端口策略">
          <n-radio-group v-model:value="form.portMode">
            <n-radio value="top100">Top100</n-radio>
            <n-radio value="all">全端口 1-65535</n-radio>
            <n-radio value="custom">自定义端口</n-radio>
          </n-radio-group>
        </n-form-item>
        <n-form-item label="超时 -time（毫秒）">
          <n-input-number v-model:value="form.timeout" :min="50" :max="30000" :step="100" style="width: 140px" />
          <span class="switch-tip">单探针等待时长，只由任务决定（节点不再有此配置）。跨网段/专线延迟高时调大，否则开着的端口会被判成关闭</span>
        </n-form-item>
        <n-form-item v-if="form.portMode === 'custom'" label="端口">
          <n-input v-model:value="form.ports" placeholder="如 22,80,443,3306,6379" />
        </n-form-item>
        <n-form-item label="指纹策略 -finger">
          <n-select v-model:value="form.finger" :options="fingerOptions" style="width: 140px" />
        </n-form-item>
        <n-form-item label="存活探测">
          <n-switch v-model:value="form.aliveProbe" />
          <span class="switch-tip">开启后先探测目标存活，只扫描存活 IP</span>
        </n-form-item>
        <n-form-item label="定时执行">
          <n-space align="center">
            <n-select v-model:value="cronInterval" :options="intervalOptions" style="width: 140px" />
            <n-select v-if="cronInterval" v-model:value="cronHour" :options="hourOptions" style="width: 110px" />
            <span class="switch-tip">{{ cronInterval ? '每天/每N天在 ' + String(cronHour).padStart(2, '0') + ':00 自动执行' : '留空=仅手动执行' }}</span>
          </n-space>
        </n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showCreate = false">取消</n-button>
          <n-button :loading="creating" @click="create(false)">创建</n-button>
          <n-button type="primary" :loading="creating" @click="create(true)">创建并执行</n-button>
        </n-space>
      </template>
    </n-modal>

    <!-- 任务配置 / 编辑弹窗 -->
    <n-modal v-model:show="showConfig" preset="card"
      :title="canEdit ? `编辑任务 · ${configTask?.name || ''}` : `任务配置 · ${configTask?.name || ''}`" style="width: 660px">
      <!-- 执行中：只读 -->
      <div v-if="!canEdit && configTask" class="cfg-grid">
        <div class="cfg-item"><span>任务 ID</span><b>{{ configTask.id }}</b></div>
        <div class="cfg-item"><span>状态</span><b>{{ statusMap[configTask.status]?.text || configTask.status }}</b></div>
        <div class="cfg-item wide"><span>扫描范围</span><b>{{ configTask.ipRanges }}</b></div>
        <div class="cfg-item"><span>扫描模块</span><b>{{ configTask.modules || 'port' }}</b></div>
        <div class="cfg-item"><span>端口策略</span><b>{{ configTask.portMode === 'top100' ? 'Top100' : configTask.portMode === 'all' ? '全端口' : (configTask.ports || '-') }}</b></div>
        <div class="cfg-item"><span>扫描速率</span><b>自动</b></div>
        <div class="cfg-item"><span>指纹策略</span><b>{{ configTask.finger || '-' }}</b></div>
        <div class="cfg-item"><span>定时</span><b>{{ cronText(configTask.cron) }}</b></div>
        <div class="cfg-item wide"><span>执行节点</span><b>{{ configTask.nodeIds === 'all' ? '全部节点' : configTask.nodeIds ? `节点 ${configTask.nodeIds}` : '本机' }}</b></div>
        <div class="cfg-item wide"><span>报告文件</span><b>{{ configTask.reportPath || '尚未生成' }}</b></div>
        <div class="cfg-item wide"><span class="edit-warn">任务执行中不可修改；停止或完成后可点击任务名编辑。</span></div>
      </div>

      <!-- 非执行中：可编辑 -->
      <n-form v-else label-placement="left" label-width="90">
        <n-form-item label="任务名称"><n-input v-model:value="editForm.name" /></n-form-item>
        <n-form-item label="扫描范围">
          <div class="range-box">
            <div class="range-line">
              <n-select v-model:value="editForm.segments" multiple filterable :options="segmentOptions"
                placeholder="① 选择网段（可选，多选）" style="flex: 1" />
              <n-button size="tiny" @click="editForm.segments = segmentOptions.map(o => o.value)">全选</n-button>
              <n-button size="tiny" quaternary @click="editForm.segments = []">清空</n-button>
            </div>
            <div class="range-line">
              <n-select v-model:value="editForm.assets" multiple filterable :options="assetOptions"
                placeholder="② 选择资产（可选，多选）" style="flex: 1" />
              <n-button size="tiny" @click="editForm.assets = assetOptions.map(o => o.value)">全选</n-button>
              <n-button size="tiny" quaternary @click="editForm.assets = []">清空</n-button>
            </div>
            <div class="range-line">
              <n-input v-model:value="editForm.manual" type="textarea" :rows="2"
                placeholder="③ 手动补充（可选）：IP / CIDR / 范围" style="flex: 1" />
            </div>
          </div>
        </n-form-item>
        <n-form-item label="扫描模块">
          <n-select v-model:value="editForm.modules" multiple :options="moduleOptions" />
        </n-form-item>
        <n-form-item label="端口策略">
          <n-radio-group v-model:value="editForm.portMode">
            <n-radio value="top100">Top100</n-radio>
            <n-radio value="all">全端口</n-radio>
            <n-radio value="custom">自定义</n-radio>
          </n-radio-group>
        </n-form-item>
        <n-form-item v-if="editForm.portMode === 'custom'" label="端口">
          <n-input v-model:value="editForm.ports" placeholder="如 22,80,443" />
        </n-form-item>
        <n-form-item label="指纹策略">
          <n-select v-model:value="editForm.finger" :options="fingerOptions" style="width: 160px" />
        </n-form-item>
        <n-form-item label="执行节点">
          <n-select v-model:value="editForm.nodeIds" multiple filterable :options="editNodeOptions"
            placeholder="默认本机执行" />
        </n-form-item>
        <n-form-item label="定时">
          <n-space align="center">
            <n-select v-model:value="editCronInterval" :options="intervalOptions" style="width: 140px" />
            <n-select v-if="editCronInterval" v-model:value="editCronHour" :options="hourOptions" style="width: 110px" />
            <span class="switch-tip">
              {{ editCronInterval ? (editCronInterval === '1' ? '每天 ' : '每 ' + editCronInterval + ' 天 ') + String(editCronHour).padStart(2, '0') + ':00 自动执行' : '留空=仅手动执行' }}
            </span>
          </n-space>
        </n-form-item>
      </n-form>

      <template #footer>
        <n-space justify="end">
          <n-button v-if="!canEdit" @click="showConfig = false">关闭</n-button>
          <template v-else>
            <n-button @click="showConfig = false">取消</n-button>
            <n-button type="primary" :loading="savingCfg" @click="saveTaskCfg">保存修改</n-button>
          </template>
        </n-space>
      </template>
    </n-modal>

    <!-- 节点进度弹窗 -->
    <n-modal v-model:show="showNodeProg" preset="card" :title="`节点进度 · ${nodeProgTask?.name || ''}`"
      style="width: 800px" @after-leave="closeNodeProg">
      <div v-if="nodeSummary.totalIPs > 0" class="sum-box">
        <n-progress type="line" :percentage="nodeSummary.pct" :height="16"
          :color="nodeSummary.pct >= 100 ? '#34d399' : '#f59e0b'" />
        <div class="sum-meta">
          <span>总进度 <b>{{ nodeSummary.doneIPs }}</b> / {{ nodeSummary.totalIPs }} 端口（{{ nodeSummary.pct }}%）</span>
          <span v-if="nodeSummary.eta != null">预计剩余 <b>{{ fmtEta(nodeSummary.eta) }}</b></span>
        </div>
      </div>
      <n-empty v-if="nodeProgList.length === 0" description="该任务未分配到节点，未走分片（请检查执行节点是否在线）" />
      <n-data-table v-else :columns="nodeProgCols" :data="nodeProgList" size="small"
        :pagination="{ pageSize: 20, showSizePicker: true, pageSizes: [10, 20, 50, 100] }" />
    </n-modal>

    <!-- 节点终端 -->
    <n-modal :show="showNodeTerm" preset="card" :title="`节点终端 · ${nodeTermName}（每 2 秒刷新）`"
      style="width: 760px" @update:show="v => { if (!v) closeNodeTerminal() }">
      <pre class="node-term" ref="nodeTermEl">{{ nodeTermLines.join('\n') }}</pre>
    </n-modal>

    <!-- 诊断记录 -->
    <n-modal v-model:show="showDiagLog" preset="card"
      :title="`诊断记录 · 分片 #${diagRec.id || ''}（${diagRec.node || ''}）`" style="width: 780px">
      <div class="diag-grid">
        <div class="diag-row"><span class="k">分片状态</span>
          <span>{{ nodeStatusText[diagRec.status] || diagRec.status || '-' }}</span></div>
        <div class="diag-row"><span class="k">诊断结论</span>
          <span :style="{ color: diagRec.verdict === 'done' ? '#16a34a' : diagRec.verdict === 'stalled' ? '#dc2626' : '#6b7280' }">
            {{ diagVerdictText(diagRec.verdict) }}</span></div>
        <div class="diag-row"><span class="k">诊断时间</span>
          <span>{{ diagRec.at ? new Date(diagRec.at).toLocaleString('zh-CN') : '未诊断过' }}</span></div>
        <div class="diag-row"><span class="k">扫描进度</span>
          <span>{{ (diagRec.scanned || 0).toLocaleString() }} / {{ (diagRec.total || 0).toLocaleString() }}
            （{{ diagRec.total ? Math.round((diagRec.scanned || 0) * 100 / diagRec.total) : 0 }}%）</span></div>
        <div class="diag-row"><span class="k">诊断说明</span><span>{{ diagRec.note || '—' }}</span></div>
      </div>
      <div class="diag-logs-title">该分片的监控与诊断日志</div>
      <pre class="node-term">{{ diagLogs.length ? diagLogs.join('\n') : '暂无记录（该分片从未触发过扫描监控或手动诊断）' }}</pre>
    </n-modal>

    <!-- 模板弹窗 -->
    <n-modal v-model:show="showTpl" preset="card" title="扫描模板" style="width: 600px">
      <n-form label-placement="left" label-width="100">
        <n-form-item label="模板名称"><n-input v-model:value="tplForm.name" placeholder="如 全端口高速扫描" /></n-form-item>
        <n-form-item label="扫描模块 -m">
          <n-select v-model:value="tplForm.modules" multiple :options="moduleOptions" />
        </n-form-item>
        <n-form-item label="端口策略">
          <n-radio-group v-model:value="tplForm.portMode">
            <n-radio value="top100">Top100</n-radio>
            <n-radio value="all">全端口 1-65535</n-radio>
            <n-radio value="custom">自定义端口</n-radio>
          </n-radio-group>
        </n-form-item>
        <n-form-item v-if="tplForm.portMode === 'custom'" label="端口">
          <n-input v-model:value="tplForm.ports" placeholder="如 22,80,443,3306,6379" />
        </n-form-item>
        <n-form-item label="并发 -t"><n-input-number v-model:value="tplForm.thread" :min="1" :max="5000" style="width: 160px" /><span class="switch-tip">智能调速开启时以本机自动值为准，这里只是下限</span></n-form-item>
        <n-form-item label="超时 -time（毫秒）"><n-input-number v-model:value="tplForm.timeout" :min="50" :max="30000" :step="100" style="width: 160px" /></n-form-item>
        <n-form-item label="指纹策略 -finger">
          <n-select v-model:value="tplForm.finger" :options="fingerOptions" style="width: 140px" />
        </n-form-item>
        <n-form-item label="额外参数">
          <n-input v-model:value="tplForm.extraArgs" placeholder="自由拼接参数，如 -pa 8080,9000" />
        </n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showTpl = false">取消</n-button>
          <n-button type="primary" @click="saveTpl">保存</n-button>
        </n-space>
      </template>
    </n-modal>

    <!-- 终端弹窗 -->
    <n-modal v-model:show="showLog" preset="card" :title="`扫描引擎终端输出${logTaskName ? ' — ' + logTaskName : ''}`" style="width: 860px">
      <!-- 队列概览条：区分「存活探测中」与「扫描中」两个阶段 -->
      <div v-if="logQueue" class="queue-bar">
        <n-tag size="small" :bordered="false" :type="logQueue.phase === 'probing' ? 'info' : 'success'">
          {{ logQueue.phase === 'probing' ? '存活探测' : '扫描' }}
        </n-tag>
        <template v-if="logQueue.phase === 'probing'">
          <span class="q-item">已探测 <b>{{ fmtNum(logTask?.probeOffset) }}</b> / {{ fmtNum(logTask?.totalIps) }}<template v-if="logQueue.percent >= 0">（{{ logQueue.percent }}%）</template></span>
          <span class="q-item q-done">存活 <b>{{ fmtNum(logTask?.aliveCount) }}</b></span>
          <span v-if="logQueue.pps > 0" class="q-item">{{ fmtNum(Math.round(logQueue.pps)) }} 台/秒</span>
        </template>
        <template v-else>
          <span class="q-item q-done">已完成 <b>{{ fmtNum(logQueue.done) }}</b> / {{ fmtNum(logQueue.total) }}</span>
          <span class="q-item q-run">进行中 <b>{{ logQueue.leased }}</b></span>
          <span class="q-item">待领取 <b>{{ fmtNum(logQueue.pending) }}</b></span>
          <span v-if="logQueue.failed > 0" class="q-item q-fail">隔离 <b>{{ logQueue.failed }}</b></span>
          <span class="q-item q-probe">探针 <b>{{ fmtNum(logQueue.probesDone) }}</b> / {{ fmtNum(logQueue.probesTotal) }}</span>
        </template>
        <span v-if="logTask?.error" class="q-error">{{ logTask.error }}</span>
      </div>
      <!-- 调速条：速率由节点按机器规格与运行状态自适应，界面只保留暂停/继续 -->
      <div class="ctrl-bar">
        <n-tag :type="logTaskPaused ? 'warning' : 'success'" size="small" :bordered="false">
          {{ logTaskPaused ? '已暂停' : '执行中' }}
        </n-tag>
        <n-button size="small" :type="logTaskPaused ? 'primary' : 'warning'"
          :disabled="!logTaskRunning" :loading="ctrlBusy" @click="toggleScanPause">
          {{ logTaskPaused ? '继续扫描' : '暂停扫描' }}
        </n-button>
        <span class="muted" style="font-size: 12px">
          <template v-if="logQueue && logQueue.phase === 'probing'">
            探测目标存活中<template v-if="logQueue.etaSeconds >= 0">，{{ etaText(logQueue.etaSeconds, logQueue.etaConfidence) }}</template>
          </template>
          <template v-else-if="logTask">
            {{ logTask.percent }}%｜{{ etaText(logTask.etaSeconds, logTask.etaConfidence, logTask.status === 'paused') }}
            <template v-if="logTask.pps > 0">｜{{ fmtNum(Math.round(logTask.pps)) }} 探针/秒</template>
            ｜速率自动调节
          </template>
        </span>
      </div>
      <!-- 识别状态条（v0.0.57）：识别是独立于扫描的第二条流水线，
           进度用单独一行表示，不再写进终端日志——日志是事件流，状态每 30 秒
           刷一行会把真正的扫描输出顶掉，用户在滚动里也不容易读到。
           数据来自 /api/services/ai-stats（弹窗打开时 3 秒一刷）。 -->
      <div v-if="aiStats" class="queue-bar ai-bar">
        <n-tag size="small" :bordered="false" type="warning">识别日志</n-tag>
        <template v-if="aiStats.backlog > 0 || aiStats.pendingRender > 0 || aiStats.rendering > 0 || aiStats.inLLM > 0">
          <span class="q-item">待识别 <b>{{ fmtNum(aiStats.backlog) }}</b></span>
          <span class="q-item">待渲染 <b>{{ fmtNum(aiStats.pendingRender) }}</b></span>
          <span class="q-item q-run">渲染中 <b>{{ aiStats.rendering }}</b></span>
          <span class="q-item q-done">模型在途 <b>{{ aiStats.inLLM }}</b></span>
        </template>
        <span v-else class="q-item q-done"><b>队列空闲</b>（无待识别条目）</span>
        <span class="q-item">并发 <b>{{ aiStats.workers }}</b></span>
        <span v-if="aiStats.llmFail > 0" class="q-item q-fail">模型失败 <b>{{ aiStats.llmFail }}</b></span>
        <span class="q-item q-probe">预计时间 <b>{{ aiEtaLongText }}</b></span>
      </div>
      <div class="terminal" ref="terminalEl">
        <div v-for="(line, i) in logLines" :key="i" class="log-line">{{ line }}</div>
        <div v-if="progressLine" class="log-line progress-line">{{ progressLine }}</div>
        <div v-if="logLines.length === 0 && !progressLine" class="log-empty">暂无输出</div>
      </div>
    </n-modal>
  </div>
</template>

<script setup>
import { ref, h, onMounted, onBeforeUnmount, computed, watch } from 'vue'
import { NTag, NButton, NPopconfirm, NIcon, NProgress, useMessage } from 'naive-ui'
import { TerminalOutline } from '@vicons/ionicons5'
import api from '../api'
import ExportButton from '../components/ExportButton.vue'
import ScheduledTasks from './ScheduledTasks.vue'

// 导出列


const message = useMessage()
const loading = ref(false)
const items = ref([])
const page = ref(1)
const pageSize = ref(20)
const pagination = ref({ page: 1, pageSize: 20, itemCount: 0 })
const showCreate = ref(false)
const creating = ref(false)
const engineOptions = [
  { label: '内置引擎', value: 'builtin' },
]
const form = ref({ name: '', templateId: null, modules: ['port', 'url'], engine: 'builtin', batchSize: 100, segments: [], assets: [], manual: '', portMode: 'top100', ports: '', thread: 600, timeout: 1500, finger: 'tiny', noPing: false, noSmart: false, aliveProbe: true, extraArgs: '', cron: '', nodeIds: [] })
const nodes = ref([])
const nodeOptions = computed(() => [
  { label: '全部节点', value: 'all' },
  ...nodes.value.map(n => ({ label: `${n.name}（${n.ip} · 并发${n.concurrency}）`, value: n.id }))
])
const segmentOptions = ref([])
const assetOptions = ref([])
const templateOptions = ref([])
const templates = ref([])

const showTpl = ref(false)
const tplForm = ref({ id: 0, name: '', modules: ['port'], engine: 'builtin', portMode: 'top100', ports: '', thread: 600, timeout: 1500, finger: 'tiny', noPing: false, noSmart: false, extraArgs: '' })

const showLog = ref(false)
const logLines = ref([])
const progressLine = ref('')
const logTaskId = ref(null)
const logTaskName = ref('')
const terminalEl = ref(null)

const moduleOptions = [
  { label: '端口扫描 port', value: 'port' },
  { label: 'Web指纹 url', value: 'url' },
  { label: 'POC验证 poc', value: 'poc' },
  { label: '弱口令 crack', value: 'crack' },
  { label: '目录扫描 dir', value: 'dir' },
  { label: 'JS敏感信息 js', value: 'js' },
  { label: '子域名 domain', value: 'domain' },
  { label: '空间测绘 cyber', value: 'cyber' }
]
const fingerOptions = [
  { label: 'min（最小）', value: 'min' },
  { label: 'tiny（精简）', value: 'tiny' },
  { label: 'all（全量）', value: 'all' }
]

const runningTask = computed(() => items.value.find(t => t.status === 'running'))

// 简单定时：每 X 天 + 小时
const cronInterval = ref('')
const cronHour = ref(2)
const intervalOptions = [
  { label: '不启用', value: '' },
  { label: '每天', value: '1' },
  { label: '每 3 天', value: '3' },
  { label: '每 7 天', value: '7' },
  { label: '每 30 天', value: '30' }
]
const hourOptions = Array.from({ length: 24 }, (_, i) => ({ label: `${String(i).padStart(2, '0')}:00`, value: i }))
function buildCron() {
  if (!cronInterval.value) return ''
  if (cronInterval.value === '1') return `0 ${cronHour.value} * * *`
  return `0 ${cronHour.value} */${cronInterval.value} * *`
}

const statusMap = {
  pending: { text: '待执行', type: 'default' },
  stopped: { text: '已停止', type: 'warning' },
  running: { text: '执行中', type: 'info' },
  paused: { text: '已暂停', type: 'warning' },
  done: { text: '已完成', type: 'success' },
  interrupted: { text: '已中断', type: 'warning' },
  failed: { text: '失败', type: 'error' }
}

const rowKey = (row) => row.id

const columns = [
  { type: 'selection' },
  { title: 'ID', key: 'id', width: 60 },
  {
    title: '名称', key: 'name',
    render: row => h(NButton, { text: true, type: 'primary', onClick: () => openConfig(row) }, { default: () => row.name })
  },
  { title: '状态', key: 'status', width: 90, render: row => h(NTag, { type: statusMap[row.status]?.type || 'default', size: 'small' }, { default: () => statusMap[row.status]?.text || row.status }) },
  {
    title: '进度', key: 'progress', width: 170,
    render: row => {
      if (row.status !== 'running' && row.status !== 'paused') {
        return row.status === 'done' ? h(NProgress, { type: 'line', percentage: 100, height: 8, showIndicator: false, color: '#34d399' }) : '-'
      }
      // 存活探测阶段：还没有扫描单元，单独显示"探测了多少 IP / 命中多少"
      if (row.queue && row.queue.phase === 'probing') {
        const total = row.totalIps || 0
        const done = row.probeOffset || 0
        const pct = total > 0 ? Math.min(99, Math.floor(done * 100 / total)) : 0
        return h('div', {}, [
          h(NProgress, { type: 'line', percentage: pct, height: 8, showIndicator: false, color: '#60a5fa' }),
          h('div', { style: 'font-size:11px;color:#6b7280;margin-top:2px' },
            `存活探测 ${fmtNum(done)}/${fmtNum(total)}｜存活 ${fmtNum(row.aliveCount || 0)}`),
          h('div', { style: 'font-size:10.5px;color:#9ca3af;margin-top:1px' },
            etaText(row.etaSeconds, row.etaConfidence, row.status === 'paused'))
        ])
      }
      if (row.percent >= 0) {
        const kids = [h('span', {}, `${row.percent}%`)]
        if (row.queue) {
          const q = row.queue
          const uniTotal = q.total || (q.done + q.pending + q.leased + q.failed)
          kids.push(h('span', { style: 'font-size:10.5px;color:#9ca3af' },
            `IP ${fmtNum(q.done)}/${fmtNum(uniTotal)}${q.failed ? `（隔离${q.failed}）` : ''}`))
        }
        // 剩余时间直接挂在进度条下面：不用点开详情就能判断"还要不要等"
        const etaLine = etaText(row.etaSeconds, row.etaConfidence, row.status === 'paused')
        return h('div', {}, [
          h(NProgress, { type: 'line', percentage: row.percent, height: 8, indicatorPlacement: 'inside',
            color: row.status === 'paused' ? '#8b5cf6' : '#f59e0b' }),
          h('div', { style: 'display:flex;justify-content:space-between;font-size:11px;color:#6b7280;margin-top:2px' }, kids),
          h('div', { style: 'font-size:10.5px;color:#9ca3af;margin-top:1px' }, etaLine)
        ])
      }

      return h(NProgress, { type: 'line', processing: true, percentage: 40, height: 8, showIndicator: false })
    }
  },
  { title: '定时', key: 'cron', width: 120, render: row => cronText(row.cron) },
  {
    title: '分配', key: 'agentTotal', width: 170,
    render: row => {
      if (!row.agentTotal) {
        return h('span', { style: 'font-size:12px;color:#6b7280' }, row.nodeIds ? '待分配' : '本机')
      }
      const done = row.agentDone || 0
      const all = row.agentTotal
      const color = done === all ? '#34d399' : '#5eead4'
      const label = row.queueMode ? `IP 单元 ${done}/${all}` : `${done}/${all} 分片`
      return h('div', { style: 'font-size:11.5px;line-height:1.5' }, [
        h('div', { style: `color:${color}` }, label),
        h('div', { style: 'color:#9ca3af;font-size:10.5px;white-space:normal' }, row.agentNodes || '')
      ])
    }
  },
  {
    title: '预计剩余', key: 'etaSeconds', width: 110,
    render: row => {
      if (row.status === 'running' && row.etaSeconds > 0) {
        return h('span', { style: 'color:#f59e0b;font-size:12px' }, `预计 ${fmtEta(row.etaSeconds)}`)
      }
      if (row.status === 'running' && row.etaSeconds === -1) {
        return h('span', { style: 'color:#8b5cf6;font-size:12px' }, '已暂停')
      }
      if (row.status === 'paused') {
        return h('span', { style: 'color:#8b5cf6;font-size:12px' }, '已暂停')
      }
      return h('span', { style: 'color:#9ca3af' }, '-')
    }
  },
  { title: '创建时间', key: 'createdAt', width: 150, render: r => fmt(r.createdAt) },
  { title: '操作', key: 'actions', width: 420, render: row => h('div', { style: 'display:flex;gap:8px;flex-wrap:wrap' }, [
    // 编辑：执行中/暂停的任务只读，其余状态都可改
    row.status !== 'running' && row.status !== 'paused'
      ? h(NButton, { size: 'small', text: true, type: 'info', onClick: () => openConfig(row) }, { default: () => '编辑' })
      : null,
    // 运行中：可暂停、停止
    row.status === 'running'
      ? h(NButton, { size: 'small', type: 'warning', text: true, onClick: () => pauseTask(row) }, { default: () => '暂停' })
      : null,
    row.status === 'running'
      ? h(NButton, { size: 'small', type: 'error', text: true, onClick: () => stopTask(row) }, { default: () => '停止' })
      : null,
    // 暂停中：可继续、停止
    row.status === 'paused'
      ? h(NButton, { size: 'small', type: 'primary', text: true, onClick: () => resumeTask(row) }, { default: () => '继续' })
      : null,
    row.status === 'paused'
      ? h(NButton, { size: 'small', type: 'error', text: true, onClick: () => stopTask(row) }, { default: () => '停止' })
      : null,
    // 已结束/待执行：可执行、重跑
    row.status !== 'running' && row.status !== 'paused'
      ? h(NButton, { size: 'small', type: 'primary', text: true, onClick: () => run(row) }, { default: () => '执行' })
      : null,
    row.status === 'stopped' || row.status === 'done' || row.status === 'failed' || row.status === 'interrupted'
      ? h(NButton, { size: 'small', type: 'warning', text: true, onClick: () => restartTask(row) }, { default: () => '重跑' })
      : null,
    h(NButton, { size: 'small', text: true, onClick: () => openLog(row) }, { default: () => '日志' }),
    h(NButton, { size: 'small', text: true, onClick: () => openNodeProgress(row) }, { default: () => '节点进度' }),
    h(NPopconfirm, { onPositiveClick: () => del(row.id) }, {
      trigger: () => h(NButton, { size: 'small', text: true, type: 'error' }, { default: () => '删除' }),
      // 执行中/已暂停的任务删除前会先停扫描（服务端走取消广播，等节点松手再删行），文案要说清
      default: () => (row.status === 'running' || row.status === 'paused')
        ? '该任务正在执行，删除会先停止扫描（已扫到的结果一并删除）。确认删除？'
        : '确认删除？'
    })
  ]) },
  // 概览：本次扫描的 IP/存活/应用/违规（放在「节点进度」右侧）
  {
    title: '概览', key: 'overview', width: 240,
    render: row => {
      const o = row.overview || {}
      const line = { style: 'font-size:12px;line-height:1.7;white-space:nowrap' }
      return h('div', {}, [
        h('div', line, `扫描 IP ${o.totalIps || 0} ｜ 存活 ${o.aliveIps || 0}`),
        h('div', line, `应用 ${o.appsFound || 0} 个（新发现 ${o.appsNew || 0}）`),
        h('div', line, `违规 ${o.violations || 0} 个（新增 ${o.violationsNew || 0}）`)
      ])
    }
  }
]

// 导出列 = 表格所有列（排除操作列）
const exportCols = computed(() =>
  columns
    .filter(c => c.key && !['actions', 'op', 'a'].includes(c.key))
    .map(c => ({ key: c.key, label: c.title }))
)

// cron 转人类可读
function cronText(cron) {
  if (!cron) return '手动'
  const m = cron.match(/^0 (\d+) \*\/(\d+) \* \*$/)
  if (m) return `每 ${m[2]} 天 ${String(m[1]).padStart(2, '0')}:00`
  const d = cron.match(/^0 (\d+) \* \* \*$/)
  if (d) return `每天 ${String(d[1]).padStart(2, '0')}:00`
  return cron
}

const tplColumns = [
  { title: '模板名', key: 'name' },
  { title: '模块', key: 'modules', width: 150, render: r => r.modules || 'port' },
  { title: '端口', key: 'ports', width: 150, render: r => r.portMode === 'top100' ? 'Top100' : r.portMode === 'all' ? '全端口' : (r.ports || '-') },
  { title: '并发', key: 'thread', width: 70 },
  { title: '指纹', key: 'finger', width: 80 },
  { title: '操作', key: 'actions', width: 140, render: row => h('div', { style: 'display:flex;gap:4px' }, [
    h(NButton, { size: 'small', text: true, type: 'primary', onClick: () => openTplModal(row) }, { default: () => '编辑' }),
    h(NPopconfirm, { onPositiveClick: () => delTpl(row.id) }, { trigger: () => h(NButton, { size: 'small', text: true, type: 'error' }, { default: () => '删除' }), default: () => '确认删除？' })
  ]) }
]

function fmt(t) { return t ? new Date(t).toLocaleString('zh-CN') : '-' }

async function load() {
  const res = await api.get('/tasks', { params: { page: page.value, size: 20 } })
  items.value = res.items
  pagination.value = { page: page.value, pageSize: pageSize.value, itemCount: res.total }
}
function onPage(p) { page.value = p; load() }

const checkedRows = ref([])
function onChecked(keys) { checkedRows.value = keys }

// 全选全部任务（跨分页）
async function selectAll() {
  const res = await api.get('/tasks', { params: { page: 1, size: 5000 } })
  checkedRows.value = res.items.map(t => t.id)
  message.success(`已全选 ${checkedRows.value.length} 个任务`)
}

async function batchDelete() {
  if (checkedRows.value.length === 0) { message.warning('请先选择任务'); return }
  try {
    await api.post('/tasks/batch-delete', { ids: checkedRows.value })
    message.success('已删除（含报告文件）')
    checkedRows.value = []
    load()
  } catch (e) { message.error(e.response?.data?.error || '删除失败') }
}

async function loadTemplates() { templates.value = await api.get('/templates') }

// 扫描范围的候选数据（网段 / 资产）。
// 新建和编辑都要加载，否则直接点「编辑」时下拉是空的
async function loadScopeOptions() {
  try {
    const segs = await api.get('/segments')
    segmentOptions.value = (segs || []).map(s => ({ label: `${s.name}（${s.cidr}）`, value: s.cidr }))
  } catch (e) { console.error('[loadScopeOptions] segments:', e) }
  try {
    const assets = await api.get('/assets', { params: { page: 1, size: 500 } })
    const items = assets?.items || []
    assetOptions.value = items.map(a => ({ label: a.systemName ? `${a.ip} - ${a.systemName}` : a.ip, value: a.ip }))
  } catch (e) { console.error('[loadScopeOptions] assets:', e) }
}

async function openCreate() {
  showCreate.value = true
  await loadScopeOptions()
  try {
    await loadTemplates()
    templateOptions.value = templates.value.map(t => ({ label: t.name, value: t.id }))
  } catch (e) { console.error(e) }
}

function applyTemplate(id) {
  if (!id) return
  const t = templates.value.find(x => x.id === id)
  if (!t) return
  form.value.modules = t.modules ? t.modules.split(',') : ['port']
  form.value.portMode = t.portMode || 'top100'
  form.value.ports = t.ports || ''
  form.value.thread = t.thread || 600
  form.value.timeout = t.timeout || 1500
  form.value.finger = t.finger || 'tiny'
  form.value.noPing = !!t.noPing
  form.value.extraArgs = t.extraArgs || ''
}

async function create(run) {
  if (!form.value.name) { message.warning('任务名必填'); return }
  const parts = [...form.value.segments, ...form.value.assets]
  const manual = form.value.manual.split(/[\n,，;]+/).map(s => s.trim()).filter(Boolean)
  parts.push(...manual)
  if (parts.length === 0) { message.warning('请至少指定扫描范围'); return }

  creating.value = true
  try {
    await api.post('/tasks', {
      name: form.value.name,
      ipRanges: parts.join(','),
      modules: form.value.modules.join(','),
      engine: form.value.engine,
      portMode: form.value.portMode,
      ports: form.value.ports,
      batchSize: form.value.batchSize,
      thread: form.value.thread,
      timeout: form.value.timeout,
      finger: form.value.finger,
      noPing: form.value.noPing,
      aliveProbe: form.value.aliveProbe,
      extraArgs: form.value.extraArgs,
      templateId: form.value.templateId,
      cron: buildCron(),
      nodeIds: form.value.nodeIds.includes('all') ? 'all' : (form.value.nodeIds.length ? form.value.nodeIds.join(',') : ''),
      run: !!run
    })
    message.success(run ? '任务已创建并下发执行' : '任务已创建（待执行）')
    showCreate.value = false
    cronInterval.value = ''
    cronHour.value = 2
    form.value = { name: '', templateId: null, modules: ['port', 'url'], engine: 'builtin', batchSize: 100, segments: [], assets: [], manual: '', portMode: 'top100', ports: '', thread: 600, timeout: 1500, finger: 'tiny', noPing: false, noSmart: false, aliveProbe: true, extraArgs: '', cron: '', nodeIds: [] }
    load()
  } catch (e) { message.error(e.response?.data?.error || '创建失败') } finally { creating.value = false }
}

async function run(row) {
  await api.post(`/tasks/${row.id}/run`)
  message.success('任务已提交执行')
  load()
}
async function del(id) { await api.delete(`/tasks/${id}`); message.success('已删除'); load() }

async function pauseTask(row) {
  try {
    await api.post(`/tasks/${row.id}/pause`)
    message.success('已暂停，未完成的 IP 已收回待扫池')
    load()
  } catch (e) { message.error(e.response?.data?.error || '暂停失败') }
}

async function resumeTask(row) {
  try {
    await api.post(`/tasks/${row.id}/resume`)
    message.success('已继续')
    load()
  } catch (e) { message.error(e.response?.data?.error || '继续失败') }
}

async function restartTask(row) {
  try {
    await api.post(`/tasks/${row.id}/restart`)
    message.success('已重跑，探测进度清零')
    load()
  } catch (e) { message.error(e.response?.data?.error || '重跑失败') }
}

async function stopTask(row) {
  try {
    await api.post(`/tasks/${row.id}/stop`)
    message.success('已停止')
    load()
  } catch (e) {
    message.error(e.response?.data?.error || '停止失败')
  }
}

function openTplModal(row) {
  if (row) {
    tplForm.value = { ...row, modules: row.modules ? row.modules.split(',') : ['port'] }
  } else {
    tplForm.value = { id: 0, name: '', modules: ['port'], portMode: 'top100', ports: '', thread: 600, timeout: 1500, finger: 'tiny', noPing: false, noSmart: false, extraArgs: '' }
  }
  showTpl.value = true
}
async function saveTpl() {
  if (!tplForm.value.name) { message.warning('模板名必填'); return }
  await api.post('/templates', { ...tplForm.value, modules: tplForm.value.modules.join(',') })
  message.success('已保存')
  showTpl.value = false
  loadTemplates()
}
async function delTpl(id) { await api.delete(`/templates/${id}`); message.success('已删除'); loadTemplates() }

function openLog(row) {
  logTaskId.value = row.id
  logTaskName.value = row.name
  logLines.value = []
  showLog.value = true
  startSSE(row.id)
}

// ===== 实时调控（暂停/继续/动态调速）：POST /tasks/:id/control =====
const ctrlBusy = ref(false)
const ctrlState = ref('none')
const logTaskPaused = computed(() => ctrlState.value === 'pause')
const logTask = computed(() => items.value.find(t => t.id === logTaskId.value))
const logTaskRunning = computed(() => logTask.value && (logTask.value.status === 'running' || logTask.value.status === 'paused'))
// 队列概览（列表接口注入的 queue 字段；非队列任务为 null 不显示概览条）
const logQueue = computed(() => logTask.value?.queue || null)

// etaText 把秒数换成"剩余 x"。
// -1 有两种含义：任务暂停（显示"已暂停"）、样本不足（显示"估算中"），由 conf/状态区分。
// 超过一天不封顶，按天/小时给，否则慢任务会显示一个骗人的"1 天"。
function etaText(sec, conf, paused) {
  if (paused) return '已暂停'
  // 低置信度时不给数字：刚开扫那一两个窗口的速率还在爬坡，
  // 拿它算出来的"剩余 3 分 17 秒"比不显示更容易误导人。
  if (conf === 'low') return '估算中…'
  if (sec == null || sec < 0) {
    return '剩余时间计算中'
  }
  if (sec === 0) return '即将完成'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const ss = sec % 60
  if (d > 0) return `剩余约 ${d} 天 ${h} 小时`
  if (h > 0) return `剩余 ${h} 小时 ${m} 分`
  if (m > 0) return `剩余 ${m} 分 ${ss} 秒`
  return `剩余 ${ss} 秒`
}

function fmtNum(n) {
  if (n == null) return '-'
  if (n >= 100000000) return (n / 100000000).toFixed(2) + '亿'
  if (n >= 10000) return (n / 10000).toFixed(1) + '万'
  return String(n)
}

async function sendControl(body, okMsg) {
  if (!logTaskId.value) return false
  ctrlBusy.value = true
  try {
    const res = await api.post(`/tasks/${logTaskId.value}/control`, body)
    const cmd = res?.cmd || {}
    ctrlState.value = cmd.paused ? 'pause' : (cmd.action || 'none')
    message.success(okMsg)
    load()
    return true
  } catch (e) {
    message.error(e.response?.data?.error || '调控失败')
    return false
  } finally {
    ctrlBusy.value = false
  }
}
function toggleScanPause() {
  if (logTaskPaused.value) sendControl({ action: 'resume' }, '已继续，下个心跳周期恢复扫描')
  else sendControl({ action: 'pause' }, '已暂停，在途探针自然结束后进度冻结')
}
// SSE 进度行同步暂停态（调度级暂停：status=paused 时按钮也要变成"继续"）
watch(logTask, (t) => {
  if (t && t.status === 'paused') ctrlState.value = 'pause'
  else if (t && t.status === 'running' && ctrlState.value === 'pause' && !ctrlBusy.value) {
    // 服务端调度恢复但执行层暂停态未知，保守保持
  }
})
function openTerminal() {
  if (runningTask.value) openLog(runningTask.value)
  else if (items.value.length > 0) openLog(items.value[0])
  else message.warning('暂无任务')
}

let eventSource = null
function startSSE(taskId) {
  stopSSE()
  logLines.value = []
  progressLine.value = ''
  const token = localStorage.getItem('token') || ''
  eventSource = new EventSource(`/api/tasks/${taskId}/log/stream?token=${encodeURIComponent(token)}`)
  eventSource.addEventListener('line', (e) => {
    logLines.value.push(e.data)
    if (logLines.value.length > 3000) logLines.value.splice(0, 1500)
    if (terminalEl.value) terminalEl.value.scrollTop = terminalEl.value.scrollHeight
  })
  eventSource.addEventListener('progress', (e) => {
    progressLine.value = e.data
    if (terminalEl.value) terminalEl.value.scrollTop = terminalEl.value.scrollHeight
  })
}
function stopSSE() {
  if (eventSource) { eventSource.close(); eventSource = null }
}
watch(showLog, (v) => { if (!v) stopSSE() })

let aiStatsTimer = null
let pollTimer = null
// ===== 分布式：节点 / 配置 / 进度 / 终端 =====
const showConfig = ref(false)
const configTask = ref(null)
const canEdit = ref(false)
const savingCfg = ref(false)
const editForm = ref({ name: '', ipRanges: '', segments: [], assets: [], manual: '', modules: [], engine: 'builtin', portMode: 'top100', ports: '', batchSize: 100, thread: 600, timeout: 1500, finger: 'tiny', nodeIds: [], cron: '' })

// 编辑时的节点选项（含「全部节点」）
const editNodeOptions = computed(() => [
  { label: '全部节点（按并发分片）', value: 'all' },
  ...nodeOptions.value
])

// 编辑弹窗的定时快捷选择
const editCronInterval = ref('')
const editCronHour = ref(2)

function parseCronExpr(cron) {
  if (!cron) return ['', 2]
  const m = String(cron).trim().match(/^0\s+(\d{1,2})\s+(?:\*\/(\d+)|\*)\s+\*\s+\*$/)
  if (!m) return ['', 2]
  return [m[2] || '1', parseInt(m[1], 10)]
}

function buildEditCron() {
  if (!editCronInterval.value) return ''
  if (editCronInterval.value === '1') return `0 ${editCronHour.value} * * *`
  return `0 ${editCronHour.value} */${editCronInterval.value} * *`
}

// 把 IP 范围字符串按 , ， 换行 ; ； 空白 拆成数组
function toArr(v) {
  return String(v || '').split(/[,，\n;；\s]+/).map(x => x.trim()).filter(Boolean)
}

// 把已有 ipRanges 拆回 网段 / 资产 / 手动 三段
function splitIpRanges(ipRanges) {
  const segSet = new Set(segmentOptions.value.map(o => o.value))
  const assetSet = new Set(assetOptions.value.map(o => o.value))
  const out = { segments: [], assets: [], manual: [] }
  for (const p of toArr(ipRanges)) {
    if (segSet.has(p)) out.segments.push(p)
    else if (assetSet.has(p)) out.assets.push(p)
    else out.manual.push(p)
  }
  return out
}

// 三段合并回逗号分隔字符串
function mergeIpRanges(f) {
  return [
    ...toArr((f.segments || []).join(',')),
    ...toArr((f.assets || []).join(',')),
    ...toArr(f.manual)
  ].join(',')
}

async function openConfig(row) {
  try {
    configTask.value = row
    canEdit.value = row.status !== 'running'
    const ids = row.nodeIds === 'all' ? ['all'] : (row.nodeIds ? String(row.nodeIds).split(',').filter(Boolean) : [])
    const split = splitIpRanges(row.ipRanges || '')
  editForm.value = {
    name: row.name || '',
    ipRanges: row.ipRanges || '',
    segments: split.segments,
    assets: split.assets,
    manual: split.manual.join(', '),
    modules: row.modules ? row.modules.split(',') : ['port'],
    portMode: row.portMode || 'top100',
    ports: row.ports || '',
    batchSize: row.batchSize || 100,
    thread: row.thread || 600,
    timeout: row.timeout || 1500,
    finger: row.finger || 'tiny',
    nodeIds: ids,
    cron: row.cron || ''
  }
    const [iv, hr] = parseCronExpr(row.cron || '')
    editCronInterval.value = iv
    editCronHour.value = hr
    // 编辑弹窗同样需要网段/资产候选，否则下拉为空
    await loadScopeOptions()
    showConfig.value = true
  } catch (e) {
    console.error('[openConfig] 打开编辑弹窗失败:', e)
    message.error('打开编辑失败：' + (e?.message || e))
  }
}

async function saveTaskCfg() {
  if (!editForm.value.name) { message.warning('任务名必填'); return }
  if (!mergeIpRanges(editForm.value).trim()) { message.warning('扫描范围必填'); return }
  savingCfg.value = true
  try {
    await api.put(`/tasks/${configTask.value.id}`, {
      name: editForm.value.name,
      ipRanges: mergeIpRanges(editForm.value),
      modules: editForm.value.modules.join(','),
      engine: editForm.value.engine,
      portMode: editForm.value.portMode,
      ports: editForm.value.ports,
      batchSize: editForm.value.batchSize,
      thread: editForm.value.thread,
      timeout: editForm.value.timeout,
      finger: editForm.value.finger,
      cron: buildEditCron(),
      nodeIds: editForm.value.nodeIds.join(',')
    })
    message.success('已保存修改')
    showConfig.value = false
    load()
  } catch (e) {
    message.error(e.response?.data?.error || '保存失败')
  } finally {
    savingCfg.value = false
  }
}

const showNodeProg = ref(false)
const nodeProgTask = ref(null)
const nodeProgList = ref([])
let nodeProgTimer = null

async function loadNodes() {
  try { nodes.value = await api.get('/nodes') } catch { /* ignore */ }
}

async function openNodeProgress(row) {
  nodeProgTask.value = row
  showNodeProg.value = true
  await loadNodeProgress()
  if (nodeProgTimer) clearInterval(nodeProgTimer)
  nodeProgTimer = setInterval(loadNodeProgress, 3000)
}
async function loadNodeProgress() {
  if (!nodeProgTask.value) return
  try { nodeProgList.value = await api.get(`/tasks/${nodeProgTask.value.id}/agent-tasks`) } catch { /* ignore */ }
}
function closeNodeProg() {
  if (nodeProgTimer) { clearInterval(nodeProgTimer); nodeProgTimer = null }
}
async function cancelNodeTask(at) {
  try {
    await api.post(`/agent-tasks/${at.id}/cancel`)
    message.success('已取消，IP 已重新分配给其他节点')
    loadNodeProgress()
  } catch (e) { message.error(e.response?.data?.error || '操作失败') }
}

const showNodeTerm = ref(false)
const nodeTermLines = ref([])
const nodeTermName = ref('')
let termTimer = null
let termNodeId = 0

async function fetchTerminal() {
  // 按分片取回显：节点日志是累积的（混着几百个分片），分片日志才是这一次的输出
  if (!termShardId) return
  try {
    const res = await api.get(`/agent-tasks/${termShardId}/log`)
    nodeTermName.value = res.node || ''
    nodeTermLines.value = String(res.log || '').split('\n').filter(x => x.trim() !== '')
  } catch {}
}

async function stopNodeTask(r) {
  try {
    await api.post(`/agent-tasks/${r.id}/stop`)
    message.success(`已停止 ${r.nodeName} 的分片`)
    loadNodeProgress()
  } catch (e) {
    message.error(e.response?.data?.error || '停止失败')
  }
}

const nodeSummary = computed(() => {
  // 排除 cancelled：故障转移后原分片仍留在列表里，会重复计入总端口
  const list = (nodeProgList.value || []).filter(t => (t.total || 0) > 0 && t.status !== 'cancelled')
  if (!list.length) return { pct: 0, totalIPs: 0, doneIPs: 0, eta: null }
  const totalIPs = list.reduce((s, t) => s + (t.total || 0), 0)
  const doneIPs = list.reduce((s, t) => s + (t.scanned || 0), 0)
  const pct = totalIPs ? Math.min(100, Math.round(doneIPs * 100 / totalIPs)) : 0
  // 直接用 TscanClient 进度条里的「剩余时间」，取最慢的节点
  const etas = list.map(t => t.etaSeconds || 0).filter(v => v > 0)
  const eta = etas.length ? Math.max(...etas) : null
  return { pct, totalIPs, doneIPs, eta }
})

function fmtEta(sec) {
  if (sec == null || sec <= 0) return '-'
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const sec2 = Math.round(sec % 60)
  if (h) return `${h} 小时 ${m} 分`
  if (m) return `${m} 分 ${sec2} 秒`
  return `${sec2} 秒`
}

let termShardId = 0


async function openNodeTerminal(at) {
  termNodeId = at.nodeId
  termShardId = at.id
  await fetchTerminal()
  showNodeTerm.value = true
  if (termTimer) clearInterval(termTimer)
  termTimer = setInterval(fetchTerminal, 2000)
}

function closeNodeTerminal() {
  if (termTimer) { clearInterval(termTimer); termTimer = null }
  showNodeTerm.value = false
}

const aiDiagnosing = ref(0)

// ===== 诊断记录 =====
// 诊断结果一直写在 agent_tasks.ai_verdict / ai_verdict_note / ai_verdict_at，
// 但界面只弹一个 toast，事后完全查不到。这里给个入口把结论 + 相关日志都列出来。
const showDiagLog = ref(false)
const diagRec = ref({})
const diagLogs = ref([])

function diagVerdictText(v) {
  if (v === 'done') return '已完成（AI 收尾，日志显示跑完但没收到结束信号）'
  if (v === 'stalled') return '卡死中断（无完成迹象，IP 已收回重派）'
  return '未诊断'
}

async function openDiagLog(r) {
  diagRec.value = { ...r }
  diagLogs.value = []
  showDiagLog.value = true
  try {
    const res = await api.get(`/agent-tasks/${r.id}/diagnose-log`)
    diagRec.value = res
    diagLogs.value = (res.logs || []).map(x => x.line)
  } catch (e) {
    message.error(e.response?.data?.error || '读取诊断记录失败')
  }
}

async function aiDiagnose(row) {
  aiDiagnosing.value = row.id
  try {
    const r = await api.post(`/agent-tasks/${row.id}/ai-diagnose`)
    message.success(`AI 诊断：${r.label}` + (r.note ? `（${r.note}）` : ''))
    openNodeProgress(nodeProgTask.value)
  } catch (e) {
    message.error(e.response?.data?.error || 'AI 诊断失败')
  } finally {
    aiDiagnosing.value = 0
  }
}

const nodeStatusText = { pending: '待执行', running: '执行中', done: '完成', done_ai: 'AI完成', stalled_ai: 'AI中断', failed: '失败', cancelled: '已取消' }
const nodeProgCols = [
  { title: '节点', key: 'nodeName', width: 120 },
  {
    title: '状态', key: 'status', width: 90,
    render: r => h(NTag, {
      size: 'small',
      type: r.status === 'done' ? 'success'
        : r.status === 'done_ai' ? 'info'
        : r.status === 'stalled_ai' ? 'error'
        : r.status === 'failed' ? 'error'
        : r.status === 'running' ? 'info' : 'default'
    }, { default: () => nodeStatusText[r.status] || r.status })
  },
  {
    title: '进度', key: 'progress', width: 190,
    render: r => h(NProgress, { type: 'line', percentage: r.progress || 0, height: 8,
      color: (r.status === 'done' || r.status === 'done_ai') ? '#84cc16' : r.status === 'failed' || r.status === 'stalled_ai' ? '#ef4444' : '#f59e0b' })
  },
  { title: '总端口数', key: 'total', width: 110, render: r => (r.total || 0).toLocaleString() },
  { title: '已扫端口数', key: 'scanned', width: 120, render: r => (r.scanned || 0).toLocaleString() },
  {
    title: '操作', key: 'a', width: 250,
    render: r => h('div', { style: 'display:flex;gap:6px;flex-wrap:wrap' }, [
      h(NButton, {
        size: 'small', text: true, type: 'error',
        disabled: r.status !== 'running' && r.status !== 'pending',
        onClick: () => stopNodeTask(r)
      }, { default: () => '停止' }),
      h(NButton, {
        size: 'small', text: true, type: 'warning',
        disabled: r.status === 'done' || r.status === 'cancelled',
        onClick: () => cancelNodeTask(r)
      }, { default: () => '故障转移' }),
      h(NButton, { size: 'small', text: true, onClick: () => openNodeTerminal(r) }, { default: () => '终端' }),
      h(NButton, {
        size: 'small', text: true, type: 'primary',
        loading: aiDiagnosing === r.id,
        disabled: r.status !== 'running' && r.status !== 'pending',
        onClick: () => aiDiagnose(r)
      }, { default: () => 'AI诊断' }),
      h(NButton, {
        size: 'small', text: true, type: 'info',
        onClick: () => openDiagLog(r)
      }, { default: () => '诊断记录' })
    ])
  }
]

// 识别队列快照（/services/ai-stats），与任务列表一起轮询但节奏更慢（15s）
const aiStats = ref(null)
const aiEtaText = computed(() => {
  const sec = aiStats.value?.etaSeconds || 0
  if (sec <= 0) return '—'
  if (sec < 60) return sec + ' 秒'
  if (sec < 3600) return Math.round(sec / 60) + ' 分钟'
  return (sec / 3600).toFixed(1) + ' 小时'
})
// aiEtaLongText 识别剩余时间，按「x时xx分xx秒」给出。
// 与扫描的 etaText 分开：那个带置信度（低置信显示"估算中"），这里只服务识别队列。
const aiEtaLongText = computed(() => {
  const st = aiStats.value
  if (!st) return '估算中'
  const sec = st.etaSeconds
  if (sec === undefined || sec === null || sec < 0) {
    return st.backlog > 0 ? '估算中' : '剩余 0'
  }
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s2 = sec % 60
  const pad = (n) => String(n).padStart(2, '0')
  return h > 0 ? `${h}时${pad(m)}分${pad(s2)}秒` : `${m}分${pad(s2)}秒`
})

const aiStatsTitle = computed(() => {
  const st = aiStats.value
  if (!st) return ''
  return `识别并发 ${st.workers} 路 · 队列 ${st.queueLen}/${st.queueCap}\n`
    + `单条平均 ${(st.avgTotalMs / 1000).toFixed(1)}s（抓包 ${st.avgTraceMs}ms / 研判 ${st.avgJudgeMs}ms / 渲染 ${st.avgRenderMs}ms / 识别 ${st.avgIdentifyMs}ms）\n`
    + `走了渲染 ${st.renderUsed} 条 · 渲染闸门跳过 ${st.renderSkipped} 条 · 模型失败 ${st.llmFail} 次`
})
async function loadAIStats() {
  try { aiStats.value = await api.get('/services/ai-stats') } catch { /* 不影响任务列表 */ }
}

// 弹窗打开时把识别状态刷新提到 3 秒：终端里那一行要"看着在动"，
// 15 秒的列表级刷新放在弹窗里会让人觉得卡住了。关闭后降回 15 秒，避免无谓请求。
watch(showLog, (v) => {
  if (aiStatsTimer) { clearInterval(aiStatsTimer); aiStatsTimer = null }
  if (v) {
    loadAIStats()
    aiStatsTimer = setInterval(loadAIStats, 3000)
  } else {
    aiStatsTimer = setInterval(loadAIStats, 15000)
  }
})

onMounted(() => {
  load()
  loadTemplates()
  loadNodes()
  loadAIStats()
  pollTimer = setInterval(load, 3000)
  aiStatsTimer = setInterval(loadAIStats, 15000)
})
onBeforeUnmount(() => { clearInterval(pollTimer); clearInterval(aiStatsTimer); stopSSE() })
</script>

<style scoped>
.sum-box { margin-bottom: 14px; }
.sum-meta { display: flex; justify-content: space-between; font-size: 12.5px; color: #6b7280; margin-top: 6px; }
.sum-meta b { color: #111827; }

.range-line { display: flex; gap: 6px; align-items: flex-start; margin-bottom: 8px; }

.toolbar { display: flex; gap: 12px; align-items: center; margin-bottom: 16px; flex-wrap: wrap; }
.running-hint { color: #fbbf24; font-size: 13px; margin-left: 8px; }
.range-box { width: 100%; }
.range-tip { color: #94a3b8; font-size: 12px; margin-top: 8px; }
.cfg-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px 20px; }
.cfg-item { display: flex; flex-direction: column; gap: 2px; }
.cfg-item.wide { grid-column: span 2; }
.cfg-item span { font-size: 12px; color: var(--text-muted); }
.cfg-item b { font-size: 13.5px; color: var(--text-main); font-weight: 500; word-break: break-all; }
.edit-warn { color: #fbbf24; font-size: 12px; }
.node-term {
  margin: 0; padding: 14px 16px; border-radius: 10px;
  background: #050507; color: #d8d2c4; font-size: 12.5px; line-height: 1.9;
  font-family: 'JetBrains Mono', Consolas, monospace; white-space: pre-wrap;
  max-height: 420px; overflow: auto;
}

.diag-grid { display: flex; flex-direction: column; gap: 8px; margin-bottom: 14px; }
.diag-row { display: flex; gap: 10px; font-size: 13px; line-height: 1.6; }
.diag-row .k { flex: 0 0 72px; color: #6b7280; }
.diag-logs-title { font-size: 12.5px; color: #6b7280; margin-bottom: 6px; }
.switch-tip { color: #94a3b8; font-size: 12px; margin-left: 8px; }
.queue-bar {
  display: flex; align-items: center; gap: 14px; flex-wrap: wrap;
  padding: 7px 10px; margin-bottom: 8px; border-radius: 8px;
  background: #10161f; border: 1px solid #1f2a3a; font-size: 12.5px; color: #8b949e;
}
.queue-bar b { color: #e5e7eb; font-weight: 600; }
.queue-bar .q-run b { color: #f59e0b; }
.queue-bar .q-done b { color: #34d399; }
.queue-bar .q-fail b { color: #ef4444; }
.queue-bar .q-probe { margin-left: auto; }
.queue-bar .q-error { color: #f87171; font-size: 12px; }
/* 识别条与扫描条并列但语义不同：扫描条用蓝/绿，识别条用琥珀色左边框区分 */
.ai-bar { border-left: 3px solid #f59e0b; }
.ctrl-bar {
  display: flex; align-items: center; gap: 10px; flex-wrap: wrap;
  padding: 8px 10px; margin-bottom: 10px; border-radius: 8px;
  background: #161b22; border: 1px solid #21262d;
}
.ctrl-bar .ctrl-label { color: #8b949e; font-size: 12px; }
.ctrl-bar .muted { color: #8b949e; }
.terminal {
  background: #0d1117;
  color: #c9d1d9;
  font-family: 'Consolas', 'Courier New', monospace;
  font-size: 12px;
  padding: 12px;
  border-radius: 8px;
  height: 480px;
  overflow-y: auto;
  white-space: pre-wrap;
  word-break: break-all;
}
.log-line { line-height: 1.5; }
.progress-line { color: #fbbf24; }
.log-empty { color: #6e7681; }
</style>
