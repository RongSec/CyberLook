<template>
  <div class="app-shell">
    <div class="aurora"></div>
    <div class="grid-veil"></div>

    <n-layout has-sider class="app-layout">
      <n-layout-sider bordered collapse-mode="width" :width="224" :collapsed-width="68" :collapsed="collapsed"
        :native-scrollbar="false" class="side" @collapse="collapsed = true" @expand="collapsed = false">
        <div class="logo" :class="{ slim: collapsed }">
          <img :src="logoSrc" class="logo-badge" alt="未见" title="系统升级" @click="goUpgrade" />
          <transition name="fade">
            <div v-if="!collapsed" class="logo-text">
              <div class="logo-title">
                <b>未见</b>
                <span class="ver" title="查看系统升级" @click="goUpgrade">v{{ sysVersion }}</span>
              </div>
              <span class="logo-sub">见你未见　照你所患</span>
            </div>
          </transition>
        </div>

        <n-menu :value="currentPath" :options="menuOptions" :collapsed="collapsed" :collapsed-width="68"
          :collapsed-icon-size="19" class="side-menu" @update:value="onMenu" />
      </n-layout-sider>

      <n-layout class="main-area">
        <n-layout-header class="header">
          <div class="header-left">
            <span class="page-title">{{ route.meta.title || '' }}</span>
            <span class="page-path">/ {{ (route.path || '').replace('/', '') }}</span>
          </div>
          <div class="header-right">
            <n-tooltip trigger="hover">
              <template #trigger>
                <button class="icon-btn" @click="openTerminal">
                  <n-icon size="17"><Terminal /></n-icon>
                </button>
              </template>
              扫描引擎终端
            </n-tooltip>
            <n-tooltip trigger="hover">
              <template #trigger>
                <button class="icon-btn" @click="themeStore.toggle()">
                  <n-icon size="17"><Sunny v-if="themeStore.theme === 'dark'" /><Moon v-else /></n-icon>
                </button>
              </template>
              切换主题
            </n-tooltip>
            <n-tooltip trigger="hover">
              <template #trigger>
                <a class="icon-btn" href="https://github.com/RongSec/CyberLook" target="_blank" rel="noopener noreferrer">
                  <n-icon size="17"><LogoGithub /></n-icon>
                </a>
              </template>
              GitHub
            </n-tooltip>
            <n-tooltip trigger="hover">
              <template #trigger>
                <button class="icon-btn" @click="router.push('/api-docs')">
                  <n-icon size="17"><DocumentTextOutline /></n-icon>
                </button>
              </template>
              API 文档
            </n-tooltip>
            <div class="divider"></div>
            <n-dropdown :options="userOptions" @select="onUserSelect">
              <button class="avatar-btn">
                <span class="avatar">{{ (auth.username || 'A')[0].toUpperCase() }}</span>
                <span class="avatar-name">{{ auth.username || 'admin' }}</span>
              </button>
            </n-dropdown>
          </div>
        </n-layout-header>

        <n-layout-content class="content" :native-scrollbar="false">
          <router-view v-slot="{ Component }">
            <transition name="page" mode="out-in">
              <component :is="Component" />
            </transition>
          </router-view>
        </n-layout-content>

        <n-layout-footer class="footer">
          <div class="foot-left">
            <span class="dot" :class="{ live: stats.runningTasks > 0 }"></span>
            <span>运行中任务 <b class="accent">{{ stats.runningTasks }}</b></span>
          </div>
          <div class="foot-right">
            <span>CPU <b :style="{ color: cpuColor }">{{ stats.cpuPercent.toFixed(1) }}%</b></span>
            <span class="sep">·</span>
            <span>内存 <b>{{ stats.memUsedMB }} / {{ stats.memTotalMB }} MB</b></span>
          </div>
        </n-layout-footer>
      </n-layout>
    </n-layout>

    <!-- 个人中心 -->
    <n-modal v-model:show="showProfile" preset="card" title="个人中心" style="width: 440px">
      <n-form label-placement="left" label-width="90">
        <n-form-item label="用户名"><n-input :value="auth.username" disabled /></n-form-item>
        <n-form-item label="旧密码"><n-input v-model:value="pwdForm.old" type="password" show-password-on="click" /></n-form-item>
        <n-form-item label="新密码"><n-input v-model:value="pwdForm.newPwd" type="password" show-password-on="click" /></n-form-item>
        <n-form-item label="确认新密码"><n-input v-model:value="pwdForm.confirm" type="password" show-password-on="click" /></n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showProfile = false">取消</n-button>
          <n-button type="primary" :loading="savingPwd" @click="savePassword">修改密码</n-button>
        </n-space>
      </template>
    </n-modal>

    <!-- 全局终端 -->
    <n-modal v-model:show="showTerminal" preset="card" title="扫描引擎终端" style="width: 900px">
      <div class="term-toolbar">
        <n-select v-model:value="termTaskId" :options="taskOptions" placeholder="选择任务" filterable
          style="width: 280px" @update:value="startSSE" />
      </div>
      <div class="terminal" ref="terminalEl">
        <div v-for="(line, i) in termLines" :key="i" class="log-line">{{ line }}</div>
        <div v-if="termProgress" class="log-line progress-line">{{ termProgress }}</div>
        <div v-if="termLines.length === 0 && !termProgress" class="log-empty">选择任务后查看实时输出</div>
      </div>
    </n-modal>
  </div>
