<template>
  <div class="assistant-page">
    <!-- 左侧会话列表 -->
    <div class="sidebar" :class="{ collapsed: sidebarCollapsed }">
      <div class="sidebar-head">
        <n-button v-if="!sidebarCollapsed" type="primary" block @click="newConversation">+ 新对话</n-button>
        <n-button v-else type="primary" circle size="small" @click="newConversation">+</n-button>
      </div>

      <div v-if="!sidebarCollapsed" class="conv-list">
        <n-empty v-if="conversations.length === 0" description="暂无历史会话" size="small" style="margin-top:40px" />
        <div
          v-for="c in conversations"
          :key="c.id"
          :class="['conv-item', { active: c.id === currentConvId }]"
          @click="loadConversation(c.id)"
        >
          <div class="conv-title">{{ c.title || '未命名' }}</div>
          <div class="conv-meta">
            <span>{{ c.messageCount }} 条</span>
            <span v-if="c.totalTokens">{{ formatTokens(c.totalTokens) }}</span>
            <span>{{ formatTime(c.updatedAt) }}</span>
          </div>
          <div class="conv-actions" @click.stop>
            <n-button size="tiny" quaternary @click="renameConv(c)">改名</n-button>
            <n-button size="tiny" quaternary type="error" @click="removeConv(c)">删除</n-button>
          </div>
        </div>
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

      <div ref="scrollRef" class="messages">
        <div v-if="messages.length === 0" class="empty">
          <div class="empty-title">可以直接问，也可以让它操作数据</div>
          <div class="samples">
            <div v-for="s in samples" :key="s" class="sample" @click="send(s)">{{ s }}</div>
          </div>
        </div>

        <div v-for="(m, i) in messages" :key="i" :class="['msg', m.role]">
          <div class="bubble">
            <div v-if="m.toolCalls && m.toolCalls.length" class="tools">
              <n-tag v-for="(t, j) in m.toolCalls" :key="j" size="tiny" type="info" round>
                {{ t.tool }}
              </n-tag>
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
const loading = ref(false)
const messages = ref([])
const conversations = ref([])
const currentConvId = ref(0)
const scrollRef = ref(null)
const showUsage = ref(false)
const usageLoading = ref(false)
const usage = ref({ total: null, byProfile: [], recent: [] })
const status = ref({ enabled: false, profileUp: 0, todayCalls: 0, todayTokens: 0 })
const sidebarCollapsed = ref(false)

const samples = [
  '现在有多少待处置资产？',
  '列出 10.24.11 网段的资产',
  '最近的任务执行情况怎么样',
  '帮我创建一个扫描 10.24.12.0/24 的任务',
  '哪些资产没有填负责人',
]

const currentTitle = computed(() => {
  const c = conversations.value.find(x => x.id === currentConvId.value)
  return c ? c.title : 'AI 助手'
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

function renderText(t) {
  if (!t) return ''
  return t.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/\n/g, '<br/>')
    .replace(/\*\*([^*]+)\*\*/g, '<b>$1</b>')
    .replace(/`([^`]+)`/g, '<code>$1</code>')
}

async function scrollBottom() {
  await nextTick()
  if (scrollRef.value) scrollRef.value.scrollTop = scrollRef.value.scrollHeight
}

async function loadStatus() {
  try { status.value = await api.get('/llm/status') } catch { /* ignore */ }
}

async function loadConversations() {
  try {
    const r = await api.get('/llm/conversations?limit=50')
    conversations.value = r.items || []
  } catch { /* ignore */ }
}

async function loadConversation(id) {
  if (loading.value) return
  currentConvId.value = id
  try {
    const r = await api.get(`/llm/conversations/${id}`)
    messages.value = (r.messages || []).map(m => ({
      role: m.role,
      content: m.content,
      toolCalls: m.toolCallsParsed || [],
    }))
    await scrollBottom()
  } catch (e) {
    message.error('加载会话失败')
  }
}

function newConversation() {
  currentConvId.value = 0
  messages.value = []
  input.value = ''
}

async function removeConv(c) {
  dialog.warning({
    title: '删除会话',
    content: `确定删除「${c.title}」？此操作不可恢复。`,
    positiveText: '删除',
    negativeText: '取消',
    onPositiveClick: async () => {
      await api.delete(`/llm/conversations/${c.id}`)
      if (currentConvId.value === c.id) newConversation()
      await loadConversations()
      message.success('已删除')
    },
  })
}

function renameConv(c) {
  const newTitle = prompt('重命名会话', c.title)
  if (newTitle === null || !newTitle.trim()) return
  api.put(`/llm/conversations/${c.id}`, { title: newTitle.trim() }).then(() => {
    loadConversations()
    message.success('已重命名')
  })
}

async function clearAll() {
  dialog.warning({
    title: '清空全部会话',
    content: '所有历史会话和消息都会被删除，不可恢复。',
    positiveText: '清空',
    negativeText: '取消',
    onPositiveClick: async () => {
      await api.post('/llm/conversations/clear-all')
      newConversation()
      await loadConversations()
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
    const r = await api.post('/llm/chat-v2', {
      conversationId: currentConvId.value,
      message: q,
      maxRounds: 5,
    }, { timeout: 180000 })

    currentConvId.value = r.conversationId
    messages.value.push({
      role: 'assistant',
      content: r.reply || '(无回复)',
      toolCalls: r.toolCalls || [],
    })
    await loadConversations()
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
  loadConversations()
})
</script>

<style scoped>
.assistant-page { height: calc(100vh - 100px); display: flex; gap: 12px; }

.sidebar {
  width: 260px; flex-shrink: 0; display: flex; flex-direction: column;
  background: #fafbfc; border-radius: 8px; border: 1px solid #eef0f3;
  transition: width .2s;
}
.sidebar.collapsed { width: 56px; }
.sidebar-head { padding: 10px; }
.conv-list { flex: 1; overflow-y: auto; padding: 0 8px; }
.conv-item { padding: 8px 10px; border-radius: 6px; cursor: pointer; margin-bottom: 4px; position: relative; }
.conv-item:hover { background: #eef2f7; }
.conv-item:hover .conv-actions { display: flex; }
.conv-item.active { background: #e3edfb; }
.conv-title { font-size: 13px; color: #333; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; margin-bottom: 3px; }
.conv-meta { font-size: 11px; color: #aaa; display: flex; gap: 8px; }
.conv-actions { display: none; position: absolute; right: 4px; top: 6px; background: inherit; gap: 2px; }
.sidebar-foot { padding: 8px; border-top: 1px solid #eef0f3; }

.chat-card { flex: 1; display: flex; flex-direction: column; min-width: 0; }
.header { display: flex; justify-content: space-between; align-items: center; }
.title { display: flex; align-items: center; gap: 8px; font-size: 15px; font-weight: 600; }
.actions { display: flex; gap: 6px; align-items: center; }
.messages { flex: 1; overflow-y: auto; padding: 8px 4px; min-height: 200px; }
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
.input-area { display: flex; gap: 8px; align-items: flex-end; padding-top: 12px; border-top: 1px solid #eee; }

.usage-summary { display: grid; grid-template-columns: repeat(4, 1fr); gap: 12px; }
.stat { text-align: center; padding: 12px 8px; background: #f8fafc; border-radius: 8px; }
.stat .num { font-size: 20px; font-weight: 700; color: #2080f0; }
.stat .lbl { font-size: 12px; color: #888; margin-top: 4px; }
</style>
