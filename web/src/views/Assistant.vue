<template>
  <div class="assistant-page">
    <!-- 左侧会话列表 -->
    <div class="sidebar" :class="{ collapsed: sidebarCollapsed }">
      <div class="sidebar-head">
        <n-button v-if="!sidebarCollapsed" type="primary" block @click="newConversation">+ 新对话</n-button>
        <n-button v-else type="primary" circle size="small" @click="newConversation">+</n-button>
        <!-- 用原生 input：n-input 的主题变量靠 class 透传，scoped 样式匹配不到它的根元素，
             在深色主题下会变成 rgba(255,255,255,.1) 的深色控件，放在浅色侧栏上等于隐形。 -->
        <input
          v-if="!sidebarCollapsed"
          v-model="searchKw"
          class="conv-search"
          type="search"
          placeholder="搜索对话…"
        />
      </div>

      <div v-if="!sidebarCollapsed" class="conv-list">
        <n-empty v-if="searchKw ? searchResults.length === 0 : tree.length === 0"
                 :description="searchKw ? '没有匹配的记录' : '暂无历史记录'"
                 size="small" style="margin-top:40px" />

        <!-- 搜索：扁平结果，每条就是一个「一问一答」，点开直接跳到命中位置 -->
        <template v-if="searchKw">
          <div v-for="c in searchResults" :key="c.id" class="conv-item" @click="openRecord(c)">
            <div class="conv-title">{{ c.title || '未命名' }}</div>
            <div class="conv-meta">
              <span class="kind-chip" :style="{ color: kindColor(c.kind) }">{{ c.label }}</span>
              <span v-if="c.taskId">任务 #{{ c.taskId }}</span>
              <span>{{ formatTime(c.updatedAt) }}</span>
              <span v-if="c.hitSnippet" class="conv-hit-tag">命中</span>
            </div>
            <div v-if="c.hitSnippet" class="conv-hit">{{ c.hitSnippet }}</div>
          </div>
        </template>

        <!-- 树：任务 → 类型。类型是叶子，不再挂会话子项 -->
        <template v-else>
          <div v-for="t in tree" :key="t.key" class="tree-task">
            <div class="tree-task-head" @click="toggleTask(t.key)">
              <span class="caret">{{ t.open ? '▾' : '▸' }}</span>
              <span class="tree-task-name" :title="t.label">{{ t.label }}</span>
              <span class="tree-count">{{ t.count }}</span>
            </div>
            <div v-if="t.open" class="tree-kinds">
              <div v-for="k in t.kinds" :key="k.kind"
                   :class="['tree-kind-head', { active: isCurrentGroup(t.taskId, k.kind) }]"
                   @click="openGroup(t.taskId, k.kind, t.taskName)">
                <span class="kind-dot" :style="{ background: kindColor(k.kind) }"></span>
                <span class="tree-kind-name">{{ k.label }}</span>
                <span class="tree-count">{{ k.count }}</span>
              </div>
            </div>
          </div>
        </template>
      </div>

      <div v-if="!sidebarCollapsed" class="sidebar-foot">
        <n-button size="tiny" quaternary block @click="clearAll">清空全部</n-button>
      </div>
    </div>

    <!-- 右侧对话区 -->
    <n-card :bordered="false" class="chat-card">
      <template #header>
        <div class="header">
          <div class="title">
            <n-button size="tiny" quaternary @click="sidebarCollapsed = !sidebarCollapsed">
              {{ sidebarCollapsed ? '»' : '«' }}
            </n-button>
            <span>{{ currentTitle }}</span>
            <n-tag v-if="status.enabled" type="success" size="small" round>{{ status.profileUp }} 个模型</n-tag>
            <n-tag v-else type="warning" size="small" round>未配置</n-tag>
          </div>
          <div class="actions">
            <n-tag size="small" :bordered="false" type="info">
              今日 {{ status.todayCalls || 0 }} 次 / {{ formatTokens(status.todayTokens) }}
            </n-tag>
            <n-button size="small" quaternary @click="showUsage = true">用量</n-button>
            <n-button size="small" quaternary @click="$router.push('/llm-settings')">配置</n-button>
          </div>
        </div>
      </template>

      <n-alert v-if="!status.enabled" type="warning" :bordered="false" style="margin-bottom: 12px">
        LLM 未配置。<n-button text type="primary" @click="$router.push('/llm-settings')">去配置 →</n-button>
      </n-alert>

      <!-- 分类内翻页：一条记录 = 一次问答/识别，不堆在一个会话里 -->
      <div v-if="view.kind" class="rec-nav">
        <span class="rec-nav-label">
          {{ view.label }}
          <span v-if="view.total"> · 第 {{ view.offset + view.idx + 1 }}/{{ view.total }} 条</span>
        </span>
        <span class="rec-nav-title" :title="view.items[view.idx]?.title">{{ view.items[view.idx]?.title || '' }}</span>
        <span class="rec-nav-btns">
          <n-button size="tiny" quaternary :disabled="!canOlder" @click="stepRecord(1)">‹ 更早</n-button>
          <n-button size="tiny" quaternary :disabled="!canNewer" @click="stepRecord(-1)">更新 ›</n-button>
        </span>
      </div>

      <div ref="scrollRef" class="messages">
        <div v-if="messages.length === 0" class="empty">
          <div class="empty-title">可以直接问，也可以让它操作数据</div>
          <div class="samples">
            <div v-for="s in samples" :key="s" class="sample" @click="send(s)">{{ s }}</div>
          </div>
        </div>

        <div v-for="(m, i) in messages" :key="m.id || i" :data-mid="m.id || null"
             :class="['msg', m.role, { 'msg-hit': hitActive && m.id === hitActive }]">
          <div class="bubble">
            <!-- 显示这次回答调用了哪些工具。加「调用」前缀，
                 否则裸工具名（get_dashboard_stats）贴在正文上面，
                 会被读成模型回答的一部分（用户问"只回数字"时尤其明显）。 -->
            <div v-if="m.toolCalls && m.toolCalls.length" class="tools">
              <n-tag v-for="(t, j) in m.toolCalls" :key="j" size="tiny" type="info" round>
                调用 {{ t.tool }}
              </n-tag>
            </div>
            <div v-if="traceOf(m)" style="margin-bottom:6px">
              <n-button size="tiny" secondary type="info" @click="openTrace(traceOf(m))">
                查看请求/响应包（{{ traceOf(m).hops.length }} 跳）
              </n-button>
            </div>
            <div class="text" v-html="renderText(m.content)"></div>
          </div>
        </div>

        <div v-if="loading" class="msg assistant">
          <div class="bubble"><n-spin size="small" /> <span style="margin-left: 8px">思考中…</span></div>
        </div>
      </div>

      <div class="input-area">
        <n-input
          v-model:value="input"
          type="textarea"
          placeholder="问点什么，或让它执行操作（Enter 发送，Shift+Enter 换行）"
          :autosize="{ minRows: 1, maxRows: 5 }"
          :disabled="loading || !status.enabled"
          @keydown="onKeydown"
        />
        <n-button type="primary" :loading="loading" :disabled="!status.enabled || !input.trim()" @click="send()">
          发送
        </n-button>
      </div>
    </n-card>

    <!-- 请求响应包：四栏（最多 4 跳，覆盖 302 与二次跳转） -->
    <n-modal v-model:show="showTrace" preset="card"
      :title="`请求响应包 · ${traceData.target || ''}（${(traceData.hops || []).length} 跳）`"
      style="width: 96vw; max-width: 1500px">
      <div class="trace-grid">
        <div v-for="h in (traceData.hops || []).slice(0, 4)" :key="h.index" class="trace-pane">
          <div class="trace-h">第 {{ h.index }} 跳 · {{ h.responseLine }}</div>
          <div class="trace-label">请求</div>
          <pre class="trace-pre">{{ h.requestLine }}
{{ h.requestHeaders }}</pre>
          <div class="trace-label">响应</div>
          <pre class="trace-pre">{{ h.responseLine }}
{{ h.responseHeaders }}
{{ h.body || h.bodyNote || '（空响应体）' }}</pre>
        </div>
      </div>
    </n-modal>

    <!-- 用量抽屉 -->
    <n-drawer v-model:show="showUsage" :width="560">
      <n-drawer-content title="Token 用量">
        <n-spin :show="usageLoading">
          <div v-if="usage.total" class="usage-summary">
            <div class="stat"><div class="num">{{ usage.total.calls }}</div><div class="lbl">总调用</div></div>
            <div class="stat"><div class="num">{{ formatTokens(usage.total.totalTokens) }}</div><div class="lbl">总 Tokens</div></div>
            <div class="stat"><div class="num">{{ usage.total.errors }}</div><div class="lbl">失败</div></div>
            <div class="stat"><div class="num">{{ usage.total.avgMs }}ms</div><div class="lbl">平均延迟</div></div>
          </div>
          <n-divider title-placement="left" style="margin: 16px 0 8px">按配置</n-divider>
          <n-table :bordered="false" size="small">
            <thead><tr><th>配置</th><th>模型</th><th style="text-align:right">调用</th><th style="text-align:right">Tokens</th><th style="text-align:right">失败</th></tr></thead>
            <tbody>
              <tr v-for="p in usage.byProfile" :key="p.profileName + p.model">
                <td>{{ p.profileName }}</td>
                <td style="font-size:12px;color:#888">{{ p.model }}</td>
                <td style="text-align:right">{{ p.calls }}</td>
                <td style="text-align:right">{{ formatTokens(p.totalTokens) }}</td>
                <td style="text-align:right" :style="{color: p.errors > 0 ? '#d03050' : ''}">{{ p.errors }}</td>
              </tr>
            </tbody>
          </n-table>
        </n-spin>
      </n-drawer-content>
    </n-drawer>
  </div>
