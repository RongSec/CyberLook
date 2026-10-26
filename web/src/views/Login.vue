<template>
  <div class="stage" ref="stageRef">
    <!-- 背景层（打开登录面板时整体高斯模糊） -->
    <div class="backdrop" :class="{ blurred: panelOpen }">
      <!-- 资产条目层：鼠标附近 + 呼吸灯附近会被照亮 -->
      <div class="asset-layer">
        <div
          v-for="(a, i) in assets"
          :key="i"
          class="asset"
          :style="{ left: a.x + '%', top: a.y + '%' }"
          :ref="el => { assetEls[i] = el }"
        >
          <span class="ip">{{ a.ip }}</span><span class="nm">{{ a.name }}</span>
        </div>
      </div>

      <!-- 萤火虫画布 -->
      <canvas ref="fxRef" class="fx"></canvas>

      <!-- 中心：呼吸灯 logo + slogan -->
      <div class="hero">
        <div class="core-glow"></div>
        <div class="hero-slogan">
          {{ sloganText }}<span class="cursor" v-show="sloganTyping">▋</span>
        </div>
      </div>
    </div>

    <!-- 底部三按钮 -->
    <div class="actions" :class="{ dimmed: panelOpen }">
      <button class="act" @click="openPanel">
        <span class="act-dot"></span>登录
      </button>
      <a class="act" href="https://github.com/RongSec/CyberLook" target="_blank" rel="noreferrer">
        <span class="act-dot"></span>项目主页
      </a>
    </div>

    <!-- 点击左侧空白关闭 -->
    <div v-if="panelOpen" class="panel-mask" @click="closePanel"></div>

    <!-- 右侧登录抽屉（与页面等高，向左滑出） -->
    <transition name="slide">
      <aside v-if="panelOpen" class="panel">
        <button class="panel-close" @click="closePanel" aria-label="close">×</button>

        <div class="panel-head">
          <div class="panel-mark"></div>
          <div class="panel-slogan">见你未见　照你所患</div>
        </div>

        <div class="panel-body">
          <label class="field">
            <span class="k">请输入用户名：</span>
            <input
              ref="userRef"
              v-model="form.username"
              class="ipt"
              autocomplete="off"
              autocapitalize="off"
              spellcheck="false"
              @keyup.enter="pwdRef?.focus()"
            />
          </label>

          <label class="field">
            <span class="k">请输入密码：</span>
            <input
              ref="pwdRef"
              v-model="form.password"
              type="password"
              class="ipt"
              autocomplete="new-password"
              spellcheck="false"
              @keyup.enter="handleLogin"
            />
          </label>

          <label class="field">
            <span class="k">请输入验证码：</span>
            <div class="cap-row">
              <input
                ref="capRef"
                v-model="form.captcha"
                class="ipt cap-input"
                autocomplete="off"
                spellcheck="false"
                @keyup.enter="handleLogin"
              />
              <canvas
                ref="capCanvas"
                class="cap-canvas"
                width="118"
                height="34"
                @click="refreshCaptcha"
              ></canvas>
            </div>
          </label>

          <div class="hint">
            <span v-if="!loading">回车登录</span>
            <span v-else class="loading">正在验证<span class="dots">...</span></span>
          </div>

          <div v-if="errorMsg" class="err">{{ errorMsg }}</div>

          <button class="submit" :disabled="loading" @click="handleLogin">
            {{ loading ? '正在验证' : '登录' }}
          </button>
        </div>
      </aside>
    </transition>

    <!-- 登录成功光爆 -->
    <div v-if="burst" class="burst" :style="{ left: burstPos.x + 'px', top: burstPos.y + 'px' }"></div>
  </div>
</template>

<script setup>
import { ref, nextTick, onMounted, onBeforeUnmount } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '../store/auth'
import { useThemeStore } from '../store/theme'

const router = useRouter()
const auth = useAuthStore()
const themeStore = useThemeStore()

const stageRef = ref(null)
const fxRef = ref(null)
const userRef = ref(null)
const pwdRef = ref(null)
const capRef = ref(null)
const capCanvas = ref(null)

