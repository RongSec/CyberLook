<template>
  <div class="dash">
    <n-card title="通知设置" size="small">
      <p class="tip">
        扫描任务结束、发现新资产、合规违规时自动推送。各渠道可单独启用，填了地址才会发送。
        企业微信 / 钉钉 / 飞书 请用「群机器人」的 Webhook 地址。
      </p>

      <n-grid :cols="2" :x-gap="16" item-responsive>
        <n-grid-item span="2 m:1">
          <n-card title="企业微信" size="small" class="ch">
            <n-form-item label="Webhook" label-placement="top">
              <div style="display:flex;gap:8px;width:100%">
                <div style="display:flex;gap:8px;width:100%">
                <n-input v-model:value="form.wecomWebhook" placeholder="https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=..." />
                <n-button size="small" :loading="testingCh === 'wecom'" @click="testOne('wecom')">测试</n-button>
              </div>
              </div>
            </n-form-item>
          </n-card>
        </n-grid-item>
        <n-grid-item span="2 m:1">
          <n-card title="钉钉" size="small" class="ch">
            <n-form-item label="Webhook" label-placement="top">
              <div style="display:flex;gap:8px;width:100%">
                <div style="display:flex;gap:8px;width:100%">
                <n-input v-model:value="form.dingtalkWebhook" placeholder="https://oapi.dingtalk.com/robot/send?access_token=..." />
                <n-button size="small" :loading="testingCh === 'dingtalk'" @click="testOne('dingtalk')">测试</n-button>
              </div>
              </div>
            </n-form-item>
          </n-card>
        </n-grid-item>
        <n-grid-item span="2 m:1">
          <n-card title="飞书" size="small" class="ch">
            <n-form-item label="Webhook" label-placement="top">
              <div style="display:flex;gap:8px;width:100%">
                <div style="display:flex;gap:8px;width:100%">
                <n-input v-model:value="form.feishuWebhook" placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/..." />
                <n-button size="small" :loading="testingCh === 'feishu'" @click="testOne('feishu')">测试</n-button>
              </div>
              </div>
            </n-form-item>
          </n-card>
        </n-grid-item>
        <n-grid-item span="2 m:1">
          <n-card title="通用 Webhook" size="small" class="ch">
            <n-form-item label="地址（POST JSON: {title, content}）" label-placement="top">
              <div style="display:flex;gap:8px;width:100%">
                <div style="display:flex;gap:8px;width:100%">
                <n-input v-model:value="form.genericWebhook" placeholder="https://your-system/hook" />
                <n-button size="small" :loading="testingCh === 'generic'" @click="testOne('generic')">测试</n-button>
              </div>
              </div>
            </n-form-item>
          </n-card>
        </n-grid-item>
      </n-grid>

      <n-card title="邮件" size="small" style="margin-top: 16px">
        <n-grid :cols="3" :x-gap="12" item-responsive>
          <n-grid-item span="3 m:1">
            <n-form-item label="SMTP 服务器" label-placement="top">
              <div style="display:flex;gap:8px;width:100%">
                <n-input v-model:value="form.emailSmtp" placeholder="smtp.exmail.qq.com" />
                <n-button size="small" :loading="testingCh === 'email'" @click="testOne('email')">测试</n-button>
              </div>
            </n-form-item>
          </n-grid-item>
          <n-grid-item span="3 m:1">
            <n-form-item label="端口" label-placement="top">
              <n-input-number v-model:value="form.emailPort" :min="1" :max="65535" style="width: 100%" />
            </n-form-item>
          </n-grid-item>
          <n-grid-item span="3 m:1">
            <n-form-item label="SSL" label-placement="top">
              <n-switch v-model:value="form.emailSsl" />
            </n-form-item>
          </n-grid-item>
          <n-grid-item span="3 m:1">
            <n-form-item label="发件账号" label-placement="top">
              <n-input v-model:value="form.emailUser" placeholder="alert@example.com" />
            </n-form-item>
          </n-grid-item>
          <n-grid-item span="3 m:1">
            <n-form-item label="密码 / 授权码" label-placement="top">
              <n-input v-model:value="form.emailPass" type="password" show-password-on="click" placeholder="留空表示不修改" />
            </n-form-item>
          </n-grid-item>
          <n-grid-item span="3 m:1">
            <n-form-item label="收件人（逗号分隔）" label-placement="top">
              <n-input v-model:value="form.emailTo" placeholder="a@example.com,b@example.com" />
            </n-form-item>
          </n-grid-item>
        </n-grid>
      </n-card>

      <n-card title="触发事件" size="small" style="margin-top: 16px">
        <n-space>
          <n-checkbox v-model:checked="form.onTaskDone">扫描任务结束</n-checkbox>
          <n-checkbox v-model:checked="form.onNewAsset">发现新资产</n-checkbox>
          <n-checkbox v-model:checked="form.onViolation">合规违规</n-checkbox>
          <n-checkbox v-model:checked="form.onFault">故障告警</n-checkbox>
        </n-space>
      </n-card>

      <n-space justify="end" style="margin-top: 16px">
        <n-button type="primary" :loading="saving" @click="save">保存</n-button>
      </n-space>

      <n-alert v-if="false" type="info" :bordered="false" style="margin-top: 12px">
        <div v-for="(v, k) in testResultsMap" :key="k" class="res-row">
          <b>{{ k }}</b> <span :class="v.startsWith('✗') ? 'bad' : 'ok'">{{ v }}</span>
        </div>
      </n-alert>
    </n-card>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { useMessage } from 'naive-ui'
import api from '../api'

const message = useMessage()
const saving = ref(false)
const testing = ref(false)
const testResult = ref('')
const testResultsMap = ref({})

const form = ref({
  wecomWebhook: '', dingtalkWebhook: '', feishuWebhook: '', genericWebhook: '',
  emailSmtp: '', emailPort: 465, emailSsl: true, emailUser: '', emailPass: '', emailTo: '',
  onTaskDone: true, onNewAsset: true, onViolation: true, onFault: true
})

async function load() {
  try {
    const res = await api.get('/notify/settings')
    form.value = { ...form.value, ...res, emailPass: '' }
  } catch { /* ignore */ }
}

async function save() {
  saving.value = true
  try {
    await api.post('/notify/settings', form.value)
    message.success('已保存')
    form.value.emailPass = ''
  } catch (e) {
    message.error(e.response?.data?.error || '保存失败')
  } finally {
    saving.value = false
  }
}

async function test() {
  testing.value = true
  testResult.value = ''
  try {
    const res = await api.post('/notify/test', {})
    testResultsMap.value = res.results || {}
    testResult.value = JSON.stringify(res.results || {})
    message.success('已发送测试，请查看各渠道结果')
  } catch (e) {
    message.error(e.response?.data?.error || '测试失败')
  } finally {
    testing.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.dash { animation: rise 0.4s cubic-bezier(0.4, 0, 0.2, 1); }
@keyframes rise { from { opacity: 0; transform: translateY(10px); } to { opacity: 1; transform: none; } }
.tip { font-size: 13px; color: var(--text-muted); margin: 0 0 16px; line-height: 1.8; }
.ch { background: var(--bg-hover); }
.res-row { font-size: 13px; line-height: 1.9; }
.ok { color: #84cc16; }
.bad { color: #ef4444; }
</style>
