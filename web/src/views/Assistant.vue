<template>
  <div class="assistant-page">
    <n-card :bordered="false" class="chat-card">
      <template #header>
        <div class="header">
          <div class="title">
            <span>AI 助手</span>
            <n-tag v-if="status.enabled" type="success" size="small" round>
              {{ status.model }}
            </n-tag>
            <n-tag v-else type="warning" size="small" round>未配置</n-tag>
          </div>
          <div class="actions">
            <n-tag size="small" :bordered="false" type="info">
              今日 {{ status.todayCalls || 0 }} 次 / {{ formatTokens(status.todayTokens) }} tokens
            </n-tag>
            <n-button size="small" quaternary @click="loadTools">能力（{{ status.tools }}）</n-button>
            <n-button size="small" quaternary @click="showUsage = true">用量</n-button>
            <n-button size="small" quaternary @click="$router.push('/llm-settings')">配置</n-button>
            <n-button size="small" quaternary @click="clearChat">清空</n-button>
          </div>
        </div>
      </template>

      <n-alert v-if="!status.enabled" type="warning" :bordered="false" style="margin-bottom: 12px">
        LLM 未配置。设置环境变量 <code>LLM_BASE_URL</code>（如 <code>http://llm.internal:8000/v1</code>），
        可选 <code>LLM_API_KEY</code>、<code>LLM_MODEL</code>，重启服务后生效。
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
          <div class="bubble">
            <n-spin size="small" /> <span style="margin-left: 8px">思考中…</span>
          </div>
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

          <n-divider title-placement="left" style="margin: 16px 0 8px">最近调用</n-divider>
          <div v-for="r in usage.recent" :key="r.id" class="recent-row">
            <n-tag size="tiny" :type="r.status === 'ok' ? 'success' : 'error'">{{ r.status }}</n-tag>
            <span class="rname">{{ r.profileName }}</span>
            <span class="rtok">{{ r.totalTokens }} tok</span>
            <span class="rms">{{ r.latencyMs }}ms</span>
          </div>
        </n-spin>
      </n-drawer-content>
    </n-drawer>

    <n-drawer v-model:show="showTools" :width="480">
      <n-drawer-content title="可用能力">
        <n-list>
          <n-list-item v-for="t in toolList" :key="t.name">
            <div class="tool-name">{{ t.name }}</div>
            <div class="tool-desc">{{ t.description }}</div>
          </n-list-item>
        </n-list>
      </n-drawer-content>
    </n-drawer>
  </div>
</template>

<script setup>
import { ref, onMounted, nextTick, watch } from 'vue'
import { NCard, NTag, NButton, NAlert, NInput, NSpin, NDrawer, NDrawerContent, NList, NListItem, NTable, NDivider, NSpace, useMessage } from 'naive-ui'
import api from '../api'
import { useRouter } from 'vue-router'

const message = useMessage()
const router = useRouter()
const input = ref('')
const loading = ref(false)
const messages = ref([])
const scrollRef = ref(null)
const showTools = ref(false)
const showUsage = ref(false)
const usageLoading = ref(false)
const usage = ref({ total: null, byProfile: [], daily: [], recent: [] })
const toolList = ref([])
const status = ref({ enabled: false, model: '', tools: 0 })

const samples = [
  '现在有多少待处置资产？',
  '列出 10.24.11 网段的资产',
  '最近的任务执行情况怎么样',
  '帮我创建一个扫描 10.24.12.0/24 的任务',
  '哪些资产没有填负责人',
]

async function loadStatus() {
  try {
    const r = await api.get('/llm/status')
    status.value = r
  } catch { /* ignore */ }
}

async function loadTools() {
  try {
    const r = await api.get('/llm/tools')
    toolList.value = r.tools || []
    showTools.value = true
  } catch (e) {
    message.error('加载失败')
  }
}

function formatTokens(n) {
  if (!n) return '0'
  if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M'
  if (n >= 1000) return (n / 1000).toFixed(1) + 'K'
  return String(n)
}