const form = ref({ username: '', password: '', captcha: '' })
const loading = ref(false)
const errorMsg = ref('')
const panelOpen = ref(false)
const timers = []

/* ===== 资产条目（全屏散布） ===== */
const assets = [
  { ip: '192.0.2.2:80', name: '门户网站', x: 6, y: 14 },
  { ip: '192.0.2.2:443', name: 'OA 系统', x: 19, y: 9 },
  { ip: '192.0.2.4:8080', name: '运维平台', x: 33, y: 16 },
  { ip: '192.0.2.6:3306', name: 'MySQL 主库', x: 47, y: 8 },
  { ip: '192.0.2.8:22', name: '堡垒机', x: 62, y: 15 },
  { ip: '192.0.2.10:6379', name: 'Redis 缓存', x: 76, y: 10 },
  { ip: '192.0.2.12:9200', name: 'ES 集群', x: 88, y: 18 },
  { ip: '192.0.2.14:8080', name: '邮件网关', x: 11, y: 30 },
  { ip: '192.0.2.16:443', name: '财务系统', x: 25, y: 26 },
  { ip: '192.0.2.18:80', name: 'HR 平台', x: 40, y: 32 },
  { ip: '192.0.2.20:8443', name: 'VPN 网关', x: 55, y: 24 },
  { ip: '192.0.2.22:8080', name: '工单系统', x: 70, y: 31 },
  { ip: '192.0.2.24:5432', name: 'PostgreSQL', x: 84, y: 26 },
  { ip: '192.0.2.26:9200', name: '日志平台', x: 7, y: 47 },
  { ip: '192.0.2.28:2375', name: '容器宿主机', x: 21, y: 55 },
  { ip: '192.0.2.30:8080', name: '报表服务', x: 35, y: 45 },
  { ip: '192.0.2.32:80', name: 'DNS 主节点', x: 50, y: 52 },
  { ip: '192.0.2.34:22', name: '跳板机', x: 65, y: 44 },
  { ip: '192.0.2.36:8080', name: '代码仓库', x: 79, y: 50 },
  { ip: '192.0.2.38:9000', name: '对象存储', x: 92, y: 42 },
  { ip: '192.0.2.40:5000', name: '镜像仓库', x: 14, y: 68 },
  { ip: '192.0.2.42:7001', name: '中间件', x: 29, y: 74 },
  { ip: '192.0.2.44:6379', name: '会话缓存', x: 44, y: 66 },
  { ip: '192.0.2.46:3306', name: '只读从库', x: 58, y: 73 },
  { ip: '192.0.2.48:8080', name: '接口网关', x: 72, y: 64 },
  { ip: '192.0.2.50:443', name: '支付回调', x: 87, y: 71 },
  { ip: '192.0.2.52:22', name: '运维跳板', x: 9, y: 85 },
  { ip: '192.0.2.54:8080', name: '调度中心', x: 24, y: 90 },
  { ip: '192.0.2.56:9090', name: '监控采集', x: 39, y: 84 },
  { ip: '192.0.2.58:8081', name: '配置中心', x: 54, y: 91 },
  { ip: '192.0.2.60:8500', name: 'Consul 集群', x: 69, y: 86 },
  { ip: '192.0.2.62:8080', name: '文件服务', x: 83, y: 92 },
  { ip: '192.0.2.64:80', name: '静态站点', x: 95, y: 82 },
  { ip: '192.0.2.3:8080', name: '统一认证', x: 13, y: 20 },
  { ip: '192.0.2.5:3306', name: '业务库', x: 28, y: 12 },
  { ip: '192.0.2.7:443', name: '支付网关', x: 41, y: 21 },
  { ip: '192.0.2.9:6379', name: '缓存集群', x: 56, y: 12 },
  { ip: '192.0.2.11:9200', name: '搜索服务', x: 69, y: 22 },
  { ip: '192.0.2.13:8080', name: '流程引擎', x: 82, y: 14 },
  { ip: '192.0.2.15:22', name: '网络设备', x: 17, y: 39 },
  { ip: '192.0.2.17:8443', name: 'SSL 网关', x: 45, y: 39 },
  { ip: '192.0.2.19:5432', name: '数据仓库', x: 74, y: 38 },
  { ip: '192.0.2.21:8080', name: '消息队列', x: 8, y: 58 },
  { ip: '192.0.2.23:9000', name: '备份存储', x: 62, y: 57 },
  { ip: '192.0.2.25:80', name: '官网集群', x: 90, y: 60 },
]
const assetEls = ref([])

