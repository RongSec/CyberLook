<template>
  <div class="llm-settings">
    <n-card :bordered="false" style="margin-bottom: 12px">
      <div style="display:flex;align-items:center;gap:16px;flex-wrap:wrap">
        <b>AI 功能</b>
        <span style="display:flex;align-items:center;gap:6px">
          <n-switch v-model:value="aiSet.serviceIdentify" @update:value="saveAISet" />
          <span>服务识别</span>
        </span>
        <span style="display:flex;align-items:center;gap:6px">
          <n-switch v-model:value="aiSet.scanMonitor" @update:value="saveAISet" />
          <span>扫描监控</span>
        </span>
        <span class="tip" style="color:#888;font-size:12px">
          服务识别＝扫描入库时判断标题/系统名；扫描监控＝节点日志 15 分钟无刷新时由 AI 诊断并收尾
        </span>
      </div>
    </n-card>
    <n-card :bordered="false">
      <template #header>
        <div class="header">
          <div class="title">
            <span>LLM 配置</span>
            <n-tag v-if="available > 0" type="success" size="small" round>{{ available }} 个可用</n-tag>
            <n-tag v-else type="error" size="small" round>无可用配置</n-tag>
          </div>
          <div class="actions">
            <n-button size="small" @click="resetAll">恢复所有熔断</n-button>
            <n-button size="small" type="primary" @click="openCreate">新增配置</n-button>
          </div>
        </div>
      </template>

      <n-alert type="info" :bordered="false" style="margin-bottom: 16px">
        <b>故障轮询</b>：按优先级从高到低依次尝试，某个配置失败自动切下一个。<br/>
        <b>熔断</b>：连续失败达到阈值后暂停使用该配置，冷却期结束自动恢复；密钥无效等确定性错误首次即熔断。
      </n-alert>

      <n-data-table
        :columns="columns"
        :data="profiles"
        :loading="loading"
        :row-key="r => r.id"
        size="small"
      />
    </n-card>

    <!-- 新增/编辑弹窗 -->
    <n-modal v-model:show="showModal" :mask-closable="false" style="width: 720px">
      <n-card :title="editing.id ? '编辑配置' : '新增配置'" :bordered="false" size="small">
        <n-form :model="editing" label-placement="top" size="small">
          <n-grid :cols="2" :x-gap="16">
            <n-form-item-gi label="名称 *">
              <n-input v-model:value="editing.name" placeholder="如 本地-Qwen32B" />
            </n-form-item-gi>
            <n-form-item-gi label="协议">
              <n-select v-model:value="editing.provider" :options="providerOptions" />
            </n-form-item-gi>
          </n-grid>

          <n-form-item label="Base URL *">
            <n-input v-model:value="editing.baseUrl" placeholder="http://llm.internal:8000/v1" />
          </n-form-item>

          <n-grid :cols="2" :x-gap="16">
            <n-form-item-gi label="API Key">
              <n-input
                v-model:value="editing.apiKey"
                type="password"
                show-password-on="click"
                :placeholder="editing.id ? '留空表示不修改' : 'sk-...'"
              />
            </n-form-item-gi>
            <n-form-item-gi label="模型名 *">
              <n-input v-model:value="editing.model" placeholder="qwen2.5-32b-instruct" />
            </n-form-item-gi>
          </n-grid>

          <n-divider title-placement="left" style="margin: 12px 0 8px">生成参数</n-divider>
          <n-grid :cols="3" :x-gap="16">
            <n-form-item-gi label="Temperature">
              <n-input-number v-model:value="editing.temperature" :min="0" :max="2" :step="0.1" style="width:100%" />
            </n-form-item-gi>
            <n-form-item-gi label="最大输出 Tokens">
              <n-input-number v-model:value="editing.maxTokens" :min="256" :step="1024" style="width:100%" placeholder="8192" />
            </n-form-item-gi>
            <n-form-item-gi label="上下文窗口 (K)">
              <n-input-number v-model:value="editing.contextWindowK" :min="8" :step="8" style="width:100%" />
            </n-form-item-gi>
          </n-grid>

          <n-divider title-placement="left" style="margin: 12px 0 8px">轮询与熔断</n-divider>
          <n-grid :cols="3" :x-gap="16">
            <n-form-item-gi>
              <template #label>
                优先级
                <n-tooltip trigger="hover">
                  <template #trigger><span style="color:#999;cursor:help;margin-left:2px">ⓘ</span></template>
                  0-10，数字越小越优先。0 最先尝试，失败后依次尝试 1、2…；相同优先级之间随机轮换（负载均衡）。
                </n-tooltip>
              </template>
              <n-input-number v-model:value="editing.priority" :min="0" :max="10" style="width:100%" />
            </n-form-item-gi>
            <n-form-item-gi label="失败阈值（次）">
              <n-input-number v-model:value="editing.failThreshold" :min="1" :max="20" style="width:100%" />
            </n-form-item-gi>
            <n-form-item-gi label="冷却时间（秒）">
              <n-input-number v-model:value="editing.cooldownSeconds" :min="10" :max="3600" style="width:100%" />
            </n-form-item-gi>
          </n-grid>

          <n-divider title-placement="left" style="margin: 12px 0 8px">AI 思考配置</n-divider>
          <n-grid :cols="3" :x-gap="16">
            <n-form-item-gi label="Provider">
              <n-select v-model:value="editing.provider" :options="providerOptions" />
            </n-form-item-gi>
            <n-form-item-gi label="思考开关">
              <n-select v-model:value="editing.thinkingType" :options="thinkingOptions" />
            </n-form-item-gi>
            <n-form-item-gi label="思考强度">
              <n-select v-model:value="editing.reasoningEffort" :options="effortOptions" />
            </n-form-item-gi>
          </n-grid>

          <n-grid :cols="2" :x-gap="16">
            <n-form-item-gi label="速率限制（每秒，0=不限）">
              <n-input-number v-model:value="editing.ratePerSecond" :min="0" :precision="1" style="width:100%" />
            </n-form-item-gi>
            <n-form-item-gi label="速率限制（每分钟，0=不限）">
              <n-input-number v-model:value="editing.ratePerMinute" :min="0" :precision="1" style="width:100%" />
            </n-form-item-gi>
          </n-grid>

          <n-divider title-placement="left" style="margin: 12px 0 8px">应用层代理</n-divider>
          <n-form-item>
            <template #label>
              应用层代理
              <n-tooltip trigger="hover">
                <template #trigger><span style="color:#999;cursor:help;margin-left:2px">ⓘ</span></template>
                仅 Docker Desktop / 受限网络需要：宿主机跑 llmproxy，填 http://host.docker.internal:8898。生产 Linux 留空即可。
              </n-tooltip>
            </template>
            <n-input v-model:value="editing.viaProxy" placeholder="留空直连；Docker Desktop 本地开发填 http://host.docker.internal:8898" />
          </n-form-item>

          <n-divider title-placement="left" style="margin: 12px 0 8px">其他</n-divider>
          <n-grid :cols="2" :x-gap="16">
            <n-form-item-gi label="请求超时（秒）">
              <n-input-number v-model:value="editing.timeoutSeconds" :min="10" :max="600" style="width:100%" />
            </n-form-item-gi>
            <n-form-item-gi label="选项">
              <n-space vertical>
                <n-checkbox v-model:checked="editing.enabled">启用</n-checkbox>
                <n-checkbox v-model:checked="editing.poolExclude">排除出自动轮询（仅手动绑定时使用）</n-checkbox>
              </n-space>
            </n-form-item-gi>
          </n-grid>
        </n-form>

        <template #footer>
          <div style="display:flex; justify-content: space-between; align-items: center">
            <n-button v-if="editing.id" @click="testProfile(editing.id, true)" :loading="testing">
              测试连通
            </n-button>
            <div style="margin-left:auto; display:flex; gap:8px">
              <n-button @click="showModal = false">取消</n-button>
              <n-button type="primary" :loading="saving" @click="save">保存</n-button>
            </div>
          </div>
        </template>
      </n-card>
    </n-modal>
  </div>
