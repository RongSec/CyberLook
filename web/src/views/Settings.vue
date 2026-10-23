<template>
  <div>
    <n-card title="系统信息">
      <n-descriptions :column="1" bordered label-placement="left" size="small">
        <n-descriptions-item label="平台名称">灯塔</n-descriptions-item>
        <n-descriptions-item label="版本">1.0.0</n-descriptions-item>
        <n-descriptions-item label="后端">Go + Gin + PostgreSQL</n-descriptions-item>
        <n-descriptions-item label="扫描引擎">TscanClient（TideSec 无影）</n-descriptions-item>
      </n-descriptions>
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

    <n-card title="系统升级" style="margin-top: 16px">
      <p class="muted" style="margin: 0 0 12px">
        从 GitHub Release 拉取更新，支持代理（仅用于获取更新信息）。更新前自动备份，可一键回滚。
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
          {{ updateInfo.hasUpdate ? `更新到 ${updateInfo.version}` : '已是最新' }}
        </n-button>
        <n-button :loading="rolling" type="warning" @click="rollback">回滚</n-button>
        <n-button type="primary" @click="saveUpdateSettings">保存设置</n-button>
      </n-space>
      <n-alert v-if="updateInfo.version" type="info" :bordered="false" style="margin-top: 12px">
        <div><b>最新版本</b>：{{ updateInfo.version }}　<b>发布时间</b>：{{ updateInfo.publishedAt }}</div>
        <div style="margin-top: 6px"><b>当前版本</b>：<code>{{ updateInfo.current }}</code></div>
        <div v-if="updateInfo.notes" style="margin-top: 8px; white-space: pre-wrap">{{ updateInfo.notes }}</div>
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
const checking = ref(false)
const applying = ref(false)
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
  if (!updateInfo.value.downloadUrl) { message.warning('请先检查更新'); return }
  applying.value = true
  try {
    await api.post('/system/update/apply', { downloadUrl: updateInfo.value.downloadUrl })
    message.success('已更新，系统正在重启')
  } catch (e) {
    message.error(e.response?.data?.error || '更新失败')
  } finally {
    applying.value = false
  }
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

onMounted(loadUpdateSettings)
</script>

<style scoped>
.desc { font-size: 13px; margin: 4px 0; color: #6b7280; }
</style>
