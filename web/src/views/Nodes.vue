<template>
  <div>
    <n-card style="margin-bottom: 16px">
      <template #header>
        <div style="display:flex;align-items:center;gap:10px">
          <span>扫描节点</span>
          <n-tag type="info" size="small" :bordered="false" style="font-family:monospace">
            控制文件最新版 {{ latestVersion || '读取中…' }}
          </n-tag>
          <span class="muted" style="font-size:12px">节点版本与此不一致时，点该行「更新控制文件」</span>
        </div>
      
    <!-- 节点的 config.db 数据库管理 -->
    <n-modal v-model:show="showDbModal" preset="card" :title="`数据库管理 · ${dbNode?.name || ''}`" style="width: 520px">
      <p class="muted" style="margin: 0 0 12px">
        以下是该节点 config.db 里累积的扫描项目（每个分片一个）。可勾选删除，释放磁盘空间。
      </p>
      <n-spin :show="dbLoading">
        <n-checkbox-group v-model:value="checkedProjects">
          <div style="max-height: 320px; overflow-y: auto">
            <div v-for="p in dbProjects" :key="p" style="padding: 4px 0">
              <n-checkbox :value="p" :label="p" />
            </div>
            <n-empty v-if="dbProjects.length === 0" description="暂无数据（等待节点下次心跳上报）" />
          </div>
        </n-checkbox-group>
      </n-spin>
      <template #footer>
        <n-space justify="space-between">
          <n-space>
            <n-button size="small" @click="checkedProjects = [...dbProjects]">全选</n-button>
            <n-button size="small" @click="checkedProjects = []">清空</n-button>
          </n-space>
          <n-space>
            <n-button @click="showDbModal = false">关闭</n-button>
            <n-button type="error" :disabled="checkedProjects.length === 0" :loading="dbDeleting" @click="deleteProjects">
              删除选中{{ checkedProjects.length ? `(${checkedProjects.length})` : '' }}
            </n-button>
          </n-space>
        </n-space>
      </template>
    </n-modal>