</template>

<script setup>
import { h, ref, computed, onMounted, onBeforeUnmount, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NIcon, useMessage } from 'naive-ui'
import { useAuthStore } from '../store/auth'
import { useThemeStore } from '../store/theme'
import {
  SpeedometerOutline, ServerOutline, LayersOutline, ShieldCheckmarkOutline,
  ImagesOutline, CalendarOutline, CubeOutline, SettingsOutline, PersonCircle,
  LogOutOutline, Terminal, Sunny, Moon, GitNetworkOutline, BusinessOutline, ClipboardOutline,
  LogoGithub, DocumentTextOutline, NotificationsOutline
} from '@vicons/ionicons5'
import api from '../api'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const themeStore = useThemeStore()
const message = useMessage()
const collapsed = ref(false)
const sysVersion = ref('0.0.1')
// 亮色背景用深色线条 logo，暗色背景用浅青线条 logo
const logoSrc = computed(() =>
  themeStore.theme === 'light' ? '/logo-dark.png' : '/logo-transparent.png'
)

function goUpgrade() {
  router.push('/settings#upgrade')
}

async function loadVersion() {
  try {
    const r = await api.get('/system/version')
    if (r?.version) sysVersion.value = r.version
  } catch (e) { /* 忽略 */ }
}

const currentPath = computed(() => route.path)
const stats = ref({ cpuPercent: 0, memUsedMB: 0, memTotalMB: 0, memPercent: 0, runningTasks: 0 })
const cpuColor = computed(() => stats.value.cpuPercent > 80 ? '#f87171' : stats.value.cpuPercent > 50 ? '#fbbf24' : '#34d399')

const showProfile = ref(false)
const savingPwd = ref(false)
const pwdForm = ref({ old: '', newPwd: '', confirm: '' })

const showTerminal = ref(false)
const termTaskId = ref(null)
const termLines = ref([])
const termProgress = ref('')
const taskOptions = ref([])
const terminalEl = ref(null)

function renderIcon(icon) {
  return () => h(NIcon, null, { default: () => h(icon) })
}