</template>

<script setup>
import { ref, onMounted, h } from 'vue'
import {
  NCard, NTag, NButton, NAlert, NDataTable, NModal, NForm, NFormItem, NFormItemGi,
  NGrid, NInput, NInputNumber, NSelect, NDivider, NCheckbox, NSpace, useMessage, useDialog
} from 'naive-ui'
import api from '../api'

const message = useMessage()
const aiSet = ref({ serviceIdentify: true, scanMonitor: true })

async function loadAISet() {
  try { aiSet.value = { ...aiSet.value, ...(await api.get('/ai/settings')) } } catch { /* ignore */ }
}
async function saveAISet() {
  try {
    await api.put('/ai/settings', aiSet.value)
    message.success('已保存')
  } catch (e) { message.error('保存失败') }
}
const dialog = useDialog()

const loading = ref(false)
const saving = ref(false)
const testing = ref(false)
const profiles = ref([])
const available = ref(0)
const showModal = ref(false)

const tlsOptions = [
  { label: '默认（TLS 1.2+）', value: '' },
  { label: 'TLS 1.2', value: 'tls1.2' },
  { label: 'TLS 1.0（老网关）', value: 'tls1.0' },
  { label: 'TLS 1.1', value: 'tls1.1' },
]


const thinkingOptions = [
  { label: '不发送（默认）', value: '' },
  { label: '开启思考', value: 'enabled' },
  { label: '关闭思考', value: 'disabled' },
]
const effortOptions = [
  { label: '不发送（默认）', value: '' },
  { label: '低', value: 'low' },
  { label: '中', value: 'medium' },
  { label: '高', value: 'high' },
  { label: '超高', value: 'xhigh' },
  { label: '最大', value: 'max' },
]