/* ===== 粒子 + 资产照亮 ===== */
const mouse = { x: -9999, y: -9999 }
const cur = { x: -9999, y: -9999 }
const trail = []
const particles = []
let rafId = null
let lastT = 0
const BREATH_MS = 4200

function spawnParticles(x, y, count) {
  for (let i = 0; i < count; i++) {
    particles.push({
      x: x + (Math.random() - 0.5) * 10,
      y: y + (Math.random() - 0.5) * 10,
      size: Math.random() * 4.2 + 1.2,
      speedX: Math.random() * 3 - 1.5,
      speedY: Math.random() * 3 - 1.5,
      hue: 38 + Math.random() * 14,
    })
  }
  if (particles.length > 520) particles.splice(0, particles.length - 520)
}

function updateParticles(ctx) {
  for (let i = particles.length - 1; i >= 0; i--) {
    const p = particles[i]
    p.x += p.speedX
    p.y += p.speedY
    p.speedX *= 0.986
    p.speedY = p.speedY * 0.986 + 0.022
    p.size -= 0.085
    if (p.size <= 0.3) { particles.splice(i, 1); continue }
    const life = Math.min(1, p.size / 3.2)
    const g = ctx.createRadialGradient(p.x, p.y, 0, p.x, p.y, p.size * 3.4)
    g.addColorStop(0, `hsla(${p.hue}, 100%, 80%, ${0.8 * life})`)
    g.addColorStop(0.34, `hsla(${p.hue}, 100%, 62%, ${0.3 * life})`)
    g.addColorStop(1, `hsla(${p.hue}, 100%, 50%, 0)`)
    ctx.fillStyle = g
    ctx.beginPath()
    ctx.arc(p.x, p.y, p.size * 3.4, 0, Math.PI * 2)
    ctx.fill()
    ctx.fillStyle = `hsla(${p.hue}, 100%, 92%, ${0.92 * life})`
    ctx.beginPath()
    ctx.arc(p.x, p.y, p.size * 0.62, 0, Math.PI * 2)
    ctx.fill()
  }
}

let logoCX = 0
let logoCY = 0

function tick(t) {
  const canvas = fxRef.value
  if (!canvas) return
  lastT = t

  const W = canvas.width = window.innerWidth
  const H = canvas.height = window.innerHeight
  const ctx = canvas.getContext('2d')
  ctx.clearRect(0, 0, W, H)

  // 呼吸相位 0..1
  const phase = (performance.now() % BREATH_MS) / BREATH_MS
  const breath = 0.5 + 0.5 * Math.sin(phase * Math.PI * 2)

  // 鼠标光点（面板打开时隐藏）
  if (!panelOpen.value) {
    cur.x += (mouse.x - cur.x) * 0.32
    cur.y += (mouse.y - cur.y) * 0.32
    trail.push({ x: cur.x, y: cur.y })
    if (trail.length > 22) trail.shift()

    for (let i = 0; i < trail.length; i++) {
      const tt = i / trail.length
      ctx.beginPath()
      ctx.arc(trail[i].x, trail[i].y, 0.7 + tt * 3.4, 0, Math.PI * 2)
      ctx.fillStyle = `rgba(251, 191, 36, ${(tt * tt * 0.34).toFixed(3)})`
      ctx.fill()
    }

    const cg = ctx.createRadialGradient(cur.x, cur.y, 0, cur.x, cur.y, 32)
    cg.addColorStop(0, 'rgba(255, 251, 235, 0.95)')
    cg.addColorStop(0.22, 'rgba(253, 230, 138, 0.52)')
    cg.addColorStop(0.55, 'rgba(251, 191, 36, 0.16)')
    cg.addColorStop(1, 'rgba(251, 191, 36, 0)')
    ctx.fillStyle = cg
    ctx.beginPath()
    ctx.arc(cur.x, cur.y, 32, 0, Math.PI * 2)
    ctx.fill()

    ctx.fillStyle = '#fffbeb'
    ctx.beginPath()
    ctx.arc(cur.x, cur.y, 3.2, 0, Math.PI * 2)
    ctx.fill()
  } else {
    trail.length = 0
  }

  updateParticles(ctx)
  updateAssets(breath)

  rafId = requestAnimationFrame(tick)
}

