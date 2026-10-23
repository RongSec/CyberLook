<template>
  <div class="stage" ref="stageRef">
    <!-- 资产条目层（全屏散布，鼠标靠近才被照亮） -->
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

    <!-- 终端内容（无边框） -->
    <div class="content">
      <div class="console">
        <div class="cmdline">
          <span class="ps">beacon@insight&gt;</span><span class="cmd">dongruo.exe --light</span>
        </div>
        <div class="slogan">见你未见 · 照你所患</div>
        <pre class="boot">{{ bootText }}<span class="cursor" v-show="typing">▋</span></pre>
      </div>

      <div class="login-area">
        <div class="line">
          <span class="ps">beacon@insight&gt;</span>
          <span class="k">请输入用户名：</span>
          <input ref="userRef" v-model="form.username" class="ipt" autocomplete="off"
            autocapitalize="off" spellcheck="false" @keyup.enter="focusPwd" />
        </div>
        <div class="line">
          <span class="k pad">请输入密码：</span>
          <input ref="pwdRef" v-model="form.password" type="password" class="ipt" autocomplete="new-password"
            spellcheck="false" @keyup.enter="handleLogin" />
        </div>
        <div class="line hint">
          <span v-if="!loading">↵ 回车登录</span>
          <span v-else class="loading">正在验证<span class="dots">...</span></span>
        </div>
        <div class="line err" v-if="errorMsg">{{ errorMsg }}</div>
      </div>
    </div>

    <!-- 登录：从鼠标位置爆开的光 -->
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

const bootText = ref('')
const typing = ref(true)
const form = ref({ username: '', password: '' })
const loading = ref(false)
const errorMsg = ref('')
const timers = []

const bootLines = ['  [ OK ] 灯塔已点亮，等待授权。']

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
  { ip: '192.0.2.2:80', name: '门户网站', x: 13, y: 26 },
  { ip: '192.0.2.2:443', name: 'OA 系统', x: 26, y: 21 },
  { ip: '192.0.2.4:8080', name: '运维平台', x: 40, y: 28 },
  { ip: '192.0.2.6:3306', name: 'MySQL 主库', x: 54, y: 20 },
  { ip: '192.0.2.8:22', name: '堡垒机', x: 69, y: 27 },
  { ip: '192.0.2.10:6379', name: 'Redis 缓存', x: 83, y: 22 },
  { ip: '192.0.2.12:9200', name: 'ES 集群', x: 0, y: 30 },
  { ip: '192.0.2.14:8080', name: '邮件网关', x: 18, y: 42 },
  { ip: '192.0.2.16:443', name: '财务系统', x: 32, y: 38 },
  { ip: '192.0.2.18:80', name: 'HR 平台', x: 47, y: 44 },
  { ip: '192.0.2.20:8443', name: 'VPN 网关', x: 62, y: 36 },
  { ip: '192.0.2.22:8080', name: '工单系统', x: 77, y: 43 },
  { ip: '192.0.2.24:5432', name: 'PostgreSQL', x: 91, y: 38 },
  { ip: '192.0.2.26:9200', name: '日志平台', x: 14, y: 59 },
  { ip: '192.0.2.28:2375', name: '容器宿主机', x: 28, y: 45 },
  { ip: '192.0.2.30:8080', name: '报表服务', x: 42, y: 57 },
  { ip: '192.0.2.32:80', name: 'DNS 主节点', x: 57, y: 42 },
  { ip: '192.0.2.34:22', name: '跳板机', x: 72, y: 56 },
  { ip: '192.0.2.36:8080', name: '代码仓库', x: 86, y: 40 },
  { ip: '192.0.2.38:9000', name: '对象存储', x: 4, y: 54 },
  { ip: '192.0.2.40:5000', name: '镜像仓库', x: 21, y: 58 },
  { ip: '192.0.2.42:7001', name: '中间件', x: 36, y: 64 },
  { ip: '192.0.2.44:6379', name: '会话缓存', x: 51, y: 56 },
  { ip: '192.0.2.46:3306', name: '只读从库', x: 65, y: 63 },
  { ip: '192.0.2.48:8080', name: '接口网关', x: 79, y: 54 },
  { ip: '192.0.2.50:443', name: '支付回调', x: 94, y: 61 },
  { ip: '192.0.2.52:22', name: '运维跳板', x: 16, y: 75 },
  { ip: '192.0.2.54:8080', name: '调度中心', x: 31, y: 80 },
  { ip: '192.0.2.56:9090', name: '监控采集', x: 46, y: 74 },
  { ip: '192.0.2.58:8081', name: '配置中心', x: 61, y: 81 },
  { ip: '192.0.2.60:8500', name: 'Consul 集群', x: 76, y: 76 },
  { ip: '192.0.2.62:8080', name: '文件服务', x: 90, y: 82 },
  { ip: '192.0.2.64:80', name: '静态站点', x: 7, y: 72 },
  { ip: '192.0.2.3:8080', name: '统一认证', x: 20, y: 32 },
  { ip: '192.0.2.5:3306', name: '业务库', x: 35, y: 24 },
  { ip: '192.0.2.7:443', name: '支付网关', x: 48, y: 33 },
  { ip: '192.0.2.9:6379', name: '缓存集群', x: 63, y: 24 },
  { ip: '192.0.2.11:9200', name: '搜索服务', x: 76, y: 34 },
  { ip: '192.0.2.13:8080', name: '流程引擎', x: 89, y: 26 },
  { ip: '192.0.2.15:22', name: '网络设备', x: 24, y: 51 },
  { ip: '192.0.2.17:8443', name: 'SSL 网关', x: 52, y: 51 },
  { ip: '192.0.2.19:5432', name: '数据仓库', x: 81, y: 50 },
  { ip: '192.0.2.21:8080', name: '消息队列', x: 15, y: 48 },
  { ip: '192.0.2.23:9000', name: '备份存储', x: 69, y: 47 },
]