const menuOptions = [
  { label: '首页', key: '/dashboard', icon: renderIcon(SpeedometerOutline) },
  { label: '资产总览', key: '/assets', icon: renderIcon(ServerOutline) },
  { label: '资产处置', key: '/disposal', icon: renderIcon(ClipboardOutline) },
  { label: '应用服务', key: '/services', icon: renderIcon(LayersOutline) },
  { label: '合规检测', key: '/compliance', icon: renderIcon(ShieldCheckmarkOutline) },
  { label: '页面快照', key: '/snapshots', icon: renderIcon(ImagesOutline) },
  { label: '扫描', key: '/plans', icon: renderIcon(CalendarOutline) },
  { label: '节点', key: '/nodes', icon: renderIcon(CubeOutline) },
  { label: 'IP 规划', key: '/network', icon: renderIcon(GitNetworkOutline) },
  { label: '组织结构', key: '/departments', icon: renderIcon(BusinessOutline) },
  { label: '故障和日志', key: '/faults', icon: renderIcon(DocumentTextOutline) },
  { label: '通知设置', key: '/notify', icon: renderIcon(NotificationsOutline) },
  { label: '系统配置', key: '/settings', icon: renderIcon(SettingsOutline) }
]

const userOptions = [
  { label: '个人中心', key: 'profile', icon: renderIcon(PersonCircle) },
  { label: '退出登录', key: 'logout', icon: renderIcon(LogOutOutline) }
]

function onMenu(key) { router.push(key) }

function onUserSelect(key) {
  if (key === 'profile') {
    showProfile.value = true
  } else if (key === 'logout') {
    auth.logout()
    router.push('/login')
  }
}

async function savePassword() {
  if (!pwdForm.value.old || !pwdForm.value.newPwd) { message.warning('请填写旧密码和新密码'); return }
  if (pwdForm.value.newPwd !== pwdForm.value.confirm) { message.warning('两次新密码不一致'); return }
  savingPwd.value = true
  try {
    await api.post('/auth/password', { oldPassword: pwdForm.value.old, newPassword: pwdForm.value.newPwd })
    message.success('密码已修改，请重新登录')
    showProfile.value = false
    pwdForm.value = { old: '', newPwd: '', confirm: '' }
    setTimeout(() => { auth.logout(); router.push('/login') }, 800)
  } catch (e) { message.error(e.response?.data?.error || '修改失败') } finally { savingPwd.value = false }
}

async function loadStats() {
  try { stats.value = await api.get('/system/stats') } catch (e) { /* ignore */ }
}

async function openTerminal() {
  showTerminal.value = true
  try {
    const res = await api.get('/tasks', { params: { page: 1, size: 50 } })
    taskOptions.value = res.items.map(t => ({ label: `#${t.id} ${t.name} (${t.status})`, value: t.id }))
    const running = res.items.find(t => t.status === 'running')
    termTaskId.value = running ? running.id : (res.items.length > 0 ? res.items[0].id : null)
    if (termTaskId.value) startSSE(termTaskId.value)
  } catch (e) { console.error(e) }
}

let eventSource = null
function startSSE(taskId) {
  stopSSE()
  if (!taskId) return
  termLines.value = []
  termProgress.value = ''
  const token = localStorage.getItem('token') || ''
  eventSource = new EventSource(`/api/tasks/${taskId}/log/stream?token=${encodeURIComponent(token)}`)
  eventSource.addEventListener('line', (e) => {
    termLines.value.push(e.data)
    if (termLines.value.length > 3000) termLines.value.splice(0, 1500)
    if (terminalEl.value) terminalEl.value.scrollTop = terminalEl.value.scrollHeight
  })
  eventSource.addEventListener('progress', (e) => {
    termProgress.value = e.data
    if (terminalEl.value) terminalEl.value.scrollTop = terminalEl.value.scrollHeight
  })
}
function stopSSE() { if (eventSource) { eventSource.close(); eventSource = null } }
watch(showTerminal, (v) => { if (!v) stopSSE() })

let statsTimer = null
onMounted(() => {
  loadStats()
  loadVersion()
  statsTimer = setInterval(loadStats, 3000)
})
onBeforeUnmount(() => { clearInterval(statsTimer); stopSSE() })
</script>