function updateAssets(breath) {
  for (let i = 0; i < assetEls.value.length; i++) {
    const el = assetEls.value[i]
    if (!el) continue
    const r = el.getBoundingClientRect()
    const cx = r.left + r.width / 2
    const cy = r.top + r.height / 2

    // 鼠标照亮
    const dm = Math.hypot(cx - mouse.x, cy - mouse.y)
    const litMouse = Math.max(0, 1 - dm / 300)

    // 呼吸灯照亮（半径更大，随呼吸强弱变化）
    const dl = Math.hypot(cx - logoCX, cy - logoCY)
    const reach = 460 + breath * 240
    const litBreath = Math.max(0, 1 - dl / reach) * (0.34 + 0.66 * breath)

    let lit = Math.max(litMouse, litBreath)
    // 文字保护圈（椭圆）：覆盖整条标语，避免资产文字穿过
    const ex = (cx - logoCX) / 380
    const ey = (cy - logoCY) / 150
    const dText = Math.sqrt(ex * ex + ey * ey)
    if (dText < 1) lit *= Math.max(0.06, dText)
    const eased = lit * lit * (3 - 2 * lit)
    el.style.opacity = (0.05 + eased * 0.95).toFixed(3)
    el.style.filter = `blur(${((1 - eased) * 1.8).toFixed(2)}px)`
  }
}

function onMove(e) {
  if (panelOpen.value) return
  const nx = e.clientX
  const ny = e.clientY
  const dx = nx - mouse.x
  const dy = ny - mouse.y
  const dist = Math.hypot(dx, dy)
  if (dist < 4000) {
    const steps = Math.min(6, Math.max(1, Math.round(dist / 14)))
    for (let i = 1; i <= steps; i++) {
      spawnParticles(mouse.x + (dx * i) / steps, mouse.y + (dy * i) / steps, 2)
    }
  }
  mouse.x = nx
  mouse.y = ny
}

/* ===== Slogan 逐字打字 ===== */
const sloganText = ref('')
const sloganTyping = ref(true)
let sloganTimer = null

function typeSlogan() {
  const full = '见你未见　照你所患'
  if (sloganTimer) clearInterval(sloganTimer)
  sloganText.value = ''
  sloganTyping.value = true
  let i = 0
  sloganTimer = setInterval(() => {
    if (i < full.length) {
      sloganText.value = full.slice(0, i + 1)
      i++
    } else {
      sloganTyping.value = false
      clearInterval(sloganTimer)
      sloganTimer = null
    }
  }, 150)
}

/* ===== 面板 ===== */
function openPanel() {
  panelOpen.value = true
  nextTick(() => {
    refreshCaptcha()
    userRef.value?.focus()
  })
}

function closePanel() {
  panelOpen.value = false
  form.value.password = ''
  form.value.captcha = ''
  errorMsg.value = ''
}

/* ===== 验证码 ===== */
const captchaCode = ref('')

