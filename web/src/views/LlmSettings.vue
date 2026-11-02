<template>
  <div class="llm-settings">
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
              <n-input-number v-model:value="editing.maxTokens" :min="256" :step="512" style="width:100%" />
            </n-form-item-gi>
            <n-form-item-gi label="上下文窗口 (K)">
              <n-input-number v-model:value="editing.contextWindowK" :min="8" :step="8" style="width:100%" />
            </n-form-item-gi>
          </n-grid>

          <n-divider title-placement="left" style="margin: 12px 0 8px">轮询与熔断</n-divider>
          <n-grid :cols="3" :x-gap="16">
            <n-form-item-gi label="优先级（高优先）">
              <n-input-number v-model:value="editing.priority" :min="1" :max="999" style="width:100%" />
            </n-form-item-gi>
            <n-form-item-gi label="失败阈值（次）">
              <n-input-number v-model:value="editing.failThreshold" :min="1" :max="20" style="width:100%" />
            </n-form-item-gi>
            <n-form-item-gi label="冷却时间（秒）">
              <n-input-number v-model:value="editing.cooldownSeconds" :min="10" :max="3600" style="width:100%" />
            </n-form-item-gi>
          </n-grid>

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
const dialog = useDialog()

const loading = ref(false)
const saving = ref(false)
const testing = ref(false)
const profiles = ref([])
const available = ref(0)
const showModal = ref(false)

const providerOptions = [
  { label: 'OpenAI 兼容', value: 'openai' },
  { label: 'Anthropic', value: 'anthropic' },
  { label: 'Ollama', value: 'ollama' },
  { label: 'Azure OpenAI', value: 'azure' },
]

const emptyForm = () => ({
  id: 0, name: '', provider: 'openai', baseUrl: '', apiKey: '', model: '',
  temperature: 0.3, maxTokens: 4096, contextWindowK: 128,
  priority: 100, failThreshold: 3, cooldownSeconds: 60, timeoutSeconds: 120,
  enabled: true, poolExclude: false,
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
      message.error(`失败: ${r.error}`)
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