<style scoped>
.app-shell {
  position: relative;
  height: 100vh;
  overflow: hidden;
  background: var(--bg-deep);
}
.aurora {
  position: fixed;
  inset: 0;
  z-index: 0;
  pointer-events: none;
  opacity: var(--aurora-opacity);
  background:
    radial-gradient(760px circle at 12% 8%, rgba(245, 158, 11, 0.15), transparent 45%),
    radial-gradient(680px circle at 88% 82%, rgba(251, 191, 36, 0.11), transparent 45%),
    radial-gradient(520px circle at 55% 105%, rgba(234, 88, 12, 0.09), transparent 45%);
  animation: auroraShift 18s ease-in-out infinite alternate;
}
@keyframes auroraShift {
  0% { transform: translate(0, 0) scale(1); opacity: 0.9; }
  100% { transform: translate(-24px, 18px) scale(1.06); opacity: 1; }
}
.grid-veil {
  position: fixed;
  inset: 0;
  z-index: 0;
  pointer-events: none;
  background-image:
    linear-gradient(rgba(148, 163, 184, 0.045) 1px, transparent 1px),
    linear-gradient(90deg, rgba(148, 163, 184, 0.045) 1px, transparent 1px);
  background-size: 56px 56px;
  opacity: calc(var(--grid-opacity) / 0.045);
  mask-image: radial-gradient(ellipse at 50% 40%, black 20%, transparent 80%);
}
.app-layout {
  position: relative;
  z-index: 1;
  height: 100vh;
  background: transparent;
}
.side {
  background: var(--bg-side) !important;
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
  border-right: 1px solid var(--border-subtle) !important;
}
.logo {
  height: 68px;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 0 14px;
  border-bottom: 1px solid rgba(148, 163, 184, 0.08);
  overflow: hidden;
}
.logo.slim { padding: 0; justify-content: center; gap: 0; }

.logo-badge {
  width: 30px;
  height: 30px;
  flex: none;
  object-fit: contain;
  cursor: pointer;
  filter: drop-shadow(0 0 12px rgba(168, 232, 236, 0.55));
  animation: logoBreathe 4.2s ease-in-out infinite;
  transition: transform 0.2s ease;
}
@keyframes logoBreathe {
  0%, 100% { opacity: 0.82; transform: scale(0.97); }
  50% { opacity: 1; transform: scale(1.03); }
}
.logo-badge:hover {
  transform: translateY(-1px);
  box-shadow: 0 0 22px -3px rgba(127, 227, 234, 1);
}