const providerOptions = [
  { label: 'OpenAI 兼容', value: 'openai' },
  { label: 'OpenAI Responses', value: 'openai-responses' },
  { label: 'OpenAI Responses', value: 'openai-responses' },
  { label: 'Anthropic', value: 'anthropic' },
  { label: 'Ollama', value: 'ollama' },
  { label: 'Azure OpenAI', value: 'azure' },
]

const emptyForm = () => ({
  id: 0, name: '', provider: 'openai', baseUrl: '', apiKey: '', model: '',
  temperature: 0.3, maxTokens: 8192, contextWindowK: 128,
  priority: 5, failThreshold: 3, cooldownSeconds: 60, timeoutSeconds: 120,
  enabled: true, poolExclude: false, viaProxy: '', provider: 'openai', thinkingType: '', reasoningEffort: '', ratePerSecond: 0, ratePerMinute: 0,
})
const editing = ref(emptyForm())

const columns = [
  {
    title: '名称', key: 'name', width: 160,
    render: (r) => h('div', { style: 'display:flex;align-items:center;gap:6px' }, [
      h('span', { style: 'font-weight:600' }, r.name),
      r.enabled ? null : h(NTag, { size: 'tiny', type: 'default' }, { default: () => '停用' }),
      r.poolExclude ? h(NTag, { size: 'tiny', type: 'warning' }, { default: () => '不轮询' }) : null,
    ]),
  },
  { title: '模型', key: 'model', width: 180, ellipsis: { tooltip: true } },
  {
    title: '状态', key: 'status', width: 130,
    render: (r) => {
      if (r.breakerOpen) return h(NTag, { size: 'small', type: 'error' }, { default: () => `熔断至 ${r.openUntil}` })
      if (!r.enabled) return h(NTag, { size: 'small', type: 'default' }, { default: () => '已停用' })
      if (r.lastError) return h(NTag, { size: 'small', type: 'warning' }, { default: () => `异常 ${r.consecFails} 次` })
      if (r.lastLatencyMs) return h(NTag, { size: 'small', type: 'success' }, { default: () => `${r.lastLatencyMs}ms` })
      return h(NTag, { size: 'small' }, { default: () => '未测试' })
    },
  },
  { title: '优先级', key: 'priority', width: 80, align: 'right' },
  { title: '阈值/冷却', key: 'fc', width: 110, align: 'center',
    render: (r) => `${r.failThreshold}次 / ${r.cooldownSeconds}s` },
  {
    title: '操作', key: 'actions', width: 210,
    render: (r) => h(NSpace, { size: 4 }, {
      default: () => [
        h(NButton, { size: 'tiny', onClick: () => testProfile(r.id) }, { default: () => '测试' }),
        h(NButton, { size: 'tiny', onClick: () => openEdit(r) }, { default: () => '编辑' }),
        r.breakerOpen
          ? h(NButton, { size: 'tiny', type: 'warning', onClick: () => resetOne(r.id) }, { default: () => '恢复' })
          : null,
        h(NButton, { size: 'tiny', type: 'error', onClick: () => remove(r) }, { default: () => '删除' }),
      ].filter(Boolean),
    }),
  },
]