</template>
      <n-data-table :columns="columns" :data="nodes" :loading="loading" size="small" />
    </n-card>

    <n-card title="一键加入集群">
      <div class="addr-row">
        <span class="addr-label">服务器 IP</span>
        <n-input v-model:value="serverIP" placeholder="如 10.1.1.1" style="width: 180px" />
        <span class="addr-label" style="margin-left: 8px">端口</span>
        <n-input-number v-model:value="serverPort" :min="1" :max="65535" style="width: 100px" />
        <n-button size="small" @click="fillCurrentAddr" style="margin-left: 8px">填入当前访问</n-button>
        <span class="muted">用于拼接加入命令；内网部署请填真实可达 IP</span>
      </div>
      <div class="toolbar" style="margin-top: 14px">
        <n-button type="primary" @click="genToken">生成一次性加入令牌</n-button>
        <span class="tip">令牌 5 分钟内有效，仅可使用一次</span>
        <span v-if="expireAt" class="muted">有效期至 {{ expireAt }}</span>
      </div>
      <n-tabs type="segment" animated>
        <n-tab-pane name="linux" tab="Linux">
          <div v-if="linuxCmd" class="cmd-box">
            <div class="cmd-bar">
              <n-button size="small" type="primary" @click="copyCmd(linuxCmd)">一键复制</n-button>
            </div>
            <n-code :code="linuxCmd" language="bash" word-wrap />
          </div>
          <p v-else class="muted">先点「生成一次性加入令牌」。</p>
        </n-tab-pane>
        <n-tab-pane name="windows" tab="Windows">
          <div v-if="winCmd" class="cmd-box">
            <div class="cmd-bar">
              <n-button size="small" type="primary" @click="copyCmd(winCmd)">一键复制</n-button>
            </div>
            <n-code :code="winCmd" language="powershell" word-wrap />
          </div>
          <p v-else class="muted">先点「生成一次性加入令牌」。</p>
        </n-tab-pane>
      </n-tabs>
      <p class="muted">
        说明：脚本会从本平台下载 <code>scope-agent</code> 与 <code>TscanClient</code>，用一次性令牌注册后常驻，
        之后由服务端唤醒下发任务，执行期间实时回报进度。
      </p>
    </n-card>

    <n-card title="熔断规则" style="margin-top: 16px">
      <div class="addr-row">
        <span class="addr-label">连续失败阈值</span>
        <n-input-number v-model:value="fuseFailThreshold" :min="1" :max="100" style="width: 140px" />
        <span class="addr-label" style="margin-left: 16px">熔断时长（分钟）</span>
        <n-input-number v-model:value="fuseDurationMin" :min="1" :max="1440" style="width: 140px" />
        <span class="addr-label" style="margin-left: 16px">心跳超时（秒）</span>
        <n-input-number v-model:value="heartbeatTimeoutSec" :min="10" :max="3600" style="width: 140px" />
        <n-button type="primary" style="margin-left: 16px" :loading="savingFuse" @click="saveFuse">保存规则</n-button>
      </div>
      <p class="muted" style="margin-top: 8px">
        节点分片连续失败达到阈值后熔断该节点，熔断期内不再派发，分片自动收回重派给其他节点；
        成功一次即清零连续失败计数。熔断事件记录在「故障和日志」，可配通知。
      </p>
    </n-card>

    <!-- 节点扫描参数 -->
    <n-modal v-model:show="showCfg" preset="card" :title="`节点扫描参数 · ${cfgForm.name}`" style="width: 460px">
      <n-form label-placement="left" label-width="110">
        <n-form-item label="并发 -t">
          <n-input-number v-model:value="cfgForm.concurrency" :min="1" :max="20000" style="width: 180px" />
        </n-form-item>
        <n-form-item label="超时 -time (ms)">
          <n-input-number v-model:value="cfgForm.timeout" :min="100" :max="60000" :step="500" style="width: 180px" />
        </n-form-item>
        <n-form-item label="Tscan License">
          <div style="width: 100%">
            <div style="display:flex;gap:8px;align-items:center;margin-bottom:8px">
              <input ref="licFileInput" type="file" accept=".lic,.txt" style="display:none" @change="onLicFile" />
              <n-button size="small" @click="$refs.licFileInput.click()">选择 License.lic 文件</n-button>
              <n-tag v-if="cfgForm.license" type="success" size="small">已载入 {{ cfgForm.license.length }} 字符</n-tag>
              <n-tag v-else type="warning" size="small">未配置（Free 模式）</n-tag>
              <n-button v-if="cfgForm.license" size="small" text type="error" @click="cfgForm.license = ''">清除</n-button>
            </div>
            <n-input v-model:value="cfgForm.license" type="textarea" :rows="3"
              placeholder="选择文件后自动填入，也可直接粘贴 License 内容" />
            <div class="muted" style="margin-top:6px">
              保存后节点下次心跳（约 30 秒内）会自动写入 <code>config/License.lic</code>
            </div>
          </div>
        </n-form-item>
      </n-form>
      <p class="muted">并发越高，服务端分配给它 IP 越多（按并发加权，尽量让所有节点同时完成）。</p>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showCfg = false">取消</n-button>
          <n-button type="primary" @click="saveCfg">保存</n-button>
        </n-space>
      
    <!-- 节点的 config.db 数据库管理 -->
    <n-modal v-model:show="showDbModal" preset="card" :title="`数据库管理 · ${dbNode?.name || ''}`" style="width: 520px">
      <p class="muted" style="margin: 0 0 12px">
        以下是该节点 config.db 里累积的扫描项目（每个分片一个）。可勾选删除，释放磁盘空间。
      </p>
      <n-spin :show="dbLoading">
        <n-checkbox-group v-model:value="checkedProjects">
          <div style="max-height: 320px; overflow-y: auto">
            <div v-for="p in dbProjects" :key="p" style="padding: 4px 0">
              <n-checkbox :value="p" :label="p" />
            </div>
            <n-empty v-if="dbProjects.length === 0" description="暂无数据（等待节点下次心跳上报）" />
          </div>
        </n-checkbox-group>
      </n-spin>
      <template #footer>
        <n-space justify="space-between">
          <n-space>
            <n-button size="small" @click="checkedProjects = [...dbProjects]">全选</n-button>
            <n-button size="small" @click="checkedProjects = []">清空</n-button>
          </n-space>
          <n-space>
            <n-button @click="showDbModal = false">关闭</n-button>
            <n-button type="error" :disabled="checkedProjects.length === 0" :loading="dbDeleting" @click="deleteProjects">
              删除选中{{ checkedProjects.length ? `(${checkedProjects.length})` : '' }}
            </n-button>
          </n-space>
        </n-space>
      </template>
    </n-modal>
