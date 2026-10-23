<template>
  <div class="dash">
    <n-card title="API 文档" size="small">
      <p class="tip">
        所有接口前缀 <code>/api</code>，除「公开接口」外均需在请求头带
        <code>Authorization: Bearer &lt;token&gt;</code>。
        Token 由 <code>POST /api/auth/login</code> 获取，有效期 24 小时。
      </p>

      <n-tabs type="line" animated>
        <n-tab-pane v-for="group in groups" :key="group.name" :name="group.name" :tab="group.name">
          <n-data-table :columns="cols" :data="group.items" size="small" :bordered="false" />
        </n-tab-pane>
      </n-tabs>
    </n-card>
  </div>
</template>

<script setup>
import { h } from 'vue'
import { NTag } from 'naive-ui'

const methodType = { GET: 'info', POST: 'success', PUT: 'warning', DELETE: 'error' }

const cols = [
  { title: '方法', key: 'method', width: 80, render: r => h(NTag, { size: 'small', type: methodType[r.method] || 'default' }, { default: () => r.method }) },
  { title: '路径', key: 'path', width: 320, render: r => h('code', r.path) },
  { title: '说明', key: 'desc' },
  { title: '鉴权', key: 'auth', width: 90, render: r => h(NTag, { size: 'small', type: r.auth === '公开' ? 'default' : 'warning' }, { default: () => r.auth }) }
]

const groups = [
  {
    name: '认证',
    items: [
      { method: 'POST', path: '/api/auth/login', desc: '登录，返回 token；连续失败 5 次锁定 15 分钟', auth: '公开' },
      { method: 'GET', path: '/api/auth/me', desc: '当前用户信息', auth: '需登录' },
      { method: 'POST', path: '/api/auth/password', desc: '修改密码（≥8 位 + 四类中至少三类）', auth: '需登录' }
    ]
  },
  {
    name: '资产与服务',
    items: [
      { method: 'GET', path: '/api/assets', desc: '资产列表，支持 keyword / status / page / size', auth: '需登录' },
      { method: 'GET', path: '/api/services', desc: '应用服务列表，webOnly=true 只返回可达 web 服务', auth: '需登录' },
      { method: 'POST', path: '/api/assets/import', desc: '导入台账 Excel（≤50MB）', auth: '需登录' },
      { method: 'GET', path: '/api/disposal', desc: '待处置资产（新出现 / 新变更 / 台账有）', auth: '需登录' },
      { method: 'POST', path: '/api/disposal/confirm', desc: '确认资产，记录页面指纹基线', auth: '需登录' },
      { method: 'POST', path: '/api/disposal/batch-delete', desc: '批量删除，body: {ips:[]}', auth: '需登录' }
    ]
  },
  {
    name: '扫描任务',
    items: [
      { method: 'GET', path: '/api/tasks', desc: '任务列表，含 percent / etaSeconds', auth: '需登录' },
      { method: 'POST', path: '/api/tasks', desc: '创建任务，run=true 立即执行，false 仅保存', auth: '需登录' },
      { method: 'PUT', path: '/api/tasks/:id', desc: '修改任务（running 状态拒绝）', auth: '需登录' },
      { method: 'POST', path: '/api/tasks/:id/run', desc: '手动执行，重跑会清空上一轮分片', auth: '需登录' },
      { method: 'POST', path: '/api/tasks/:id/stop', desc: '停止任务', auth: '需登录' },
      { method: 'GET', path: '/api/tasks/:id/log', desc: '任务日志（含进度条）', auth: '需登录' },
      { method: 'GET', path: '/api/tasks/:id/agent-tasks', desc: '分片明细，含 total / scanned / etaSeconds', auth: '需登录' },
      { method: 'POST', path: '/api/agent-tasks/:id/stop', desc: '停止单个分片（不重分配）', auth: '需登录' },
      { method: 'POST', path: '/api/agent-tasks/:id/cancel', desc: '故障转移（重分配给其他节点）', auth: '需登录' }
    ]
  },
  {
    name: '节点（Agent）',
    items: [
      { method: 'POST', path: '/api/nodes/register', desc: '注册，返回 64 字符节点密钥', auth: '公开' },
      { method: 'POST', path: '/api/nodes/:id/heartbeat', desc: '心跳，返回升级指令', auth: '节点密钥' },
      { method: 'GET', path: '/api/nodes/:id/tasks', desc: '拉取待执行分片', auth: '节点密钥' },
      { method: 'POST', path: '/api/nodes/:id/results', desc: '上报扫描结果（每批 200 条）', auth: '节点密钥' },
      { method: 'GET', path: '/api/agent/tasks/:id', desc: '分片详情（agent 用）', auth: '节点密钥' },
      { method: 'POST', path: '/api/agent/tasks/:id/progress', desc: '进度上报，body 含 total/scanned/progress/eta', auth: '节点密钥' },
      { method: 'POST', path: '/api/agent/tasks/:id/finish', desc: '完成上报', auth: '节点密钥' }
    ]
  },
  {
    name: '合规与快照',
    items: [
      { method: 'GET', path: '/api/compliance/results', desc: '合规检测结果，含服务名', auth: '需登录' },
      { method: 'POST', path: '/api/compliance/check', desc: '一键检测', auth: '需登录' },
      { method: 'GET', path: '/api/snapshots', desc: '页面快照列表', auth: '需登录' },
      { method: 'POST', path: '/api/snapshots/capture', desc: '立即截图全部 web 服务', auth: '需登录' },
      { method: 'GET', path: '/api/snapshots/:id/image', desc: '快照图片', auth: '需登录' }
    ]
  },
  {
    name: '统计',
    items: [
      { method: 'GET', path: '/api/dashboard/summary', desc: '首页汇总（服务数=可达 web 服务）', auth: '需登录' },
      { method: 'GET', path: '/api/dashboard/trend?range=7d', desc: '趋势，range 支持 7d / 30d / 90d / 365d', auth: '需登录' },
      { method: 'GET', path: '/api/system/stats', desc: '系统资源占用', auth: '需登录' }
    ]
  },
  {
    name: '导出',
    items: [
      { method: 'POST', path: '/api/export', desc: '导出 Excel，body: {type, columns, rows}', auth: '需登录' },
      { method: 'POST', path: '/api/disposal/export', desc: '导出待处置资产', auth: '需登录' }
    ]
  }
]
</script>

<style scoped>
.dash { animation: rise 0.4s cubic-bezier(0.4, 0, 0.2, 1); }
@keyframes rise { from { opacity: 0; transform: translateY(10px); } to { opacity: 1; transform: none; } }
.tip { font-size: 13px; color: var(--text-muted); margin: 0 0 16px; line-height: 1.8; }
.tip code { background: var(--bg-hover); padding: 1px 6px; border-radius: 4px; font-size: 12px; }
</style>