async function load() {
  loading.value = true
  try {
    const r = await api.get('/llm/profiles')
    profiles.value = r.items || []
    const s = await api.get('/llm/status')
    available.value = s.available || 0
  } catch (e) {
    message.error('加载失败')
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editing.value = emptyForm()
  showModal.value = true
}

function openEdit(r) {
  editing.value = { ...r, apiKey: '' }
  showModal.value = true
}

async function save() {
  if (!editing.value.name || !editing.value.baseUrl || !editing.value.model) {
    message.warning('名称、Base URL、模型名必填')
    return
  }
  saving.value = true
  try {
    const payload = { ...editing.value }
    if (editing.value.id) {
      await api.put(`/llm/profiles/${editing.value.id}`, payload)
      message.success('已更新')
    } else {
      await api.post('/llm/profiles', payload)
      message.success('已创建')
    }
    showModal.value = false
    load()
  } catch (e) {
    message.error(e.response?.data?.error || '保存失败')
  } finally {
    saving.value = false
  }
}

async function testProfile(id, inModal = false) {
  testing.value = true
  try {
    const r = await api.post(`/llm/profiles/${id}/test`)
    if (r.ok) {
      message.success(`连通正常，延迟 ${r.latencyMs}ms，回复: ${(r.reply || '').slice(0, 30)}`)
    } else {
      dialog.error({
        title: '连通失败',
        content: () => h('div', [
          h('div', { style: 'margin-bottom:8px;color:#d03050' }, r.error),
          r.hint ? h('div', { style: 'font-size:13px;color:#666;line-height:1.6' }, r.hint) : null,
        ].filter(Boolean)),
        positiveText: '知道了',
      })
    }
    if (!inModal) load()
  } catch (e) {
    message.error(e.response?.data?.error || '测试失败')
  } finally {
    testing.value = false
  }
}

async function resetOne(id) {
  await api.post(`/llm/profiles/${id}/reset`)
  message.success('已恢复')
  load()
}

async function resetAll() {
  await api.post('/llm/breakers/reset-all')
  message.success('已恢复全部')
  load()
}

function remove(r) {
  dialog.warning({
    title: '确认删除',
    content: `删除配置「${r.name}」？删除后该 LLM 不再参与轮询。`,
    positiveText: '删除',
    negativeText: '取消',
    onPositiveClick: async () => {
      await api.delete(`/llm/profiles/${r.id}`)
      message.success('已删除')
      load()
    },
  })
}

onMounted(load)
</script>

<style scoped>
.header { display: flex; justify-content: space-between; align-items: center; }
.title { display: flex; align-items: center; gap: 10px; font-size: 16px; font-weight: 600; }
.actions { display: flex; gap: 8px; }
</style>