</template>
    </n-modal>
  </div>

    <!-- 节点的 config.db 数据库管理 -->
    <n-modal v-model:show="showDbModal" preset="card" :title="`数据库管理 · ${dbNode?.name || ''}`" style="width: 520px">
      <p class="muted" style="margin: 0 0 12px">
        以下是该节点 config.db 里累积的扫描项目（每个分片一个）。可勾选删除，释放磁盘空间。
      </p>
      <n-spin :show="dbLoading">
        <n-checkbox-group v-model:value="checkedProjects">
          <div style="max-height: 320px; overflow-y: auto">
            <div v-for="p in dbProjects" :key="p" style="padding: 4px 0">
              <n-checkbox :value="p" :label="p" />
            </div>
            <n-empty v-if="dbProjects.length === 0" description="暂无数据（等待节点下次心跳上报）" />
          </div>
        </n-checkbox-group>
      </n-spin>
      <template #footer>
        <n-space justify="space-between">
          <n-space>
            <n-button size="small" @click="checkedProjects = [...dbProjects]">全选</n-button>
            <n-button size="small" @click="checkedProjects = []">清空</n-button>
          </n-space>
          <n-space>
            <n-button @click="showDbModal = false">关闭</n-button>
            <n-button type="error" :disabled="checkedProjects.length === 0" :loading="dbDeleting" @click="deleteProjects">
              删除选中{{ checkedProjects.length ? `(${checkedProjects.length})` : '' }}
            </n-button>
          </n-space>
        </n-space>
      </template>
    </n-modal>
</template>

<script setup>
import { ref, h, onMounted, watch } from 'vue'
import { NTag, NButton, NPopconfirm, useMessage } from 'naive-ui'
import api from '../api'

// 数据库管理状态
const showDbModal = ref(false)
const dbNode = ref(null)
const dbProjects = ref([])
const checkedProjects = ref([])
const dbLoading = ref(false)
const dbDeleting = ref(false)

async function openDb(row) {
  dbNode.value = row
  showDbModal.value = true
  checkedProjects.value = []
  dbLoading.value = true
  try {
    const res = await api.get(`/nodes/${row.id}/projects`)
    dbProjects.value = res.projects || []
  } catch {
    dbProjects.value = []
  } finally {
    dbLoading.value = false
  }
}

async function deleteProjects() {
  if (!checkedProjects.value.length) return
  dbDeleting.value = true
  try {
    await api.post(`/nodes/${dbNode.value.id}/projects/delete`, { projects: checkedProjects.value })
    message.success(`已下发清理指令（${checkedProjects.value.length} 个 project）`)
    showDbModal.value = false
  } catch (e) {
    message.error(e.response?.data?.error || '下发失败')
  } finally {
    dbDeleting.value = false
  }
}

const message = useMessage()
const loading = ref(false)
const nodes = ref([])
const latestVersion = ref('')
const linuxCmd = ref('')
const winCmd = ref('')
const expireAt = ref('')

// 本平台地址（用于拼接命令，持久化）
const serverIP = ref(localStorage.getItem('serverIP') || window.location.hostname)
const serverPort = ref(parseInt(localStorage.getItem('serverPort')) || 18888)
watch([serverIP, serverPort], () => {
  localStorage.setItem('serverIP', serverIP.value)
  localStorage.setItem('serverPort', serverPort.value)
})