function refreshCaptcha() {
  const canvas = capCanvas.value
  if (!canvas) return
  const ctx = canvas.getContext('2d')
  const w = canvas.width
  const h = canvas.height

  // 关键：先彻底清空，避免多次刷新叠加
  ctx.clearRect(0, 0, w, h)
  ctx.fillStyle = 'rgba(255,255,255,0.04)'
  ctx.fillRect(0, 0, w, h)

  for (let i = 0; i < 3; i++) {
    ctx.strokeStyle = 'rgba(255,255,255,0.16)'
    ctx.beginPath()
    ctx.moveTo(Math.random() * w, Math.random() * h)
    ctx.lineTo(Math.random() * w, Math.random() * h)
    ctx.stroke()
  }

  const chars = 'ABCDEFGHJKMNPQRSTUVWXYZ23456789'
  let code = ''
  for (let i = 0; i < 4; i++) code += chars[Math.floor(Math.random() * chars.length)]
  captchaCode.value = code

  ctx.font = 'bold 22px "MiSans", Arial, sans-serif'
  ctx.fillStyle = '#fbbf24'
  ctx.textAlign = 'center'
  ctx.textBaseline = 'middle'
  for (let i = 0; i < code.length; i++) {
    ctx.save()
    ctx.translate(w / 2 + (i - 1.5) * 24, h / 2)
    ctx.rotate((Math.random() - 0.5) * 0.36)
    ctx.fillText(code[i], 0, 0)
    ctx.restore()
  }
}

/* ===== 登录 ===== */
const burst = ref(false)
const burstPos = ref({ x: 0, y: 0 })

async function handleLogin() {
  if (loading.value) return
  if (!form.value.username || !form.value.password) {
    errorMsg.value = '用户名和密码不能为空'
    return
  }
  if (form.value.captcha.toUpperCase() !== captchaCode.value) {
    errorMsg.value = '验证码错误'
    refreshCaptcha()
    form.value.captcha = ''
    return
  }
  errorMsg.value = ''
  loading.value = true
  try {
    await auth.login(form.value.username, form.value.password)
    burstPos.value = { x: window.innerWidth * 0.14, y: window.innerHeight * 0.12 }
    burst.value = true
    timers.push(setTimeout(() => {
      try { themeStore.setTheme('light') } catch {}
      router.push('/dashboard').catch(() => { window.location.href = '/dashboard' })
    }, 700))
  } catch (e) {
    errorMsg.value = '用户名或密码错误'
    form.value.password = ''
    refreshCaptcha()
    form.value.captcha = ''
    nextTick(() => pwdRef.value?.focus())
    loading.value = false
  }
}

onMounted(() => {
  typeSlogan()
  // 呼吸光源固定在画面中心（无形光核）
  const measure = () => {
    logoCX = window.innerWidth / 2
    logoCY = window.innerHeight / 2 - window.innerHeight * 0.02
  }
  measure()
  window.addEventListener('resize', measure)

  rafId = requestAnimationFrame(tick)
  window.addEventListener('mousemove', onMove, { passive: true })

  const q = new URLSearchParams(location.search)
  // ?panel=1 可直接打开登录抽屉
  if (q.get('panel') === '1') {
    setTimeout(openPanel, 600)
  }
})

onBeforeUnmount(() => {
  timers.forEach(clearTimeout)
  if (rafId) cancelAnimationFrame(rafId)
  if (sloganTimer) clearInterval(sloganTimer)
  window.removeEventListener('mousemove', onMove)
  window.removeEventListener('resize', () => {})
})
</script>

<style scoped>
.stage {
  position: relative;
  min-height: 100vh;
  background: #06080a;
  overflow: hidden;
  font-family: 'MiSans', 'PingFang SC', 'Microsoft YaHei', sans-serif;
}

/* ===== 背景层（面板打开时模糊） ===== */
.backdrop {
  position: absolute;
  inset: 0;
  transition: filter 0.45s cubic-bezier(0.22, 1, 0.36, 1),
              transform 0.45s cubic-bezier(0.22, 1, 0.36, 1),
              opacity 0.45s ease;
}
.backdrop.blurred {
  filter: blur(14px) brightness(0.68) saturate(0.9);
  transform: scale(1.03);
  pointer-events: none;
}

.asset-layer {
  position: absolute;
  inset: 0;
  z-index: 1;
  pointer-events: none;
}
.asset {
  position: absolute;
  display: flex;
  align-items: baseline;
  gap: 6px;
  font-size: 11.5px;
  white-space: nowrap;
  color: #fde68a;
  opacity: 0.05;
  filter: blur(1.8px);
  transition: opacity 0.12s linear, filter 0.12s linear;
  will-change: opacity, filter;
}
.ip { color: rgba(253, 230, 138, 0.72); }
.nm { color: rgba(255, 251, 235, 0.92); }

