<template>
  <div>
    <n-card title="系统信息">
      <n-descriptions :column="1" bordered label-placement="left" size="small">
        <n-descriptions-item label="平台名称">未见</n-descriptions-item>
        <n-descriptions-item label="版本">v{{ curVersion }}</n-descriptions-item>
        <n-descriptions-item label="扫描引擎">TscanClient 3.4.3</n-descriptions-item>
      </n-descriptions>
          <n-alert v-if="updateLog.length" type="info" :bordered="false" style="margin-top: 12px">
        <div style="max-height: 200px; overflow-y: auto; font-family: monospace; font-size: 12px; white-space: pre-wrap">
          <div v-for="(line, i) in updateLog" :key="i">{{ line }}</div>
        </div>
      </n-alert>
    </n-card>

    <n-card title="修改密码" style="margin-top: 16px">
      <n-form label-placement="left" label-width="100" style="max-width: 460px">
        <n-form-item label="当前密码">
          <n-input v-model:value="pwd.oldPassword" type="password" show-password-on="click"
            placeholder="输入当前登录密码" @keyup.enter="savePwd" />
        </n-form-item>
        <n-form-item label="新密码">
          <n-input v-model:value="pwd.newPassword" type="password" show-password-on="click"
            placeholder="至少 6 位" @keyup.enter="savePwd" />
        </n-form-item>
        <n-form-item label="确认新密码">
          <n-input v-model:value="pwd.confirm" type="password" show-password-on="click"
            placeholder="再输入一次" @keyup.enter="savePwd" />
        </n-form-item>
        <n-form-item>
          <n-space>
            <n-button type="primary" :loading="saving" @click="savePwd">保存</n-button>
            <n-button quaternary @click="resetPwd">清空</n-button>
          </n-space>
        </n-form-item>
      </n-form>
      <p class="desc">修改后需用新密码重新登录，已签发的 Token 在过期前仍然有效。</p>
    </n-card>
  </div>

    <n-card id="upgrade" title="系统升级" style="margin-top: 16px">
      <p class="muted" style="margin: 0 0 10px">
        从 GitHub Release 拉取更新，支持代理（仅用于获取更新信息）。更新前自动备份，可一键回滚。
      </p>
      <p class="muted" style="margin: 0 0 12px">
        当前版本 <code>v{{ curVersion }}</code>
        <span v-if="curBuild"> · 构建于 {{ curBuild }}</span>
      </p>
      <n-grid :cols="2" :x-gap="16" item-responsive>
        <n-grid-item span="2 m:1">
          <n-form-item label="GitHub 仓库" label-placement="top">
            <n-input v-model:value="updateForm.githubRepo" placeholder="owner/repo，如 tide/sec-platform" />
          </n-form-item>
        </n-grid-item>
        <n-grid-item span="2 m:1">
          <n-form-item label="GitHub Token（可选，私有仓库用）" label-placement="top">
            <n-input v-model:value="updateForm.githubToken" type="password" show-password-on="click" placeholder="留空表示不修改" />
          </n-form-item>
        </n-grid-item>
        <n-grid-item span="2 m:1">
          <n-form-item label="代理（仅用于获取更新，形如 http://host:port）" label-placement="top">
            <n-input v-model:value="updateForm.proxy" placeholder="http://127.0.0.1:7890" />
          </n-form-item>
        </n-grid-item>
        <n-grid-item span="2 m:1">
          <n-form-item label="服务端口（默认 18888，改后需重启）" label-placement="top">
            <n-input-number v-model:value="updateForm.port" :min="1" :max="65535" style="width: 100%" />
          </n-form-item>
        </n-grid-item>
      </n-grid>
      <n-space justify="end" style="margin-top: 12px">
        <n-button :loading="checking" @click="checkUpdate">检查更新</n-button>
        <n-button :loading="applying" type="primary" :disabled="!updateInfo.hasUpdate" @click="applyUpdate">
          {{ applying ? '更新中…（预计 2-5 分钟）' : (updateInfo.hasUpdate ? ('更新到 ' + updateInfo.version) : '已是最新') }}
        </n-button>
        <n-button :loading="rolling" type="warning" @click="rollback">回滚</n-button>
        <n-button type="primary" @click="saveUpdateSettings">保存设置</n-button>
      </n-space>
      <n-alert v-if="updateInfo.version" type="info" :bordered="false" style="margin-top: 12px">
        <div><b>最新版本</b>：{{ updateInfo.version }}　<b>发布时间</b>：{{ updateInfo.publishedAt }}</div>
        <div style="margin-top: 6px"><b>当前版本</b>：<code>{{ updateInfo.current }}</code></div>
        <div v-if="updateInfo.notes" style="margin-top: 8px; white-space: pre-wrap">{{ updateInfo.notes }}</div>
      </n-alert>
      <n-alert v-if="updateLog.length" type="info" :bordered="false" style="margin-top: 12px">
        <div style="max-height: 200px; overflow-y: auto; font-family: monospace; font-size: 12px; white-space: pre-wrap">
          <div v-for="(line, i) in updateLog" :key="i">{{ line }}</div>
        </div>
      </n-alert>
    </n-card>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { useMessage } from 'naive-ui'
import api from '../api'

const message = useMessage()
const pwd = ref({ oldPassword: '', newPassword: '', confirm: '' })
const saving = ref(false)

function resetPwd() {
  pwd.value = { oldPassword: '', newPassword: '', confirm: '' }
}