function fillCurrentAddr() {
  serverIP.value = window.location.hostname
  serverPort.value = parseInt(window.location.port) || 18888
  message.success('已填入当前访问地址')
}

async function copyCmd(text) {
  try {
    await navigator.clipboard.writeText(text)
    message.success('已复制到剪贴板')
  } catch (e) {
    // 降级：选中文本
    const ta = document.createElement('textarea')
    ta.value = text
    document.body.appendChild(ta)
    ta.select()
    try { document.execCommand('copy'); message.success('已复制') } catch { message.error('复制失败，请手动选择') }
    document.body.removeChild(ta)
  }
}

const columns = [
  { title: '节点名', key: 'name' },
  { title: 'IP', key: 'ip', width: 150 },
  {
    title: '状态', key: 'status', width: 100,
    render: row => h(NTag, { type: row.status === 'online' ? 'success' : 'default', size: 'small' },
      { default: () => row.status === 'online' ? '在线' : '离线' })
  },
  { title: '并发', key: 'concurrency', width: 80, render: r => r.concurrency || 1000 },
  { title: '超时(ms)', key: 'timeout', width: 100, render: r => r.timeout || 3000 },
  {
    title: '控制文件版本', key: 'agentVersion', width: 150,
    render: row => {
      const v = row.agentVersion || '-'
      const outdated = v !== '-' && latestVersion.value && v !== latestVersion.value
      return h('span', {
        style: `font-family:monospace;font-size:12px;color:${outdated ? '#f59e0b' : '#22c55e'}`
      }, outdated ? `${v} (旧)` : v)
    }
  },
  { title: '最近心跳', key: 'lastHeartbeat', width: 180, render: r => fmt(r.lastHeartbeat) },
  {
    title: '操作', key: 'actions', width: 320,
    render: row => {
      const isLatest = !!row.agentVersion && row.agentVersion === latestVersion.value
      const offline = row.status !== 'online'
      return h('div', { style: 'display:flex;gap:8px;flex-wrap:wrap' }, [
        h(NButton, { size: 'small', text: true, type: 'primary', onClick: () => openCfg(row) }, { default: () => '配置' }),
        h(NButton, { size: 'small', text: true, onClick: () => health(row) }, { default: () => '健康检查' }),
        h(NButton, { size: 'small', text: true, type: 'info', onClick: () => openDb(row) }, { default: () => '数据库管理' }),
        h(NButton, {
          size: 'small',
          text: true,
          type: isLatest ? 'default' : 'warning',
          disabled: offline || isLatest,
          onClick: () => upgrade(row)
        }, { default: () => (isLatest ? '已是最新' : (offline ? '离线' : '更新控制文件')) }),
        h(NPopconfirm, { onPositiveClick: () => del(row.id) },
          { trigger: () => h(NButton, { size: 'small', text: true, type: 'error' }, { default: () => '删除' }), default: () => '确认删除节点？' })
      ])
    }
  }
]

function fmt(t) { return t ? new Date(t).toLocaleString('zh-CN') : '-' }

async function load() {
  loading.value = true
  try {
    nodes.value = await api.get('/nodes')
    try {
      const vi = await api.get('/nodes/agent-version')
      latestVersion.value = (vi && vi.latestVersion) || ''
    } catch (e) {
      console.warn('[nodes] 获取控制文件版本失败', e)
    }
  } finally {
    loading.value = false
  }
}

async function genToken() {
  const res = await api.post('/nodes/join-token')
  const base = serverIP.value
    ? `http://${serverIP.value}:${serverPort.value}`
    : window.location.origin
  // Linux：sudo 安装 systemd 服务（开机自启 + 崩溃自动重启）
  linuxCmd.value = `curl -fsSL "${base}/api/nodes/install.sh?token=${res.token}" | sudo bash`
  // Windows：脚本内部自动申请一次 UAC，之后计划任务常驻 + 看门狗自愈
  winCmd.value = `powershell -NoProfile -ExecutionPolicy Bypass -Command "iwr -useb '${base}/api/nodes/install.ps1?token=${res.token}' | iex"`
  expireAt.value = new Date(Date.now() + res.expiresIn * 1000).toLocaleTimeString('zh-CN')
  message.success('令牌已生成，请在 5 分钟内使用')
}