.fx {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  z-index: 2;
  pointer-events: none;
}

/* ===== 中心呼吸灯 ===== */
.hero {
  position: absolute;
  inset: 0;
  z-index: 3;
  display: grid;
  place-items: center;
  pointer-events: none;
}

/* 无形光核：只有光，没有实体图形 */
.core-glow {
  position: absolute;
  top: 50%;
  left: 50%;
  margin-top: calc(-1 * clamp(60px, 7.5vw, 90px));
  margin-left: calc(-1 * clamp(60px, 7.5vw, 90px));
  width: clamp(120px, 15vw, 180px);
  height: clamp(120px, 15vw, 180px);
  border-radius: 50%;
  pointer-events: none;
  background: radial-gradient(circle,
    rgba(214, 246, 250, 0.22) 0%,
    rgba(168, 232, 236, 0.12) 38%,
    rgba(168, 232, 236, 0.02) 62%,
    transparent 74%);
  filter: blur(12px);
  animation: coreBreathe 4.2s ease-in-out infinite;
  will-change: transform, opacity;
}
@keyframes coreBreathe {
  0%, 100% { transform: scale(0.86); opacity: 0.55; }
  50% { transform: scale(1.10); opacity: 1; }
}
@keyframes breathe {
  0%, 100% {
    transform: scale(0.95);
    opacity: 0.75;
    filter: drop-shadow(0 0 18px rgba(168, 232, 236, 0.35));
  }
  50% {
    transform: scale(1.03);
    opacity: 1;
    filter: drop-shadow(0 0 46px rgba(168, 232, 236, 0.75))
            drop-shadow(0 0 90px rgba(120, 200, 220, 0.28));
  }
}

.hero-slogan {
  position: relative;
  z-index: 1;
  /* mimo.mi.com 同款标题字体 */
  font-family: 'LXGW WenKai TC', 'MiSans', sans-serif;
  font-size: clamp(28px, 3.3vw, 46px);
  font-weight: 300;
  letter-spacing: 0.02em;
  text-indent: 0.02em;
  color: #fdf8ec;
  text-shadow: 0 0 26px rgba(251, 191, 36, 0.26), 0 1px 2px rgba(0, 0, 0, 0.5);
  white-space: pre;
  min-height: 1.4em;
}
.cursor { color: #fbbf24; animation: blink 1s steps(1) infinite; }
@keyframes blink { 50% { opacity: 0; } }

/* ===== 底部三按钮 ===== */
.actions {
  position: absolute;
  left: 50%;
  bottom: 9vh;
  transform: translateX(-50%);
  z-index: 4;
  display: flex;
  align-items: center;
  gap: 10px;
  transition: opacity 0.35s ease, transform 0.35s ease;
}
.actions.dimmed {
  opacity: 0;
  transform: translateX(-50%) translateY(8px);
  pointer-events: none;
}

.act {
  display: inline-flex;
  align-items: center;
  gap: 9px;
  padding: 14px 30px;
  font-family: inherit;
  font-size: 15px;
  letter-spacing: 0.08em;
  color: rgba(255, 248, 226, 0.82);
  background: rgba(255, 255, 255, 0.03);
  border: 1px solid rgba(255, 255, 255, 0.12);
  border-radius: 999px;
  cursor: pointer;
  text-decoration: none;
  transition: all 0.25s ease;
  backdrop-filter: blur(6px);
}
.act:hover {
  color: #fff8e2;
  border-color: rgba(251, 191, 36, 0.55);
  background: rgba(251, 191, 36, 0.1);
  box-shadow: 0 0 24px -8px rgba(251, 191, 36, 0.7);
  transform: translateY(-1px);
}
.act-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: #7fe3ea;
  box-shadow: 0 0 10px 2px rgba(127, 227, 234, 0.8);
  animation: dotPulse 2.1s ease-in-out infinite;
}
@keyframes dotPulse {
  0%, 100% { opacity: 0.45; }
  50% { opacity: 1; }
}
.act-ico { opacity: 0.85; }