</template>

<script setup>
import { ref, onMounted, nextTick, watch, computed } from 'vue'
import {
  NCard, NTag, NButton, NAlert, NInput, NSpin, NDrawer, NDrawerContent,
  NTable, NDivider, NEmpty, useMessage, useDialog
} from 'naive-ui'
import { useRouter } from 'vue-router'
import api from '../api'

const message = useMessage()
const dialog = useDialog()
const router = useRouter()

const input = ref('')
const showTrace = ref(false)
const traceData = ref({ target: '', hops: [] })

// 消息 meta 里可能带 http_trace（完整请求/响应包），前端折叠成按钮
function traceOf(m) {
  if (!m || !m.meta) return null
  try {
    const o = typeof m.meta === 'string' ? JSON.parse(m.meta) : m.meta
    if (o && o.kind === 'http_trace' && o.hops && o.hops.length) return o
  } catch (e) { /* ignore */ }
  return null
}
function openTrace(o) {
  traceData.value = o
  showTrace.value = true
}

const loading = ref(false)
const messages = ref([])
const currentConvId = ref(0)
const scrollRef = ref(null)
const showUsage = ref(false)
const usageLoading = ref(false)
const usage = ref({ total: null, byProfile: [], recent: [] })
const status = ref({ enabled: false, profileUp: 0, todayCalls: 0, todayTokens: 0 })
const sidebarCollapsed = ref(false)