async function upgrade(row) {
  try {
    const res = await api.post(`/nodes/${row.id}/upgrade`)
    message.success(res.message || '已下发更新指令')
    load()
  } catch (e) {
    message.error(e.response?.data?.error || '下发失败')
  }
}

async function del(id) {
  await api.delete(`/nodes/${id}`)
  message.success('已删除')
  load()
}

// ===== 节点配置 / 健康检查 =====
const showCfg = ref(false)
const cfgForm = ref({ id: 0, name: '', concurrency: 1000, timeout: 3000, license: '' })

function openCfg(row) {
  cfgForm.value = { id: row.id, name: row.name, concurrency: row.concurrency || 1000, timeout: row.timeout || 3000, license: '' }
  showCfg.value = true
}
async function saveCfg() {
  try {
    await api.put(`/nodes/${cfgForm.value.id}/config`, {
      timeout: cfgForm.value.timeout,
      concurrency: cfgForm.value.concurrency
    })
    if (cfgForm.value.license && cfgForm.value.license.trim()) {
      await api.put(`/nodes/${cfgForm.value.id}/license`, { content: cfgForm.value.license })
    }
    message.success('已保存' + (cfgForm.value.license ? '，License 将在节点下次心跳写入' : ''))
    showCfg.value = false
    load()
  } catch (e) { message.error(e.response?.data?.error || '保存失败') }
}

function onLicFile(ev) {
  const f = ev.target.files && ev.target.files[0]
  if (!f) return
  const reader = new FileReader()
  reader.onload = () => { cfgForm.value.license = String(reader.result || '') }
  reader.readAsText(f)
  ev.target.value = ''
}

async function health(row) {
  try {
    const r = await api.post(`/nodes/${row.id}/health`)
    const lat = r.latencySeconds >= 0 ? `${r.latencySeconds}s 前心跳` : '从未心跳'
    if (r.healthy) {
      message.success(`✅ ${row.name} 健康 · ${lat}`, { duration: 4000 })
    } else {
      message.error(`❌ ${row.name} 异常 · ${lat}`, { duration: 5000 })
    }
    load()
  } catch (e) { message.error('健康检查失败') }
}

const fuseFailThreshold = ref(5)
const fuseDurationMin = ref(5)
const heartbeatTimeoutSec = ref(90)
const savingFuse = ref(false)

async function loadFuse() {
  try {
    const r = await api.get('/system/fuse')
    fuseFailThreshold.value = r.fuseFailThreshold || 5
    fuseDurationMin.value = r.fuseDurationMin || 5
    heartbeatTimeoutSec.value = r.nodeHeartbeatTimeoutSec || 90
    heartbeatTimeoutSec.value = r.nodeHeartbeatTimeoutSec || 90
  } catch (e) { /* 忽略，用默认值 */ }
}

async function saveFuse() {
  savingFuse.value = true
  try {
    await api.post('/system/fuse', {
      fuseFailThreshold: fuseFailThreshold.value,
      fuseDurationMin: fuseDurationMin.value,
      nodeHeartbeatTimeoutSec: heartbeatTimeoutSec.value
    })
    message.success('熔断规则已保存')
  } catch (e) {
    message.error(e.response?.data?.error || '保存失败')
  } finally {
    savingFuse.value = false
  }
}

onMounted(() => {
  load()
  loadFuse()
})
</script>

<style scoped>
.toolbar { display: flex; gap: 12px; align-items: center; margin-bottom: 14px; }
.addr-row { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
.addr-label { font-size: 13px; color: var(--text-main); font-weight: 500; }
.cmd-box { position: relative; }
.cmd-bar {
  display: flex;
  justify-content: flex-end;
  margin-bottom: 6px;
}
.desc { font-size: 14px; margin: 8px 0; }
.muted { color: var(--text-muted); font-size: 12px; margin-top: 10px; }
</style>