/* ===== 语言菜单 ===== */
.lang-menu {
  position: absolute;
  left: 50%;
  bottom: calc(9vh + 52px);
  transform: translateX(-50%);
  z-index: 6;
  display: flex;
  flex-direction: column;
  min-width: 140px;
  padding: 6px;
  background: rgba(10, 14, 18, 0.94);
  border: 1px solid rgba(255, 255, 255, 0.12);
  border-radius: 12px;
  backdrop-filter: blur(14px);
  box-shadow: 0 24px 60px -20px rgba(0, 0, 0, 0.9);
}
.lang-item {
  padding: 9px 14px;
  font-family: inherit;
  font-size: 13px;
  text-align: left;
  color: rgba(255, 248, 226, 0.78);
  background: transparent;
  border: none;
  border-radius: 8px;
  cursor: pointer;
  transition: all 0.18s ease;
}
.lang-item:hover { background: rgba(255, 255, 255, 0.07); color: #fff; }
.lang-item.on { color: #fbbf24; background: rgba(251, 191, 36, 0.1); }

/* 左侧点击关闭（透明遮罩，不遮挡视觉） */
.panel-mask {
  position: fixed;
  inset: 0;
  z-index: 10;
  background: transparent;
  cursor: pointer;
}

/* ===== 右侧登录抽屉 ===== */
.panel {
  position: fixed;
  top: 0;
  right: 0;
  z-index: 20;
  width: min(452px, 92vw);
  height: 100vh;
  box-sizing: border-box;
  display: flex;
  flex-direction: column;
  padding: 44px 46px 40px;
  background: linear-gradient(160deg, rgba(14, 20, 26, 0.96), rgba(8, 11, 14, 0.98));
  border-left: 1px solid rgba(255, 255, 255, 0.1);
  box-shadow: -40px 0 100px -30px rgba(0, 0, 0, 0.95);
  overflow-y: auto;
  cursor: auto;
}
.panel::before {
  content: '';
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  height: 1px;
  background: linear-gradient(90deg, transparent, rgba(251, 191, 36, 0.5), transparent);
}

.panel-close {
  position: absolute;
  top: 18px;
  right: 18px;
  width: 30px;
  height: 30px;
  display: grid;
  place-items: center;
  font-size: 20px;
  line-height: 1;
  color: rgba(255, 248, 226, 0.55);
  background: transparent;
  border: 1px solid rgba(255, 255, 255, 0.1);
  border-radius: 50%;
  cursor: pointer;
  transition: all 0.2s ease;
}
.panel-close:hover {
  color: #fff;
  border-color: rgba(251, 191, 36, 0.5);
  transform: rotate(90deg);
}

.panel-head {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 22px;
  padding: 18px 0 34px;
}
.panel-mark {
  width: 44px;
  height: 2px;
  border-radius: 2px;
  background: linear-gradient(90deg, transparent, rgba(127, 227, 234, 0.9), transparent);
  box-shadow: 0 0 18px 2px rgba(127, 227, 234, 0.45);
  animation: markPulse 4.2s ease-in-out infinite;
}
@keyframes markPulse {
  0%, 100% { opacity: 0.5; transform: scaleX(0.8); }
  50% { opacity: 1; transform: scaleX(1); }
}
.panel-slogan {
  font-family: 'LXGW WenKai TC', 'MiSans', sans-serif;
  font-weight: 300;
  font-size: 24px;
  font-weight: 600;
  letter-spacing: 0.2em;
  text-indent: 0.2em;
  text-align: center;
  color: rgba(255, 248, 226, 0.86);
  text-shadow: 0 0 18px rgba(251, 191, 36, 0.24);
}

.panel-body {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.field {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.k {
  font-size: 12.5px;
  letter-spacing: 0.06em;
  color: rgba(180, 170, 148, 0.9);
}
.ipt {
  width: 100%;
  box-sizing: border-box;
  padding: 11px 13px;
  font-family: inherit;
  font-size: 14px;
  color: #fdf6e3;
  background: rgba(255, 255, 255, 0.035);
  border: 1px solid rgba(255, 255, 255, 0.1);
  border-radius: 9px;
  outline: none;
  caret-color: #fbbf24;
  transition: border-color 0.22s ease, box-shadow 0.22s ease, background 0.22s ease;
}
.ipt::placeholder { color: rgba(255, 255, 255, 0.22); }
.ipt:focus {
  border-color: rgba(251, 191, 36, 0.6);
  background: rgba(251, 191, 36, 0.045);
  box-shadow: 0 0 0 3px rgba(251, 191, 36, 0.1);
}
.ipt:-webkit-autofill,
.ipt:-webkit-autofill:hover,
.ipt:-webkit-autofill:focus {
  -webkit-text-fill-color: #fdf6e3 !important;
  -webkit-box-shadow: 0 0 0 1000px #0b0f13 inset !important;
  transition: background-color 5000s ease-in-out 0s;
}

.cap-row {
  display: flex;
  align-items: center;
  gap: 10px;
}
.cap-input { flex: 1; text-transform: uppercase; letter-spacing: 0.16em; }
.cap-canvas {
  flex: none;
  border: 1px solid rgba(255, 255, 255, 0.12);
  border-radius: 9px;
  cursor: pointer;
  background: rgba(255, 255, 255, 0.02);
  transition: border-color 0.2s ease;
}
.cap-canvas:hover { border-color: rgba(251, 191, 36, 0.5); }

.hint {
  font-size: 12px;
  color: rgba(140, 132, 116, 0.9);
  min-height: 16px;
}
.loading { color: #fbbf24; }
.dots { animation: blink 1s steps(1) infinite; }
.err { font-size: 12.5px; color: #f87171; }

.submit {
  margin-top: 4px;
  padding: 12px 20px;
  font-family: inherit;
  font-size: 14.5px;
  font-weight: 600;
  letter-spacing: 0.14em;
  text-indent: 0.14em;
  color: #0a0d10;
  background: linear-gradient(100deg, #f7cf6a, #fbbf24 55%, #e8a41c);
  border: none;
  border-radius: 10px;
  cursor: pointer;
  transition: filter 0.22s ease, transform 0.22s ease, box-shadow 0.22s ease;
}
.submit:hover:not(:disabled) {
  filter: brightness(1.08);
  transform: translateY(-1px);
  box-shadow: 0 14px 34px -14px rgba(251, 191, 36, 0.9);
}
.submit:disabled { opacity: 0.55; cursor: not-allowed; }

/* 抽屉滑入 */
.slide-enter-active,
.slide-leave-active { transition: transform 0.46s cubic-bezier(0.22, 1, 0.36, 1); }
.slide-enter-from,
.slide-leave-to { transform: translateX(100%); }

.fade-enter-active,
.fade-leave-active { transition: opacity 0.2s ease; }
.fade-enter-from,
.fade-leave-to { opacity: 0; }

/* ===== 登录光爆 ===== */
.burst {
  position: fixed;
  width: 14px;
  height: 14px;
  margin: -7px 0 0 -7px;
  border-radius: 50%;
  background: radial-gradient(circle,
    #ffffff 0%, #f7fbfd 20%, #e2f2f6 40%,
    rgba(190, 226, 236, 0.5) 62%, rgba(190, 226, 236, 0) 80%);
  transform: scale(0);
  animation: burstOut 1.05s cubic-bezier(0.22, 1, 0.36, 1) forwards;
  pointer-events: none;
  z-index: 99;
  filter: blur(1px);
}
@keyframes burstOut {
  0% { transform: scale(0); opacity: 1; }
  55% { transform: scale(90); opacity: 1; }
  100% { transform: scale(200); opacity: 1; }
}

@media (max-width: 900px) {
  .asset { font-size: 10px; }
  .panel { padding: 40px 26px 32px; }
  .actions { bottom: 7vh; gap: 8px; }
  .act { padding: 9px 15px; font-size: 12.5px; }
}
</style>