async function loadUsage() {
  usageLoading.value = true
  try {
    const r = await api.get('/llm/usage?days=7')
    usage.value = r
  } catch { /* ignore */ }
  finally { usageLoading.value = false }
}

function clearChat() {
  messages.value = []
}

function onKeydown(e) {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    send()
  }
}

function renderText(t) {
  if (!t) return ''
  return t.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/\n/g, '<br/>')
    .replace(/`([^`]+)`/g, '<code>$1</code>')
}

async function scrollBottom() {
  await nextTick()
  if (scrollRef.value) scrollRef.value.scrollTop = scrollRef.value.scrollHeight
}

async function send(text) {
  const q = (text || input.value).trim()
  if (!q || loading.value) return
  input.value = ''
  messages.value.push({ role: 'user', content: q })
  loading.value = true
  await scrollBottom()

  try {
    // 只发送对话历史（不含 tool 消息）
    const history = messages.value
      .filter(m => m.role === 'user' || m.role === 'assistant')
      .map(m => ({ role: m.role, content: m.content }))

    const r = await api.post('/llm/chat', { messages: history, maxRounds: 5 }, { timeout: 180000 })
    messages.value.push({
      role: 'assistant',
      content: r.reply || '(无回复)',
      toolCalls: r.toolCalls || [],
    })
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
})
</script>

<style scoped>
.assistant-page { height: calc(100vh - 100px); }
.chat-card { height: 100%; display: flex; flex-direction: column; }
.header { display: flex; justify-content: space-between; align-items: center; }
.title { display: flex; align-items: center; gap: 10px; font-size: 16px; font-weight: 600; }
.actions { display: flex; gap: 4px; }
.messages { flex: 1; overflow-y: auto; padding: 8px 4px; min-height: 200px; }
.empty { text-align: center; padding: 40px 0; color: #888; }
.empty-title { font-size: 15px; margin-bottom: 20px; }
.samples { display: flex; flex-direction: column; gap: 8px; align-items: center; }
.sample {
  cursor: pointer; padding: 8px 16px; border-radius: 16px;
  background: #f5f7fa; font-size: 13px; transition: all .2s;
}
.sample:hover { background: #e8eef7; color: #2080f0; }
.msg { display: flex; margin-bottom: 12px; }
.msg.user { justify-content: flex-end; }
.bubble {
  max-width: 78%; padding: 10px 14px; border-radius: 10px;
  font-size: 14px; line-height: 1.6; word-break: break-word;
}
.msg.user .bubble { background: #2080f0; color: #fff; }
.msg.assistant .bubble { background: #f5f7fa; color: #333; }
.tools { margin-bottom: 6px; display: flex; flex-wrap: wrap; gap: 4px; }
.text :deep(code) {
  background: rgba(0,0,0,.06); padding: 1px 5px; border-radius: 3px;
  font-family: ui-monospace, monospace; font-size: 13px;
}
.input-area { display: flex; gap: 8px; align-items: flex-end; padding-top: 12px; border-top: 1px solid #eee; }
.usage-summary { display: grid; grid-template-columns: repeat(4, 1fr); gap: 12px; margin-bottom: 8px; }
.stat { text-align: center; padding: 12px 8px; background: #f8fafc; border-radius: 8px; }
.stat .num { font-size: 20px; font-weight: 700; color: #2080f0; }
.stat .lbl { font-size: 12px; color: #888; margin-top: 4px; }
.recent-row { display: flex; align-items: center; gap: 8px; padding: 6px 0; border-bottom: 1px solid #f5f5f5; font-size: 12px; }
.rname { flex: 1; color: #555; }
.rtok { color: #888; }
.rms { color: #aaa; width: 60px; text-align: right; }
.tool-name { font-family: ui-monospace, monospace; font-weight: 600; font-size: 13px; color: #2080f0; }
.tool-desc { font-size: 13px; color: #666; margin-top: 4px; }
</style>