async function savePwd() {
  const p = pwd.value
  if (!p.oldPassword) return message.warning('请输入当前密码')
  if (!p.newPassword || p.newPassword.length < 6) return message.warning('新密码至少 6 位')
  if (p.newPassword !== p.confirm) return message.warning('两次输入的新密码不一致')
  if (p.newPassword === p.oldPassword) return message.warning('新密码不能与当前密码相同')

  saving.value = true
  try {
    await api.post('/auth/password', {
      oldPassword: p.oldPassword,
      newPassword: p.newPassword
    })
    message.success('密码已修改，请用新密码重新登录')
    resetPwd()
  } catch (e) {
    message.error(e.response?.data?.error || '修改失败')
  } finally {
    saving.value = false
  }
}

// ===== 系统升级 =====
const updateForm = ref({ githubRepo: '', githubToken: '', proxy: '', port: 18888 })
const updateInfo = ref({ version: '', current: '', notes: '', publishedAt: '', hasUpdate: false, downloadUrl: '' })
const curVersion = ref('0.0.1')
const curBuild = ref('')

async function loadVersion() {
  try {
    const r = await api.get('/system/version')
    curVersion.value = r.version || curVersion.value
    curBuild.value = r.buildTime && r.buildTime !== 'dev' ? r.buildTime : ''
  } catch { /* ignore */ }
}
const checking = ref(false)
const applying = ref(false)
const updateLog = ref([])
const rolling = ref(false)

async function loadUpdateSettings() {
  try {
    const res = await api.get('/system/settings')
    updateForm.value = { githubRepo: res.githubRepo || '', githubToken: '', proxy: res.proxy || '', port: res.port || 18888 }
  } catch { /* ignore */ }
}

async function saveUpdateSettings() {
  try {
    await api.post('/system/settings', updateForm.value)
    message.success('已保存')
    updateForm.value.githubToken = ''
  } catch (e) {
    message.error(e.response?.data?.error || '保存失败')
  }
}

async function checkUpdate() {
  checking.value = true
  try {
    const res = await api.get('/system/update/check')
    updateInfo.value = { ...updateInfo.value, ...res }
    message.success('已获取最新版本信息')
  } catch (e) {
    message.error(e.response?.data?.error || '检查更新失败')
  } finally {
    checking.value = false
  }
}

async function applyUpdate() {
  if (!updateInfo.value.version) { message.warning('请先检查更新'); return }
  applying.value = true
  updateLog.value = []
  try {
    const token = localStorage.getItem('token')
    const resp = await fetch('/api/system/update/apply', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token },
      body: JSON.stringify({ version: updateInfo.value.version })
    })
    if (!resp.ok && !resp.body) {
      const data = await resp.json().catch(() => ({}))
      message.error(data.error || '更新失败')
      return
    }
    // 流式读取日志
    const reader = resp.body.getReader()
    const decoder = new TextDecoder()
    let done = false
    while (!done) {
      const { value, done: d } = await reader.read()
      done = d
      if (value) {
        const text = decoder.decode(value, { stream: true })
        const lines = text.split('\n').filter(Boolean)
        for (const line of lines) {
          updateLog.value.push(line)
        }
      }
    }
    // 判断结果
    const last = updateLog.value[updateLog.value.length - 1] || ''
    if (last.startsWith('SUCCESS')) {
      message.success(last.replace('SUCCESS ', ''))
    } else if (last.startsWith('ERROR')) {
      message.error(last.replace('ERROR ', ''))
    }
  } catch (e) {
    // 流中断是预期的（server 容器重建会断开连接）
    // 轮询 /api/health 确认更新是否实际成功
    updateLog.value.push('连接中断（server 重启中），正在确认状态...')
    const ok = await confirmUpdateSuccess()
    if (ok) {
      message.success('已更新到 ' + updateInfo.value.version + '，3 秒后刷新页面')
      setTimeout(() => window.location.reload(), 3000)
    } else {
      message.error('更新失败: ' + (e.message || '网络错误'))
    }
  } finally {
    applying.value = false
  }
}

// 轮询 /api/health 确认更新成功（最多 90 秒）
async function confirmUpdateSuccess() {
  const target = updateInfo.value.version?.replace(/^v/, '') || ''
  for (let i = 0; i < 30; i++) {
    await new Promise(r => setTimeout(r, 3000))
    try {
      const resp = await fetch('/api/health')
      const data = await resp.json()
      if (data.ok && data.version === target) {
        updateLog.value.push('确认成功: version=' + data.version)
        return true
      }
      updateLog.value.push('等待服务启动... (当前 version=' + (data.version || '?') + ')')
    } catch {
      updateLog.value.push('服务未就绪，继续等待...')
    }
  }
  return false
}

async function rollback() {
  rolling.value = true
  try {
    await api.post('/system/update/rollback', {})
    message.success('已回滚，系统正在重启')
  } catch (e) {
    message.error(e.response?.data?.error || '回滚失败')
  } finally {
    rolling.value = false
  }
}

onMounted(async () => {
  await Promise.all([loadUpdateSettings(), loadVersion()])
  // 从左上角版本号跳转过来时定位到升级卡片
  if (location.hash === '#upgrade') {
    setTimeout(() => {
      document.getElementById('upgrade')?.scrollIntoView({ behavior: 'smooth', block: 'center' })
    }, 120)
  }
})
</script>

<style scoped>
.desc { font-size: 13px; margin: 4px 0; color: #6b7280; }
</style>