const samples = [
  '现在有多少待处置资产？',
  '列出 198.18.11 网段的资产',
  '最近的任务执行情况怎么样',
  '帮我创建一个扫描 198.18.12.0/24 的任务',
  '哪些资产没有填负责人',
]

const currentTitle = computed(() => {
  const v = view.value
  if (v.items[v.idx] && v.items[v.idx].title) return v.items[v.idx].title
  if (v.label) return v.label
  return 'AI 助手'
})

function formatTokens(n) {
  if (!n) return '0'
  if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M'
  if (n >= 1000) return (n / 1000).toFixed(1) + 'K'
  return String(n)
}

function formatTime(t) {
  if (!t) return ''
  const d = new Date(t)
  const diff = new Date() - d
  if (diff < 60000) return '刚刚'
  if (diff < 3600000) return Math.floor(diff / 60000) + '分钟前'
  if (diff < 86400000) return Math.floor(diff / 3600000) + '小时前'
  if (diff < 604800000) return Math.floor(diff / 86400000) + '天前'
  return `${d.getMonth() + 1}/${d.getDate()}`
}

function escHtml(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}
function fmtText(s) {
  return escHtml(s)
    .replace(/\n/g, '<br/>')
    .replace(/\*\*([^*]+)\*\*/g, '<b>$1</b>')
    .replace(/`([^`]+)`/g, '<code>$1</code>')
}

// 识别记录后半段是完整请求/响应报文：默认折叠，展开能看到逐字节的证据
function renderText(t) {
  if (!t) return ''
  const m = t.match(/\n*={5,}\s*(原始请求[^\n]*|渲染链路明细)\s*={5,}\n?/)
  if (!m) return fmtText(t)
  const i = t.indexOf(m[0])
  const head = t.slice(0, i)
  const raw = t.slice(i + m[0].length)
  const kb = (new Blob([raw]).size / 1024).toFixed(1)
  const title = m[1].replace(/={5,}/g, '').trim()
  return fmtText(head)
    + `<details class="raw-box"><summary>${escHtml(title)} · ${kb} KB（点开查看）</summary>`
    + `<pre class="raw-pre">${escHtml(raw)}</pre></details>`
}

async function scrollBottom() {
  await nextTick()
  if (scrollRef.value) scrollRef.value.scrollTop = scrollRef.value.scrollHeight
}

async function loadStatus() {
  try { status.value = await api.get('/llm/status') } catch { /* ignore */ }
}

// ===== 左侧树：任务 → 类型（类型是叶子）=====
// 一次问答/一次识别 = 一条记录 = 一条会话；类型节点点进去看该类型下的记录，一条条翻。
const KIND_META = {
  chat:       { label: '对话',           color: '#3b82f6' },
  svc_ident:  { label: '服务识别',       color: '#10b981' },
  svc_review: { label: '服务准确性判别', color: '#f59e0b' },
  render:     { label: '渲染复核',       color: '#8b5cf6' },
  task:       { label: '任务会话',       color: '#64748b' },
}
function kindColor(k) { return (KIND_META[k] || {}).color || '#94a3b8' }
function kindLabel(k) { return (KIND_META[k] || {}).label || k }

const groups = ref([])          // 后端聚合：{taskId, taskName, kind, label, count}
const searchResults = ref([])   // 搜索模式下的扁平结果
const searchKw = ref('')
const openTasks = ref({})       // taskKey -> bool，只有显式 true 才展开
const hitActive = ref(0)

// 当前正在浏览的分类与页内位置
const view = ref({ kind: '', label: '', taskId: 0, taskName: '', total: 0, offset: 0, idx: 0, items: [] })

const tree = computed(() => {
  const byTask = new Map()
  for (const g of groups.value) {
    const tid = g.taskId || 0
    if (!byTask.has(tid)) {
      byTask.set(tid, {
        key: 't' + tid, taskId: tid, taskName: g.taskName || '',
        label: tid ? ('任务 #' + tid + (g.taskName ? ' · ' + g.taskName : '')) : '独立对话',
        kinds: [], count: 0,
      })
    }
    const node = byTask.get(tid)
    node.kinds.push({ kind: g.kind, label: g.label || kindLabel(g.kind), count: g.count })
    node.count += g.count
  }
  const order = ['chat', 'svc_ident', 'svc_review', 'render', 'task']
  const out = []
  for (const node of byTask.values()) {
    node.kinds.sort((a, b) => order.indexOf(a.kind) - order.indexOf(b.kind))
    node.open = openTasks.value[node.key] === true
    out.push(node)
  }
  return out.sort((a, b) => (b.taskId ? 1 : 0) - (a.taskId ? 1 : 0) || b.count - a.count)
})

function toggleTask(key) { openTasks.value = { ...openTasks.value, [key]: openTasks.value[key] !== true } }
function isCurrentGroup(taskId, kind) {
  return view.value.kind === kind && view.value.taskId === (taskId || 0)
}

let searchTimer = null
function onSearchInput() {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(() => loadIndex(), 300)
}
watch(searchKw, onSearchInput)

async function loadIndex() {
  try {
    const kw = searchKw.value.trim()
    if (kw) {
      const r = await api.get('/llm/conversations?limit=200&keyword=' + encodeURIComponent(kw))
      searchResults.value = r.items || []
      return
    }
    searchResults.value = []
    const r = await api.get('/llm/conversations')
    groups.value = r.groups || []
    // 保持当前分类所在任务展开
    if (view.value.kind) {
      openTasks.value = { ...openTasks.value, ['t' + (view.value.taskId || 0)]: true }
    }
  } catch { /* ignore */ }
}

// 打开某任务某类型的记录（最新一条）
async function openGroup(taskId, kind, taskName = '') {
  if (loading.value) return
  openTasks.value = { ...openTasks.value, ['t' + (taskId || 0)]: true }
  view.value = {
    kind, label: kindLabel(kind), taskId: taskId || 0, taskName,
    total: 0, offset: 0, idx: 0, items: [],
  }
  await loadGroupPage(0)
}

async function loadGroupPage(offset) {
  const v = view.value
  try {
    const r = await api.get(`/llm/conversation-group?taskId=${v.taskId}&kind=${encodeURIComponent(v.kind)}&offset=${offset}&limit=20`)
    v.total = r.total || 0
    v.offset = offset
    v.items = r.items || []
    v.idx = 0
    if (v.items.length) {
      await loadConversation(v.items[0].id)
    } else {
      currentConvId.value = 0
      messages.value = []
    }
  } catch {
    message.error('加载记录失败')
  }
}

// dir = 1 更早，-1 更新
const canOlder = computed(() => {
  const v = view.value
  if (!v.kind) return false
  return v.idx + 1 < v.items.length || v.offset + v.items.length < v.total
})
const canNewer = computed(() => {
  const v = view.value
  if (!v.kind) return false
  return v.idx > 0 || v.offset > 0
})

async function stepRecord(dir) {
  const v = view.value
  if (!v.kind) return
  const next = v.idx + dir
  if (next >= 0 && next < v.items.length) {
    v.idx = next
    await loadConversation(v.items[next].id)
    return
  }
  // 翻页：更早 → offset 往后；更新 → offset 往前
  const newOffset = dir > 0 ? v.offset + v.items.length : Math.max(0, v.offset - 20)
  const keepIdx = dir > 0 ? 0 : 19
  const r = await api.get(`/llm/conversation-group?taskId=${v.taskId}&kind=${encodeURIComponent(v.kind)}&offset=${newOffset}&limit=20`)
  v.total = r.total || 0
  v.items = r.items || []
  v.offset = newOffset
  v.idx = dir > 0 ? 0 : Math.max(0, v.items.length - 1)
  if (v.items.length) {
    await loadConversation(v.items[Math.min(keepIdx, v.items.length - 1)].id)
    v.idx = Math.min(keepIdx, v.items.length - 1)
  }
}

function openRecord(c) {
  // 搜索结果：直接打开该条，并跳到命中的那条消息
  view.value = {
    kind: c.kind || 'chat', label: c.label || kindLabel(c.kind),
    taskId: c.taskId || 0, taskName: c.taskName || '',
    total: 0, offset: 0, idx: 0, items: [{ id: c.id, title: c.title }],
  }
  loadConversation(c.id, c.hitId || 0)
}

async function loadConversation(id, hitId = 0) {
  if (loading.value) return
  currentConvId.value = id
  hitActive.value = 0
  try {
    const r = await api.get(`/llm/conversations/${id}`)
    messages.value = (r.messages || []).map(m => ({
      id: m.id,
      role: m.role,
      content: m.content,
      toolCalls: m.toolCallsParsed || [],
      meta: m.meta || '',   // http_trace 请求响应包（折叠成按钮）
    }))
    if (hitId) {
      await nextTick()
      jumpToHit(hitId)
    } else {
      await scrollBottom()
    }
  } catch (e) {
    message.error('加载记录失败')
  }
}

// 滚到搜索命中的那条并高亮，不再让用户自己翻
function jumpToHit(mid) {
  hitActive.value = mid
  const el = document.querySelector(`[data-mid="${mid}"]`)
  if (el && el.scrollIntoView) el.scrollIntoView({ block: 'center', behavior: 'smooth' })
  else scrollBottom()
}

function newConversation() {
  currentConvId.value = 0
  messages.value = []
  input.value = ''
  hitActive.value = 0
  view.value = { kind: '', label: '', taskId: 0, taskName: '', total: 0, offset: 0, idx: 0, items: [] }
}

async function removeConv(c) {
  dialog.warning({
    title: '删除记录',
    content: `确定删除「${c.title}」？此操作不可恢复。`,
    positiveText: '删除',
    negativeText: '取消',
    onPositiveClick: async () => {
      await api.delete(`/llm/conversations/${c.id}`)
      if (currentConvId.value === c.id) newConversation()
      await loadIndex()
      message.success('已删除')
    },
  })
}

async function clearAll() {
  dialog.warning({
    title: '清空全部记录',
    content: '所有历史记录和消息都会被删除，不可恢复。',
    positiveText: '清空',
    negativeText: '取消',
    onPositiveClick: async () => {
      await api.post('/llm/conversations/clear-all')
      newConversation()
      await loadIndex()
      message.success('已清空')
    },
  })
}

function onKeydown(e) {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    send()
  }
}

async function loadUsage() {
  usageLoading.value = true
  try { usage.value = await api.get('/llm/usage?days=7') } catch { /* ignore */ }
  finally { usageLoading.value = false }
}

async function send(text) {
  const q = (text || input.value).trim()
  if (!q || loading.value) return
  input.value = ''
  messages.value.push({ role: 'user', content: q })
  loading.value = true
  await scrollBottom()

  try {
    // 一次提问 = 一条会话，不续用上一条、不带历史
    const r = await api.post('/llm/chat-v2', {
      message: q,
      maxRounds: 5,
    }, { timeout: 180000 })

    currentConvId.value = r.conversationId
    messages.value.push({
      role: 'assistant',
      content: r.reply || '(无回复)',
      toolCalls: r.toolCalls || [],
    })
    // 刷新索引；这次问答作为「独立对话 / 对话」的最新一条
    view.value = { kind: 'chat', label: kindLabel('chat'), taskId: 0, taskName: '', total: 0, offset: 0, idx: 0, items: [{ id: r.conversationId, title: q.slice(0, 30) }] }
    await loadIndex()
    loadStatus()
  } catch (e) {
    const msg = e.response?.data?.error || e.message || '请求失败'
    messages.value.push({ role: 'assistant', content: '❌ ' + msg })
  } finally {
    loading.value = false
    await scrollBottom()
  }
}

watch(showUsage, (v) => { if (v) loadUsage() })

onMounted(() => {
  loadStatus()
  loadIndex()
})
</script>

<style scoped>
/* 高度必须用「视口 - 头部 - 内容区上下 padding」，之前的 calc(100vh - 100px) 比实际可用高度大，
   导致整个内容区出现外层滚动条，左侧会话栏跟着页面一起滚。 */
.assistant-page {
  height: calc(100vh - 142px);
  height: calc(100dvh - 142px);
  min-height: 420px;
  display: flex; gap: 12px;
  overflow: hidden;   /* 内部各自滚动，外层不再滚 */
}

.sidebar {
  width: 260px; flex-shrink: 0; display: flex; flex-direction: column;
  height: 100%; overflow: hidden;   /* 列表自己滚，整栏固定 */
  background: #fafbfc; border-radius: 8px; border: 1px solid #eef0f3;
  transition: width .2s;
}
.sidebar.collapsed { width: 56px; }
.sidebar-head { padding: 10px; }
.conv-list { flex: 1; overflow-y: auto; padding: 0 8px 12px; }
/* 侧栏本身是浅色卡片（.sidebar 背景 #fafbfc），但全局文字色是深色主题的白，
   所以这里必须**显式给深色字**——用 color:inherit 会变成白字白底，整棵树看不见。 */
.tree-task { margin-bottom: 6px; }
.tree-task-head {
  display: flex; align-items: center; gap: 5px; padding: 6px 6px;
  border-radius: 6px; cursor: pointer; user-select: none;
}
.tree-task-head:hover { background: #eef2f7; }
.tree-task-name {
  flex: 1; font-size: 12.5px; font-weight: 600; color: #334155;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.caret { font-size: 10px; color: #94a3b8; width: 9px; display: inline-block; }
.caret.small { width: 8px; }
.tree-count {
  font-size: 11px; color: #64748b; background: #eef2f7;
  border-radius: 8px; padding: 0 6px; line-height: 16px; flex: none;
}
.tree-kinds { margin-left: 12px; border-left: 1px dashed #dbe3ec; padding-left: 6px; }
.tree-kind { margin-bottom: 2px; }
.tree-kind-head {
  display: flex; align-items: center; gap: 5px; padding: 5px 5px;
  border-radius: 5px; cursor: pointer; user-select: none;
}
.tree-kind-head:hover { background: #f3f6fa; }
/* 选中态：类型本身就是"入口"，点中它等于打开这个分类 */
.tree-kind-head.active { background: #e3edfb; }
.tree-kind-head.active .tree-kind-name { color: #1d4ed8; font-weight: 600; }
.kind-dot { width: 6px; height: 6px; border-radius: 50%; flex: none; }
.tree-kind-name {
  flex: 1; font-size: 12px; color: #475569;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.kind-chip { font-size: 11px; }
/* 分类内翻页条 */
.rec-nav {
  display: flex; align-items: center; gap: 10px; flex-shrink: 0;
  padding: 6px 4px; margin-bottom: 4px;
  border-bottom: 1px solid #eef0f3;
}
.rec-nav-label { font-size: 12px; color: #475569; font-weight: 600; flex: none; }
.rec-nav-title {
  flex: 1; min-width: 0; font-size: 12px; color: #94a3b8;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.rec-nav-btns { display: flex; gap: 2px; flex: none; }
/* 搜索框：naive-ui 的输入框在深色主题下默认是深底白字，和浅色侧栏撞色后几乎看不见。
   用它的 CSS 变量显式改回浅色底 + 深色字。 */
.conv-search {
  width: 100%; box-sizing: border-box; margin-top: 8px;
  height: 30px; padding: 0 9px;
  font-size: 12.5px; color: #334155;
  background: #ffffff; border: 1px solid #d9e0e8; border-radius: 6px;
  outline: none;
}
.conv-search::placeholder { color: #94a3b8; }
.conv-search:focus { border-color: #94a3b8; box-shadow: 0 0 0 2px rgba(148, 163, 184, .18); }
.conv-item { padding: 8px 10px; border-radius: 6px; cursor: pointer; margin-bottom: 4px; position: relative; }
.conv-item:hover { background: #eef2f7; }
.conv-item:hover .conv-actions { display: flex; }
.conv-item.active { background: #e3edfb; }
.conv-title { font-size: 13px; color: #333; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; margin-bottom: 3px; }
/* 搜索结果里显示命中上下文，点进去直接定位到该条消息 */
.conv-hit {
  font-size: 11.5px; color: #6b7280; line-height: 1.45;
  margin: 2px 0 4px; padding-left: 6px; border-left: 2px solid #fed7aa;
  display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden;
}
.conv-hit-tag { color: #b45309; background: #fff7ed; border-radius: 4px; padding: 0 4px; }
.conv-meta { font-size: 11px; color: #aaa; display: flex; gap: 8px; }
.conv-actions { display: none; position: absolute; right: 4px; top: 6px; background: inherit; gap: 2px; }
.sidebar-foot { padding: 8px; border-top: 1px solid #eef0f3; }

.chat-card { flex: 1; display: flex; flex-direction: column; min-width: 0; }
/* naive-ui 里这个类名是 n-card-content（单连字符），不是 n-card__content。
   写错类名 → 规则从未生效 → 消息区不撑满、输入框贴在对话末尾（看着像在页面中间）。 */
.chat-card :deep(.n-card-content) {
  flex: 1; min-height: 0;
  display: flex; flex-direction: column;
}
.header { display: flex; justify-content: space-between; align-items: center; }
.title { display: flex; align-items: center; gap: 8px; font-size: 15px; font-weight: 600; }
.actions { display: flex; gap: 6px; align-items: center; }
.messages { flex: 1; overflow-y: auto; padding: 8px 4px; min-height: 0; }
.empty { text-align: center; padding: 40px 0; color: #888; }
.empty-title { font-size: 15px; margin-bottom: 20px; }
.samples { display: flex; flex-direction: column; gap: 8px; align-items: center; }
.sample { cursor: pointer; padding: 8px 16px; border-radius: 16px; background: #f5f7fa; font-size: 13px; transition: all .2s; }
.sample:hover { background: #e8eef7; color: #2080f0; }
.msg { display: flex; margin-bottom: 12px; }
.msg.user { justify-content: flex-end; }
.bubble { max-width: 78%; padding: 10px 14px; border-radius: 10px; font-size: 14px; line-height: 1.6; word-break: break-word; }
.msg.user .bubble { background: #2080f0; color: #fff; }
.msg.assistant .bubble { background: #f5f7fa; color: #333; }
.tools { margin-bottom: 6px; display: flex; flex-wrap: wrap; gap: 4px; }
.text :deep(code) { background: rgba(0,0,0,.06); padding: 1px 5px; border-radius: 3px; font-family: ui-monospace, monospace; font-size: 13px; }
/* 完整报文折叠块 */
.raw-box { margin-top: 8px; border-top: 1px dashed rgba(128,128,128,.35); padding-top: 6px; }
.raw-box summary {
  cursor: pointer; font-size: 12px; color: #2563eb; outline: none; user-select: none;
}
.raw-box summary:hover { text-decoration: underline; }
.raw-pre {
  margin: 6px 0 0; padding: 8px; max-height: 460px; overflow: auto;
  background: rgba(0,0,0,.05); border-radius: 6px;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 11.5px; line-height: 1.45; white-space: pre-wrap; word-break: break-all;
}
.input-area { display: flex; gap: 8px; align-items: flex-end; flex-shrink: 0; padding-top: 12px; border-top: 1px solid #eee; }

.trace-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
.trace-pane { border: 1px solid rgba(128,128,128,.3); border-radius: 8px; padding: 10px 12px; min-width: 0; }
.trace-h { font-weight: 600; font-size: 13px; margin-bottom: 8px; color: #4098fc; }
.trace-label { font-size: 12px; color: rgba(128,128,128,1); margin: 6px 0 2px; }
.trace-pre { margin: 0; padding: 8px; background: rgba(128,128,128,.12); color: inherit; border-radius: 6px; font-size: 12px;
  line-height: 1.5; max-height: 32vh; overflow: auto; white-space: pre-wrap; word-break: break-all; }

/* 搜索命中：滚到该条后整块高亮，方便一眼看到 */
.msg-hit .bubble { box-shadow: 0 0 0 2px #f59e0b, 0 0 0 6px rgba(245, 158, 11, .18); }

.usage-summary { display: grid; grid-template-columns: repeat(4, 1fr); gap: 12px; }
.stat { text-align: center; padding: 12px 8px; background: #f8fafc; border-radius: 8px; }
.stat .num { font-size: 20px; font-weight: 700; color: #2080f0; }
.stat .lbl { font-size: 12px; color: #888; margin-top: 4px; }
</style>