.logo-text {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}
.logo-title {
  display: flex;
  align-items: center;
  gap: 7px;
  min-width: 0;
}
.logo-title b {
  font-size: 17px;
  font-weight: 600;
  letter-spacing: 2px;
  color: var(--text-primary);
  white-space: nowrap;
  line-height: 1.2;
}
.ver {
  flex: none;
  padding: 1px 6px;
  font-family: ui-monospace, 'Cascadia Code', Consolas, monospace;
  font-size: 10.5px;
  line-height: 1.5;
  color: var(--text-muted);
  border: 1px solid rgba(127, 227, 234, 0.28);
  border-radius: 5px;
  cursor: pointer;
  transition: all 0.2s ease;
}
.ver:hover {
  color: #7fe3ea;
  border-color: rgba(127, 227, 234, 0.7);
  background: rgba(127, 227, 234, 0.1);
}
.logo-sub {
  font-size: 9.5px;
  line-height: 1.3;
  letter-spacing: 0.02em;
  color: var(--text-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.fade-enter-active, .fade-leave-active { transition: opacity 0.2s; }
.fade-enter-from, .fade-leave-to { opacity: 0; }

.side-menu { padding: 10px 8px; }
:deep(.n-menu-item-content) {
  border-radius: 10px;
  transition: background 0.22s, color 0.22s, padding-left 0.22s;
}
:deep(.n-menu-item-content:hover) {
  background: linear-gradient(90deg, rgba(245, 158, 11, 0.14), rgba(245, 158, 11, 0.02));
}
:deep(.n-menu-item-content--selected) {
  background: linear-gradient(90deg, rgba(245, 158, 11, 0.24), rgba(249, 115, 22, 0.10)) !important;
  box-shadow: inset 2px 0 0 #fbbf24;
}
:deep(.n-menu-item-content--selected .n-menu-item-content-header) { color: #dbeafe; font-weight: 600; }

.main-area { background: transparent !important; }

.header {
  height: 64px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 22px;
  background: var(--bg-header);
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
  border-bottom: 1px solid var(--border-subtle);
  position: relative;
}
.header::after {
  content: '';
  position: absolute;
  left: 0; right: 0; bottom: -1px;
  height: 1px;
  background: linear-gradient(90deg, transparent, rgba(245, 158, 11, 0.5), rgba(249, 115, 22, 0.4), transparent);
}
.header-left { display: flex; align-items: baseline; gap: 10px; }
.page-title { font-size: 17px; font-weight: 600; color: var(--text-main); letter-spacing: 0.4px; }.page-path { font-size: 12px; color: #475569; letter-spacing: 0.5px; }
.header-right { display: flex; align-items: center; gap: 10px; }
.icon-btn {
  width: 34px; height: 34px;
  display: flex; align-items: center; justify-content: center;
  border-radius: 10px;
  border: 1px solid transparent;
  background: transparent;
  color: #94a3b8;
  cursor: pointer;
  transition: all 0.2s;
}
.icon-btn:hover {
  color: #dbeafe;
  background: rgba(245, 158, 11, 0.12);
  border-color: rgba(251, 191, 36, 0.25);
}
.divider { width: 1px; height: 22px; background: rgba(148, 163, 184, 0.16); margin: 0 2px; }
.avatar-btn {
  display: flex; align-items: center; gap: 8px;
  padding: 3px 10px 3px 4px;
  border-radius: 999px;
  border: 1px solid rgba(148, 163, 184, 0.16);
  background: rgba(30, 41, 59, 0.4);
  color: #cbd5e1;
  cursor: pointer;
  transition: all 0.2s;
}
.avatar-btn:hover { border-color: rgba(251, 191, 36, 0.4); background: rgba(245, 158, 11, 0.12); }
.avatar {
  width: 26px; height: 26px;
  border-radius: 50%;
  display: flex; align-items: center; justify-content: center;
  font-size: 12px; font-weight: 700; color: #fff;
  background: linear-gradient(135deg, #f59e0b, #f97316);
}
.avatar-name { font-size: 13px; }

.content {
  padding: 20px 22px 54px;
  background: transparent !important;
}

.footer {
  position: fixed;
  left: 0;
  right: 0;
  bottom: 0;
  z-index: 50;
  height: 34px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 22px;
  font-size: 12px;
  color: var(--text-muted);
  background: var(--bg-header);
  backdrop-filter: blur(16px);
  -webkit-backdrop-filter: blur(16px);
  border-top: 1px solid var(--border-subtle);
}
.foot-left, .foot-right { display: flex; align-items: center; gap: 8px; }
.footer b { color: #cbd5e1; font-weight: 600; }
.footer .accent { color: #fbbf24; }
.footer .sep { color: #334155; }
.dot { width: 7px; height: 7px; border-radius: 50%; background: #475569; }
.dot.live { background: #34d399; box-shadow: 0 0 0 3px rgba(52, 211, 153, 0.18); animation: pulse 1.6s infinite; }
@keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.45; } }

.term-toolbar { margin-bottom: 12px; }
.terminal {
  background: #060a12;
  color: #c9d1d9;
  font-family: 'JetBrains Mono', 'Consolas', 'Courier New', monospace;
  font-size: 12px;
  padding: 14px;
  border-radius: 12px;
  border: 1px solid rgba(148, 163, 184, 0.12);
  height: 480px;
  overflow-y: auto;
  white-space: pre-wrap;
  word-break: break-all;
}
.log-line { line-height: 1.55; }
.progress-line { color: #fbbf24; }
.log-empty { color: #475569; }
</style>