const assetEls = ref([])

/* ===== Canvas 粒子跟随（博客园经典：迸发 → 飘散 → 缩小 → 消失） ===== */
const mouse = { x: -9999, y: -9999 }
const cur = { x: 0, y: 0 }
const trail = []
const particles = []
let rafId = null
let lastT = 0

function spawnParticles(x, y, count) {
  for (let i = 0; i < count; i++) {
    particles.push({
      x: x + (Math.random() - 0.5) * 10,
      y: y + (Math.random() - 0.5) * 10,
      size: Math.random() * 4.2 + 1.2,
      speedX: Math.random() * 3 - 1.5,
      speedY: Math.random() * 3 - 1.5,
      hue: 38 + Math.random() * 14
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

    if (p.size <= 0.3) {
      particles.splice(i, 1)
      continue
    }
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

function tick(t) {
  const canvas = fxRef.value
  if (!canvas) return
  const dt = Math.min((t - lastT) / 16.67, 2.2) || 1
  lastT = t

  const W = canvas.width = window.innerWidth
  const H = canvas.height = window.innerHeight
  const ctx = canvas.getContext('2d')
  ctx.clearRect(0, 0, W, H)

  /* ===== 主光标：光点 + 拖尾 ===== */
  cur.x += (mouse.x - cur.x) * 0.32
  cur.y += (mouse.y - cur.y) * 0.32
  trail.push({ x: cur.x, y: cur.y })
  if (trail.length > 22) trail.shift()

  for (let i = 0; i < trail.length; i++) {
    const t = i / trail.length
    ctx.beginPath()
    ctx.arc(trail[i].x, trail[i].y, 0.7 + t * 3.4, 0, Math.PI * 2)
    ctx.fillStyle = `rgba(251, 191, 36, ${(t * t * 0.34).toFixed(3)})`
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

  updateParticles(ctx)

  updateAssets()
  rafId = requestAnimationFrame(tick)
}

/* 鼠标附近 → 资产条目变亮变清晰 */
function updateAssets() {
  for (let i = 0; i < assetEls.value.length; i++) {
    const el = assetEls.value[i]
    if (!el) continue
    const r = el.getBoundingClientRect()
    const cx = r.left + r.width / 2
    const cy = r.top + r.height / 2
    const d = Math.hypot(cx - mouse.x, cy - mouse.y)
    const lit = Math.max(0, 1 - d / 300)
    const eased = lit * lit * (3 - 2 * lit)
    el.style.opacity = (0.05 + eased * 0.95).toFixed(3)
    el.style.filter = `blur(${((1 - eased) * 1.8).toFixed(2)}px)`
  }
}

function onMove(e) {
  const nx = e.clientX
  const ny = e.clientY
  const dx = nx - mouse.x
  const dy = ny - mouse.y
  const dist = Math.hypot(dx, dy)
  const steps = Math.min(6, Math.max(1, Math.round(dist / 14)))
  for (let i = 1; i <= steps; i++) {
    spawnParticles(mouse.x + (dx * i) / steps, mouse.y + (dy * i) / steps, 2)
  }
  mouse.x = nx
  mouse.y = ny
}

/* ===== 登录 ===== */
const burst = ref(false)
const burstPos = ref({ x: 0, y: 0 })

function typeBoot() {
  let li = 0, ci = 0
  const step = () => {
    if (li >= bootLines.length) {
      typing.value = false
      nextTick(() => userRef.value?.focus())
      return
    }
    const line = bootLines[li]
    if (ci < line.length) {
      bootText.value += line[ci++]
      timers.push(setTimeout(step, 9))
    } else {
      bootText.value += '\n'
      li++
      ci = 0
      timers.push(setTimeout(step, 150))
    }
  }
  step()
}

function focusPwd() {
  pwdRef.value?.focus()
}

async function handleLogin() {
  if (loading.value) return
  errorMsg.value = ''
  if (!form.value.username || !form.value.password) {
    errorMsg.value = '✗ 用户名和密码不能为空'
    return
  }
  loading.value = true
  try {
    await auth.login(form.value.username, form.value.password)
    burstPos.value = {
      x: mouse.x > 0 ? mouse.x : window.innerWidth / 2,
      y: mouse.y > 0 ? mouse.y : window.innerHeight / 2
    }
    burst.value = true
    timers.push(setTimeout(() => {
      try { themeStore.setTheme('light') } catch {}
      // 路由跳转失败（懒加载 chunk 丢失、守卫异常）时硬跳转兜底
      router.push('/dashboard').catch(() => { window.location.href = '/dashboard' })
    }, 700))
  } catch (e) {
    errorMsg.value = '✗ 用户名或密码错误'
    form.value.password = ''
    nextTick(() => pwdRef.value?.focus())
    loading.value = false
  }
}

onMounted(() => {
  // 初始把"光"放在画面中心，进入即可见
  mouse.x = window.innerWidth * 0.52
  mouse.y = window.innerHeight * 0.56
  cur.x = mouse.x
  cur.y = mouse.y
  for (let i = 0; i < 30; i++) spawnParticles(mouse.x, mouse.y, 1)
  rafId = requestAnimationFrame(tick)
  window.addEventListener('mousemove', onMove, { passive: true })
  typeBoot()
})
onBeforeUnmount(() => {
  timers.forEach(clearTimeout)
  if (rafId) cancelAnimationFrame(rafId)
  window.removeEventListener('mousemove', onMove)
})
</script>

<style scoped>
.stage {
  position: relative;
  min-height: 100vh;
  background: #000;
  overflow: hidden;
  cursor: none;
  font-family: 'JetBrains Mono', 'Cascadia Mono', Consolas, 'Courier New', monospace;
}

/* ===== 资产层 ===== */
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

/* ===== 萤火虫画布 ===== */
.fx {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  z-index: 2;
  pointer-events: none;
}

/* ===== 终端内容 ===== */
.content {
  position: relative;
  z-index: 3;
  min-height: 100vh;
  box-sizing: border-box;
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  padding: 7vh 8vw;
  pointer-events: none;
}
.content .login-area { pointer-events: auto; }

.console { max-width: 660px; }
.cmdline { font-size: 14px; line-height: 2; white-space: nowrap; }
.ps { color: #fbbf24; text-shadow: 0 0 14px rgba(251, 191, 36, 0.5); }
.cmd { color: #e5e7eb; }

.slogan {
  font-size: 13px;
  line-height: 2;
  letter-spacing: 3px;
  color: #fbbf24;
  opacity: 0.86;
  margin-bottom: 10px;
  text-shadow: 0 0 14px rgba(251, 191, 36, 0.28);
}

.boot {
  margin: 0;
  font-size: 13px;
  line-height: 2.05;
  color: #b9b0a0;
  white-space: pre-wrap;
  min-height: 30px;
  text-shadow: 0 0 20px rgba(245, 158, 11, 0.18);
}
.cursor { color: #fbbf24; animation: blink 1s steps(1) infinite; }
@keyframes blink { 50% { opacity: 0; } }

/* ===== 登录 ===== */
.login-area { max-width: 760px; padding-bottom: 12vh; }
.line { display: flex; align-items: center; font-size: 13.5px; line-height: 2.4; flex-wrap: wrap; }
.k { color: #a3977f; }
.k.pad { color: #a3977f; }
.ipt {
  flex: 1;
  min-width: 180px;
  background: transparent !important;
  -webkit-appearance: none;
  appearance: none;
  border: none;
  outline: none;
  border-bottom: 1px dashed rgba(251, 191, 36, 0.45);
  padding: 2px;
  cursor: text;
  font-family: inherit;
  font-size: 13.5px;
  color: #fdf6e3 !important;
  -webkit-text-fill-color: #fdf6e3;
  caret-color: #fbbf24;
  transition: border-color 0.2s, box-shadow 0.2s;
}
.ipt:focus {
  border-bottom-color: #fbbf24;
  box-shadow: 0 12px 26px -20px rgba(251, 191, 36, 1);
}
.ipt:-webkit-autofill,
.ipt:-webkit-autofill:hover,
.ipt:-webkit-autofill:focus,
.ipt:-webkit-autofill:active {
  -webkit-text-fill-color: #fdf6e3 !important;
  -webkit-box-shadow: 0 0 0 1000px #000 inset !important;
  box-shadow: 0 0 0 1000px #000 inset !important;
  caret-color: #fbbf24;
  transition: background-color 5000s ease-in-out 0s;
}
.hint { color: #6b7280; font-size: 12px; margin-top: 8px; }
.loading { color: #fbbf24; }
.dots { animation: blink 1s steps(1) infinite; }
.err { color: #f87171; font-size: 12.5px; }

/* ===== 登录光爆（以鼠标为焦点） ===== */
.burst {
  position: fixed;
  width: 14px;
  height: 14px;
  margin: -7px 0 0 -7px;
  border-radius: 50%;
  background: radial-gradient(circle,
    #fffbeb 0%,
    #fde68a 22%,
    #fbbf24 42%,
    rgba(249, 115, 22, 0.55) 62%,
    rgba(249, 115, 22, 0) 78%);
  transform: scale(0);
  animation: burstOut 1.05s cubic-bezier(0.22, 1, 0.36, 1) forwards;
  pointer-events: none;
  z-index: 99;
  filter: blur(1px);
}
@keyframes burstOut {
  0%   { transform: scale(0); opacity: 1; }
  55%  { transform: scale(90); opacity: 1; }
  100% { transform: scale(200); opacity: 1; }
}

@media (max-width: 900px) {
  .content { padding: 6vh 6vw; }
  .asset { font-size: 10px; }
}
</style>
